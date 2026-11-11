package services

// 對應 Python tests/test_fishing.py 與 tests/test_fishing_guard.py 的服務部分。
// 設定、釣具與金幣與 Python 一樣經 AdminService 調整。
// 亂數：Python 用 random.Random(7)，Go 的序列不同，斷言只依賴「依提示操作一定釣得起來」等不變的性質。

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/repotest"

	fishrepo "farmgm/internal/repositories/fishing"
)

var bamboo = fishing.Effects{"reel_power": 10, "control_pct": 0, "reaction_bonus_ms": 0}

// settingsWith 預設設定套用 change 後轉成管理網站送來的 JSON 形狀（Python settings_with）。
func settingsWith(change func(*fishing.Settings)) map[string]any {
	s := fishing.DefaultSettings()
	change(&s)
	data, _ := json.Marshal(s)
	raw, _ := featurecfg.Decode(data)
	return raw
}

// setFishingSettings 新增一個設定版本（Python admin.update_fishing_settings）。
func (e *env) setFishingSettings(change func(*fishing.Settings)) {
	e.t.Helper()
	if _, err := e.a.UpdateFishingSettings(e.ctx, "test", settingsWith(change)); err != nil {
		e.t.Fatal(err)
	}
}

func successPct(pct int) func(*fishing.Settings) {
	return func(s *fishing.Settings) { s.BaseSuccessPct = pct }
}

// fisher 固定亂數來源的服務，結果可重現。
func (e *env) fisher(seed uint64) {
	e.f.FishingRng = rand.New(rand.NewPCG(seed, 0)).Float64
}

func (e *env) equipStarter(pid int64, bait int) {
	e.t.Helper()
	e.setGold(pid, 10_000)
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "bamboo_rod", 1))
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "basic_line", 1))
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "worm_bait", bait))
}

func (e *env) fov(pid int64) FishingOverview {
	e.t.Helper()
	return must[FishingOverview](e.t)(e.f.FishingOverview(e.ctx, pid))
}

func (e *env) view(v FishingView, err error) FishingView {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *env) castAndHook(pid int64) FishingView {
	e.t.Helper()
	v := e.view(e.f.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	return e.view(e.f.HookFish(e.ctx, pid, v.ID))
}

func (e *env) fight(pid int64, v FishingView) FishingView {
	e.t.Helper()
	for range 300 {
		if v.Phase != "fighting" {
			return v
		}
		v = e.view(e.f.FishingAction(e.ctx, pid, v.ID, fishing.SmartAction(v.PolicyState(), fishing.Difficulty{}, bamboo), v.Turn))
	}
	e.t.Fatal("拉扯沒有結束")
	return v
}

func (e *env) landOne(pid int64) FishView {
	e.t.Helper()
	r := e.fight(pid, e.castAndHook(pid))
	if r.Status != "landed" {
		e.t.Fatal(r.Status)
	}
	return *r.Fish
}

func (e *env) shop(pid int64) map[string]GearView {
	e.t.Helper()
	out := map[string]GearView{}
	for _, g := range must[FishingShop](e.t)(e.f.FishingShop(e.ctx, pid)).Items {
		out[g.Code] = g
	}
	return out
}

func (e *env) basket(pid int64) FishBasket {
	e.t.Helper()
	return must[FishBasket](e.t)(e.f.FishBasket(e.ctx, pid))
}

// ---------- 釣具商店與裝備 ----------

func TestBuyGearUsesFarmGoldAndAutoEquips(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGold(pid, 1000)
	if r := e.ok(e.f.BuyFishingGear(e.ctx, pid, "bamboo_rod", 1)); r.Gold != 900 || r.GoldDelta != -100 {
		t.Fatalf("%+v", r)
	}
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "basic_line", 1))
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "worm_bait", 5))
	ov := e.fov(pid)
	if ov.Rod.Name != "竹製魚竿" || ov.Line.Name != "普通魚線" || ov.BaitQuantity != 5 || ov.Gold != 1000-100-50-15 {
		t.Fatalf("%+v", ov)
	}
	_, err := e.f.BuyFishingGear(e.ctx, pid, "bamboo_rod", 1)
	wantKind(t, err, gameerr.AlreadyExists)
	_, err = e.f.BuyFishingGear(e.ctx, pid, "iron_rod", 1) // 需要 Lv.4
	wantKind(t, err, gameerr.Validation)
	shop := e.shop(pid)
	if !shop["bamboo_rod"].Owned || !shop["bamboo_rod"].Equipped || shop["iron_rod"].Unlocked || shop["worm_bait"].Quantity != 5 {
		t.Fatalf("%+v", shop["bamboo_rod"])
	}
}

