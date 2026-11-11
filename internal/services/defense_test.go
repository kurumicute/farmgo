package services

// 對應 Python tests/test_defense.py（純規則測試在 game/rules；網頁監控與 CMD 等對應階段移植），
// 以及 tests/test_pets.py 的狗擋偷菜。

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/steals"

	petrules "farmgm/internal/game/pets"
)

var (
	priceScarecrow = balance.Defenses["scarecrow"].Price
	priceDog       = balance.Defenses["guard_dog"].Price
	priceFence     = balance.Defenses["fence"].Price
)

// dice 可控制的亂數：依序回傳 values（用完後重複最後一個），並記錄呼叫次數。
type dice struct {
	mu     sync.Mutex
	values []float64
	calls  int
}

func newDice(values ...float64) *dice {
	if len(values) == 0 {
		values = []float64{0.99}
	}
	return &dice{values: values}
}

func (d *dice) roll() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return d.values[min(d.calls-1, len(d.values)-1)]
}

func (e *env) withDice(d *dice) { e.s.rng = d.roll }

// noRoll 不該進行防守判定。
func (e *env) noRoll() {
	t := e.t
	e.s.rng = func() float64 { t.Error("不該進行防守判定"); return 0.99 }
}

type defWorld struct{ g, thief, thief2, owner int64 }

func (e *env) defWorld() defWorld {
	e.t.Helper()
	g := e.group("防守群")
	w := defWorld{g, e.farmer("小偷", g), e.farmer("小偷二號", g), e.farmer("農夫", g)}
	e.setGold(w.owner, 10_000)
	return w
}

func (e *env) ripe(owner int64, plots ...int) {
	e.t.Helper()
	for _, n := range plots {
		e.grow(owner, n, "strawberry")
	}
	e.clock.Advance(600)
}

func (e *env) remaining(pid int64, n int) int {
	e.t.Helper()
	return *e.plot(pid, n).RemainingQuantity
}

func (e *env) defStatus(pid int64) DefenseStatus {
	e.t.Helper()
	return must[DefenseStatus](e.t)(e.so.DefenseStatus(e.ctx, pid))
}

func (e *env) stealOutcomes() []string {
	e.t.Helper()
	var out []string
	repotestQuery(e.t, e, "SELECT outcome || ':' || coalesce(blocked_by, '') FROM steal_records ORDER BY outcome", &out)
	return out
}

// ---------- 購買與放置 ----------

func TestDefensePurchaseLimitsAndGold(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	if r := e.ok(e.so.BuyDefense(e.ctx, w.owner, "稻草人")); !strings.Contains(r.Message, "第 1 個") {
		t.Fatal(r.Message)
	}
	if r := e.ok(e.so.BuyDefense(e.ctx, w.owner, "scarecrow")); !strings.Contains(r.Message, "第 2 個") {
		t.Fatal(r.Message)
	}
	_, err := e.so.BuyDefense(e.ctx, w.owner, "稻草人")
	wantKind(t, err, gameerr.AlreadyExists)
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "警戒犬"))
	_, err = e.so.BuyDefense(e.ctx, w.owner, "dog")
	wantKind(t, err, gameerr.AlreadyExists)
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "圍欄"))
	_, err = e.so.BuyDefense(e.ctx, w.owner, "fence")
	wantKind(t, err, gameerr.AlreadyExists)
	if e.gold(w.owner) != 10_000-priceScarecrow*2-priceDog-priceFence {
		t.Fatal(e.gold(w.owner))
	}
	_, err = e.so.BuyDefense(e.ctx, w.owner, "機關槍")
	wantKind(t, err, gameerr.NotFound)
	_, err = e.so.BuyDefense(e.ctx, w.thief, "警戒犬") // 只有 500 金幣
	wantKind(t, err, gameerr.InsufficientGold)
	if len(e.defStatus(w.thief).Devices) != 0 || e.gold(w.thief) != 500 {
		t.Fatal("沒錢也買到")
	}
}

