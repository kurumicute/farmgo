package services

// 對應 Python tests/test_community.py（CMD 與網站管理 API 對應階段再補）。

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/repositories/transactions"
)

const dayDur = 24 * time.Hour

var settleGrace = time.Duration(balance.CompetitionSettleGraceSeconds) * time.Second

// commWorld A 群：alice、carol（主要群組 A）；B 群：bob、dave（主要群組 B）。alice 也加入 B，dave 也加入 A。
type commWorld struct{ a, b, alice, carol, bob, dave int64 }

func (e *env) commWorld() commWorld {
	e.t.Helper()
	w := commWorld{a: e.group("A 群"), b: e.group("B 群")}
	w.alice, w.carol = e.farmer("alice", w.a), e.farmer("carol", w.a)
	w.bob, w.dave = e.farmer("bob", w.b), e.farmer("dave", w.b)
	must[bool](e.t)(e.so.AddMember(e.ctx, w.b, w.alice))
	must[bool](e.t)(e.so.AddMember(e.ctx, w.a, w.dave))
	e.exec("UPDATE farms SET gold = 10000")
	return w
}

func (e *env) quest(groupID int64, change func(*NewQuest)) int64 {
	e.t.Helper()
	now := e.clock.Now()
	q := NewQuest{GroupID: groupID, Name: "收成任務", QuestType: "harvest", Target: 5, RewardMode: "fixed",
		RewardType: "gold", RewardAmount: 100, StartsAt: now, EndsAt: now.Add(dayDur)}
	if change != nil {
		change(&q)
	}
	return must[QuestView](e.t)(e.co.CreateQuest(e.ctx, "admin", q)).QuestID
}

func (e *env) competition(change func(*NewCompetition)) int64 {
	e.t.Helper()
	now := e.clock.Now()
	c := NewCompetition{Name: "收成大賽", CompType: "harvest", Rewards: []int{500, 300}, StartsAt: now, EndsAt: now.Add(dayDur)}
	if change != nil {
		change(&c)
	}
	return must[CompetitionView](e.t)(e.co.CreateCompetition(e.ctx, "admin", c)).CompetitionID
}

// harvestIn 在指定群組上下文中種植並收成（小麥收成 2 個）。groupID 為 0 表示沒有群組上下文。
func (e *env) harvestIn(pid, groupID int64, plot int) ActionResult {
	e.t.Helper()
	s := e.s
	if groupID != 0 {
		s = e.s.InGroupContext(&groupID)
	}
	e.ok(s.BuySeeds(e.ctx, pid, "wheat", 1))
	e.ok(s.PlantCrop(e.ctx, pid, plot, "wheat"))
	e.ok(s.WaterCrop(e.ctx, pid, plot))
	e.clock.Advance(600)
	return e.ok(s.HarvestCrop(e.ctx, pid, plot))
}

func (e *env) qstatus(groupID, pid, questID int64) QuestView {
	e.t.Helper()
	return must[QuestView](e.t)(e.co.QuestStatus(e.ctx, groupID, pid, questID))
}

func (e *env) progress(groupID, pid, questID int64) int {
	return e.qstatus(groupID, pid, questID).Progress
}

func (e *env) compDetail(id int64, pid *int64) CompetitionDetail {
	e.t.Helper()
	return must[CompetitionDetail](e.t)(e.co.CompetitionDetail(e.ctx, id, pid))
}

func scores(d CompetitionDetail) []string {
	var out []string
	for _, r := range d.Ranking {
		out = append(out, r.GroupName+":"+itoa10(r.Score))
	}
	return out
}

// ---------- 任務進度與跨群組計分 ----------

func TestOnlyHomeGroupContextCounts(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	big := func(q *NewQuest) { q.Target = 100 }
	qa, qb := e.quest(w.a, big), e.quest(w.b, big)
	e.harvestIn(w.alice, w.a, 1) // 主要群組 A、在 A 操作 → 計入 A
	e.harvestIn(w.alice, w.b, 1) // 也是 B 成員，但主要群組是 A → 不計入任何群組
	e.harvestIn(w.alice, 0, 1)   // CMD 未選群組
	e.harvestIn(w.bob, w.b, 1)   // bob 主要群組 B → 計入 B
	if e.progress(w.a, w.alice, qa) != 2 || e.progress(w.b, w.bob, qb) != 2 || e.qstatus(w.b, w.alice, qb).MyContribution != 0 {
		t.Fatal("跨群組計分不對")
	}
}

