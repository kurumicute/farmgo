package services

// 對應 Python tests/test_game_service.py。

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/transactions"
)

func wantKind(t *testing.T, err error, kind gameerr.Kind) {
	t.Helper()
	ge, ok := gameerr.As(err)
	if !ok || ge.Kind != kind {
		t.Fatalf("想要錯誤種類 %d，得到 %v", kind, err)
	}
}

func TestCreatePlayerAndFarm(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	p := must[PlayerInfo](t)(e.s.CreatePlayer(e.ctx, "小花", nil, nil))
	st := must[FarmStatus](t)(e.s.CreateFarm(e.ctx, p.ID, "", &home))
	if st.HomeGroupID == nil || *st.HomeGroupID != home || st.Level != 1 || st.Gold != 500 {
		t.Fatalf("%+v", st)
	}
	if len(st.Plots) != 4 || st.EmptyPlots() != 4 || st.WarehouseCapacity != 50 || st.WarehouseUsed != 0 {
		t.Fatalf("%+v", st)
	}
	if inv := must[Inventory](t)(e.s.GetInventory(e.ctx, p.ID)); len(inv.Items) != 0 {
		t.Fatal(inv)
	}
	_, err := e.s.CreateFarm(e.ctx, p.ID, "", &home)
	wantKind(t, err, gameerr.AlreadyExists)
}

func TestOperationsRequireFarm(t *testing.T) {
	e := setup(t)
	p := must[PlayerInfo](t)(e.s.CreatePlayer(e.ctx, "沒農場", nil, nil))
	_, err := e.s.BuySeeds(e.ctx, p.ID, "wheat", 1)
	wantKind(t, err, gameerr.NotFound)
	_, err = e.s.GetFarmStatus(e.ctx, 999)
	wantKind(t, err, gameerr.NotFound)
}

func TestBuySeedsUpdatesGoldAndInventory(t *testing.T) {
	e := setup(t)
	pid := e.player()
	r := e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 5))
	if r.Gold != 450 || r.GoldDelta != -50 || e.qty(pid, "seed_wheat") != 5 {
		t.Fatalf("%+v", r)
	}
	e.ok(e.s.BuySeeds(e.ctx, pid, "小麥", 1)) // 中文名稱也可以
	if e.qty(pid, "seed_wheat") != 6 {
		t.Fatal(e.qty(pid, "seed_wheat"))
	}
}

func TestBuyWithoutEnoughGoldFailsAndChangesNothing(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.s.BuySeeds(e.ctx, pid, "strawberry", 9) // 540 > 500
	wantKind(t, err, gameerr.InsufficientGold)
	if e.status(pid).Gold != 500 {
		t.Fatal("金幣被扣")
	}
	if inv := must[Inventory](t)(e.s.GetInventory(e.ctx, pid)); len(inv.Items) != 0 {
		t.Fatal(inv)
	}
}

func TestInvalidInput(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.s.BuySeeds(e.ctx, pid, "wheat", 0)
	wantKind(t, err, gameerr.Validation)
	_, err = e.s.BuySeeds(e.ctx, pid, "banana", 1)
	wantKind(t, err, gameerr.NotFound)
	_, err = e.plantWatered(pid, 99, "wheat")
	wantKind(t, err, gameerr.NotFound)
}

func TestPlantOnEmptyPlot(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	e.ok(e.s.PlantCrop(e.ctx, pid, 1, "wheat"))
	plot := e.status(pid).Plots[0]
	if plot.State != rules.PlotWaitingWater || *plot.RemainingQuantity != 2 {
		t.Fatalf("%+v", plot)
	}
	e.clock.Advance(600) // 未澆水不會生長
	if e.status(pid).Plots[0].State != rules.PlotWaitingWater {
		t.Fatal("未澆水卻生長")
	}
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	plot = e.status(pid).Plots[0]
	if plot.State != rules.PlotGrowing || *plot.CropCode != "wheat" || !plot.WateredAt.Equal(e.clock.Now()) ||
		plot.RemainingSeconds != 60 {
		t.Fatalf("%+v", plot)
	}
	if e.qty(pid, "seed_wheat") != 0 {
		t.Fatal("種子沒扣")
	}
}