func TestConcurrentDefensePurchasesRespectLimit(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	keys := []string{"稻草人", "稻草人", "稻草人", "稻草人", "警戒犬", "警戒犬"}
	res := runConcurrentlyIdx(len(keys), func(i int) (ActionResult, error) { return e.so.BuyDefense(e.ctx, w.owner, keys[i]) })
	if n := countOK(t, res, gameerr.AlreadyExists); n != 3 {
		t.Fatal(n)
	}
	owned := e.defStatus(w.owner).Owned
	if owned["scarecrow"] != 2 || owned["guard_dog"] != 1 || owned["fence"] != 0 ||
		e.gold(w.owner) != 10_000-priceScarecrow*2-priceDog ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'buy_item'") != 3 {
		t.Fatalf("%+v", owned)
	}
}

func runConcurrentlyIdx(n int, fn func(i int) (ActionResult, error)) []outcome {
	fns := make([]func() (ActionResult, error), n)
	for i := range n {
		fns[i] = func() (ActionResult, error) { return fn(i) }
	}
	return concurrently(fns...)
}

func TestDefensePlacementAndStatus(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	for _, d := range []string{"稻草人", "稻草人", "圍欄", "警戒犬"} {
		e.ok(e.so.BuyDefense(e.ctx, w.owner, d))
	}
	st := e.defStatus(w.owner)
	var got []string
	for _, d := range st.Devices {
		s := d.Code + ":" + itoaPtr(&d.Slot) + ":" + itoaPtr(d.StartPlot) + ":" + itoaPtr(d.EndPlot)
		got = append(got, s)
	}
	sort.Strings(got)
	if !slices.Equal(got, []string{"fence:1:1:4", "guard_dog:1:-:-", "scarecrow:1:1:3", "scarecrow:2:1:3"}) {
		t.Fatal(got)
	}
	if !near(st.Plots[0].Chance, 0.58) { // 兩個稻草人重疊只計算一次
		t.Fatal(st.Plots[0].Chance)
	}
	if r := e.ok(e.so.PlaceDefense(e.ctx, w.owner, "稻草人", 2, 2)); !strings.HasSuffix(r.Message, "第 2–4 格。") {
		t.Fatal(r.Message)
	}
	_, err := e.so.PlaceDefense(e.ctx, w.owner, "稻草人", 9, 1) // 只有 4 格土地
	wantKind(t, err, gameerr.Validation)
	_, err = e.so.PlaceDefense(e.ctx, w.owner, "警戒犬", 1, 1) // 守護全部，不需放置
	wantKind(t, err, gameerr.Validation)
	_, err = e.so.PlaceDefense(e.ctx, w.owner, "圍欄", 1, 2) // 圍欄只有 1 組
	wantKind(t, err, gameerr.Validation)
	e.ok(e.so.PlaceDefense(e.ctx, w.owner, "圍欄", 3, 1))
	plots := e.defStatus(w.owner).Plots
	if !near(plots[0].Chance, 1-0.75*0.7) || !near(plots[3].Chance, 0.58) {
		t.Fatal(plots[0].Chance, plots[3].Chance)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func itoaPtr(p *int) string {
	if p == nil {
		return "-"
	}
	return string(rune('0' + *p))
}

func TestPlaceUnownedDevice(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	_, err := e.so.PlaceDefense(e.ctx, w.owner, "稻草人", 1, 1)
	wantKind(t, err, gameerr.NotFound)
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "稻草人"))
	_, err = e.so.PlaceDefense(e.ctx, w.owner, "稻草人", 1, 2)
	wantKind(t, err, gameerr.NotFound)
}

// ---------- 各設施判定 ----------