func TestNonMatchingEventsAndFailedOperationsDoNotCount(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	qa := e.quest(w.a, func(q *NewQuest) { q.Target = 100 })
	ctx := e.s.InGroupContext(&w.a)
	e.ok(ctx.BuySeeds(e.ctx, w.alice, "wheat", 3)) // 購買不是收成
	e.ok(ctx.PlantCrop(e.ctx, w.alice, 1, "wheat"))
	e.ok(ctx.WaterCrop(e.ctx, w.alice, 1))
	_, err := ctx.HarvestCrop(e.ctx, w.alice, 1) // 失敗的操作
	wantKind(t, err, gameerr.PlotNotReady)
	if e.progress(w.a, w.alice, qa) != 0 {
		t.Fatal("失敗的操作計分了")
	}
	// 計分寫入失敗時，整個遊戲操作一起回滾
	e.clock.Advance(60)
	boom := errors.New("模擬計分失敗")
	addQuestContribution = func(context.Context, *db.Tx, int64, int64, int64, int, time.Time) (bool, error) { return false, boom }
	t.Cleanup(func() { addQuestContribution = community.AddQuestContribution })
	if _, err := ctx.HarvestCrop(e.ctx, w.alice, 1); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	addQuestContribution = community.AddQuestContribution
	if e.plot(w.alice, 1).State != "ready" || e.progress(w.a, w.alice, qa) != 0 { // 作物仍在
		t.Fatal("沒有回滾")
	}
	if r := e.ok(ctx.HarvestCrop(e.ctx, w.alice, 1)); r.Quantity != 2 || e.progress(w.a, w.alice, qa) != 2 {
		t.Fatal(r.Quantity)
	}
}

func TestSameTransactionCountedOnce(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	qa := e.quest(w.a, func(q *NewQuest) { q.Target = 100 })
	cid := e.competition(nil)
	e.harvestIn(w.alice, w.a, 1)
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error { // 重複處理同一筆交易紀錄（例如重試或重啟後重放）
		row, err := db.QueryStruct[models.GameTransaction](ctx, tx, "SELECT * FROM game_transactions WHERE event_type = 'harvest'")
		if err != nil {
			return err
		}
		farm, err := farms.GetByID(ctx, tx, row.FarmID, false)
		if err != nil {
			return err
		}
		rec := transactions.TxRecord{ID: &row.ID, FarmID: row.FarmID, EventType: row.EventType, ItemCode: row.ItemCode,
			Quantity: row.Quantity, GoldDelta: row.GoldDelta, ContextGroupID: row.ContextGroupID, CreatedAt: row.CreatedAt}
		for range 2 {
			if err := onEvent(ctx, tx, farm, rec); err != nil {
				return err
			}
		}
		return nil
	})
	if e.progress(w.a, w.alice, qa) != 2 || e.compDetail(cid, nil).Ranking[0].Score != 2 {
		t.Fatal("重複計分")
	}
}

func TestQuestPeriodLimits(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	now := e.clock.Now()
	later := e.quest(w.a, func(q *NewQuest) { q.StartsAt, q.EndsAt = now.Add(dayDur), now.Add(2*dayDur) })
	short := e.quest(w.a, func(q *NewQuest) { q.EndsAt = now.Add(5 * time.Minute) })
	if e.qstatus(w.a, w.alice, later).State != "scheduled" {
		t.Fatal("應為尚未開始")
	}
	e.harvestIn(w.alice, w.a, 1) // 收成發生在第 10 分鐘：short 已截止，later 尚未開始
	if e.progress(w.a, w.alice, short) != 0 || e.progress(w.a, w.alice, later) != 0 || e.qstatus(w.a, w.alice, short).State != "expired" {
		t.Fatal("期間外計分了")
	}
	_, err := e.co.ClaimQuest(e.ctx, w.a, w.alice, short)
	wantMsg(t, err, gameerr.Validation, "已截止且未達成")
	e.clock.Advance(dayDur.Seconds())
	e.harvestIn(w.alice, w.a, 2)
	if e.progress(w.a, w.alice, later) != 2 {
		t.Fatal("開始後沒計分")
	}
}

