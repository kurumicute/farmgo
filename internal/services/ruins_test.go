package services

// 對應 Python tests/test_ruins.py 的服務部分（純規則測試已在 game/ruins、game/exploration 移植）。

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/ruins"
)

// smallStages Python 的 small 夾具：縮小三個階段需要的素材。
func smallStages(t *testing.T) {
	old := ruins.Stages
	ruins.Stages = []ruins.Stage{
		{Name: "清除碎石", Materials: map[string]int{"stone": 5, "wood": 5}, MaterialOrder: []string{"stone", "wood"}},
		{Name: "修補石柱", Materials: map[string]int{"copper_ore": 3}, MaterialOrder: []string{"copper_ore"}},
		{Name: "喚醒遺跡核心", Materials: map[string]int{"gear": 2}, MaterialOrder: []string{"gear"}},
	}
	t.Cleanup(func() { ruins.Stages = old })
}

func (e *env) have(pid int64, code string) int {
	e.t.Helper()
	return e.scalar(`SELECT coalesce(sum(i.quantity), 0) FROM inventory_items i JOIN farms f ON f.id = i.farm_id
		WHERE f.player_id = $1 AND i.item_code = $2 AND i.quality = 'normal' AND i.mutation = 'none'`, pid, code)
}

func (e *env) ruinsStatus(pid int64) RuinsStatus {
	e.t.Helper()
	return must[RuinsStatus](e.t)(e.x.RuinsStatus(e.ctx, pid))
}

func (e *env) repair(pid int64) {
	e.t.Helper()
	for _, m := range []struct {
		code string
		n    int
	}{{"stone", 5}, {"wood", 5}, {"copper_ore", 3}, {"gear", 2}} {
		e.give(pid, m.code, m.n)
		e.ok(e.x.DonateRuins(e.ctx, pid, m.code, m.n))
	}
}

func (e *env) ruinsArea(pid int64) AreaView {
	e.t.Helper()
	for _, a := range e.overview(pid).Areas {
		if a.Code == "ruins" {
			return a
		}
	}
	e.t.Fatal("沒有古代遺跡")
	return AreaView{}
}

func TestDonatePartialStageAdvanceAndCompletion(t *testing.T) {
	e := setup(t)
	smallStages(t)
	home := e.group("主群組")
	a := e.farmer("阿明", home)
	b := e.farmer("小華", home)
	e.give(a, "stone", 10)
	e.give(b, "wood", 5)
	e.give(b, "copper_ore", 3)
	e.give(a, "gear", 2)

	st := e.ruinsStatus(a)
	if st.Stage != 0 || st.Percent != 0 || st.Completed || st.Mine != 0 || len(st.Materials) != 2 ||
		st.Materials[0] != (RuinsMaterial{"stone", "石塊", 5, 0, 10}) || st.Materials[1] != (RuinsMaterial{"wood", "木材", 5, 0, 0}) {
		t.Fatalf("%+v", st)
	}
	r := e.ok(e.x.DonateRuins(e.ctx, a, "stone", 8)) // 只差 5 個：只捐 5，其餘留在背包
	if r.Quantity != 5 || !strings.Contains(r.Message, "其餘留在背包") || e.have(a, "stone") != 5 {
		t.Fatal(r.Message)
	}
	_, err := e.x.DonateRuins(e.ctx, a, "stone", 1)
	wantMsg(t, err, gameerr.Validation, "已經夠了")
	_, err = e.x.DonateRuins(e.ctx, a, "gear", 1) // 不是目前階段的素材
	wantMsg(t, err, gameerr.Validation, "只需要")
	_, err = e.x.DonateRuins(e.ctx, a, "wood", 1)
	wantKind(t, err, gameerr.InsufficientItems)

	if r := e.ok(e.x.DonateRuins(e.ctx, b, "wood", 5)); !strings.Contains(r.Message, "下一階段") || e.ruinsStatus(a).Stage != 1 {
		t.Fatal(r.Message)
	}
	e.ok(e.x.DonateRuins(e.ctx, b, "copper_ore", 3))
	if r := e.ok(e.x.DonateRuins(e.ctx, a, "gear", 2)); !strings.Contains(r.Message, "修復完成") {
		t.Fatal(r.Message)
	}
	st = e.ruinsStatus(b)
	if !st.Completed || st.Percent != 100 || !st.CompletedAt.Equal(e.clock.Now()) {
		t.Fatalf("%+v", st)
	}
	var top []string
	for _, c := range st.Top {
		top = append(top, c.Name+":"+strconv.Itoa(c.Quantity))
	}
	if !slices.Equal(top, []string{"小華的農場:8", "阿明的農場:7"}) {
		t.Fatal(top)
	}
	_, err = e.x.DonateRuins(e.ctx, a, "stone", 1)
	wantMsg(t, err, gameerr.Validation, "已經修復完成")
	// 出過力的成員都拿到稱號；交易紀錄有捐獻、金幣不變式成立
	if e.scalar("SELECT count(*) FROM farm_titles WHERE title = $1 AND source = $2", ruins.Title, ruins.TitleSource) != 2 ||
		e.scalar("SELECT count(*) FROM farm_titles") != 2 ||
		e.scalar("SELECT sum(quantity) FROM game_transactions WHERE event_type = 'ruins_donate'") != 15 || !e.goldMatchesTransactions() {
		t.Fatal("稱號或交易紀錄不對")
	}
}

