package steals

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/dbtest"
)

// 2026-01-01 00:00 UTC = 台灣 08:00；台灣當天 00:00 = 2025-12-31 16:00 UTC。
var (
	now      = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dayStart = time.Date(2026, 1, 1, 0, 0, 0, 0, clock.TaipeiTZ)
)

func setup(t *testing.T) *db.DB {
	d := dbtest.Open(t)
	dbtest.Reset(t, d)
	return d
}

type world struct {
	group, wheat int64
	players      []int64
	farms        []int64
	plots        map[[2]int64]int64 // (farm, plot_number) → plot id
}

func build(ctx context.Context, t *testing.T, tx *db.Tx, names ...string) *world {
	t.Helper()
	w := &world{plots: map[[2]int64]int64{}}
	var err error
	if w.wheat, _, err = db.QueryValue[int64](ctx, tx, "SELECT id FROM crop_types WHERE code = 'wheat'"); err != nil {
		t.Fatal(err)
	}
	if w.group, err = db.InsertReturningID(ctx, tx, "groups", db.C("name", "測試群")); err != nil {
		t.Fatal(err)
	}
	ready := now.Add(-time.Hour)
	for _, n := range names {
		pid, err := db.InsertReturningID(ctx, tx, "players", db.C("display_name", n))
		if err != nil {
			t.Fatal(err)
		}
		fid, err := db.InsertReturningID(ctx, tx, "farms", db.C("player_id", pid), db.C("name", n+"農場"),
			db.C("level", 1), db.C("gold", 0), db.C("warehouse_capacity", 50))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Insert(ctx, tx, "group_members", db.C("group_id", w.group), db.C("player_id", pid)); err != nil {
			t.Fatal(err)
		}
		for no := 1; no <= 2; no++ {
			id, err := db.InsertReturningID(ctx, tx, "farm_plots", db.C("farm_id", fid), db.C("plot_number", no),
				db.C("crop_type_id", w.wheat), db.C("planted_at", ready.Add(-time.Hour)), db.C("watered_at", ready.Add(-time.Hour)),
				db.C("ready_at", ready), db.C("remaining_quantity", 2), db.C("quality", "normal"), db.C("mutation", "none"))
			if err != nil {
				t.Fatal(err)
			}
			w.plots[[2]int64{fid, int64(no)}] = id
		}
		w.players = append(w.players, pid)
		w.farms = append(w.farms, fid)
	}
	return w
}