func TestCompletionStopsProgressAndFixedRewards(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	qa := e.quest(w.a, func(q *NewQuest) { q.Target = 3 })
	e.harvestIn(w.alice, w.a, 1) // 2
	_, err := e.co.ClaimQuest(e.ctx, w.a, w.alice, qa)
	wantMsg(t, err, gameerr.Validation, "尚未完成")
	e.harvestIn(w.carol, w.a, 1) // 4 ≥ 3 → 完成
	if st := e.qstatus(w.a, w.alice, qa); st.State != "completed" || st.Progress != 4 {
		t.Fatalf("%+v", st)
	}
	e.harvestIn(w.carol, w.a, 2) // 完成後不再累積
	if e.progress(w.a, w.alice, qa) != 4 {
		t.Fatal("完成後仍累積")
	}
	before := e.gold(w.alice)
	if r := e.ok(e.co.ClaimQuest(e.ctx, w.a, w.alice, qa)); !strings.Contains(r.Message, "100 金幣") || e.gold(w.alice) != before+100 {
		t.Fatal(r.Message)
	}
	_, err = e.co.ClaimQuest(e.ctx, w.a, w.alice, qa)
	wantKind(t, err, gameerr.AlreadyExists)
	_, err = e.co.ClaimQuest(e.ctx, w.a, w.dave, qa) // A 成員但沒有貢獻
	wantMsg(t, err, gameerr.Validation, "沒有參與")
	_, err = e.co.ClaimQuest(e.ctx, w.a, w.bob, qa)
	wantKind(t, err, gameerr.NotGroupMember)
	_, err = e.co.ClaimQuest(e.ctx, w.b, w.alice, qa) // 任務不屬於目前群組
	wantKind(t, err, gameerr.NotFound)
	if e.gold(w.alice) != before+100 {
		t.Fatal("重複發獎")
	}
}

func TestConcurrentQuestClaimsAndRestart(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	qa := e.quest(w.a, func(q *NewQuest) { q.Target, q.RewardAmount = 2, 250 })
	e.harvestIn(w.alice, w.a, 1)
	before := e.gold(w.alice)
	restarted := &CommunityService{&SocialService{&FishingService{&ExplorationService{restartedRanch(t, e)}, e.f.FishingRng}}}
	if st := must[QuestView](t)(restarted.QuestStatus(e.ctx, w.a, w.alice, qa)); st.State != "completed" { // 重啟後完成狀態仍在
		t.Fatal(st.State)
	}
	res := runConcurrently(5, func() (ActionResult, error) { return restarted.ClaimQuest(e.ctx, w.a, w.alice, qa) })
	if n := countOK(t, res, gameerr.AlreadyExists); n != 1 {
		t.Fatal(n)
	}
	if e.gold(w.alice) != before+250 || e.qstatus(w.a, w.alice, qa).MyClaim.Amount != 250 {
		t.Fatal("領取結果不對")
	}
}

func TestProportionalRewardsAndItemRewards(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	qa := e.quest(w.a, func(q *NewQuest) { q.Target, q.RewardMode = 8, "proportional" })
	for plot := 1; plot <= 3; plot++ {
		e.harvestIn(w.alice, w.a, plot) // 6
	}
	e.harvestIn(w.carol, w.a, 1) // 8 → 完成；alice 6/8、carol 2/8
	if e.qstatus(w.a, w.alice, qa).MyReward != 75 || e.qstatus(w.a, w.carol, qa).MyReward != 25 {
		t.Fatal("比例分配不對")
	}
	qi := e.quest(w.a, func(q *NewQuest) { q.Target, q.RewardType, q.RewardAmount = 2, "fertilizer", 3 })
	e.harvestIn(w.carol, w.a, 2)
	used := must[Inventory](t)(e.s.GetInventory(e.ctx, w.carol)).Used
	e.give(w.carol, "carrot", 48-used) // 背包 48/50
	_, err := e.co.ClaimQuest(e.ctx, w.a, w.carol, qi)
	wantMsg(t, err, gameerr.WarehouseFull, "保留") // 背包不足：獎勵保留
	e.ok(e.r.Sell(e.ctx, w.carol, "carrot", 10, "normal", "none"))
	if r := e.ok(e.co.ClaimQuest(e.ctx, w.a, w.carol, qi)); !strings.Contains(r.Message, "3 個化肥") ||
		must[Backpack](t)(e.s.GetBackpack(e.ctx, w.carol)).Consumables[0].Quantity != 3 {
		t.Fatal(r.Message)
	}
}

