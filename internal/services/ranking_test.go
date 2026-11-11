package services

// 對應 Python tests/test_ranking.py 的服務部分（純規則在 game/ranking；CMD 與網站 API 對應階段再補，
// API 測試中屬於服務層的「改成每月又改回每週仍會結算」改寫為 TestRuleUpdateRestartsFromNow）。
// 測試時鐘起點 2026-01-01 00:00 UTC = 台灣時間週四 08:00；本週為 2025-12-29（一）～ 2026-01-05（一）00:00（台灣時間）。

import (
	"slices"
	"strings"
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/leaderboard"
)

const (
	weekEnd     = 3*86400 + 16*3600 // 從測試時鐘起點到本週結束的秒數
	dayEnd      = 16 * 3600         // 到台灣時間 1/2 00:00
	rankingWait = balance.RankingSettleGraceSeconds + 1
)

// rankWorld 兩個群組：北區（甲、乙）與南區（丙）；北區賣比較多草莓，丙個人賣最多小麥。
type rankWorld struct{ north, south, a, b, c int64 }

func (e *env) rankWorld() rankWorld {
	e.t.Helper()
	w := rankWorld{north: e.group("北區"), south: e.group("南區")}
	w.a, w.b, w.c = e.farmer("甲", w.north), e.farmer("乙", w.north), e.farmer("丙", w.south)
	for _, pid := range []int64{w.a, w.b} {
		e.grow(pid, 1, "strawberry")
	}
	for plot := 1; plot <= 3; plot++ {
		e.grow(w.c, plot, "wheat")
	}
	e.clock.Advance(600)
	for _, pid := range []int64{w.a, w.b} {
		e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
		e.ok(e.s.SellCrop(e.ctx, pid, "strawberry", 5, "normal", "none")) // 各 5 個、125 金幣
	}
	for plot := 1; plot <= 3; plot++ {
		e.ok(e.s.HarvestCrop(e.ctx, w.c, plot))
	}
	e.ok(e.s.SellCrop(e.ctx, w.c, "wheat", 6, "normal", "none")) // 6 個、90 金幣
	return w
}

func reward(rt string, n int) map[string]any { return map[string]any{"reward_type": rt, "amount": n} }

func rankRule(change func(map[string]any)) map[string]any {
	r := map[string]any{"name": "每週群組金幣王", "scope": "group", "metric": "sales", "period": "weekly",
		"rewards": []any{reward("gold", 2000)}}
	if change != nil {
		change(r)
	}
	return r
}

func (e *env) createRule(raw map[string]any) RuleView {
	e.t.Helper()
	return must[RuleView](e.t)(e.rk.CreateRankingRule(e.ctx, "admin", raw))
}

func (e *env) settle() []int64 {
	e.t.Helper()
	return must[[]int64](e.t)(e.rk.SettleDueRankings(e.ctx, "system"))
}

func (e *env) groupScores(kind, view string) map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, r := range must[[]leaderboard.GroupRow](e.t)(e.rk.GroupLeaderboard(e.ctx, kind, view, 10)) {
		out[r.GroupName] = r.Score
	}
	return out
}

func (e *env) playerScores(kind, view string) map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, r := range must[[]leaderboard.Row](e.t)(e.rk.PeriodLeaderboard(e.ctx, kind, view, 10)) {
		out[r.PlayerName] = r.Score
	}
	return out
}

