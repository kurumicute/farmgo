package services

// 對應 Python tests/test_perf_batching.py：一鍵操作的延後寫入、遊戲目錄快取的正確性。
//
// 延後寫入的單元測試（查詢前先送出、圖鑑合併保留最佳品質、失敗時丟棄）在 internal/db/db_test.go、
// repositories/inventory、repositories/codex。SQL 次數以 dbtest.SQL 計算（pgx Tracer；一個 pgx.Batch 算一條，
// 對應 SQLAlchemy executemany）。Python 的「快照不附加在 Session 上」是 ORM 特有的問題，Go 回傳值複製，
// 改測「呼叫端修改自己的切片不影響快取」。

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/genetics"
	"farmgm/internal/repositories/repotest"
)

// plotsReady 補到 plots 格、全部種下 crop 並澆水，推進 600 秒（Python plots_ready）。
func (e *env) plotsReady(svc *AdminService, pid int64, plots int, crop string) {
	e.t.Helper()
	gold := 1_000_000
	must[FarmValues](e.t)(e.a.SetFarmValues(e.ctx, "test", pid, &gold, nil))
	if missing := plots - len(e.status(pid).Plots); missing > 0 {
		must[PlotsAdded](e.t)(e.a.AddPlots(e.ctx, "test", pid, missing))
	}
	e.ok(svc.PlantAll(e.ctx, pid, crop))
	e.ok(svc.WaterAll(e.ctx, pid))
	e.clock.Advance(600)
}

func TestHarvestAllMergesWritesAndKeepsEveryRecord(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.plotsReady(e.a, pid, 16, "wheat")
	dbtest.SQL.Start(t)
	r := e.ok(e.a.HarvestAll(e.ctx, pid))
	dbtest.SQL.Stop()
	if r.Quantity <= 0 {
		t.Fatalf("%+v", r)
	}
	// 16 格同一種作物，原本三種寫入各 16 條；合併後最多 2 條（第一格之後的第一次查詢會先送出已累積的部分）
	for _, prefix := range []string{"INSERT INTO inventory_items", "INSERT INTO farm_codex", "INSERT INTO game_transactions"} {
		if n := dbtest.SQL.Count(prefix); n > 2 {
			t.Errorf("%s 執行 %d 次", prefix, n)
		}
	}
	var desc []string
	repotestQuery(t, e, "SELECT description FROM game_transactions WHERE event_type = 'harvest' ORDER BY id", &desc)
	if len(desc) != 16 {
		t.Fatal(len(desc))
	}
	for i, d := range desc {
		if first, _, _ := strings.Cut(d, "，"); first != fmt.Sprintf("收成第 %d 格", i+1) {
			t.Fatal(i, d)
		}
	}
	if q := e.scalar("SELECT sum(quantity) FROM game_transactions WHERE event_type = 'harvest'"); q != r.Quantity {
		t.Fatal(q, r.Quantity)
	}
	if q := e.scalar("SELECT quantity FROM inventory_items WHERE item_code = 'wheat'"); q != r.Quantity {
		t.Fatal(q, r.Quantity)
	}
	if q := e.scalar(`SELECT c.quantity FROM farm_codex c JOIN farms f ON f.id = c.farm_id
		WHERE f.player_id = $1 AND c.item_code = 'wheat'`, pid); q != r.Quantity {
		t.Fatal(q, r.Quantity)
	}
	if c := e.statsCheck(); c.FarmStatsMismatches != 0 {
		t.Fatalf("%+v", c)
	}
}

// 有進行中的任務時交易紀錄要逐筆寫（計分需要 ID）；任務進度與貢獻都要正確。
func TestQuestScoringStillGetsTransactionIDs(t *testing.T) {
	e := setup(t)
	pid := e.player()
	home := e.home(pid).ID
	now := e.clock.Now()
	must[QuestView](t)(e.a.CreateQuest(e.ctx, "root", NewQuest{GroupID: home, Name: "收成任務", QuestType: "harvest",
		Target: 100000, RewardMode: "fixed", RewardType: "gold", RewardAmount: 10, StartsAt: now, EndsAt: now.Add(24 * time.Hour)}))
	scoped := e.a.InGroupContext(&home)
	e.plotsReady(scoped, pid, 4, "wheat")
	r := e.ok(scoped.HarvestAll(e.ctx, pid))
	var contrib, harvests []string
	repotestQuery(t, e, "SELECT transaction_id::text FROM quest_contributions ORDER BY transaction_id", &contrib)
	repotestQuery(t, e, "SELECT id::text FROM game_transactions WHERE event_type = 'harvest' ORDER BY id", &harvests)
	if len(contrib) != 4 || !slices.Equal(contrib, harvests) { // 每格一筆（各自的交易紀錄 ID）
		t.Fatal(contrib, harvests)
	}
	if q := must[[]QuestView](t)(e.a.GroupQuests(e.ctx, home, pid))[0]; q.Progress != r.Quantity {
		t.Fatalf("%+v", q)
	}
}

