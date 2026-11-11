package community

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/dbtest"
)

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func setup(t *testing.T) *db.DB {
	d := dbtest.Open(t)
	dbtest.Reset(t, d)
	return d
}

func run(t *testing.T, d *db.DB, fn func(ctx context.Context, tx *db.Tx) error) {
	t.Helper()
	if err := d.InTx(context.Background(), db.TxOptions{}, func(tx *db.Tx) error {
		return fn(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

type member struct{ player, farm int64 }

// must 測試用：發生錯誤時 panic（測試失敗）。
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func addMember(ctx context.Context, t *testing.T, tx *db.Tx, name string, group int64, home *int64) member {
	t.Helper()
	pid := must(db.InsertReturningID(ctx, tx, "players", db.C("display_name", name)))
	fid := must(db.InsertReturningID(ctx, tx, "farms", db.C("player_id", pid), db.C("name", name+"農場"),
		db.C("level", 1), db.C("gold", 0), db.C("warehouse_capacity", 50), db.C("home_group_id", home)))
	if err := db.Insert(ctx, tx, "group_members", db.C("group_id", group), db.C("player_id", pid)); err != nil {
		t.Fatal(err)
	}
	return member{pid, fid}
}

func txn(ctx context.Context, t *testing.T, tx *db.Tx, farm int64) int64 {
	t.Helper()
	return must(db.InsertReturningID(ctx, tx, "game_transactions", db.C("farm_id", farm), db.C("event_type", "harvest"),
		db.C("quantity", 1), db.C("description", "test"), db.C("created_at", now)))
}

func TestAdminActions(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		id := int64(7)
		if err := AddAdminAction(ctx, tx, "admin", "set_gold", "farm", &id, map[string]any{"gold": 10}, now); err != nil {
			return err
		}
		if err := AddAdminAction(ctx, tx, "admin", "note", "system", nil, nil, now.Add(time.Second)); err != nil {
			return err
		}
		list := must(ListAdminActions(ctx, tx, 50))
		if len(list) != 2 || list[0].Action != "note" || list[1].Details["gold"] != float64(10) || *list[1].TargetID != 7 {
			t.Fatalf("%+v", list)
		}
		return nil
	})
}

func TestQuests(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		g := must(db.InsertReturningID(ctx, tx, "groups", db.C("name", "甲群")))
		g2 := must(db.InsertReturningID(ctx, tx, "groups", db.C("name", "乙群")))
		a := addMember(ctx, t, tx, "甲", g, &g)
		b := addMember(ctx, t, tx, "乙", g, &g)
		q := must(db.InsertReturningID(ctx, tx, "quests", db.C("group_id", g), db.C("name", "收成"),
			db.C("quest_type", "harvest"), db.C("metric", "quantity"), db.C("target", 10), db.C("reward_mode", "fixed"),
			db.C("reward_type", "gold"), db.C("reward_amount", 100), db.C("starts_at", now.Add(-time.Hour)),
			db.C("ends_at", now.Add(time.Hour)), db.C("created_by", "admin"), db.C("created_at", now)))
		must(db.InsertReturningID(ctx, tx, "quests", db.C("group_id", g2), db.C("name", "未來"),
			db.C("quest_type", "harvest"), db.C("metric", "quantity"), db.C("target", 10), db.C("reward_mode", "fixed"),
			db.C("reward_type", "gold"), db.C("reward_amount", 100), db.C("starts_at", now.Add(time.Hour)),
			db.C("ends_at", now.Add(2*time.Hour)), db.C("created_by", "admin"), db.C("created_at", now)))

		open := must(OpenQuestsForEvent(ctx, tx, g, now))
		if len(open) != 1 || open[0].ID != q {
			t.Fatalf("%+v", open)
		}
		if got := must(GetQuest(ctx, tx, q, true)); got == nil || got.Status != "active" {
			t.Fatal("get quest")
		}
		t1, t2, t3 := txn(ctx, t, tx, a.farm), txn(ctx, t, tx, b.farm), txn(ctx, t, tx, b.farm)
		if !must(AddQuestContribution(ctx, tx, q, a.player, t1, 3, now)) {
			t.Fatal("first contribution")
		}
		if must(AddQuestContribution(ctx, tx, q, a.player, t1, 3, now)) {
			t.Fatal("duplicate transaction must be ignored")
		}
		must(AddQuestContribution(ctx, tx, q, b.player, t2, 2, now))
		must(AddQuestContribution(ctx, tx, q, b.player, t3, 2, now))
		cs := must(QuestContributors(ctx, tx, q))
		if len(cs) != 2 || cs[0].PlayerID != b.player || cs[0].Total != 4 || cs[1].PlayerName != "甲" {
			t.Fatalf("%+v", cs)
		}
		if n := must(PlayerQuestContribution(ctx, tx, q, a.player)); n != 3 {
			t.Fatal(n)
		}
		if n := must(QuestTotalContribution(ctx, tx, q)); n != 7 {
			t.Fatal(n)
		}
		if c := must(GetClaim(ctx, tx, q, a.player)); c != nil {
			t.Fatal("no claim yet")
		}
		if err := db.Insert(ctx, tx, "quest_claims", db.C("quest_id", q), db.C("player_id", a.player),
			db.C("reward_type", "gold"), db.C("amount", 100), db.C("claimed_at", now)); err != nil {
			return err
		}
		claims := must(QuestClaims(ctx, tx, q))
		if len(claims) != 1 || claims[0].PlayerName != "甲" || must(GetClaim(ctx, tx, q, a.player)) == nil {
			t.Fatalf("%+v", claims)
		}

		all := must(ListQuests(ctx, tx, nil, nil, 50))
		if len(all) != 2 || all[0].GroupName != "乙群" {
			t.Fatalf("%+v", all)
		}
		byGroup := must(ListQuests(ctx, tx, []int64{g}, []string{"active"}, 50))
		if len(byGroup) != 1 || byGroup[0].ID != q {
			t.Fatalf("%+v", byGroup)
		}
		if none := must(ListQuests(ctx, tx, []int64{}, nil, 50)); len(none) != 0 {
			t.Fatalf("empty group list must match nothing: %+v", none)
		}
		return nil
	})
}

