package services

// 對應 Python tests/test_ranch.py（排行榜、網頁監控、CMD 的部分等對應服務移植後再補）。

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/dbtest"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/ranch"
	"farmgm/internal/repositories/transactions"
)

const day = 24 * 3600

// rancher Python 的 farmer 夾具：「牧場主」，金幣 100,000。
func (e *env) rancher() int64 {
	pid := e.farmer("牧場主", e.group("主群組"))
	e.setGold(pid, 100_000)
	return pid
}

func (e *env) states(pid int64) []string {
	e.t.Helper()
	var out []string
	for _, a := range must[[]AnimalView](e.t)(e.r.ListAnimals(e.ctx, pid)) {
		out = append(out, a.State)
	}
	return out
}

func (e *env) animalNos(pid int64) []int {
	e.t.Helper()
	var out []int
	for _, a := range must[[]AnimalView](e.t)(e.r.ListAnimals(e.ctx, pid)) {
		out = append(out, a.AnimalNo)
	}
	return out
}

func (e *env) orders(pid int64, includeCollected bool) []OrderView {
	e.t.Helper()
	return must[[]OrderView](e.t)(e.r.CraftOrders(e.ctx, pid, includeCollected, 20))
}

func wantMsg(t *testing.T, err error, kind gameerr.Kind, substr string) {
	t.Helper()
	wantKind(t, err, kind)
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("錯誤訊息 %q 不含 %q", err.Error(), substr)
	}
}

// ---------- 建築 ----------

func TestBuildLimits(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	for i := 1; i <= 3; i++ {
		if r := e.ok(e.r.Build(e.ctx, f, "畜舍")); !strings.Contains(r.Message, fmt.Sprintf("第 %d 座", i)) {
			t.Fatal(r.Message)
		}
	}
	_, err := e.r.Build(e.ctx, f, "barn")
	wantKind(t, err, gameerr.AlreadyExists)
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	_, err = e.r.Build(e.ctx, f, "mill")
	wantKind(t, err, gameerr.AlreadyExists)
	_, err = e.r.Build(e.ctx, f, "城堡")
	wantKind(t, err, gameerr.NotFound)
	if e.gold(f) != 100_000-3*1000-2000 || must[WorkshopStatus](t)(e.r.WorkshopStatus(e.ctx, f)).Capacity != 12 {
		t.Fatal(e.gold(f))
	}
}

func TestBuildRequiresGold(t *testing.T) {
	e := setup(t)
	poor := e.farmer("窮農", e.group("主群組"))
	_, err := e.r.Build(e.ctx, poor, "烘焙坊")
	wantKind(t, err, gameerr.InsufficientGold)
	if e.gold(poor) != 500 || must[WorkshopStatus](t)(e.r.WorkshopStatus(e.ctx, poor)).Counts["bakery"] != 0 {
		t.Fatal("沒錢也蓋了")
	}
}

// ---------- 購買與容量 ----------

func TestAnimalsNeedBarnAndCapacity(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	_, err := e.r.BuyAnimals(e.ctx, f, "雞", 1)
	wantMsg(t, err, gameerr.Validation, "畜舍")
	if e.gold(f) != 100_000 {
		t.Fatal(e.gold(f))
	}
	e.ok(e.r.Build(e.ctx, f, "畜舍")) // 容量 4
	_, err = e.r.BuyAnimals(e.ctx, f, "雞", 5)
	wantMsg(t, err, gameerr.Validation, "容量不足")
	if e.gold(f) != 99_000 || len(e.animalNos(f)) != 0 { // 容量不足時不扣款
		t.Fatal("容量不足仍扣款")
	}
	r := e.ok(e.r.BuyAnimals(e.ctx, f, "雞", 3))
	if !strings.Contains(r.Message, "編號 1–3") || e.gold(f) != 99_000-1500 {
		t.Fatal(r.Message)
	}
	e.ok(e.r.BuyAnimals(e.ctx, f, "牛", 1))
	_, err = e.r.BuyAnimals(e.ctx, f, "羊", 1)
	wantMsg(t, err, gameerr.Validation, "容量不足")
	_, err = e.r.BuyAnimals(e.ctx, f, "恐龍", 1)
	wantKind(t, err, gameerr.NotFound)
	if !slices.Equal(e.animalNos(f), []int{1, 2, 3, 4}) {
		t.Fatal(e.animalNos(f))
	}
}