// 自動販售在同一個一鍵操作中先收成再賣：背包要先寫入，才賣得掉。
func TestAutoSellerSeesHarvestInInventory(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.plotsReady(e.a, pid, 4, "wheat")
	gold := 1_000_000
	must[FarmValues](t)(e.a.SetFarmValues(e.ctx, "test", pid, &gold, nil))
	e.exec("INSERT INTO farm_tools (farm_id, tool_code, enabled, acquired_at) SELECT id, 'auto_seller', true, now() FROM farms")
	before := e.gold(pid)
	r := e.ok(e.a.HarvestAll(e.ctx, pid))
	if e.gold(pid) <= before || r.Quantity <= 0 {
		t.Fatalf("%+v", r)
	}
	if q := e.scalar("SELECT coalesce(sum(quantity), 0) FROM inventory_items WHERE item_code = 'wheat'"); q != 0 {
		t.Fatal(q)
	}
	if c := e.statsCheck(); c.FarmStatsMismatches != 0 {
		t.Fatalf("%+v", c)
	}
}

// 一鍵收成（延後寫入）同時被多人偷菜：農場列的鎖讓兩者依序執行，作物不會多也不會少，紀錄與背包一致。
func TestConcurrentHarvestAllAndStealsConserveCrops(t *testing.T) {
	e := setup(t)
	pid := e.player()
	home := e.home(pid).ID
	e.withDice(newDice(0.99)) // 不觸發防守判定
	var thieves []int64
	for i := range 8 {
		thieves = append(thieves, e.farmer(fmt.Sprintf("小偷%d", i), home))
	}
	e.plotsReady(e.a, pid, 16, "wheat")
	planted := 16 * 2 // 小麥每格 2 個（測試設定：100% 普通、不突變）
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	for i, thief := range thieves {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for _, n := range []int{i + 1, i + 2} {
				if _, err := e.a.StealCrop(e.ctx, home, thief, pid, n, 1); err != nil {
					if _, game := gameerr.As(err); !game { // 土地已被收成、偷光或達到上限都是合法結果
						fail(err)
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if _, err := e.a.HarvestAll(e.ctx, pid); err != nil {
			fail(err)
		}
	}()
	close(start)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	total := func(event string) int {
		return e.scalar("SELECT coalesce(sum(quantity), 0) FROM game_transactions WHERE event_type = $1", event)
	}
	left := e.scalar("SELECT coalesce(sum(remaining_quantity), 0) FROM farm_plots")
	stock := e.scalar("SELECT coalesce(sum(quantity), 0) FROM inventory_items WHERE item_code = 'wheat'")
	if total("harvest")+total("steal")+left != planted || stock != total("harvest")+total("steal") {
		t.Fatal(total("harvest"), total("steal"), left, stock)
	}
	if c := e.statsCheck(); c.FarmStatsMismatches != 0 {
		t.Fatalf("%+v", c)
	}
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

// ---------- 目錄快取 ----------

// cacheOn 開啟目錄快取（setup 預設關閉；結束時由 setup 的 Cleanup 還原並清空）。
func (e *env) cacheOn() {
	db.CatalogTTL = 10 * time.Second
	db.CatalogClear()
}

func (e *env) listCrops() []string {
	e.t.Helper()
	var out []string
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		all, err := crops.ListAll(ctx, tx, false)
		for _, c := range all {
			out = append(out, fmt.Sprintf("%s:%d:%d", c.Code, c.SellPrice, c.GrowSeconds))
		}
		return err
	})
	return out
}

func (e *env) wheatField(field string) int {
	e.t.Helper()
	for _, c := range e.listCrops() {
		if p := strings.Split(c, ":"); p[0] == "wheat" {
			var sell, grow int
			fmt.Sscan(p[1], &sell)
			fmt.Sscan(p[2], &grow)
			if field == "sell" {
				return sell
			}
			return grow
		}
	}
	e.t.Fatal("找不到小麥")
	return 0
}

func TestCatalogReadsAreCachedAcrossTransactions(t *testing.T) {
	e := setup(t)
	e.cacheOn()
	e.listCrops()
	dbtest.SQL.Start(t)
	var list []string
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		all, err := crops.ListAll(ctx, tx, false)
		if err != nil {
			return err
		}
		for _, c := range all {
			list = append(list, c.Code)
		}
		if _, _, err := genetics.Current(ctx, tx); err != nil {
			return err
		}
		_, _, err = genetics.Current(ctx, tx)
		return err
	})
	dbtest.SQL.Stop()
	if len(list) == 0 || dbtest.SQL.CountContaining("FROM crop_types") != 0 {
		t.Fatal(list, dbtest.SQL.Statements())
	}
	if n := dbtest.SQL.CountContaining("FROM crop_genetics_settings"); n != 1 { // 第一次讀，之後快取
		t.Fatal(n)
	}
}