func TestBuyGearChecksGoldStockAndRecords(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 50)
	_, err := e.f.BuyFishingGear(e.ctx, pid, "bamboo_rod", 1)
	wantKind(t, err, gameerr.InsufficientGold)
	if _, err := e.a.UpdateFishingGear(e.ctx, "test", "worm_bait", map[string]any{
		"name": "普通蚯蚓", "emoji": "🪱", "description": "", "price": 3, "required_level": 1, "stock": 2,
		"effects": map[string]any{"rare_bp": 0, "epic_bp": 0, "legendary_bp": 0, "bite_speed_pct": 0}, "sort_order": 90,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = e.f.BuyFishingGear(e.ctx, pid, "worm_bait", 3)
	wantKind(t, err, gameerr.InsufficientItems)
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "worm_bait", 2))
	if s := e.shop(pid)["worm_bait"].Stock; s == nil || *s != 0 {
		t.Fatal(s)
	}
	if e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'fishing_buy' AND gold_delta = -6 AND quantity = 2") != 1 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'fishing_buy'") != 1 {
		t.Fatal("交易紀錄不對")
	}
}

func TestEquipSwitchesWithinKind(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGold(pid, 10_000)
	e.setFarm(pid, "level", 3)
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "bamboo_rod", 1))
	e.ok(e.f.BuyFishingGear(e.ctx, pid, "wooden_rod", 1))
	if e.fov(pid).Rod.Code != "bamboo_rod" { // 已有裝備時不自動換
		t.Fatal(e.fov(pid).Rod.Code)
	}
	e.ok(e.f.EquipFishingGear(e.ctx, pid, "wooden_rod"))
	if shop := e.shop(pid); !shop["wooden_rod"].Equipped || shop["bamboo_rod"].Equipped {
		t.Fatal("沒有切換")
	}
	_, err := e.f.EquipFishingGear(e.ctx, pid, "pro_rod")
	wantKind(t, err, gameerr.NotFound)
}

// ---------- 拋竿、咬鉤與拉扯 ----------