func TestReleaseAnimalFreesSlotWithoutRefund(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, f, "雞", 4))
	_, err := e.r.BuyAnimals(e.ctx, f, "牛", 1)
	wantMsg(t, err, gameerr.Validation, "容量不足")
	before := e.gold(f)
	r := e.ok(e.r.ReleaseAnimal(e.ctx, f, 2))
	if !strings.Contains(r.Message, "已放生 雞#2") || !strings.Contains(r.Message, "3/4") || e.gold(f) != before {
		t.Fatal(r.Message)
	}
	if !slices.Equal(e.animalNos(f), []int{1, 3, 4}) {
		t.Fatal(e.animalNos(f))
	}
	e.ok(e.r.BuyAnimals(e.ctx, f, "牛", 1)) // 空出的欄位可以買更高級的動物
	list := must[[]AnimalView](t)(e.r.ListAnimals(e.ctx, f))
	if last := list[len(list)-1]; last.AnimalNo != 5 || last.Code != "cow" {
		t.Fatalf("%+v", last)
	}
	_, err = e.r.ReleaseAnimal(e.ctx, f, 2) // 已經放生
	wantKind(t, err, gameerr.NotFound)
	// 生產中可以放生（餵食費不退）；產品還沒收集時不能放生
	e.ok(e.r.FeedAnimal(e.ctx, f, 1))
	if r := e.ok(e.r.ReleaseAnimal(e.ctx, f, 1)); !strings.Contains(r.Message, "餵食不會退還") {
		t.Fatal(r.Message)
	}
	e.ok(e.r.FeedAnimal(e.ctx, f, 3))
	e.clock.Advance(day)
	_, err = e.r.ReleaseAnimal(e.ctx, f, 3)
	wantMsg(t, err, gameerr.Validation, "先收集")
	if !slices.Contains(e.animalNos(f), 3) {
		t.Fatal("產品沒收集卻被放生")
	}
	// 只能放生自己的動物；紀錄寫入交易紀錄（金幣不變）
	other := e.farmer("別人", e.group("別的群"))
	_, err = e.r.ReleaseAnimal(e.ctx, other, 4)
	wantKind(t, err, gameerr.NotFound)
	if n := e.scalar(`SELECT count(*) FROM game_transactions WHERE event_type = 'release_animal'
		AND item_code = 'chicken' AND quantity = 1 AND gold_delta = 0`); n != 2 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'release_animal'") != 2 {
		t.Fatal(n)
	}
}

func TestBuyAnimalsInsufficientGold(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.setGold(f, 1999)
	_, err := e.r.BuyAnimals(e.ctx, f, "牛", 1)
	wantKind(t, err, gameerr.InsufficientGold)
	if e.gold(f) != 1999 || len(e.animalNos(f)) != 0 {
		t.Fatal("金幣不足仍購買")
	}
}

func TestConcurrentAnimalPurchasesRespectCapacity(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	results := runConcurrently(6, func() (ActionResult, error) { return e.r.BuyAnimals(e.ctx, f, "雞", 1) })
	if n := countOK(t, results, gameerr.Validation); n != 4 {
		t.Fatal(n)
	}
	if len(e.animalNos(f)) != 4 || e.gold(f) != 100_000-1000-4*500 {
		t.Fatal("超過容量")
	}
}

// ---------- 餵食、生產、收集 ----------

