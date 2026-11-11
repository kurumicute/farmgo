package farmstats_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/repositories/transactions"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func ptr[T any](v T) *T { return &v }

func TestOnTransactionMatchesRebuild(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 1)
	// 台灣 2026-01-01 23:30 與 2026-01-02 00:30（跨日）
	day1 := time.Date(2026, 1, 1, 15, 30, 0, 0, time.UTC)
	day2 := day1.Add(time.Hour)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		farm, _ := farms.GetByID(ctx, tx, f.FarmID, true)
		if err := farmstats.EnsureRow(ctx, tx, farm); err != nil {
			return err
		}
		recs := []transactions.AddParams{
			{FarmID: f.FarmID, EventType: "create_farm", Description: "x", CreatedAt: day1, GoldDelta: 500},
			{FarmID: f.FarmID, EventType: "sell", Description: "x", CreatedAt: day1, ItemCode: ptr("wheat"), Quantity: 2, GoldDelta: 22},
			{FarmID: f.FarmID, EventType: "sell", Description: "x", CreatedAt: day2, ItemCode: ptr("egg"), Quantity: 1, GoldDelta: 25},
			{FarmID: f.FarmID, EventType: "buy_seed", Description: "x", CreatedAt: day2, GoldDelta: -10},
			{FarmID: f.FarmID, EventType: "steal", Description: "x", CreatedAt: day2, ItemCode: ptr("wheat"), Quantity: 1},
		}
		for _, p := range recs {
			rec, err := transactions.Add(ctx, tx, p)
			if err != nil {
				return err
			}
			if err := farmstats.OnTransaction(ctx, tx, farm, rec); err != nil {
				return err
			}
		}
		var cropQty, goodsIncome, nonOp, steals int
		if err := tx.QueryRow(ctx, `SELECT crop_sold_quantity, goods_sales_income, non_operating_gold, steal_count
			FROM farm_stats WHERE farm_id = $1`, f.FarmID).Scan(&cropQty, &goodsIncome, &nonOp, &steals); err != nil {
			return err
		}
		if cropQty != 2 || goodsIncome != 47 || nonOp != 500 || steals != 1 {
			t.Fatal(cropQty, goodsIncome, nonOp, steals)
		}
		days, err := db.QueryValues[int](ctx, tx, "SELECT net_gold FROM farm_daily_stats WHERE farm_id = $1 ORDER BY day", f.FarmID)
		if err != nil || len(days) != 2 || days[0] != 22 || days[1] != 15 {
			t.Fatal(days, err)
		}
		chk, err := farmstats.Check(ctx, tx, 20)
		if err != nil || chk.FarmStatsMismatches != 0 || chk.FarmDailyStatsMismatches != 0 {
			t.Fatalf("%+v %v", chk, err)
		}
		if _, err := tx.Exec(ctx, "UPDATE farm_stats SET steal_count = 9"); err != nil {
			return err
		}
		if chk, _ = farmstats.Check(ctx, tx, 20); chk.FarmStatsMismatches != 1 || len(chk.Examples) != 1 {
			t.Fatalf("%+v", chk)
		}
		r, err := farmstats.Rebuild(ctx, tx)
		if err != nil || r.FarmStats != 1 || r.FarmDailyStats != 2 {
			t.Fatal(r, err)
		}
		if chk, _ = farmstats.Check(ctx, tx, 20); chk.FarmStatsMismatches != 0 {
			t.Fatalf("%+v", chk)
		}
		return nil
	})
}

func TestDayHelpers(t *testing.T) {
	at := time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC) // 台灣 1/2 00:00
	if !farmstats.IsDayAligned(&at) || farmstats.IsDayAligned(ptr(at.Add(time.Second))) || !farmstats.IsDayAligned(nil) {
		t.Fatal("aligned")
	}
	if d := farmstats.TaipeiDay(at); d.Day() != 2 {
		t.Fatal(d)
	}
}