func TestCancelRecordsReasonAndKeepsHistory(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	qa := e.quest(w.a, func(q *NewQuest) { q.Target = 100 })
	e.harvestIn(w.alice, w.a, 1)
	_, err := e.co.CancelQuest(e.ctx, "admin", qa, " ")
	wantMsg(t, err, gameerr.Validation, "取消原因")
	v := must[QuestView](t)(e.co.CancelQuest(e.ctx, "admin", qa, "目標設定錯誤"))
	if v.State != "cancelled" || *v.CancelReason != "目標設定錯誤" {
		t.Fatalf("%+v", v)
	}
	e.harvestIn(w.alice, w.a, 2)
	if e.progress(w.a, w.alice, qa) != 2 { // 取消後不再累積，既有貢獻保留
		t.Fatal("取消後仍累積")
	}
	_, err = e.co.ClaimQuest(e.ctx, w.a, w.alice, qa)
	wantMsg(t, err, gameerr.Validation, "已取消")
	_, err = e.co.CancelQuest(e.ctx, "admin", qa, "再取消一次")
	wantKind(t, err, gameerr.Validation)
	if e.scalar("SELECT count(*) FROM quest_contributions") != 1 ||
		e.scalar("SELECT count(*) FROM admin_actions WHERE details->>'reason' = '目標設定錯誤'") != 1 {
		t.Fatal("紀錄不對")
	}
}

func TestRewardTermsAreFixed(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	qa, cid := e.quest(w.a, nil), e.competition(nil)
	for _, sql := range []string{
		"UPDATE quests SET reward_amount = 999999 WHERE id = $1",
		"UPDATE quests SET target = 1 WHERE id = $1",
		"UPDATE competitions SET rewards = '[99999]' WHERE id = $2",
		"UPDATE competitions SET ends_at = ends_at + interval '1 day' WHERE id = $2",
	} {
		err := e.sqlErr("WITH x AS (SELECT $1::bigint AS q, $2::bigint AS c) "+strings.Replace(strings.Replace(sql, "$1", "(SELECT q FROM x)", 1), "$2", "(SELECT c FROM x)", 1), qa, cid)
		if !isIntegrity(err) || !strings.Contains(err.Error(), "fixed after creation") {
			t.Fatal(sql, err)
		}
	}
}

func TestCommunityCreateValidation(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	now := e.clock.Now()
	egg := "egg"
	cases := []func(*NewQuest){
		func(q *NewQuest) { q.Metric = "amount" }, // 只有販售能依金額
		func(q *NewQuest) { q.ItemFilter = &egg },
		func(q *NewQuest) { q.StartsAt, q.EndsAt = now.Add(-2*dayDur), now.Add(-dayDur) },
		func(q *NewQuest) { q.EndsAt = now.Add(100 * dayDur) },
	}
	for i, change := range cases {
		q := NewQuest{GroupID: w.a, Name: "收成任務", QuestType: "harvest", Target: 5, RewardMode: "fixed",
			RewardType: "gold", RewardAmount: 100, StartsAt: now, EndsAt: now.Add(dayDur)}
		change(&q)
		if _, err := e.co.CreateQuest(e.ctx, "admin", q); !gameerr.Is(err, gameerr.Validation) {
			t.Fatal(i, err)
		}
	}
	_, err := e.co.CreateQuest(e.ctx, "admin", NewQuest{GroupID: 999, Name: "x", QuestType: "harvest", Target: 5,
		RewardMode: "fixed", RewardType: "gold", RewardAmount: 100, StartsAt: now, EndsAt: now.Add(dayDur)})
	wantKind(t, err, gameerr.NotFound)
	_, err = e.co.CreateCompetition(e.ctx, "admin", NewCompetition{Name: "x", CompType: "harvest", Rewards: []int{1, 2, 3, 4},
		StartsAt: now, EndsAt: now.Add(dayDur)})
	wantKind(t, err, gameerr.Validation)
}

// ---------- 各任務類型 ----------