func (w *world) steal(ctx context.Context, t *testing.T, tx *db.Tx, thief, victim, plotNo int, at time.Time, outcome string) {
	t.Helper()
	pid := w.plots[[2]int64{w.farms[victim], int64(plotNo)}]
	var blockedBy *string
	if outcome == "blocked" {
		by := "scarecrow"
		blockedBy = &by
	}
	if _, err := Add(ctx, tx, NewRecord{GroupID: w.group, ThiefPlayerID: w.players[thief], VictimPlayerID: w.players[victim],
		PlotID: &pid, PlotNumber: plotNo, PlantingNo: 0, CropTypeID: w.wheat, Quantity: 1, CreatedAt: at,
		Outcome: outcome, BlockedBy: blockedBy}); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, d *db.DB, fn func(ctx context.Context, tx *db.Tx) error) {
	t.Helper()
	if err := d.InTx(context.Background(), db.TxOptions{}, func(tx *db.Tx) error {
		return fn(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCountsAndTaipeiDayBoundary(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		w := build(ctx, t, tx, "甲", "乙", "丙", "丁")
		// 台灣前一天 23:59 的紀錄不算今天
		w.steal(ctx, t, tx, 0, 1, 1, dayStart.Add(-time.Minute), "")
		w.steal(ctx, t, tx, 0, 1, 2, now, "")
		w.steal(ctx, t, tx, 2, 1, 1, now, "")
		w.steal(ctx, t, tx, 3, 1, 1, now, "blocked")
		n, _ := PlotsStolenSince(ctx, tx, w.players[0], w.players[1], dayStart)
		if n != 1 {
			t.Fatalf("plots since = %d", n)
		}
		n, _ = ThievesSince(ctx, tx, w.players[1], dayStart, nil)
		if n != 2 {
			t.Fatalf("thieves = %d", n)
		}
		n, _ = ThievesSince(ctx, tx, w.players[1], dayStart, &w.players[0])
		if n != 1 {
			t.Fatalf("thieves excluding = %d", n)
		}
		lim, _ := VictimsAtLimit(ctx, tx, w.players[0], dayStart, 1)
		if len(lim) != 1 || lim[0] != w.players[1] {
			t.Fatal(lim)
		}
		full, _ := VictimsFull(ctx, tx, w.players[3], dayStart, 2) // 甲、丙都偷過乙
		if len(full) != 1 || full[0] != w.players[1] {
			t.Fatal(full)
		}
		full, _ = VictimsFull(ctx, tx, w.players[0], dayStart, 2) // 排除自己後只剩丙
		if len(full) != 0 {
			t.Fatal(full)
		}
		ok, _ := AlreadyStolen(ctx, tx, w.plots[[2]int64{w.farms[1], 1}], 0, w.players[0])
		if !ok {
			t.Fatal("already stolen")
		}
		q, _ := StolenQuantity(ctx, tx, w.plots[[2]int64{w.farms[1], 1}], 0)
		if q != 2 { // 甲（前一天）＋丙；丁被阻止
			t.Fatalf("stolen qty = %d", q)
		}
		m, _ := StolenByCurrentPlantings(ctx, tx, w.farms[1])
		if len(m) != 2 || m[w.plots[[2]int64{w.farms[1], 1}]] != 2 {
			t.Fatal(m)
		}
		return nil
	})
}

func TestCandidatePlotsAndLimits(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		w := build(ctx, t, tx, "甲", "乙", "丙")
		cutoff := now.Add(-16 * time.Hour)
		all, err := CandidatePlots(ctx, tx, w.group, w.players[0], now, cutoff, dayStart, nil, nil)
		if err != nil {
			return err
		}
		if len(all) != 4 || all[0].PlayerID != w.players[1] || all[0].PlotNumber != 1 || all[0].CropCode != "wheat" ||
			all[0].RemainingQuantity != 2 || all[0].FarmName != "乙農場" {
			t.Fatalf("%+v", all)
		}
		w.steal(ctx, t, tx, 0, 1, 1, now, "")
		one := 1
		limited, _ := CandidatePlots(ctx, tx, w.group, w.players[0], now, cutoff, dayStart, &one, nil)
		if len(limited) != 2 || limited[0].PlayerID != w.players[2] {
			t.Fatalf("%+v", limited) // 乙今天已偷滿 1 格
		}
		unlimited, _ := CandidatePlots(ctx, tx, w.group, w.players[0], now, cutoff, dayStart, nil, nil)
		if len(unlimited) != 3 { // 偷過的那格（同一次種植）不能再偷
			t.Fatalf("%+v", unlimited)
		}
		// 丙偷過乙之後，乙被 1 位其他人偷過：上限 1 時甲不能再偷乙
		w.steal(ctx, t, tx, 2, 1, 2, now, "")
		full, _ := CandidatePlots(ctx, tx, w.group, w.players[0], now, cutoff, dayStart, nil, &one)
		for _, c := range full {
			if c.PlayerID == w.players[1] {
				t.Fatalf("%+v", full)
			}
		}
		// 已乾枯的不列出
		withered, _ := CandidatePlots(ctx, tx, w.group, w.players[0], now, now, dayStart, nil, nil)
		if len(withered) != 0 {
			t.Fatalf("%+v", withered)
		}
		return nil
	})
}

func TestSearch(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		w := build(ctx, t, tx, "甲", "乙", "丙")
		w.steal(ctx, t, tx, 0, 1, 1, now.Add(-2*time.Hour), "")
		w.steal(ctx, t, tx, 2, 1, 2, now.Add(-time.Hour), "blocked")
		w.steal(ctx, t, tx, 1, 2, 1, now, "")
		all, err := Search(ctx, tx, SearchFilter{Limit: 10})
		if err != nil {
			return err
		}
		if len(all) != 3 || all[0].ThiefName != "乙" || all[0].VictimName != "丙" || all[0].CropCode != "wheat" ||
			all[0].GroupName != "測試群" {
			t.Fatalf("%+v", all)
		}
		mine, _ := Search(ctx, tx, SearchFilter{Limit: 10, PlayerID: &w.players[0]})
		if len(mine) != 1 {
			t.Fatalf("%+v", mine)
		}
		victim, _ := Search(ctx, tx, SearchFilter{Limit: 10, VictimPlayerID: &w.players[1], Outcome: "blocked"})
		if len(victim) != 1 || victim[0].ThiefPlayerID != w.players[2] {
			t.Fatalf("%+v", victim)
		}
		since, until := now.Add(-90*time.Minute), now
		ranged, _ := Search(ctx, tx, SearchFilter{Limit: 10, Since: &since, Until: &until, GroupID: &w.group})
		if len(ranged) != 1 || ranged[0].Outcome != "blocked" {
			t.Fatalf("%+v", ranged)
		}
		limited, _ := Search(ctx, tx, SearchFilter{Limit: 1})
		if len(limited) != 1 {
			t.Fatal(limited)
		}
		return nil
	})
}