func TestCompetitions(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		g1 := must(db.InsertReturningID(ctx, tx, "groups", db.C("name", "甲群")))
		g2 := must(db.InsertReturningID(ctx, tx, "groups", db.C("name", "乙群")))
		a := addMember(ctx, t, tx, "甲", g1, &g1)
		b := addMember(ctx, t, tx, "乙", g2, &g2)
		c := addMember(ctx, t, tx, "丙", g2, &g1) // 主要群組不是乙群：不領乙群的獎
		comp := must(db.InsertReturningID(ctx, tx, "competitions", db.C("name", "收成大賽"), db.C("comp_type", "harvest"),
			db.C("rewards", []int{100, 50}), db.C("starts_at", now.Add(-time.Hour)), db.C("ends_at", now.Add(time.Hour)),
			db.C("created_by", "admin"), db.C("created_at", now)))

		if open := must(OpenCompetitionsForEvent(ctx, tx, now)); len(open) != 1 || open[0].Rewards[0] != 100 {
			t.Fatalf("%+v", open)
		}
		if due := must(DueCompetitions(ctx, tx, now)); len(due) != 0 {
			t.Fatal(due)
		}
		if due := must(DueCompetitions(ctx, tx, now.Add(time.Hour))); len(due) != 1 {
			t.Fatal(due)
		}
		must(AddCompetitionContribution(ctx, tx, comp, g1, a.player, txn(ctx, t, tx, a.farm), 5, now))
		must(AddCompetitionContribution(ctx, tx, comp, g2, b.player, txn(ctx, t, tx, b.farm), 3, now))
		tc := txn(ctx, t, tx, c.farm)
		must(AddCompetitionContribution(ctx, tx, comp, g2, c.player, tc, 4, now))
		if must(AddCompetitionContribution(ctx, tx, comp, g2, c.player, tc, 4, now)) {
			t.Fatal("duplicate")
		}
		live := must(LiveRanking(ctx, tx, comp))
		if len(live) != 2 || live[0].Rank != 1 || live[0].GroupID != g2 || live[0].Score != 7 || live[0].Contributors != 2 ||
			live[1].Rank != 2 || live[1].GroupName != "甲群" {
			t.Fatalf("%+v", live)
		}
		pts := must(CompetitionPlayerPoints(ctx, tx, comp, 20))
		if len(pts) != 3 || pts[0].PlayerID != a.player || pts[0].Points != 5 || pts[2].PlayerName != "乙" {
			t.Fatalf("%+v", pts)
		}
		if n := must(PlayerCompetitionPoints(ctx, tx, comp, c.player)); n != 4 {
			t.Fatal(n)
		}
		if err := AddResults(ctx, tx, comp, live); err != nil {
			return err
		}
		if final := must(FinalRanking(ctx, tx, comp)); len(final) != 2 || final[1].Score != 5 {
			t.Fatalf("%+v", final)
		}
		rec := must(AwardRecipients(ctx, tx, g2))
		if len(rec) != 1 || rec[0].ID != b.farm || rec[0].PlayerName != "乙" {
			t.Fatalf("%+v", rec)
		}
		aw := models.CompetitionAward{CompetitionID: comp, PlayerID: b.player, GroupID: g2, PlayerName: "乙", Rank: 1, Gold: 100, CreatedAt: now}
		if !must(AddAward(ctx, tx, aw)) || must(AddAward(ctx, tx, aw)) {
			t.Fatal("award once")
		}
		if got := must(PlayerAward(ctx, tx, comp, b.player)); got == nil || got.Gold != 100 {
			t.Fatal("player award")
		}
		if got := must(PlayerAward(ctx, tx, comp, a.player)); got != nil {
			t.Fatal("no award")
		}
		if list := must(CompetitionAwards(ctx, tx, comp)); len(list) != 1 {
			t.Fatal(list)
		}
		if list := must(ListCompetitions(ctx, tx, []string{"settled"}, 50)); len(list) != 0 {
			t.Fatal(list)
		}
		if list := must(ListCompetitions(ctx, tx, nil, 50)); len(list) != 1 {
			t.Fatal(list)
		}
		if got := must(GetCompetition(ctx, tx, comp, true)); got == nil {
			t.Fatal("get competition")
		}
		return nil
	})
}