func TestQuestTypesSellBuildAnimalDefense(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	week := e.clock.Now().Add(7 * dayDur) // 動物生產需要 24 小時，期限要涵蓋
	qAmount := e.quest(w.a, func(q *NewQuest) { q.QuestType, q.Metric, q.Target, q.EndsAt = "sell", "amount", 1000, week })
	qBuild := e.quest(w.a, func(q *NewQuest) { q.QuestType, q.Target, q.EndsAt = "build", 10, week })
	egg := "egg"
	qEgg := e.quest(w.a, func(q *NewQuest) { q.QuestType, q.ItemFilter, q.Target, q.EndsAt = "animal", &egg, 10, week })
	qDef := e.quest(w.a, func(q *NewQuest) { q.QuestType, q.Target, q.EndsAt = "defense", 10, week })
	ctx := e.co.InGroupContext(&w.a)
	e.harvestIn(w.alice, w.a, 1)
	e.ok(ctx.Sell(e.ctx, w.alice, "wheat", 2, "normal", "none")) // +30 金幣
	e.ok(ctx.UpgradeFarm(e.ctx, w.alice))
	e.ok(ctx.Build(e.ctx, w.alice, "畜舍"))
	e.ok(ctx.BuyAnimals(e.ctx, w.alice, "雞", 1))
	e.ok(ctx.BuyAnimals(e.ctx, w.alice, "牛", 1))
	e.ok(ctx.FeedAll(e.ctx, w.alice))
	e.clock.Advance(dayDur.Seconds())
	e.ok(ctx.CollectAnimal(e.ctx, w.alice, 1)) // 雞蛋 2
	e.ok(ctx.CollectAnimal(e.ctx, w.alice, 2)) // 牛奶不計入「雞蛋」任務
	if e.progress(w.a, w.alice, qAmount) != 30 || e.progress(w.a, w.alice, qBuild) != 2 || e.progress(w.a, w.alice, qEgg) != 2 {
		t.Fatal(e.progress(w.a, w.alice, qAmount), e.progress(w.a, w.alice, qBuild), e.progress(w.a, w.alice, qEgg))
	}
	// 防守任務：被偷者 carol（主要群組 A）在 A 的偷菜中防守成功
	e.ok(e.so.BuyDefense(e.ctx, w.carol, "警戒犬"))
	e.grow(w.carol, 1, "strawberry")
	e.clock.Advance(600)
	must[bool](t)(e.so.AddMember(e.ctx, w.a, w.bob))
	e.s.rng = func() float64 { return 0 }
	if e.steal(w.a, w.bob, w.carol, 1, 1).Success || e.progress(w.a, w.carol, qDef) != 1 {
		t.Fatal("防守任務沒計分")
	}
}

// ---------- 競賽 ----------

func TestCompetitionScoringSettlementAndHistory(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	cid := e.competition(nil)
	e.harvestIn(w.alice, w.a, 1) // A +2
	e.harvestIn(w.alice, w.a, 2) // A +2
	e.harvestIn(w.alice, w.b, 3) // alice 在 B 操作：不計分
	e.harvestIn(w.bob, w.b, 1)   // B +2
	d := e.compDetail(cid, &w.alice)
	if !slices.Equal(scores(d), []string{"A 群:4", "B 群:2"}) || d.MyPoints != 4 || d.Final {
		t.Fatal(scores(d))
	}
	_, err := e.co.SettleCompetition(e.ctx, cid, "system")
	wantMsg(t, err, gameerr.Validation, "尚未截止")
	e.clock.Advance(d.EndsAt.Sub(e.clock.Now()).Seconds() - 600)
	bctx := e.s.InGroupContext(&w.b)
	e.ok(bctx.BuySeeds(e.ctx, w.bob, "strawberry", 1))
	e.ok(bctx.PlantCrop(e.ctx, w.bob, 2, "strawberry"))
	e.ok(bctx.WaterCrop(e.ctx, w.bob, 2)) // 600 秒後（剛好截止時）成熟
	e.clock.Advance(600)
	e.ok(bctx.HarvestCrop(e.ctx, w.bob, 2)) // 截止當下的操作不計分
	if e.compDetail(cid, nil).Ranking[1].Score != 2 {
		t.Fatal("截止後仍計分")
	}
	_, err = e.co.SettleCompetition(e.ctx, cid, "system") // 仍在結算緩衝時間內
	wantKind(t, err, gameerr.Validation)
	e.clock.Advance(settleGrace.Seconds())
	ps := []int64{w.alice, w.carol, w.bob, w.dave}
	before := map[int64]int{}
	for _, p := range ps {
		before[p] = e.gold(p)
	}
	trues := 0
	res := runConcurrently(4, func() (ActionResult, error) { // 並行結算只會成功一次
		ok, err := e.co.SettleCompetition(e.ctx, cid, "system")
		r := ActionResult{Success: ok}
		return r, err
	})
	for _, r := range res {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.r.Success {
			trues++
		}
	}
	if trues != 1 {
		t.Fatal(trues)
	}
	// 第 1 名 A：主要群組為 A 的全體成員（alice、carol）；dave 是 A 成員但主要群組 B
	want := map[int64]int{w.alice: 500, w.carol: 500, w.bob: 300, w.dave: 300}
	for _, p := range ps {
		if e.gold(p)-before[p] != want[p] {
			t.Fatal(p, e.gold(p)-before[p])
		}
	}
	if ok := must[bool](t)(e.co.SettleCompetition(e.ctx, cid, "system")); ok || e.scalar("SELECT count(*) FROM competition_awards") != 4 {
		t.Fatal("重複結算")
	}
	// 結算後成員變動、群組改名都不改寫歷史成績
	must[bool](t)(e.so.RemoveMember(e.ctx, w.a, w.alice))
	e.exec("UPDATE groups SET name = '改名' WHERE id = $1", w.a)
	final := e.compDetail(cid, &w.alice)
	if !final.Final || !slices.Equal(scores(final), []string{"A 群:4", "B 群:2"}) || final.MyAward.Rank != 1 || final.MyAward.Gold != 500 {
		t.Fatalf("%+v", final)
	}
	if h := must[[]CompetitionDetail](t)(e.co.CompetitionHistory(e.ctx, &w.alice, 10)); len(h) != 1 || h[0].CompetitionID != cid {
		t.Fatal(h)
	}
}