func TestFeedProduceCollectCycle(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, f, "雞", 1))
	before := e.gold(f)
	e.clock.Advance(10 * day)
	if !slices.Equal(e.states(f), []string{"hungry"}) { // 未餵食不生產，也不會死亡
		t.Fatal(e.states(f))
	}
	_, err := e.r.CollectAnimal(e.ctx, f, 1)
	wantMsg(t, err, gameerr.Validation, "尚未餵食")
	e.ok(e.r.FeedAnimal(e.ctx, f, 1))
	_, err = e.r.FeedAnimal(e.ctx, f, 1) // 已餵食：不重複扣款
	wantMsg(t, err, gameerr.Validation, "生產中")
	if e.gold(f) != before-20 {
		t.Fatal(e.gold(f))
	}
	e.clock.Advance(day - 1)
	_, err = e.r.CollectAnimal(e.ctx, f, 1)
	wantMsg(t, err, gameerr.Validation, "生產中")
	e.clock.Advance(1)
	if !slices.Equal(e.states(f), []string{"ready"}) {
		t.Fatal(e.states(f))
	}
	_, err = e.r.FeedAnimal(e.ctx, f, 1)
	wantMsg(t, err, gameerr.Validation, "請先收集")
	if r := e.ok(e.r.CollectAnimal(e.ctx, f, 1)); r.Quantity != 2 || e.qty(f, "egg") != 2 {
		t.Fatalf("%+v", r)
	}
	_, err = e.r.CollectAnimal(e.ctx, f, 1) // 同一週期只能收集一次
	wantMsg(t, err, gameerr.Validation, "尚未餵食")
	e.clock.Advance(5 * day)
	if !slices.Equal(e.states(f), []string{"hungry"}) || must[[]AnimalView](t)(e.r.ListAnimals(e.ctx, f))[0].CyclesCompleted != 1 {
		t.Fatal(e.states(f))
	}
	_, err = e.r.FeedAnimal(e.ctx, f, 99)
	wantKind(t, err, gameerr.NotFound)
}

func TestSheepCycleIs48Hours(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, f, "羊", 1))
	e.ok(e.r.FeedAnimal(e.ctx, f, 1))
	e.clock.Advance(day)
	if !slices.Equal(e.states(f), []string{"producing"}) {
		t.Fatal(e.states(f))
	}
	e.clock.Advance(day)
	if r := e.ok(e.r.CollectAnimal(e.ctx, f, 1)); r.Quantity != 1 || e.qty(f, "wool") != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestFeedAllWithInsufficientGold(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, f, "雞", 1)) // #1 20
	e.ok(e.r.BuyAnimals(e.ctx, f, "牛", 1)) // #2 50
	e.ok(e.r.BuyAnimals(e.ctx, f, "雞", 1)) // #3 20
	e.setGold(f, 45)
	r := e.ok(e.r.FeedAll(e.ctx, f))
	// 依編號順序餵到金幣不夠為止：只餵 #1，只扣 20
	if r.Quantity != 1 || !strings.Contains(r.Message, "未餵食：牛#2、雞#3") || e.gold(f) != 25 {
		t.Fatalf("%+v", r)
	}
	want := []string{"producing", "hungry", "hungry"}
	if !slices.Equal(e.states(f), want) || e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'feed_animal'") != 1 {
		t.Fatal(e.states(f))
	}
	e.setGold(f, 10)
	_, err := e.r.FeedAll(e.ctx, f)
	wantKind(t, err, gameerr.InsufficientGold)
	if e.gold(f) != 10 || !slices.Equal(e.states(f), want) {
		t.Fatal("失敗仍有變動")
	}
	e.setGold(f, 1000)
	if r := e.ok(e.r.FeedAll(e.ctx, f)); !strings.Contains(r.Message, "已餵食 2 隻") || e.gold(f) != 930 {
		t.Fatal(r.Message)
	}
	if r := e.ok(e.r.FeedAll(e.ctx, f)); r.Message != "目前沒有需要餵食的動物。" {
		t.Fatal(r.Message)
	}
}

func TestFeedAllRollsBackOnFailure(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, f, "雞", 3))
	before := e.gold(f)
	calls := 0
	boom := errors.New("模擬寫入失敗")
	addTransaction = func(ctx context.Context, t *db.Tx, p transactions.AddParams) (transactions.TxRecord, error) {
		calls++
		if calls == 2 {
			return transactions.TxRecord{}, boom
		}
		return transactions.Add(ctx, t, p)
	}
	t.Cleanup(func() { addTransaction = transactions.Add })
	if _, err := e.r.FeedAll(e.ctx, f); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	addTransaction = transactions.Add
	// 不會出現「扣了錢但動物沒更新」或部分餵食
	if e.gold(f) != before || !slices.Equal(e.states(f), []string{"hungry", "hungry", "hungry"}) {
		t.Fatal("沒有整筆回滾")
	}
}