func TestContributorWithoutCompletionGetsNoTitle(t *testing.T) {
	e := setup(t)
	smallStages(t)
	a := e.farmer("阿明", e.group("主群組"))
	e.give(a, "stone", 5)
	e.ok(e.x.DonateRuins(e.ctx, a, "stone", 5))
	if e.scalar("SELECT count(*) FROM farm_titles") != 0 {
		t.Fatal("沒修完卻拿到稱號")
	}
}

func TestRuinsAreaRequiresOwnGroupRepaired(t *testing.T) {
	e := setup(t)
	smallStages(t)
	e.s.exploreRng = func() float64 { return 0 }
	a := e.farmer("阿明", e.group("主群組"))
	c := e.farmer("小美", e.group("別的群組"))
	e.setFarm(a, "level", 4)
	e.setFarm(c, "level", 4)
	_, err := e.x.Explore(e.ctx, a, "ruins")
	wantMsg(t, err, gameerr.Validation, "還沒修好")
	if area := e.ruinsArea(a); area.Unlocked || !area.NeedsRuins || area.RuinsOpen {
		t.Fatalf("%+v", area)
	}
	e.repair(a)
	_, err = e.x.Explore(e.ctx, c, "ruins") // 別的群組沒修，不能進
	wantMsg(t, err, gameerr.Validation, "還沒修好")
	e.setFarm(a, "level", 3)
	_, err = e.x.Explore(e.ctx, a, "ruins")
	wantMsg(t, err, gameerr.Validation, "Lv.4")
	e.setFarm(a, "level", 4)
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, a, "grassland")) // 一般探險進行中，遺跡照樣可以額外探索
	r := must[ExploreOutcome](t)(e.x.Explore(e.ctx, a, "古代遺跡")).Reward
	if !r.Instant || r.AreaName != "古代遺跡" || r.ExtraRolls != 1 || r.BonusRolls != 0 ||
		len(r.Items) != 2 || r.Items[0] != (ExploreItem{"wood", "木材", 15, "普通"}) || // 森林模板 4 份 + 多 1 份
		r.Items[1] != (ExploreItem{"sawdust", "木屑", 2, "普通"}) { // Go 版新增：另外帶回木屑
		t.Fatalf("%+v", r)
	}
	if e.have(a, "wood") != 15 { // 立即放進背包
		t.Fatal(e.have(a, "wood"))
	}
	_, err = e.x.ExploreRuins(e.ctx, a)
	wantMsg(t, err, gameerr.Validation, "今天已經探索過")
	area := e.ruinsArea(a)
	if !area.Unlocked || area.Ready || !area.NextAt.Equal(e.clock.Now().Add(24*time.Hour)) {
		t.Fatalf("%+v", area)
	}
	e.clock.Advance(float64(exploration.Areas["ruins"].DurationSeconds))
	if r := must[ExploreReward](t)(e.x.ExploreRuins(e.ctx, a)); len(r.Items) == 0 { // 冷卻 1 天後可以再探索
		t.Fatal(r)
	}
	if r := must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, a)); r.Area != "grassland" { // 一般探險不受影響
		t.Fatal(r.Area)
	}
}