func TestPlantWithoutSeedFails(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.plantWatered(pid, 1, "wheat")
	wantKind(t, err, gameerr.InsufficientItems)
}

func TestCannotPlantOccupiedPlot(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 2))
	e.ok(e.plantWatered(pid, 1, "wheat"))
	_, err := e.plantWatered(pid, 1, "wheat")
	wantKind(t, err, gameerr.PlotOccupied)
	if e.qty(pid, "seed_wheat") != 1 { // 失敗不扣種子
		t.Fatal(e.qty(pid, "seed_wheat"))
	}
}

func TestCannotHarvestUnripeOrEmpty(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.s.HarvestCrop(e.ctx, pid, 1)
	wantKind(t, err, gameerr.PlotEmpty)
	e.ok(e.s.BuySeeds(e.ctx, pid, "carrot", 1))
	e.ok(e.plantWatered(pid, 1, "carrot"))
	e.clock.Advance(179)
	_, err = e.s.HarvestCrop(e.ctx, pid, 1)
	wantKind(t, err, gameerr.PlotNotReady)
}

func TestHarvestOnlyOnce(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	e.clock.Advance(60)
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); r.Quantity != 2 {
		t.Fatalf("%+v", r)
	}
	if e.qty(pid, "wheat") != 2 {
		t.Fatal(e.qty(pid, "wheat"))
	}
	_, err := e.s.HarvestCrop(e.ctx, pid, 1)
	wantKind(t, err, gameerr.PlotEmpty)
	if e.qty(pid, "wheat") != 2 || e.status(pid).Plots[0].State != rules.PlotEmpty {
		t.Fatal("重複收成")
	}
}

// 種子放素材背包（2026-11-12 起）：購買種子檢查素材背包，不佔倉庫。
func TestBuySeedsBeyondBagCapacityFails(t *testing.T) {
	e := setup(t)
	pid := e.player()
	setBag(t, 50)
	e.exec("UPDATE farms SET gold = 10000")
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 49)) // 素材背包 49/50
	if e.status(pid).WarehouseUsed != 0 {
		t.Fatal("種子不應佔倉庫")
	}
	_, err := e.s.BuySeeds(e.ctx, pid, "wheat", 2)
	wantMsg(t, err, gameerr.WarehouseFull, "素材背包空間不足")
	if e.status(pid).Gold != 10_000-490 || e.qty(pid, "seed_wheat") != 49 {
		t.Fatal("素材背包滿時仍有變動")
	}
}

func TestWarehouseFullKeepsCropOnPlot(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	e.setInv(pid, "carrot", "normal", "none", 49) // 倉庫 49/50
	e.clock.Advance(60)
	_, err := e.s.HarvestCrop(e.ctx, pid, 1) // 收成 2 個放不下
	wantKind(t, err, gameerr.WarehouseFull)
	if e.status(pid).Plots[0].State != rules.PlotReady || e.qty(pid, "wheat") != 0 { // 作物仍在土地上，沒有遺失
		t.Fatal("作物遺失")
	}
	e.setInv(pid, "carrot", "normal", "none", 48) // 騰出空間 → 48/50
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	if e.qty(pid, "wheat") != 2 || must[Inventory](t)(e.s.GetInventory(e.ctx, pid)).Used != 50 {
		t.Fatal("收成結果不對")
	}
}