func TestEachDeviceBlocksByItsChance(t *testing.T) {
	for _, c := range []struct {
		device          string
		block, passRoll float64
	}{{"稻草人", 0.2499, 0.25}, {"警戒犬", 0.2999, 0.30}, {"圍欄", 0.1999, 0.20}} {
		t.Run(c.device, func(t *testing.T) {
			e := setup(t)
			w := e.defWorld()
			e.ok(e.so.BuyDefense(e.ctx, w.owner, c.device))
			e.ripe(w.owner, 1)
			e.withDice(newDice(c.block))
			blocked := e.steal(w.g, w.thief, w.owner, 1, 2)
			if blocked.Success || blocked.Quantity != 0 || !strings.Contains(blocked.Message, "偷菜失敗") {
				t.Fatalf("%+v", blocked)
			}
			if e.remaining(w.owner, 1) != 5 || e.qty(w.thief, "strawberry") != 0 { // 防守成功：雙方都不變
				t.Fatal("防守成功卻有變動")
			}
			e.noRoll()
			_, err := e.so.StealCrop(e.ctx, w.g, w.thief, w.owner, 1, 1) // 被阻止也用掉這次機會
			wantKind(t, err, gameerr.AlreadyStolen)
			e.withDice(newDice(c.passRoll))
			if ok := e.steal(w.g, w.thief2, w.owner, 1, 2); !ok.Success || e.qty(w.thief2, "strawberry") != 2 || e.remaining(w.owner, 1) != 3 {
				t.Fatalf("%+v", ok)
			}
			code := FindDefense(c.device)
			if got := e.stealOutcomes(); !slices.Equal(got, []string{"blocked:" + code, "success:"}) {
				t.Fatal(got)
			}
		})
	}
}

func TestAllDevicesOneRoundInOrder(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	for _, d := range []string{"稻草人", "警戒犬", "圍欄"} {
		e.ok(e.so.BuyDefense(e.ctx, w.owner, d))
	}
	e.ripe(w.owner, 1, 2)
	d := newDice(0.46)
	e.withDice(d)
	if r := e.steal(w.g, w.thief, w.owner, 1, 1); !strings.Contains(r.Message, "警戒犬") || d.calls != 1 { // 每次偷菜只判定一輪
		t.Fatal(r.Message, d.calls)
	}
	e.withDice(newDice(0.59))
	if !e.steal(w.g, w.thief2, w.owner, 1, 1).Success { // 超過 58%
		t.Fatal("應該偷到")
	}
	e.withDice(newDice(0.5))
	if r := e.steal(w.g, w.thief, w.owner, 2, 1); !strings.Contains(r.Message, "防盜圍欄") {
		t.Fatal(r.Message)
	}
	if e.scalar("SELECT count(*) FROM steal_records WHERE abs(block_chance - 0.58) < 1e-9") != 3 {
		t.Fatal("block_chance 不對")
	}
}

func TestUnrelatedPlotsAreNotAffected(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "稻草人")) // 守護第 1–3 格
	e.ripe(w.owner, 1, 4)
	e.withDice(newDice(0.1))
	if e.steal(w.g, w.thief, w.owner, 1, 2).Success { // 被阻止不算進每日 1 格的上限
		t.Fatal("應該被擋")
	}
	e.noRoll()
	if !e.steal(w.g, w.thief, w.owner, 4, 2).Success { // 第 4 格不在任何設施範圍：完全不擲骰
		t.Fatal("應該偷到")
	}
	if e.remaining(w.owner, 1) != 5 || e.remaining(w.owner, 4) != 3 || e.qty(w.thief, "strawberry") != 2 {
		t.Fatal("數量不對")
	}
}

func TestBackpackFullRejectedBeforeDefense(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "警戒犬"))
	e.ripe(w.owner, 1)
	e.setGold(w.thief, 10_000)
	e.ok(e.s.BuySeeds(e.ctx, w.thief, "wheat", 49)) // 背包 49/50
	e.noRoll()
	_, err := e.so.StealCrop(e.ctx, w.g, w.thief, w.owner, 1, 2)
	wantKind(t, err, gameerr.WarehouseFull)
	if len(e.stealOutcomes()) != 0 { // 沒有判定、沒有紀錄，機會沒有被消耗
		t.Fatal("消耗了機會")
	}
	e.withDice(newDice(0.99))
	if !e.steal(w.g, w.thief, w.owner, 1, 1).Success {
		t.Fatal("應該偷到")
	}
}