func TestCastRequiresGearAndDeductsOneBait(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.f.StartFishing(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒有裝備")
	e.equipStarter(pid, 3)
	v := e.view(e.f.StartFishing(e.ctx, pid))
	if v.Phase != "waiting" || e.fov(pid).BaitQuantity != 2 {
		t.Fatalf("%+v", v)
	}
	_, err = e.f.StartFishing(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "已經在釣魚")
	if e.fov(pid).BaitQuantity != 2 { // 失敗的拋竿不扣餌
		t.Fatal(e.fov(pid).BaitQuantity)
	}
	_, err = e.f.HookFish(e.ctx, pid, v.ID)
	wantMsg(t, err, gameerr.Validation, "還沒上鉤")
}

func TestReactionTimeoutMeansFishEscapes(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	v := e.view(e.f.StartFishing(e.ctx, pid))
	e.clock.Set(v.ReactDeadline)
	e.clock.Advance(fishing.ReactionGraceSeconds + 1)
	late := e.view(e.f.HookFish(e.ctx, pid, v.ID))
	if late.Status != "missed" || late.Fish != nil || e.scalar("SELECT count(*) FROM farm_fish") != 0 {
		t.Fatalf("%+v", late)
	}
	if v := e.view(e.f.StartFishing(e.ctx, pid)); v.Phase != "waiting" { // 可以再拋竿
		t.Fatal(v.Phase)
	}
}

func TestSensiblePlayLandsFishOnce(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	pid := e.player()
	e.setFishingSettings(successPct(100))
	e.equipStarter(pid, 10)
	v := e.castAndHook(pid)
	if v.Phase != "fighting" || v.Turn != 0 {
		t.Fatalf("%+v", v)
	}
	r := e.fight(pid, v)
	if r.Status != "landed" {
		t.Fatal(r.Status)
	}
	var spec fishing.FishSpec
	for _, f := range fishing.InitialFish {
		if f.Code == r.Fish.Code {
			spec = f
		}
	}
	if r.Fish.Price < spec.MinPrice || r.Fish.Price > spec.MaxPrice {
		t.Fatalf("%+v", r.Fish)
	}
	// 重複送出同一個操作：Session 已結束，不會再發魚
	if again := e.view(e.f.FishingAction(e.ctx, pid, r.ID, "reel", r.Turn-1)); again.Status != "landed" {
		t.Fatal(again.Status)
	}
	if e.scalar("SELECT count(*) FROM farm_fish") != 1 || e.basket(pid).Fish[0].ID != r.Fish.ID {
		t.Fatal("重複發魚")
	}
}

func TestFishPriceAbove200CanBeLanded(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	pid := e.player()
	e.setFishingSettings(successPct(100))
	e.exec("UPDATE fish_types SET min_price = 300, max_price = 500")
	e.equipStarter(pid, 10)
	fish := e.landOne(pid)
	if fish.Price < 300 || fish.Price > 500 {
		t.Fatal(fish.Price)
	}
	gold := e.fov(pid).Gold
	if r := e.ok(e.f.SellFish(e.ctx, pid, nil)); r.Gold != gold+fish.Price {
		t.Fatalf("%+v", r)
	}
}

func TestStaleTurnIsNotAppliedTwice(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	pid := e.player()
	e.setFishingSettings(successPct(100))
	e.equipStarter(pid, 10)
	v := e.castAndHook(pid)
	if first := e.view(e.f.FishingAction(e.ctx, pid, v.ID, "wait", 0)); first.Turn != 1 {
		t.Fatal(first.Turn)
	}
	if dup := e.view(e.f.FishingAction(e.ctx, pid, v.ID, "wait", 0)); !dup.Stale || dup.Turn != 1 { // 重複點擊
		t.Fatalf("%+v", dup)
	}
}

func TestActionTimeoutAndGiveUp(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	e.castAndHook(pid)
	e.clock.Advance(float64(fishing.DefaultSettings().ActionTimeoutSeconds) + fishing.ReactionGraceSeconds + 1)
	if st := must[*FishingView](t)(e.f.FishingState(e.ctx, pid)); st.Status != "timeout" {
		t.Fatal(st.Status)
	}
	v := e.view(e.f.StartFishing(e.ctx, pid))
	if g := e.view(e.f.GiveUpFishing(e.ctx, pid, v.ID)); g.Status != "abandoned" {
		t.Fatal(g.Status)
	}
}

func TestFailuresUnderSensiblePlayAreOnlySlips(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	pid := e.player()
	e.setFishingSettings(successPct(50))
	e.equipStarter(pid, 40)
	seen := map[string]int{}
	for range 30 {
		seen[e.fight(pid, e.castAndHook(pid)).Status]++
		if len(e.basket(pid).Fish) > 0 {
			e.ok(e.f.SellFish(e.ctx, pid, nil))
		}
	}
	// 依提示操作時只會因為脫鉤失敗（50% 基礎成功率）
	if len(seen) != 2 || seen["landed"] == 0 || seen["slipped"] == 0 {
		t.Fatal(seen)
	}
}

func TestSettingsChangeDoesNotAffectActiveSession(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	v := e.view(e.f.StartFishing(e.ctx, pid))
	e.setFishingSettings(func(s *fishing.Settings) { s.ReactionSeconds, s.ActionTimeoutSeconds = 15, 60 })
	if e.scalar("SELECT settings_version FROM fishing_sessions WHERE id = $1", v.ID) != 0 ||
		e.scalar("SELECT (settings->>'reaction_seconds')::int FROM fishing_sessions WHERE id = $1", v.ID) != 5 {
		t.Fatal("進行中的 Session 被新設定影響")
	}
	e.view(e.f.GiveUpFishing(e.ctx, pid, v.ID))
	v2 := e.castAndHook(pid)
	if e.scalar("SELECT (settings->>'reaction_seconds')::int FROM fishing_sessions WHERE id = $1", v2.ID) != 15 {
		t.Fatal("新 Session 沒用新設定")
	}
}

func TestBasketFullBlocksCasting(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setFishingSettings(func(s *fishing.Settings) { s.BasketCapacity = 5 })
	e.equipStarter(pid, 10)
	v := e.view(e.f.StartFishing(e.ctx, pid))
	for i := range 5 { // 直接放 5 隻魚進魚簍
		e.exec(`INSERT INTO fishing_sessions (farm_id, status, settings_version, settings, gear, fish_code, rarity,
			weight_g, price, state, turns, started_at, bite_at, react_deadline, ended_at)
			SELECT farm_id, 'landed', 0, settings, gear, 'loach', 'common', 100, 5, state, 1,
			started_at - interval '1 hour' - make_interval(secs => $2), bite_at - interval '1 hour',
			react_deadline - interval '1 hour', now() FROM fishing_sessions WHERE id = $1`, v.ID, i)
	}
	e.exec(`INSERT INTO farm_fish (farm_id, session_id, fish_code, rarity, weight_g, price, caught_at)
		SELECT farm_id, id, 'loach', 'common', 100, 5, now() FROM fishing_sessions WHERE status = 'landed'`)
	e.view(e.f.GiveUpFishing(e.ctx, pid, v.ID))
	_, err := e.f.StartFishing(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "魚簍已滿")
}

func TestStateKeepsUnknownFields(t *testing.T) {
	// Python 另外存在 state 的欄位，經過 Go 的拉扯操作後不能遺失
	e := setup(t)
	e.fisher(7)
	pid := e.player()
	e.setFishingSettings(successPct(100))
	e.equipStarter(pid, 10)
	v := e.castAndHook(pid)
	e.exec(`UPDATE fishing_sessions SET state = (state::jsonb || '{"py_extra": {"a": 1}}')::json WHERE id = $1`, v.ID)
	e.view(e.f.FishingAction(e.ctx, pid, v.ID, "wait", 0))
	if e.scalar("SELECT (state->'py_extra'->>'a')::int FROM fishing_sessions WHERE id = $1", v.ID) != 1 ||
		e.scalar("SELECT (state->>'turn')::int FROM fishing_sessions WHERE id = $1", v.ID) != 1 {
		t.Fatal("state 的未知欄位遺失")
	}
}

// ---------- 販售與權限 ----------

func TestSellFishAddsGoldOnce(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	pid := e.player()
	e.setFishingSettings(successPct(100))
	e.equipStarter(pid, 10)
	a, b := e.landOne(pid), e.landOne(pid)
	gold := e.fov(pid).Gold
	if r := e.ok(e.f.SellFish(e.ctx, pid, []int64{a.ID})); r.Gold != gold+a.Price {
		t.Fatalf("%+v", r)
	}
	_, err := e.f.SellFish(e.ctx, pid, []int64{a.ID}) // 已賣出
	wantKind(t, err, gameerr.Validation)
	if r := e.ok(e.f.SellFish(e.ctx, pid, nil)); r.Gold != gold+a.Price+b.Price {
		t.Fatalf("%+v", r)
	}
	_, err = e.f.SellFish(e.ctx, pid, nil)
	wantKind(t, err, gameerr.Validation)
	if e.scalar("SELECT sum(gold_delta) FROM game_transactions WHERE event_type = 'fish_sell'") != a.Price+b.Price {
		t.Fatal("販售金額不對")
	}
	if must[FishCodex](t)(e.f.FishCodex(e.ctx, pid)).Discovered < 1 { // 賣掉的魚仍在圖鑑
		t.Fatal("圖鑑沒有紀錄")
	}
}

func TestCannotSellOrShareOtherPlayersFish(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	e.setFishingSettings(successPct(100))
	e.equipStarter(pid, 10)
	fish := e.landOne(pid)
	other := e.farmer("小華", home)
	_, err := e.f.SellFish(e.ctx, other, []int64{fish.ID})
	wantKind(t, err, gameerr.Validation)
	_, err = e.f.InGroupContext(&home).ShareFish(e.ctx, other, fish.ID)
	wantKind(t, err, gameerr.NotFound)
	if e.basket(pid).Fish[0].ID != fish.ID {
		t.Fatal("魚不見了")
	}
}

func TestShareFishKeepsFishAndNeedsGroup(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	e.setFishingSettings(successPct(100))
	e.equipStarter(pid, 10)
	fish := e.landOne(pid)
	_, err := e.f.ShareFish(e.ctx, pid, fish.ID) // 沒有群組（私訊）
	wantMsg(t, err, gameerr.Validation, "伺服器")
	in := e.f.InGroupContext(&home)
	card := must[SharedFish](t)(in.ShareFish(e.ctx, pid, fish.ID))
	if card.Name != fish.Name || card.PlayerName != "阿明" || !card.Record.Species {
		t.Fatalf("%+v", card)
	}
	_, err = in.ShareFish(e.ctx, pid, fish.ID)
	wantMsg(t, err, gameerr.Validation, "剛剛")
	e.clock.Advance(fishing.ShareCooldownSeconds + 1)
	must[SharedFish](t)(in.ShareFish(e.ctx, pid, fish.ID))
	if len(e.basket(pid).Fish) != 1 || e.scalar("SELECT count(*) FROM fish_shares") != 2 { // 分享不消耗
		t.Fatal("分享結果不對")
	}
	// 不是成員的群組不記錄為分享群組
	other := e.group("別的群組")
	_, err = e.f.InGroupContext(&other).ShareFish(e.ctx, pid, fish.ID)
	wantMsg(t, err, gameerr.Validation, "伺服器")
}

func TestFishingLeaderboardAndCodex(t *testing.T) {
	e := setup(t)
	e.fisher(7)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	e.setFishingSettings(successPct(100))
	e.equipStarter(pid, 10)
	e.landOne(pid)
	board := must[fishrepo.Leaderboard](t)(e.f.FishingLeaderboard(e.ctx, &home))
	if board.Catches[0].PlayerName != "阿明" || board.Catches[0].Count != 1 || board.Best[0].PlayerName != "阿明" {
		t.Fatalf("%+v", board)
	}
	empty := e.group("空群組")
	if b := must[fishrepo.Leaderboard](t)(e.f.FishingLeaderboard(e.ctx, &empty)); len(b.Catches) != 0 {
		t.Fatalf("%+v", b)
	}
}

// ---------- Session 清理與併發 ----------

func TestCleanupStaleSessions(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	e.view(e.f.StartFishing(e.ctx, pid))
	if n := must[int](t)(e.f.CleanupFishingSessions(e.ctx)); n != 0 {
		t.Fatal(n)
	}
	e.clock.Advance(120)
	if n := must[int](t)(e.f.CleanupFishingSessions(e.ctx)); n != 1 {
		t.Fatal(n)
	}
	if st := must[*FishingView](t)(e.f.FishingState(e.ctx, pid)); st.Status != "missed" {
		t.Fatal(st.Status)
	}
}

func TestConcurrentCastsDeductBaitOnce(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	var mu sync.Mutex
	var wg sync.WaitGroup
	ok, errs := 0, 0
	start := make(chan struct{})
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.f.StartFishing(e.ctx, pid)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else {
				errs++
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok != 1 || errs != 5 || e.fov(pid).BaitQuantity != 9 {
		t.Fatal(ok, errs, e.fov(pid).BaitQuantity)
	}
}

func TestManyPlayersFishAtTheSameTime(t *testing.T) {
	e := setup(t)
	var rngMu sync.Mutex
	r := rand.New(rand.NewPCG(3, 0))
	e.f.FishingRng = func() float64 { rngMu.Lock(); defer rngMu.Unlock(); return r.Float64() }
	home := e.group("主群組")
	e.setFishingSettings(func(s *fishing.Settings) { s.BaseSuccessPct, s.ReactionSeconds = 100, 15 })
	var pids []int64
	for i := range 8 {
		pid := e.farmer("玩家"+string(rune('0'+i)), home)
		e.equipStarter(pid, 10)
		pids = append(pids, pid)
	}
	views := map[int64]FishingView{}
	var latest time.Time
	for _, pid := range pids {
		views[pid] = e.view(e.f.StartFishing(e.ctx, pid))
		if views[pid].BiteAt.After(latest) {
			latest = views[pid].BiteAt
		}
	}
	e.clock.Set(latest)
	e.clock.Advance(0.1)
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := map[int64]string{}
	var failures []error
	for _, pid := range pids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := e.f.HookFish(e.ctx, pid, views[pid].ID)
			for err == nil && v.Phase == "fighting" {
				v, err = e.f.FishingAction(e.ctx, pid, v.ID, fishing.SmartAction(v.PolicyState(), fishing.Difficulty{}, bamboo), v.Turn)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			results[pid] = v.Status
		}()
	}
	wg.Wait()
	if len(failures) > 0 || len(results) != 8 {
		t.Fatal(failures, results)
	}
	for _, s := range results {
		if s != "landed" {
			t.Fatal(results)
		}
	}
}

// ---------- 防腳本（Python tests/test_fishing_guard.py） ----------

func guardOn(t *testing.T) {
	fishing.GuardEnabled = true // setup 的 Cleanup 會還原
}

type guardRowT struct {
	CastsSinceCheck, CheckAfter, Suspicion, FailedTotal, PassedTotal int
	Answer                                                           *string
	Options                                                          []string
}

func (e *env) guard() guardRowT {
	e.t.Helper()
	var g guardRowT
	var options *string
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		return tx.QueryRow(ctx, `SELECT casts_since_check, check_after, suspicion, failed_total, passed_total,
			challenge->>'answer', (challenge->'options')::text FROM fishing_guards`).
			Scan(&g.CastsSinceCheck, &g.CheckAfter, &g.Suspicion, &g.FailedTotal, &g.PassedTotal, &g.Answer, &options)
	})
	if options != nil {
		for _, s := range strings.Split(strings.Trim(*options, "[]"), ",") {
			g.Options = append(g.Options, strings.Trim(strings.TrimSpace(s), `"`))
		}
	}
	return g
}