func TestSellUpdatesGoldAndInventory(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat") // 490
	e.clock.Advance(60)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	_, err := e.s.SellCrop(e.ctx, pid, "wheat", 3, "normal", "none")
	wantKind(t, err, gameerr.InsufficientItems)
	r := e.ok(e.s.SellCrop(e.ctx, pid, "wheat", 2, "normal", "none"))
	if r.Gold != 490+30 || r.GoldDelta != 30 || e.qty(pid, "wheat") != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestUpgradeAddsPlots(t *testing.T) {
	e := setup(t)
	pid := e.player()
	r := e.ok(e.s.UpgradeFarm(e.ctx, pid)) // 第 5 格 500 金幣
	st := e.status(pid)
	if r.Gold != 0 || st.Gold != 0 || !strings.Contains(r.Message, "下一次擴建需要 1000 金幣") || st.Level != 2 {
		t.Fatalf("%+v %+v", r, st)
	}
	var numbers []int
	for _, p := range st.Plots {
		numbers = append(numbers, p.PlotNumber)
	}
	if !slices.Equal(numbers, []int{1, 2, 3, 4, 5}) {
		t.Fatal(numbers)
	}
	_, err := e.s.UpgradeFarm(e.ctx, pid)
	wantKind(t, err, gameerr.InsufficientGold)
	if !strings.Contains(err.Error(), "1000") || len(e.status(pid).Plots) != 5 {
		t.Fatal(err)
	}
}

func TestUpgradePriceLadder(t *testing.T) {
	var got []int
	for n := 4; n < 17; n++ {
		p, ok := balance.UpgradePrice(n)
		if !ok {
			p = -1
		}
		got = append(got, p)
	}
	if !slices.Equal(got, append(slices.Clone(balance.UpgradePrices), -1)) {
		t.Fatal(got)
	}
	if p, _ := balance.UpgradePrice(1); p != balance.UpgradePrices[0] { // 管理員刪減後低於初始格數
		t.Fatal(p)
	}
	if len(balance.UpgradePrices) != balance.MaxPlots-balance.InitialPlots {
		t.Fatal(len(balance.UpgradePrices))
	}
}

func TestUpgradeStopsAtMaxPlots(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.exec("UPDATE farms SET gold = 1000000")
	for len(e.status(pid).Plots) < balance.MaxPlots {
		e.ok(e.s.UpgradeFarm(e.ctx, pid))
	}
	before := e.status(pid)
	sum := 0
	for _, p := range balance.UpgradePrices {
		sum += p
	}
	if before.Gold != 1_000_000-sum || before.Level != 1+len(balance.UpgradePrices) {
		t.Fatalf("%+v", before)
	}
	records := e.scalar("SELECT count(*) FROM game_transactions")
	_, err := e.s.UpgradeFarm(e.ctx, pid)
	wantKind(t, err, gameerr.Validation)
	if !strings.Contains(err.Error(), "上限") {
		t.Fatal(err)
	}
	after := e.status(pid)
	if after.Gold != before.Gold || after.Level != before.Level || len(after.Plots) != balance.MaxPlots ||
		e.scalar("SELECT count(*) FROM game_transactions") != records {
		t.Fatal("達上限後仍有變動")
	}
}

func TestFailureMidTransactionRollsBack(t *testing.T) {
	e := setup(t)
	pid := e.player()
	boom := errors.New("模擬寫入交易紀錄失敗")
	addTransaction = func(context.Context, *db.Tx, transactions.AddParams) (transactions.TxRecord, error) {
		return transactions.TxRecord{}, boom
	}
	t.Cleanup(func() { addTransaction = transactions.Add })
	if _, err := e.s.BuySeeds(e.ctx, pid, "wheat", 5); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := e.s.UpgradeFarm(e.ctx, pid); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	addTransaction = transactions.Add
	st := e.status(pid)
	if st.Gold != 500 || len(st.Plots) != 4 || st.Level != 1 {
		t.Fatalf("%+v", st)
	}
	if inv := must[Inventory](t)(e.s.GetInventory(e.ctx, pid)); len(inv.Items) != 0 {
		t.Fatal(inv)
	}
	if n := e.scalar("SELECT count(*) FROM game_transactions"); n != 1 { // 只有建立農場
		t.Fatal(n)
	}
}

type outcome struct {
	r   ActionResult
	err error
}

func runConcurrently(n int, fn func() (ActionResult, error)) []outcome {
	var wg sync.WaitGroup
	start := make(chan struct{})
	out := make([]outcome, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := fn()
			out[i] = outcome{r, err}
		}()
	}
	close(start)
	wg.Wait()
	return out
}

func countOK(t *testing.T, results []outcome, kind gameerr.Kind) int {
	t.Helper()
	n := 0
	for _, r := range results {
		if r.err == nil {
			n++
		} else if !gameerr.Is(r.err, kind) {
			t.Fatalf("非預期的錯誤：%v", r.err)
		}
	}
	return n
}