func TestGuardianAttackDefeatBonusAndCollapse(t *testing.T) {
	e := setup(t)
	smallStages(t)
	e.s.exploreRng = func() float64 { return 0 }
	a := e.farmer("阿明", e.group("主群組"))
	e.setFarm(a, "level", 4)
	e.repair(a) // 2026-01-01（台灣週四）修好
	if g := e.ruinsStatus(a).Guardian; g.Active || g.HPMax != 4000 {
		t.Fatalf("%+v", g)
	}
	e.give(a, "ancient_core", 60)
	_, err := e.x.AttackGuardian(e.ctx, a, "ancient_core", 1)
	wantMsg(t, err, gameerr.Validation, "還沒出現")

	e.clock.Advance(4 * day) // 台灣週一 08:00：守護者出現
	g := e.ruinsStatus(a).Guardian
	if !g.Active || g.Defeated || len(g.Weapons) != 1 || g.Weapons[0].Code != "ancient_core" {
		t.Fatalf("%+v", g)
	}
	_, err = e.x.AttackGuardian(e.ctx, a, exploration.FakeCoin, 1)
	wantMsg(t, err, gameerr.Validation, "假幣")
	if r := e.ok(e.x.AttackGuardian(e.ctx, a, "ancient_core", 10)); !strings.Contains(r.Message, "**800**") ||
		e.ruinsStatus(a).Guardian.Damage != 800 {
		t.Fatal(r.Message)
	}
	r := e.ok(e.x.AttackGuardian(e.ctx, a, "ancient_core", 50)) // 剩 3200 HP：只需要 40 個
	if r.Quantity != 40 || !strings.Contains(r.Message, "倒下了") || e.have(a, "ancient_core") != 10 {
		t.Fatal(r.Message)
	}
	_, err = e.x.AttackGuardian(e.ctx, a, "ancient_core", 1)
	wantMsg(t, err, gameerr.Validation, "已經被打倒")
	trip := must[ExploreOutcome](t)(e.x.Explore(e.ctx, a, "ruins")).Reward
	if trip.BonusRolls != 2 || trip.Items[0].Quantity != 3*(4+1+2) { // 獎勵加倍：多 2 份
		t.Fatalf("%+v", trip)
	}
	if e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'ruins_guardian'") != 2 || !e.goldMatchesTransactions() {
		t.Fatal("攻擊紀錄不對")
	}

	e.clock.Advance(7 * day) // 第二週：探索遺跡自動造成傷害，但沒打倒
	got := must[ExploreReward](t)(e.x.ExploreRuins(e.ctx, a))
	if !strings.Contains(got.GuardianNote, "造成 200 點傷害") || got.BonusRolls != 0 || e.ruinsStatus(a).Guardian.Damage != 200 {
		t.Fatalf("%+v", got)
	}
	e.clock.Advance(7 * day) // 第二週結束：遺跡崩塌回第三階段
	st := e.ruinsStatus(a)
	if st.Stage != 2 || st.Completed || st.CollapsedAt == nil || st.Guardian != nil {
		t.Fatalf("%+v", st)
	}
	_, err = e.x.Explore(e.ctx, a, "ruins")
	wantMsg(t, err, gameerr.Validation, "還沒修好")
	e.give(a, "gear", 2)
	e.ok(e.x.DonateRuins(e.ctx, a, "gear", 2)) // 重新捐核心就修好
	if !e.ruinsStatus(a).Completed {
		t.Fatal("沒有重新修好")
	}
}