func (e *env) forceCheck(suspicion int) {
	e.t.Helper()
	e.exec("UPDATE fishing_guards SET casts_since_check = check_after, suspicion = $1", suspicion)
}

func (e *env) wrongAnswer() string {
	g := e.guard()
	for _, c := range g.Options {
		if c != *g.Answer {
			return c
		}
	}
	e.t.Fatal("沒有錯的選項")
	return ""
}

func TestPeriodicCheckBlocksCastWithoutUsingBait(t *testing.T) {
	e := setup(t)
	guardOn(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	v := e.view(e.f.StartFishing(e.ctx, pid))
	if v.Phase != "waiting" || e.guard().CastsSinceCheck != 1 {
		t.Fatalf("%+v", v)
	}
	e.view(e.f.GiveUpFishing(e.ctx, pid, v.ID))
	e.forceCheck(0)
	bait := e.fov(pid).BaitQuantity
	check := e.view(e.f.StartFishing(e.ctx, pid))
	if check.Phase != "check" || len(check.Options) != fishing.GuardOptions || e.fov(pid).BaitQuantity != bait { // 沒有拋竿，不扣魚餌
		t.Fatalf("%+v", check)
	}
	found := false
	for _, c := range fishing.GuardCreatures {
		found = found || c.Name == check.Target
	}
	if !found {
		t.Fatal(check.Target) // 題目只有中文名稱，選項只有 emoji
	}
	if again := e.view(e.f.StartFishing(e.ctx, pid)); again.Target != check.Target { // 再按一次拿到同一題
		t.Fatal("換題了")
	}
}

func TestCorrectAnswerCastsAndResets(t *testing.T) {
	e := setup(t)
	guardOn(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	e.view(e.f.GiveUpFishing(e.ctx, pid, e.view(e.f.StartFishing(e.ctx, pid)).ID))
	e.forceCheck(9)
	e.view(e.f.StartFishing(e.ctx, pid))
	if v := e.view(e.f.AnswerFishingCheck(e.ctx, pid, *e.guard().Answer)); v.Phase != "waiting" {
		t.Fatal(v.Phase)
	}
	g := e.guard()
	lo, hi := fishing.GuardCheckEveryCasts[0], fishing.GuardCheckEveryCasts[1]
	if g.Answer != nil || g.Suspicion != 0 || g.CastsSinceCheck != 1 || g.PassedTotal != 1 || g.CheckAfter < lo || g.CheckAfter > hi {
		t.Fatalf("%+v", g)
	}
}

func TestWrongAnswerLocksFishingButCostsNothing(t *testing.T) {
	e := setup(t)
	guardOn(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	e.view(e.f.GiveUpFishing(e.ctx, pid, e.view(e.f.StartFishing(e.ctx, pid)).ID))
	e.forceCheck(0)
	e.view(e.f.StartFishing(e.ctx, pid))
	before := e.fov(pid)
	wrong := e.wrongAnswer()
	failed := e.view(e.f.AnswerFishingCheck(e.ctx, pid, wrong))
	if failed.Phase != "check_failed" || failed.Minutes != fishing.GuardLockMinutes {
		t.Fatalf("%+v", failed)
	}
	if after := e.fov(pid); after.Gold != before.Gold || after.BaitQuantity != before.BaitQuantity {
		t.Fatal("答錯扣了資產")
	}
	_, err := e.f.StartFishing(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "暫停")
	_, err = e.f.AnswerFishingCheck(e.ctx, pid, wrong) // 不能靠重複送出答題繞過暫停
	wantMsg(t, err, gameerr.Validation, "暫停")
	// 時間到後要重新確認，連續答錯暫停加倍
	e.clock.Advance(fishing.GuardLockMinutes*60 + 1)
	if v := e.view(e.f.StartFishing(e.ctx, pid)); v.Phase != "check" {
		t.Fatal(v.Phase)
	}
	if v := e.view(e.f.AnswerFishingCheck(e.ctx, pid, e.wrongAnswer())); v.Minutes != fishing.GuardLockMinutes*2 {
		t.Fatal(v.Minutes)
	}
}

func TestExpiredChallengeGetsANewOneWithoutPenalty(t *testing.T) {
	e := setup(t)
	guardOn(t)
	pid := e.player()
	e.equipStarter(pid, 10)
	e.view(e.f.GiveUpFishing(e.ctx, pid, e.view(e.f.StartFishing(e.ctx, pid)).ID))
	e.forceCheck(0)
	e.view(e.f.StartFishing(e.ctx, pid))
	e.clock.Advance(fishing.GuardChallengeSeconds + 1)
	v := e.view(e.f.AnswerFishingCheck(e.ctx, pid, "anything"))
	if v.Phase != "check" || !strings.Contains(v.Note, "過期") || e.guard().FailedTotal != 0 {
		t.Fatalf("%+v", v)
	}
}

func TestInhumanTimingTriggersCheckButNormalPlayDoesNot(t *testing.T) {
	e := setup(t)
	guardOn(t)
	pid := e.player()
	e.setFishingSettings(func(s *fishing.Settings) { s.BaseSuccessPct, s.ReactionSeconds = 100, 15 })
	e.equipStarter(pid, 20)
	// 正常操作：上鉤 1 秒後拉竿，每步間隔不規律且超過門檻
	v := e.view(e.f.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	e.clock.Advance(1)
	v = e.view(e.f.HookFish(e.ctx, pid, v.ID))
	for _, gap := range []float64{1.2, 3.0, 0.9, 2.2} {
		e.clock.Advance(gap)
		v = e.view(e.f.FishingAction(e.ctx, pid, v.ID, "wait", v.Turn))
	}
	e.view(e.f.GiveUpFishing(e.ctx, pid, v.ID))
	if s := e.guard().Suspicion; s != 0 {
		t.Fatal(s)
	}
	// 腳本：一上鉤就拉竿，之後每步都立刻送出
	v = e.view(e.f.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	v = e.view(e.f.HookFish(e.ctx, pid, v.ID))
	if s := e.guard().Suspicion; s != fishing.GuardScore["fast_hook"] {
		t.Fatal(s)
	}
	for range 4 {
		v = e.view(e.f.FishingAction(e.ctx, pid, v.ID, "wait", v.Turn))
	}
	e.view(e.f.GiveUpFishing(e.ctx, pid, v.ID))
	if s := e.guard().Suspicion; s < fishing.GuardSuspicionLimit {
		t.Fatal(s)
	}
	if v := e.view(e.f.StartFishing(e.ctx, pid)); v.Phase != "check" {
		t.Fatal(v.Phase)
	}
}

func TestDuplicateClickDoesNotCountAsFastAction(t *testing.T) {
	e := setup(t)
	guardOn(t)
	pid := e.player()
	e.setFishingSettings(func(s *fishing.Settings) { s.BaseSuccessPct, s.ReactionSeconds = 100, 15 })
	e.equipStarter(pid, 10)
	v := e.view(e.f.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	e.clock.Advance(2)
	v = e.view(e.f.HookFish(e.ctx, pid, v.ID))
	e.clock.Advance(2)
	e.view(e.f.FishingAction(e.ctx, pid, v.ID, "wait", 0))
	e.view(e.f.FishingAction(e.ctx, pid, v.ID, "wait", 0)) // 連點：畫面還沒更新
	if s := e.guard().Suspicion; s != 0 {
		t.Fatal(s)
	}
}

func TestSettingsValidationRequires100Percent(t *testing.T) {
	e := setup(t)
	bad := settingsWith(func(s *fishing.Settings) {
		s.RarityBP = map[string]int{"common": 8000, "rare": 1500, "epic": 400, "legendary": 50}
	})
	_, err := e.a.UpdateFishingSettings(e.ctx, "test", bad)
	wantMsg(t, err, gameerr.Validation, "100%")
	_, err = e.a.UpdateFishingSettings(e.ctx, "test", settingsWith(successPct(120)))
	wantKind(t, err, gameerr.Validation)
}