func TestConcurrentHarvestOnlyOnce(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	e.clock.Advance(60)
	results := runConcurrently(8, func() (ActionResult, error) { return e.s.HarvestCrop(e.ctx, pid, 1) })
	if n := countOK(t, results, gameerr.PlotEmpty); n != 1 {
		t.Fatal(n)
	}
	if e.qty(pid, "wheat") != 2 || e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'harvest'") != 1 {
		t.Fatal("重複收成")
	}
}

func TestConcurrentPurchasesNeverOverdraw(t *testing.T) {
	e := setup(t)
	pid := e.player()
	// 每次 60 金幣，500 金幣最多只能成功 8 次
	results := runConcurrently(12, func() (ActionResult, error) { return e.s.BuySeeds(e.ctx, pid, "strawberry", 1) })
	if n := countOK(t, results, gameerr.InsufficientGold); n != 8 {
		t.Fatal(n)
	}
	if e.status(pid).Gold != 500-8*60 || e.qty(pid, "seed_strawberry") != 8 {
		t.Fatal("超額扣款")
	}
}

func TestConcurrentPlantSamePlot(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 5))
	results := runConcurrently(5, func() (ActionResult, error) { return e.plantWatered(pid, 1, "wheat") })
	if n := countOK(t, results, gameerr.PlotOccupied); n != 1 {
		t.Fatal(n)
	}
	if e.qty(pid, "seed_wheat") != 4 {
		t.Fatal(e.qty(pid, "seed_wheat"))
	}
}

func TestCropStateRestoredAfterRestart(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.BuySeeds(e.ctx, pid, "tomato", 1))
	e.ok(e.plantWatered(pid, 3, "tomato"))
	plantedAt := e.clock.Now()

	// 模擬程式重新啟動：全新的連線池與服務實例
	d, err := db.Open(e.ctx, dbtest.URL(), db.PoolOptions{MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	restarted := NewGameService(d, Options{Clock: e.clock})
	e.clock.Advance(120)
	plot := must[FarmStatus](t)(restarted.GetFarmStatus(e.ctx, pid)).Plots[2]
	if plot.State != rules.PlotGrowing || plot.RemainingSeconds != 180 || !plot.PlantedAt.Equal(plantedAt) {
		t.Fatalf("%+v", plot)
	}
	e.clock.Advance(180)
	if must[FarmStatus](t)(restarted.GetFarmStatus(e.ctx, pid)).Plots[2].State != rules.PlotReady {
		t.Fatal("沒有成熟")
	}
	if r := e.ok(restarted.HarvestCrop(e.ctx, pid, 3)); r.Quantity != 4 {
		t.Fatalf("%+v", r)
	}
}

func TestGoldEqualsSumOfTransactionDeltas(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 3))
	e.ok(e.plantWatered(pid, 1, "wheat"))
	e.clock.Advance(60)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	e.ok(e.s.SellCrop(e.ctx, pid, "wheat", 2, "normal", "none"))
	gold := e.scalar("SELECT gold FROM farms")
	if delta := e.scalar("SELECT sum(gold_delta) FROM game_transactions"); gold != delta || gold != 500-30+30 {
		t.Fatal(gold, delta)
	}
	var evs []string
	repotestQuery(t, e, "SELECT event_type FROM game_transactions ORDER BY id", &evs)
	if !slices.Equal(evs, []string{"create_farm", "buy_seed", "plant", "water", "harvest", "sell"}) {
		t.Fatal(evs)
	}
	if e.scalar("SELECT count(*) FROM farm_plots") != 4 || e.scalar("SELECT sum(quantity) FROM inventory_items") != 2 {
		t.Fatal("土地或庫存數量不對")
	}
}