// 呼叫端可以改自己拿到的切片，不影響快取（Python：快照不附加在 Session 上）。
func TestCatalogCallerSliceIsIndependent(t *testing.T) {
	e := setup(t)
	e.cacheOn()
	first := e.listCrops()
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		all, err := crops.ListAll(ctx, tx, false)
		if err != nil {
			return err
		}
		if all[0].Code == "" {
			t.Fatal("快照沒有資料")
		}
		all[0].Code = "changed"
		return nil
	})
	if got := e.listCrops(); !slices.Equal(got, first) {
		t.Fatal(got, first)
	}
}

func TestWriteInSameProcessInvalidatesCacheAndSeesOwnChanges(t *testing.T) {
	e := setup(t)
	e.cacheOn()
	e.listCrops()
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		if _, err := crops.ByCode(ctx, tx, "wheat", true); err != nil {
			return err
		}
		if _, err := crops.ListAll(ctx, tx, false); err != nil { // 修改前先讀進快取
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE crop_types SET sell_price = 123 WHERE code = 'wheat'"); err != nil {
			return err
		}
		all, err := crops.ListAll(ctx, tx, false) // 交易內看得到自己未提交的修改
		for _, c := range all {
			if c.Code == "wheat" && c.SellPrice != 123 {
				t.Fatal(c.SellPrice)
			}
		}
		return err
	})
	if p := e.wheatField("sell"); p != 123 { // 提交後立即生效
		t.Fatal(p)
	}
}

func TestRolledBackWriteDoesNotStayInCache(t *testing.T) {
	e := setup(t)
	e.cacheOn()
	original := e.wheatField("sell")
	boom := fmt.Errorf("rollback")
	err := e.d.InTx(e.ctx, db.TxOptions{}, func(tx *db.Tx) error {
		if _, err := tx.Exec(e.ctx, "UPDATE crop_types SET sell_price = sell_price + 999 WHERE code = 'wheat'"); err != nil {
			return err
		}
		if _, err := crops.ListAll(e.ctx, tx, false); err != nil {
			return err
		}
		return boom
	})
	if err != boom {
		t.Fatal(err)
	}
	if p := e.wheatField("sell"); p != original {
		t.Fatal(p, original)
	}
}

// 管理網站與 Bot 是不同程序：繞過 Tx 的寫入（另一個程序）在 TTL 過後才看得到。
// Python 以 monkeypatch time.monotonic 跳過 TTL；Go 把 TTL 設短再等待。
func TestOtherProcessChangesAppearAfterTTL(t *testing.T) {
	e := setup(t)
	e.cacheOn()
	db.CatalogTTL = 300 * time.Millisecond
	e.listCrops()
	if _, err := e.d.Pool.Exec(e.ctx, "UPDATE crop_types SET sell_price = 456 WHERE code = 'wheat'"); err != nil {
		t.Fatal(err)
	}
	if p := e.wheatField("sell"); p == 456 {
		t.Fatal("TTL 內不應看到另一個程序的修改")
	}
	time.Sleep(db.CatalogTTL + 50*time.Millisecond)
	if p := e.wheatField("sell"); p != 456 {
		t.Fatal(p)
	}
}

func TestAdminCropUpdateIsVisibleImmediatelyWithCache(t *testing.T) {
	e := setup(t)
	e.cacheOn()
	e.listCrops()
	must[CropInfo](t)(e.a.UpdateCropGrowSeconds(e.ctx, "test", "wheat", 777))
	if g := e.wheatField("grow"); g != 777 || e.scalar("SELECT grow_seconds FROM crop_types WHERE code = 'wheat'") != 777 {
		t.Fatal(g)
	}
}