func eqMap(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func TestGroupLeaderboardSumsMembers(t *testing.T) {
	e := setup(t)
	w := e.rankWorld()
	sales := must[[]leaderboard.GroupRow](t)(e.rk.GroupLeaderboard(e.ctx, "sales", "all", 10))
	if len(sales) != 2 || sales[0].GroupName != "北區" || sales[0].MemberCount != 2 || sales[0].Score != 10 ||
		sales[1].GroupName != "南區" || sales[1].MemberCount != 1 || sales[1].Score != 6 {
		t.Fatalf("%+v", sales)
	}
	if !eqMap(e.groupScores("goods", "all"), map[string]int{"北區": 250, "南區": 90}) ||
		e.groupScores("money", "all")["北區"] != e.gold(w.a)+e.gold(w.b) {
		t.Fatal("goods／money 不對")
	}
	// 期間：本週金幣只算賣出農產品的收入，買種子的花費不扣分
	if !eqMap(e.groupScores("money", "week"), map[string]int{"北區": 250, "南區": 90}) {
		t.Fatal(e.groupScores("money", "week"))
	}
	e.clock.Advance(weekEnd + rankingWait) // 下週還沒有任何操作
	if !eqMap(e.groupScores("sales", "week"), map[string]int{"北區": 0, "南區": 0}) || e.groupScores("sales", "all")["北區"] != 10 {
		t.Fatal("下週分數不對")
	}
	_, err := e.rk.GroupLeaderboard(e.ctx, "sales", "year", 10)
	wantKind(t, err, gameerr.Validation)
}

func TestWeeklyGroupAndIndividualRewards(t *testing.T) {
	e := setup(t)
	w := e.rankWorld()
	e.createRule(rankRule(nil))
	e.createRule(rankRule(func(r map[string]any) {
		r["name"], r["scope"] = "每週個人販售", "individual"
		r["rewards"] = []any{reward("gold", 10000), reward("fertilizer", 3)}
	}))
	before := map[int64]int{w.a: e.gold(w.a), w.b: e.gold(w.b), w.c: e.gold(w.c)}
	if len(e.settle()) != 0 { // 本週還沒結束
		t.Fatal("太早結算")
	}
	e.clock.Advance(weekEnd - 600) // 現在剛好是本週結束
	if len(e.settle()) != 0 {      // 仍在結算緩衝時間內
		t.Fatal("緩衝時間內結算")
	}
	e.clock.Advance(rankingWait)
	settled := e.settle()
	if len(settled) != 2 {
		t.Fatal(settled)
	}
	// 群組第 1 名（北區）全體 +2000；個人第 1 名（丙，6 個）+10000；第 2 名（甲，5 個、ID 較小）3 個化肥
	if e.gold(w.a) != before[w.a]+2000 || e.gold(w.b) != before[w.b]+2000 || e.gold(w.c) != before[w.c]+10000 ||
		e.qty(w.a, "fertilizer") != 3 {
		t.Fatal("獎勵不對")
	}
	if len(e.settle()) != 0 || e.scalar("SELECT count(*) FROM ranking_awards") != 4 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'ranking_reward' AND description LIKE '%排名獎勵%'") != 4 {
		t.Fatal("重複發獎")
	}
	d := must[SettlementView](t)(e.rk.RankingSettlementDetail(e.ctx, settled[0]))
	if len(d.Awards) != 2 || d.Awards[0].PlayerName != "甲" || d.Awards[0].Rank != 1 || d.Awards[1].PlayerName != "乙" {
		t.Fatalf("%+v", d.Awards)
	}
	if a := must[[]PlayerRankingAward](t)(e.rk.PlayerRankingAwards(e.ctx, w.c, 10)); a[0].RewardText != "10000 金幣" {
		t.Fatalf("%+v", a)
	}
	// 下一週沒有任何販售：結算但沒有人得獎（分數必須 > 0）
	e.clock.Advance(7 * 86400)
	next := e.settle()
	if len(next) != 2 {
		t.Fatal(next)
	}
	for _, id := range next {
		if must[SettlementView](t)(e.rk.RankingSettlementDetail(e.ctx, id)).AwardCount != 0 {
			t.Fatal("零分得獎")
		}
	}
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestRewardMoneyExcludesRewardsAndAdminAdjust(t *testing.T) {
	e := setup(t)
	w := e.rankWorld()
	e.setGoldRecorded(w.c, 999_999) // 管理員調整不算
	rows := must[[]leaderboard.Row](t)(e.rk.PeriodLeaderboard(e.ctx, "money", "week", 10))
	if (rows[0].PlayerName != "甲" && rows[0].PlayerName != "乙") || rows[0].Score != 125 {
		t.Fatalf("%+v", rows[0])
	}
}

func TestRewardMoneyNotAffectedByBuying(t *testing.T) {
	e := setup(t)
	w := e.rankWorld()
	before := e.playerScores("money", "week")
	e.ok(e.s.BuySeeds(e.ctx, w.a, "wheat", 10)) // 甲花錢買種子，排名分數不變
	after := e.playerScores("money", "week")
	if !eqMap(before, after) || !eqMap(after, map[string]int{"甲": 125, "乙": 125, "丙": 90}) ||
		!eqMap(e.groupScores("money", "week"), map[string]int{"北區": 250, "南區": 90}) {
		t.Fatal(after)
	}
}

func TestDisabledRuleNotBackfilledAndDailyPeriod(t *testing.T) {
	e := setup(t)
	e.rankWorld()
	weekly := e.createRule(rankRule(nil))
	daily := e.createRule(rankRule(func(r map[string]any) { r["name"], r["period"] = "每日", "daily" }))
	must[RuleView](t)(e.rk.SetRankingRuleEnabled(e.ctx, "admin", weekly.ID, false))
	e.clock.Advance(dayEnd + rankingWait)
	if len(e.settle()) != 1 { // 只有每日（1/1）
		t.Fatal("每日沒結算")
	}
	e.clock.Advance(weekEnd) // 跨過本週結束
	must[RuleView](t)(e.rk.SetRankingRuleEnabled(e.ctx, "admin", weekly.ID, true))
	for _, id := range e.settle() { // 每週規則在停用期間結束的本週不回溯；每日規則補結算錯過的天數
		if d := must[SettlementView](t)(e.rk.RankingSettlementDetail(e.ctx, id)); *d.RuleID != daily.ID {
			t.Fatal("每週規則回溯結算")
		}
	}
	for _, r := range must[[]RuleView](t)(e.rk.ListRankingRules(e.ctx)) {
		if r.ID == weekly.ID && !r.Enabled {
			t.Fatal("沒有啟用")
		}
	}
	// 刪除規則後歷史保留
	must[int64](t)(e.rk.DeleteRankingRule(e.ctx, "admin", daily.ID))
	h := must[[]SettlementView](t)(e.rk.RankingSettlements(e.ctx, 30))
	if len(h) == 0 || h[0].RuleID != nil || h[0].RuleName != "每日" {
		t.Fatalf("%+v", h)
	}
	_, err := e.rk.UpdateRankingRule(e.ctx, "admin", daily.ID, rankRule(nil))
	wantKind(t, err, gameerr.NotFound)
}

func TestItemRewardIgnoresWarehouseCapacity(t *testing.T) {
	e := setup(t)
	w := e.rankWorld()
	used := must[Inventory](t)(e.s.GetInventory(e.ctx, w.a)).Used
	e.give(w.a, "seed_wheat", balance.InitialWarehouseCapacity-used) // 倉庫全滿
	e.createRule(rankRule(func(r map[string]any) {
		r["scope"], r["metric"] = "individual", "goods"
		r["rewards"] = []any{reward("earthworm", 2)}
	}))
	e.clock.Advance(weekEnd + rankingWait)
	e.settle()
	if e.qty(w.a, "earthworm") != 2 { // 倉庫已滿仍發放，不會遺失獎勵
		t.Fatal(e.qty(w.a, "earthworm"))
	}
}

// TestRuleUpdateRestartsFromNow Python test_ranking_api 的服務部分：改成每月又改回每週，從修改時間起算，
// 本週在修改之後才結束，所以仍會結算；獎勵文字與名次。
func TestRuleUpdateRestartsFromNow(t *testing.T) {
	e := setup(t)
	e.rankWorld()
	rewards := []any{reward("gold", 2000), reward("patrol_ticket", 1)}
	body := rankRule(func(r map[string]any) { r["rewards"] = rewards })
	rid := e.createRule(body).ID
	_, err := e.rk.CreateRankingRule(e.ctx, "admin", rankRule(func(r map[string]any) { r["rewards"] = []any{} }))
	wantKind(t, err, gameerr.Validation)
	monthly := rankRule(func(r map[string]any) { r["period"], r["rewards"] = "monthly", rewards })
	if v := must[RuleView](t)(e.rk.UpdateRankingRule(e.ctx, "admin", rid, monthly)); v.Period != "monthly" {
		t.Fatal(v.Period)
	}
	must[RuleView](t)(e.rk.UpdateRankingRule(e.ctx, "admin", rid, body))
	e.clock.Advance(weekEnd + rankingWait)
	settled := e.settle()
	if len(settled) != 1 {
		t.Fatal(settled)
	}
	if rules := must[[]RuleView](t)(e.rk.ListRankingRules(e.ctx)); rules[0].RewardsText != "第 1 名 2000 金幣；第 2 名 1 個巡查券" {
		t.Fatal(rules[0].RewardsText)
	}
	list := must[[]SettlementView](t)(e.rk.RankingSettlements(e.ctx, 30))
	d := must[SettlementView](t)(e.rk.RankingSettlementDetail(e.ctx, settled[0]))
	var ranks []int
	for _, a := range d.Awards {
		ranks = append(ranks, a.Rank)
	}
	if list[0].AwardCount != 3 || !slices.Equal(ranks, []int{1, 1, 2}) || !strings.HasPrefix(list[0].Summary, "群組｜農產品販售｜") {
		t.Fatalf("%+v %v", list[0], ranks)
	}
	_, err = e.rk.RankingSettlementDetail(e.ctx, 999)
	wantKind(t, err, gameerr.NotFound)
}

// TestBackgroundSettlementLock Go 版補充：advisory lock 被占用時不結算，回傳 nil。
func TestBackgroundSettlementLock(t *testing.T) {
	e := setup(t)
	e.rankWorld()
	e.createRule(rankRule(nil))
	e.clock.Advance(weekEnd + rankingWait)
	conn, err := e.d.Pool.Acquire(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(e.ctx, "SELECT pg_advisory_lock($1)", SettlementLockKey); err != nil {
		t.Fatal(err)
	}
	if r := must[*BackgroundSettlement](t)(e.rk.RunBackgroundSettlement(e.ctx, "system")); r != nil {
		t.Fatal("鎖被占用時仍結算")
	}
	_, _ = conn.Exec(e.ctx, "SELECT pg_advisory_unlock($1)", SettlementLockKey)
	conn.Release()
	if r := must[*BackgroundSettlement](t)(e.rk.RunBackgroundSettlement(e.ctx, "system")); r == nil || len(r.Rankings) != 1 {
		t.Fatalf("%+v", r)
	}
}

// ---------- 之前延後的測試：群組轉移與任務計分、群組排行（Python tests/test_group_transfer.py） ----------

func TestTransferDoesNotMoveQuestProgress(t *testing.T) {
	e := setup(t)
	a, b, _ := e.groups3()
	alice := e.farmer("Alice", a)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice))
	qa := e.quest(a, func(q *NewQuest) { q.Name, q.Target = "A 收成", 100 })
	qb := e.quest(b, func(q *NewQuest) { q.Name, q.Target = "B 收成", 100 })
	e.clock.Advance(1)
	e.harvestIn(alice, a, 1)
	progressA := e.progress(a, alice, qa)
	if progressA <= 0 {
		t.Fatal(progressA)
	}
	e.ok(e.s.TransferHomeGroup(e.ctx, alice, b))
	for _, g := range []int64{a, b} { // 在舊群組操作不再計分；在新群組操作計入新群組
		e.harvestIn(alice, g, 1)
	}
	if e.progress(a, alice, qa) != progressA || e.progress(b, alice, qb) != progressA {
		t.Fatal(e.progress(a, alice, qa), e.progress(b, alice, qb))
	}
}

func TestTransferUpdatesGroupLeaderboard(t *testing.T) {
	e := setup(t)
	a, b, _ := e.groups3()
	alice := e.farmer("Alice", a)
	e.farmer("Anna", a)
	e.farmer("Bob", b)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice))
	e.ok(e.s.TransferHomeGroup(e.ctx, alice, b))
	totals := map[int64]int{}
	for _, r := range must[[]leaderboard.GroupRow](t)(e.rk.GroupLeaderboard(e.ctx, "money", "all", 10)) {
		totals[r.GroupID] = r.MemberCount
	}
	if len(totals) != 2 || totals[a] != 1 || totals[b] != 2 {
		t.Fatal(totals)
	}
}