func repotestQuery(t *testing.T, e *env, sql string, out *[]string) {
	t.Helper()
	if err := e.d.InTx(e.ctx, db.TxOptions{ReadOnly: true}, func(tx *db.Tx) error {
		v, err := db.QueryValues[string](e.ctx, tx, sql)
		*out = v
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// ---------- Go 版額外的測試：RunBatch、updated_at、Python 數值行為 ----------

func TestRunBatchSharesOneTransaction(t *testing.T) {
	e := setup(t)
	pid := e.player()
	// 成功：兩個操作一起提交
	if err := e.s.RunBatch(e.ctx, func(ctx context.Context) error {
		if _, err := e.s.BuySeeds(ctx, pid, "wheat", 2); err != nil {
			return err
		}
		_, err := e.s.PlantCrop(ctx, pid, 1, "wheat")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if e.qty(pid, "seed_wheat") != 1 {
		t.Fatal(e.qty(pid, "seed_wheat"))
	}
	// fn 吃掉錯誤：整筆回滾，之後的呼叫也被拒絕
	err := e.s.RunBatch(e.ctx, func(ctx context.Context) error {
		if _, err := e.s.BuySeeds(ctx, pid, "wheat", 1); err != nil {
			return err
		}
		if _, err := e.s.BuySeeds(ctx, pid, "banana", 1); err == nil {
			t.Fatal("應該失敗")
		}
		if _, err := e.s.GetFarmStatus(ctx, pid); err == nil || !strings.Contains(err.Error(), "先前的操作已失敗") {
			t.Fatal(err)
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "已回滾") {
		t.Fatal(err)
	}
	if e.qty(pid, "seed_wheat") != 1 {
		t.Fatal("共用交易沒有回滾")
	}
}

func TestFarmUpdatedAtUsesClock(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.clock.Advance(3600)
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	var at time.Time
	if err := e.d.InTx(e.ctx, db.TxOptions{}, func(tx *db.Tx) error {
		return tx.QueryRow(e.ctx, "SELECT updated_at FROM farms").Scan(&at)
	}); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(e.clock.Now()) {
		t.Fatal(at, e.clock.Now())
	}
}

func TestMulDurationMatchesPython(t *testing.T) {
	// Python：timedelta(seconds=60) * 0.85 == timedelta(seconds=51)；timedelta(microseconds=1) * 0.5 == 0（五成雙）
	cases := []struct {
		d    time.Duration
		f    float64
		want time.Duration
	}{
		{60 * time.Second, 0.85, 51 * time.Second},
		{time.Microsecond, 0.5, 0},
		{3 * time.Microsecond, 0.5, 2 * time.Microsecond},
		{7 * time.Microsecond, 0.85, 6 * time.Microsecond}, // 5.95 → 6
		{-60 * time.Second, 0.85, -51 * time.Second},
	}
	for _, c := range cases {
		if got := mulDuration(c.d, c.f); got != c.want {
			t.Errorf("%v × %v = %v，想要 %v", c.d, c.f, got, c.want)
		}
	}
}

func TestLongDuration(t *testing.T) {
	cases := map[int]string{
		7*24*3600 - 60: "6 天 23 小時 59 分",
		7 * 24 * 3600:  "7 天",
		3600:           "1 小時",
		3660:           "1 小時 1 分",
		59:             rules.FormatDuration(59),
	}
	for sec, want := range cases {
		if got := LongDuration(sec); got != want {
			t.Errorf("LongDuration(%d) = %q，想要 %q", sec, got, want)
		}
	}
}

func TestGeneticsLabelAndSummary(t *testing.T) {
	if GeneticsLabel("rare", "golden") != "【稀有・黃金作物】" || GeneticsLabel("normal", "variant") != "" || GeneticsLabel("", "") != "" {
		t.Fatal(GeneticsLabel("rare", "golden"))
	}
	details := []HarvestDetail{
		{Name: "小麥", Quantity: 2, Quality: "normal", Mutation: "none", Value: 30},
		{Name: "小麥【稀有】", Quantity: 3, Quality: "rare", Mutation: "none", Value: 60, Stolen: 1},
		{Name: "小麥", Quantity: 2, Quality: "normal", Mutation: "none", Value: 30, CatBonus: 1},
	}
	want := "✨ 稀有收穫！小麥【稀有】 ×3\n🧺 收成 3 格，共 7 個作物。\n小麥 ×4、小麥【稀有】 ×3" +
		"\n📦 約值 120 金（到「販售」賣出）\n🐱 貓咪多收 +1 個\n😢 被偷走 1 個"
	if got := HarvestSummary(details); got != want {
		t.Fatalf("%q", got)
	}
	if GoldChange(4240, 1240) != "\n💰 金幣 3,000 → 4,240" {
		t.Fatal(GoldChange(4240, 1240))
	}
}