// ---------- 並行、重複請求、重啟 ----------

func TestConcurrentThievesOnDefendedPlot(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "警戒犬"))
	e.ripe(w.owner, 1)
	e.withDice(newDice(0.1, 0.9)) // 先到者被擋，後到者成功
	res := concurrently(
		func() (ActionResult, error) { return e.so.StealCrop(e.ctx, w.g, w.thief, w.owner, 1, 2) },
		func() (ActionResult, error) { return e.so.StealCrop(e.ctx, w.g, w.thief2, w.owner, 1, 2) })
	var succ []bool
	for _, r := range res {
		if r.err != nil {
			t.Fatal(r.err)
		}
		succ = append(succ, r.r.Success)
	}
	slices.SortFunc(succ, func(a, b bool) int { return boolInt(a) - boolInt(b) })
	if !slices.Equal(succ, []bool{false, true}) {
		t.Fatal(succ)
	}
	if e.qty(w.thief, "strawberry")+e.qty(w.thief2, "strawberry") != 2 || e.remaining(w.owner, 1) != 3 { // 只發放一次
		t.Fatal("物品不守恆")
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestDuplicateStealRequestsJudgedOnce(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "稻草人"))
	e.ripe(w.owner, 1)
	d := newDice(0.1)
	e.withDice(d)
	res := runConcurrently(6, func() (ActionResult, error) { return e.so.StealCrop(e.ctx, w.g, w.thief, w.owner, 1, 2) })
	blocked, dup := 0, 0
	for _, r := range res {
		switch {
		case r.err == nil && !r.r.Success:
			blocked++
		case gameerr.Is(r.err, gameerr.AlreadyStolen):
			dup++
		default:
			t.Fatalf("%+v", r)
		}
	}
	if blocked != 1 || dup != 5 || d.calls != 1 || len(e.stealOutcomes()) != 1 ||
		e.remaining(w.owner, 1) != 5 || e.qty(w.thief, "strawberry") != 0 {
		t.Fatal(blocked, dup, d.calls)
	}
}

func TestRestartKeepsDevicesAndOutcomes(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "稻草人"))
	e.ok(e.so.PlaceDefense(e.ctx, w.owner, "稻草人", 2, 1))
	e.ripe(w.owner, 2)
	e.withDice(newDice(0))
	if e.steal(w.g, w.thief, w.owner, 2, 1).Success {
		t.Fatal("應該被擋")
	}
	restarted := &SocialService{&FishingService{&ExplorationService{restartedRanch(t, e)}, e.f.FishingRng}}
	restarted.rng = func() float64 { t.Error("不該進行防守判定"); return 0.99 }
	st := must[DefenseStatus](t)(restarted.DefenseStatus(e.ctx, w.owner))
	if len(st.Devices) != 1 || st.Devices[0].Code != "scarecrow" || *st.Devices[0].StartPlot != 2 {
		t.Fatalf("%+v", st.Devices)
	}
	_, err := restarted.StealCrop(e.ctx, w.g, w.thief, w.owner, 2, 1) // 重啟後也不會重新判定
	wantKind(t, err, gameerr.AlreadyStolen)
	if g := must[FarmStatus](t)(restarted.GetFarmStatus(e.ctx, w.owner)).Gold; g != 10_000-priceScarecrow-60 {
		t.Fatal(g)
	}
}

func TestStealTargetsWithDefense(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "警戒犬"))
	e.ripe(w.owner, 1)
	if ts := e.targets(w.g, w.thief); len(ts) != 1 || ts[0].PlotNumber != 1 { // 偷菜者看不出有防守
		t.Fatalf("%+v", ts)
	}
	e.withDice(newDice(0))
	e.steal(w.g, w.thief, w.owner, 1, 1)
	if ts := e.targets(w.g, w.thief); len(ts) != 0 { // 已判定過，不再列出
		t.Fatalf("%+v", ts)
	}
}