func TestCollectWithFullBackpackKeepsProduct(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, f, "牛", 1))
	e.ok(e.r.FeedAnimal(e.ctx, f, 1))
	e.clock.Advance(day)
	e.give(f, "wheat", 49) // 背包 49/50，牛奶需要 2 格
	_, err := e.r.CollectAnimal(e.ctx, f, 1)
	wantMsg(t, err, gameerr.WarehouseFull, "保留")
	if !slices.Equal(e.states(f), []string{"ready"}) || e.qty(f, "milk") != 0 {
		t.Fatal("產品遺失")
	}
	e.ok(e.r.Sell(e.ctx, f, "wheat", 10, "normal", "none"))
	if r := e.ok(e.r.CollectAnimal(e.ctx, f, 1)); r.Quantity != 2 {
		t.Fatalf("%+v", r)
	}
}

func restartedRanch(t *testing.T, e *env) *RanchService {
	t.Helper()
	d, err := db.Open(e.ctx, dbtest.URL(), db.PoolOptions{MaxConns: 6})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return NewRanchService(d, Options{Clock: e.clock})
}

func TestRestartAndConcurrentCollect(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, f, "雞", 1))
	e.ok(e.r.FeedAnimal(e.ctx, f, 1))
	readyAt := *must[[]AnimalView](t)(e.r.ListAnimals(e.ctx, f))[0].ReadyAt
	e.clock.Advance(day / 2)
	restarted := restartedRanch(t, e)
	a := must[[]AnimalView](t)(restarted.ListAnimals(e.ctx, f))[0]
	if a.State != "producing" || !a.ReadyAt.Equal(readyAt) || a.RemainingSeconds != day/2 { // 重啟不重設時間
		t.Fatalf("%+v", a)
	}
	e.clock.Advance(day / 2)
	results := runConcurrently(5, func() (ActionResult, error) { return restarted.CollectAnimal(e.ctx, f, 1) })
	if n := countOK(t, results, gameerr.Validation); n != 1 {
		t.Fatal(n)
	}
	if bp := must[Backpack](t)(restarted.GetBackpack(e.ctx, f)); bp.Goods[0].Quantity != 2 { // 不重複發放
		t.Fatalf("%+v", bp.Goods)
	}
}

// ---------- 加工 ----------

func TestCraftRequiresBuildingAndIngredients(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.give(f, "wheat", 5)
	_, err := e.r.StartCraft(e.ctx, f, "麵粉", 1)
	wantMsg(t, err, gameerr.Validation, "磨坊")
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	_, err = e.r.StartCraft(e.ctx, f, "麵粉", 2)
	wantMsg(t, err, gameerr.InsufficientItems, "小麥 需要 6、只有 5")
	if e.qty(f, "wheat") != 5 { // 原料不足時不扣除
		t.Fatal(e.qty(f, "wheat"))
	}
	_, err = e.r.StartCraft(e.ctx, f, "麵粉", 11)
	wantKind(t, err, gameerr.Validation)
	_, err = e.r.StartCraft(e.ctx, f, "披薩", 1)
	wantKind(t, err, gameerr.NotFound)
	if e.scalar("SELECT count(*) FROM craft_orders") != 0 {
		t.Fatal("建立了訂單")
	}
}

