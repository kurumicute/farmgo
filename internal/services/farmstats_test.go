package services

// 對應 Python tests/test_farm_stats.py：排行用統計表（farm_stats／farm_daily_stats）與交易紀錄重算一致、
// 排行結果與直接加總交易紀錄相同。

import (
	"context"
	"reflect"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/repositories/repotest"
)

type playedWorld struct {
	a, b    int64
	players []int64
}

// played 兩個群組、四位玩家，跨數天販售作物、動物產品，並有管理員調整（非經營金幣）。
func (e *env) played() playedWorld {
	e.t.Helper()
	w := playedWorld{a: e.group("A 群"), b: e.group("B 群")}
	for i, n := range []string{"甲", "乙", "丙", "丁"} {
		g := w.a
		if i >= 2 {
			g = w.b
		}
		w.players = append(w.players, e.farmer(n, g))
	}
	for day := range 3 {
		for i, pid := range w.players {
			g := w.a
			if i >= 2 {
				g = w.b
			}
			for range i + 1 {
				e.grow(pid, 1, "wheat")
				e.clock.Advance(61)
				e.ok(e.a.InGroupContext(&g).HarvestCrop(e.ctx, pid, 1))
			}
			e.ok(e.a.Sell(e.ctx, pid, "wheat", i+1, "normal", "none"))
			if i%2 == 0 {
				e.setInv(pid, "egg", "normal", "none", 3)
				e.ok(e.a.Sell(e.ctx, pid, "egg", 2, "normal", "none"))
			}
		}
		e.setGoldRecorded(w.players[3], 5000+day)
		e.clock.Advance(24 * 3600)
	}
	return w
}

func (e *env) statsCheck() farmstats.CheckResult {
	e.t.Helper()
	var r farmstats.CheckResult
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		var err error
		r, err = farmstats.Check(ctx, tx, 20)
		return err
	})
	return r
}

func TestStatsMatchTransactions(t *testing.T) {
	e := setup(t)
	e.played()
	if r := e.statsCheck(); r.FarmStatsMismatches != 0 || r.FarmDailyStatsMismatches != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestRebuildRestoresCorruptedStats(t *testing.T) {
	e := setup(t)
	e.played()
	e.exec("UPDATE farm_stats SET crop_sold_quantity = 999")
	if r := e.statsCheck(); r.FarmStatsMismatches != 4 {
		t.Fatalf("%+v", r)
	}
	var counts farmstats.RebuildResult
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		var err error
		counts, err = farmstats.Rebuild(ctx, tx)
		return err
	})
	if counts.FarmStats != 4 || counts.FarmDailyStats <= 0 {
		t.Fatalf("%+v", counts)
	}
	if r := e.statsCheck(); r.FarmStatsMismatches != 0 {
		t.Fatalf("%+v", r)
	}
}

type rankingSnapshot struct {
	people, group []leaderboard.Row
	groups        []leaderboard.GroupRow
}

func (e *env) rankings(kind string, group int64, since, until *time.Time) rankingSnapshot {
	e.t.Helper()
	var s rankingSnapshot
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		var err error
		if s.people, err = leaderboard.Ranking(ctx, tx, kind, nil, 50, since, until); err != nil {
			return err
		}
		if s.group, err = leaderboard.Ranking(ctx, tx, kind, &group, 50, since, until); err != nil {
			return err
		}
		s.groups, err = leaderboard.GroupRanking(ctx, tx, kind, 50, since, until)
		return err
	})
	return s
}

func TestRankingsEqualTransactionScan(t *testing.T) {
	e := setup(t)
	w := e.played()
	for _, kind := range leaderboard.Kinds {
		for _, period := range []string{"all", "day", "week", "month"} {
			t.Run(kind+"/"+period, func(t *testing.T) {
				since, until, err := ViewBounds(period, e.clock.Now().Add(-24*time.Hour)) // 前一天：有資料的期間
				if err != nil {
					t.Fatal(err)
				}
				fast := e.rankings(kind, w.a, since, until)
				restore := leaderboard.ForceTxStatsForTest(true)
				slow := e.rankings(kind, w.a, since, until)
				restore()
				if !reflect.DeepEqual(fast, slow) {
					t.Fatalf("統計表：%+v\n交易紀錄：%+v", fast, slow)
				}
				positive := false
				for _, r := range fast.people {
					positive = positive || r.Score > 0
				}
				if !positive {
					t.Fatalf("%+v", fast.people)
				}
			})
		}
	}
}

func TestNonAlignedPeriodFallsBackToTransactions(t *testing.T) {
	e := setup(t)
	e.played()
	since := e.clock.Now().Add(-30 * time.Hour) // 不是台灣午夜：直接加總交易紀錄
	now := e.clock.Now()
	var rows []leaderboard.Row
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		var err error
		rows, err = leaderboard.Ranking(ctx, tx, "goods", nil, 50, &since, &now)
		return err
	})
	for _, r := range rows {
		if r.SalesIncome < 0 {
			t.Fatalf("%+v", r)
		}
	}
}