// ---------- 防守紀錄與巡查券 ----------

func TestDefenseLogAndPatrol(t *testing.T) {
	e := setup(t)
	w := e.defWorld()
	e.ok(e.so.BuyDefense(e.ctx, w.owner, "稻草人"))
	e.ripe(w.owner, 1)
	e.grow(w.thief2, 1, "wheat") // 另一座農場的偷菜紀錄，不該出現在農夫的巡查中
	e.clock.Advance(60)
	e.withDice(newDice(0))
	e.steal(w.g, w.thief, w.owner, 1, 2)
	e.withDice(newDice(0.9))
	e.steal(w.g, w.thief2, w.owner, 1, 1)
	e.noRoll()
	e.steal(w.g, w.owner, w.thief2, 1, 1)
	log := must[[]DefenseLogRow](t)(e.so.DefenseLog(e.ctx, w.owner, 20))
	if len(log) != 1 || log[0].PlotNumber != 1 || *log[0].BlockedBy != "scarecrow" || log[0].Quantity != 2 {
		t.Fatalf("%+v", log) // DefenseLogRow 沒有偷菜者欄位
	}
	_, err := e.so.Patrol(e.ctx, w.owner)
	wantKind(t, err, gameerr.InsufficientItems)
	e.ok(e.s.BuyItem(e.ctx, w.owner, "patrol_ticket", 2))
	rows := must[[]steals.SearchRow](t)(e.so.Patrol(e.ctx, w.owner))
	var got []string
	for _, r := range rows {
		got = append(got, r.ThiefName+":"+r.Outcome)
		if r.VictimPlayerID != w.owner { // 只能看自己的農場
			t.Fatalf("%+v", r)
		}
	}
	sort.Strings(got)
	if !slices.Equal(got, []string{"小偷:blocked", "小偷二號:success"}) || e.qty(w.owner, "patrol_ticket") != 1 {
		t.Fatal(got)
	}
	e.clock.Advance(balance.PatrolDays*86_400 + 1)
	if rows := must[[]steals.SearchRow](t)(e.so.Patrol(e.ctx, w.owner)); len(rows) != 0 || e.qty(w.owner, "patrol_ticket") != 0 {
		t.Fatalf("%+v", rows) // 超過 7 天的不再顯示
	}
}

// ---------- 狗狗（Python tests/test_pets.py） ----------

func TestDogBlocksStealAfterDefenses(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	e.withDice(newDice(0.1, 0.5))
	owner := e.farmer("飼主", home)
	thief := e.farmer("小偷", home)
	thief2 := e.farmer("小偷二號", home)
	e.setGoldRecorded(owner, 200_000) // 領養 + 升到 Lv4 共 105,000
	e.ok(e.r.BuyPet(e.ctx, owner, petrules.Dog))
	for range 4 {
		e.ok(e.r.UpgradePet(e.ctx, owner, petrules.Dog)) // Lv4：25%
	}
	e.grow(owner, 1, "strawberry")
	e.clock.Advance(600)
	if r := e.steal(home, thief, owner, 1, 1); r.Success || !strings.Contains(r.Message, "狗狗") { // 狗擲 0.1 < 0.25 → 擋下
		t.Fatal(r.Message)
	}
	if r := e.steal(home, thief2, owner, 1, 1); !r.Success || r.Quantity != 1 { // 0.5 ≥ 0.25 → 偷到
		t.Fatalf("%+v", r)
	}
	if e.scalar("SELECT count(*) FROM steal_records WHERE outcome = 'blocked' AND blocked_by = $1 AND abs(block_chance - 0.25) < 1e-9",
		petrules.DogBlocker) != 1 {
		t.Fatal("狗擋下的紀錄不對")
	}
}
