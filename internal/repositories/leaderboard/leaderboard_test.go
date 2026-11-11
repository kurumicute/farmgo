package leaderboard_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/repositories/transactions"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func ptr[T any](v T) *T { return &v }

func sell(t *testing.T, d *db.DB, farmID int64, at time.Time, item string, qty, gold int) {
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		farm, _ := farms.GetByID(ctx, tx, farmID, true)
		if err := farmstats.EnsureRow(ctx, tx, farm); err != nil {
			return err
		}
		rec, err := transactions.Add(ctx, tx, transactions.AddParams{FarmID: farmID, EventType: "sell", Description: "x",
			CreatedAt: at, ItemCode: &item, Quantity: qty, GoldDelta: gold})
		if err != nil {
			return err
		}
		return farmstats.OnTransaction(ctx, tx, farm, rec)
	})
}

func TestRankingPathsAgree(t *testing.T) {
	d := setup(t)
	a := repotest.MakeFarm(t, d, "甲", 1000, 1)
	b := repotest.MakeFarm(t, d, "乙", 100, 1)
	// 台灣 2026-01-01 的 10:00 與 2026-01-02 的 10:00
	d1 := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	d2 := d1.Add(24 * time.Hour)
	sell(t, d, a.FarmID, d1, "wheat", 2, 22)
	sell(t, d, b.FarmID, d1, "wheat", 5, 55)
	sell(t, d, b.FarmID, d2, "egg", 1, 25)
	sell(t, d, a.FarmID, d2, "egg", 10, 250)
	since := time.Date(2025, 12, 31, 16, 0, 0, 0, time.UTC) // 台灣 1/1 00:00
	until := since.Add(24 * time.Hour)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		all, err := leaderboard.Ranking(ctx, tx, "money", nil, 10, nil, nil)
		if err != nil || len(all) != 2 || all[0].PlayerID != a.PlayerID || all[0].Score != 1000 || all[0].Rank != 1 {
			t.Fatal(all, err)
		}
		sales, _ := leaderboard.Ranking(ctx, tx, "sales", nil, 10, nil, nil)
		if sales[0].PlayerID != b.PlayerID || sales[0].Score != 5 {
			t.Fatal(sales)
		}
		goods, _ := leaderboard.Ranking(ctx, tx, "goods", nil, 10, nil, nil)
		if goods[0].PlayerID != a.PlayerID || goods[0].Score != 272 {
			t.Fatal(goods)
		}
		// 對齊台灣日期（farm_daily_stats）與非標準期間（交易紀錄）結果相同
		aligned, _ := leaderboard.Ranking(ctx, tx, "money", nil, 10, &since, &until)
		odd, _ := leaderboard.Ranking(ctx, tx, "money", nil, 10, ptr(since.Add(time.Second)), &until)
		if aligned[0].PlayerID != b.PlayerID || aligned[0].Score != 55 || odd[0].Score != 55 || aligned[1].Score != 22 {
			t.Fatal(aligned, odd)
		}
		grp, _ := leaderboard.Ranking(ctx, tx, "money", &a.GroupID, 10, nil, nil)
		if len(grp) != 1 || grp[0].PlayerID != a.PlayerID {
			t.Fatal(grp)
		}
		gr, err := leaderboard.GroupRanking(ctx, tx, "money", 10, nil, nil)
		if err != nil || len(gr) != 2 || gr[0].GroupID != a.GroupID || gr[0].Gold != 1000 || gr[0].MemberCount != 1 {
			t.Fatal(gr, err)
		}
		gp, _ := leaderboard.GroupRanking(ctx, tx, "sales", 10, &since, &until)
		if gp[0].GroupID != b.GroupID || gp[0].Score != 5 {
			t.Fatal(gp)
		}
		if _, err := leaderboard.Ranking(ctx, tx, "bad", nil, 1, nil, nil); err == nil {
			t.Fatal("bad kind")
		}
		return nil
	})
}