func TestCraftLifecycle(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 7)
	r := e.ok(e.r.StartCraft(e.ctx, f, "flour", 2))
	if e.qty(f, "wheat") != 1 { // 開單時立即扣除 6 個小麥
		t.Fatal(e.qty(f, "wheat"))
	}
	list := e.orders(f, false)
	if len(list) != 1 {
		t.Fatal(list)
	}
	o := list[0]
	if len(o.InputCodes) != 1 || o.InputCodes["wheat"] != 6 || o.OutputQuantity != 2 || o.State != "in_progress" ||
		o.RemainingSeconds != 3600 || o.ReadyAt.Sub(o.StartedAt).Seconds() != 3600 ||
		!strings.Contains(r.Message, fmt.Sprintf("#%d", o.OrderID)) {
		t.Fatalf("%+v %s", o, r.Message)
	}
	e.give(f, "wheat", 2)
	queued := e.ok(e.r.StartCraft(e.ctx, f, "flour", 1)) // 設施忙碌：排在後面
	if !strings.Contains(queued.Message, fmt.Sprintf("排在訂單 #%d 之後", o.OrderID)) ||
		!strings.Contains(strings.ReplaceAll(queued.Message, " ", ""), "1小時30分") {
		t.Fatal(queued.Message)
	}
	var second OrderView
	for _, x := range e.orders(f, false) {
		if x.OrderID != o.OrderID {
			second = x
		}
	}
	if !second.StartedAt.Equal(o.ReadyAt) || second.RemainingSeconds != 90*60 {
		t.Fatalf("%+v", second)
	}
	if st, err := e.r.WorkshopStatus(e.ctx, f); err != nil || st.Queued["mill"] != 1 {
		t.Fatal(st.Queued, err)
	}
	_, err := e.r.CollectCraft(e.ctx, f, o.OrderID)
	wantMsg(t, err, gameerr.Validation, "加工中")
	e.clock.Advance(3600)
	if st, err := e.r.WorkshopStatus(e.ctx, f); err != nil || st.Busy["mill"] == nil || st.Busy["mill"].OrderID != second.OrderID {
		t.Fatal(st.Busy["mill"], err)
	}
	// 已完成但未領取的訂單不佔用設施
	e.give(f, "wheat", 3)
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1))
	if r := e.ok(e.r.CollectCraft(e.ctx, f, o.OrderID)); r.Quantity != 2 {
		t.Fatalf("%+v", r)
	}
	_, err = e.r.CollectCraft(e.ctx, f, o.OrderID)
	wantMsg(t, err, gameerr.Validation, "已經領取")
	if e.qty(f, "flour") != 2 {
		t.Fatal(e.qty(f, "flour"))
	}
	var st []string
	for _, x := range e.orders(f, true) {
		st = append(st, x.State)
	}
	if !slices.Equal(st, []string{"in_progress", "in_progress", "collected"}) {
		t.Fatal(st)
	}
}

// readyChain 依完成時間排序的訂單，每張的開始時間都等於前一張的完成時間（排隊不重疊）。
func readyChain(t *testing.T, orders []OrderView) {
	t.Helper()
	slices.SortFunc(orders, func(a, b OrderView) int { return a.ReadyAt.Compare(b.ReadyAt) })
	for i := 1; i < len(orders); i++ {
		if !orders[i-1].ReadyAt.Equal(orders[i].StartedAt) {
			t.Fatalf("訂單 #%d 完成 %v，#%d 開始 %v", orders[i-1].OrderID, orders[i-1].ReadyAt, orders[i].OrderID, orders[i].StartedAt)
		}
	}
}

func TestCraftQueueLimitAndCanMake(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 3*(balance.CraftQueueMax+1)+2)
	flour := func(pid *int64) *int {
		rows, err := e.r.Recipes(e.ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.Code == "flour" {
				return r.CanMake
			}
		}
		t.Fatal("沒有 flour")
		return nil
	}
	if c := flour(&f); c == nil || *c != balance.CraftMaxQuantity { // 上限內
		t.Fatal(c)
	}
	if c := flour(nil); c != nil {
		t.Fatal(*c)
	}
	for range balance.CraftQueueMax {
		e.ok(e.r.StartCraft(e.ctx, f, "flour", 1))
	}
	_, err := e.r.StartCraft(e.ctx, f, "flour", 1)
	wantMsg(t, err, gameerr.Validation, "排隊訂單已滿")
	if e.qty(f, "wheat") != 5 { // 失敗不扣原料
		t.Fatal(e.qty(f, "wheat"))
	}
	if c := flour(&f); c == nil || *c != 1 {
		t.Fatal(c)
	}
	e.clock.Advance(1800) // 第一張完成：空出一個名額，新訂單排在最後一張之後
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1))
	var busy []OrderView
	for _, o := range e.orders(f, false) {
		if o.State == "in_progress" {
			busy = append(busy, o)
		}
	}
	if len(busy) != balance.CraftQueueMax {
		t.Fatal(len(busy))
	}
	readyChain(t, busy)
}