func TestCompetitionTypesAndCancel(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	dev := e.competition(func(c *NewCompetition) { c.Name, c.CompType, c.Rewards = "發展", "development", []int{100} })
	sales := e.competition(func(c *NewCompetition) { c.Name, c.CompType, c.Rewards = "銷售", "sales", []int{100} })
	cancelled := e.competition(func(c *NewCompetition) { c.Name, c.Rewards = "取消", []int{100} })
	must[CompetitionView](t)(e.co.CancelCompetition(e.ctx, "admin", cancelled, "活動延期"))
	ca, cb := e.co.InGroupContext(&w.a), e.co.InGroupContext(&w.b)
	e.ok(ca.UpgradeFarm(e.ctx, w.alice)) // 10 分
	e.ok(ca.Build(e.ctx, w.alice, "磨坊")) // 5 分
	e.ok(cb.Build(e.ctx, w.bob, "畜舍"))   // 5 分
	e.harvestIn(w.alice, w.a, 1)
	e.ok(ca.Sell(e.ctx, w.alice, "wheat", 2, "normal", "none")) // 收入 30
	if !slices.Equal(scores(e.compDetail(dev, nil)), []string{"A 群:15", "B 群:5"}) ||
		e.compDetail(sales, nil).Ranking[0].Score != 30 || len(e.compDetail(cancelled, nil).Ranking) != 0 { // 取消後不再計分
		t.Fatal("競賽計分不對")
	}
	e.clock.Advance((dayDur + settleGrace).Seconds())
	got := must[[]int64](t)(e.co.SettleDueCompetitions(e.ctx, "admin"))
	slices.Sort(got)
	if !slices.Equal(got, []int64{dev, sales}) || e.compDetail(cancelled, nil).State != "cancelled" {
		t.Fatal(got)
	}
}

func TestCompetitionsSurviveRestart(t *testing.T) {
	e := setup(t)
	w := e.commWorld()
	cid := e.competition(nil)
	e.harvestIn(w.alice, w.a, 1)
	e.clock.Advance((dayDur + settleGrace).Seconds())
	restarted := &CommunityService{&SocialService{&FishingService{&ExplorationService{restartedRanch(t, e)}, e.f.FishingRng}}}
	if d := must[CompetitionDetail](t)(restarted.CompetitionDetail(e.ctx, cid, nil)); !d.Final { // 重啟後查詢時結算
		t.Fatal("沒有結算")
	}
	if d := must[CompetitionDetail](t)(restarted.CompetitionDetail(e.ctx, cid, &w.alice)); d.MyAward.Gold != 500 {
		t.Fatalf("%+v", d.MyAward)
	}
}

func TestAdminActionDetailsUsePythonDatetime(t *testing.T) {
	// Go 版補充：管理紀錄的時間格式與 Python str(datetime) 相同
	e := setup(t)
	w := e.commWorld()
	e.quest(w.a, nil)
	var starts []string
	repotestQuery(t, e, "SELECT details->>'starts_at' FROM admin_actions WHERE action = 'create_quest'", &starts)
	if len(starts) != 1 || starts[0] != "2026-01-01 00:00:00+00:00" {
		t.Fatal(starts)
	}
}

func itoa10(n int) string { return strconvItoa64(int64(n)) }