func TestBreadAndJamRecipes(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "烘焙坊"))
	e.ok(e.r.Build(e.ctx, f, "果醬工坊"))
	e.give(f, "flour", 4)
	e.give(f, "strawberry", 3)
	bread := e.ok(e.r.StartCraft(e.ctx, f, "麵包", 2))
	jam := e.ok(e.r.StartCraft(e.ctx, f, "果醬", 1)) // 不同設施可同時加工
	if !strings.Contains(bread.Message, "2 份麵包") || !strings.Contains(jam.Message, "1 份草莓果醬") {
		t.Fatal(bread.Message, jam.Message)
	}
	e.clock.Advance(2 * 3600)
	for _, o := range e.orders(f, false) {
		e.ok(e.r.CollectCraft(e.ctx, f, o.OrderID))
	}
	if e.qty(f, "bread") != 2 || e.qty(f, "strawberry_jam") != 1 || e.qty(f, "flour") != 0 || e.qty(f, "strawberry") != 0 {
		t.Fatal("加工結果不對")
	}
}

func TestCraftFullBackpackKeepsPending(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 3)
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1))
	e.clock.Advance(1800)
	e.give(f, "carrot", 50)
	id := e.orders(f, false)[0].OrderID
	_, err := e.r.CollectCraft(e.ctx, f, id)
	wantMsg(t, err, gameerr.WarehouseFull, "待領取")
	if e.orders(f, false)[0].State != "ready" {
		t.Fatal("訂單狀態改變")
	}
	e.ok(e.r.Sell(e.ctx, f, "carrot", 1, "normal", "none"))
	if r := e.ok(e.r.CollectCraft(e.ctx, f, id)); r.Quantity != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestCraftConcurrencyAndDuplicates(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 30)
	results := runConcurrently(5, func() (ActionResult, error) { return e.r.StartCraft(e.ctx, f, "flour", 2) })
	if n := countOK(t, results, gameerr.Validation); n != 5 || e.qty(f, "wheat") != 0 { // 依序排隊、每張各扣一次
		t.Fatal(n, e.qty(f, "wheat"))
	}
	readyChain(t, e.orders(f, false)) // 不重疊
	e.clock.Advance(3600)
	var ready []int64
	for _, o := range e.orders(f, false) {
		if o.State == "ready" {
			ready = append(ready, o.OrderID)
		}
	}
	if len(ready) != 1 {
		t.Fatal(ready)
	}
	id := ready[0]
	results = runConcurrently(5, func() (ActionResult, error) { return e.r.CollectCraft(e.ctx, f, id) })
	if n := countOK(t, results, gameerr.Validation); n != 1 || e.qty(f, "flour") != 2 { // 成品只發放一次
		t.Fatal(n, e.qty(f, "flour"))
	}
}

func TestCraftFailureRollsBackIngredients(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 3)
	boom := errors.New("模擬建立訂單失敗")
	addOrder = func(context.Context, *db.Tx, ranch.NewOrder) (*models.CraftOrder, error) { return nil, boom }
	t.Cleanup(func() { addOrder = ranch.AddOrder })
	if _, err := e.r.StartCraft(e.ctx, f, "flour", 1); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	addOrder = ranch.AddOrder
	if e.qty(f, "wheat") != 3 {
		t.Fatal(e.qty(f, "wheat"))
	}
}

func TestCannotTouchOtherPlayersOrders(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	other := e.farmer("別人", e.group("別群"))
	e.setGold(other, 10_000)
	e.ok(e.r.Build(e.ctx, other, "磨坊"))
	e.give(other, "wheat", 3)
	e.ok(e.r.StartCraft(e.ctx, other, "flour", 1))
	_, err := e.r.CollectCraft(e.ctx, f, e.orders(other, false)[0].OrderID)
	wantKind(t, err, gameerr.NotFound)
}

func TestCraftProgressSurvivesRestart(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 3)
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1))
	e.clock.Advance(1000)
	restarted := restartedRanch(t, e)
	if o := must[[]OrderView](t)(restarted.CraftOrders(e.ctx, f, false, 20))[0]; o.RemainingSeconds != 800 {
		t.Fatalf("%+v", o)
	}
	e.clock.Advance(800)
	if o := must[[]OrderView](t)(restarted.CraftOrders(e.ctx, f, false, 20))[0]; o.State != "ready" {
		t.Fatalf("%+v", o)
	}
}

// ---------- 販售 ----------

// TestGoodsSales Python test_goods_sales_and_leaderboards 的販售部分；排行榜（RankingService）尚未移植，
// 改為直接檢查排行用的 farm_stats 欄位。
func TestGoodsSales(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	f := e.farmer("牧場主", home)
	e.setGold(f, 100_000)
	rival := e.farmer("對手", home)
	e.give(f, "egg", 4)
	e.give(f, "bread", 1)
	e.give(rival, "wheat", 3)
	before := e.gold(f)
	if r := e.ok(e.r.Sell(e.ctx, f, "雞蛋", 4, "normal", "none")); r.GoldDelta != 100 || e.gold(f) != before+100 {
		t.Fatalf("%+v", r)
	}
	e.ok(e.r.Sell(e.ctx, f, "麵包", 1, "normal", "none"))
	e.ok(e.r.Sell(e.ctx, rival, "wheat", 3, "normal", "none"))
	_, err := e.r.Sell(e.ctx, f, "麵包", 1, "normal", "none")
	wantKind(t, err, gameerr.InsufficientItems)
	if n := e.scalar(`SELECT count(*) FROM game_transactions WHERE item_code = 'egg' AND event_type = 'sell'
		AND quantity = 4 AND gold_delta = 100 AND description LIKE '%單價 25%'`); n != 1 {
		t.Fatal(n)
	}
	// 農產品販售只統計作物；商品總販售納入動物產品與加工品
	stat := func(pid int64, col string) int {
		return e.scalar("SELECT "+col+" FROM farm_stats s JOIN farms f ON f.id = s.farm_id WHERE f.player_id = $1", pid)
	}
	if stat(f, "crop_sold_quantity") != 0 || stat(rival, "crop_sold_quantity") != 3 || stat(rival, "crop_sales_income") != 45 {
		t.Fatal("作物販售統計不對")
	}
	if stat(f, "goods_sold_quantity") != 5 || stat(f, "goods_sales_income") != 100+balance.Goods["bread"].SellPrice {
		t.Fatal("商品販售統計不對")
	}
	_, err = e.r.Sell(e.ctx, f, "雞蛋", 1, "rare", "none")
	wantMsg(t, err, gameerr.Validation, "沒有品質或突變")
}

func TestBackpackShowsGoods(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.give(f, "milk", 2)
	bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, f))
	if bp.IsEmpty() || len(bp.Goods) != 1 || bp.Goods[0].Name != "牛奶" || bp.Goods[0].Quantity != 2 || bp.Goods[0].Price != 60 {
		t.Fatalf("%+v", bp.Goods)
	}
}

func TestRecipesAndWorkshopViews(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	rows := must[[]RecipeView](t)(e.r.Recipes(e.ctx, &f))
	if rows[0].Code != "flour" || !rows[0].Built || rows[1].Built || rows[0].Inputs["小麥"] != 3 ||
		rows[0].OutputName != "麵粉" || *rows[0].SellPrice != balance.Goods["flour"].SellPrice {
		t.Fatalf("%+v", rows[0])
	}
	e.give(f, "wheat", 3)
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1))
	ws := must[WorkshopStatus](t)(e.r.WorkshopStatus(e.ctx, f))
	if ws.Counts["mill"] != 1 || ws.Busy["mill"] == nil || ws.Busy["bakery"] != nil || ws.Busy["mill"].Inputs["小麥"] != 3 {
		t.Fatalf("%+v", ws)
	}
}
