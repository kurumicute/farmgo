package services

import (
	"strings"
	"testing"

	"farmgm/internal/game/beehive"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/weather"
)

// 蜂箱（2026-11-20）：放置、搬移、那一格不能種、全部種滿跳過、產蜜、升級。
func TestBeehive(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	pid := e.player()
	e.setGoldRecorded(pid, 100_000)
	e.exec("UPDATE farms SET level = 2 WHERE player_id = $1", pid)
	_, err := e.s.PlaceBeehive(e.ctx, pid, 1)
	wantMsg(t, err, gameerr.Validation, "Lv.3")

	e.exec("UPDATE farms SET level = 3 WHERE player_id = $1", pid)
	r := e.ok(e.s.PlaceBeehive(e.ctx, pid, 1))
	if r.GoldDelta != -beehive.Prices[1] {
		t.Fatal(r.Message)
	}
	r = e.ok(e.s.PlaceBeehive(e.ctx, pid, 2)) // 搬家
	if r.GoldDelta != -beehive.MoveFee || !strings.Contains(r.Message, "搬到第 2 格") {
		t.Fatal(r.Message)
	}
	st := e.status(pid)
	if st.Beehive == nil || st.Beehive.Plot != 2 || st.Plots[1].State != rules.PlotBeehive || st.Plots[0].State != rules.PlotEmpty {
		t.Fatalf("%+v", st.Plots)
	}

	// 蜂箱那一格不能種；全部種滿會跳過
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	_, err = e.s.PlantCrop(e.ctx, pid, 2, "wheat")
	wantKind(t, err, gameerr.PlotOccupied)
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	st = e.status(pid)
	if st.Plots[1].State != rules.PlotBeehive || st.EmptyPlots() != 0 {
		t.Fatalf("%+v", st.Plots)
	}
	_, err = e.s.PlaceBeehive(e.ctx, pid, 1)
	wantKind(t, err, gameerr.PlotOccupied) // 只能放在空地

	// 陰天 12 小時：Lv.1 每 6 小時 1 罐
	_, err = e.s.CollectHoney(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒有蜂蜜")
	e.clock.Advance(2 * beehive.IntervalSeconds)
	e.ok(e.s.CollectHoney(e.ctx, pid))
	if e.qty(pid, beehive.Honey) != 2 {
		t.Fatal(e.qty(pid, beehive.Honey))
	}

	r = e.ok(e.s.UpgradeBeehive(e.ctx, pid))
	if r.GoldDelta != -beehive.Prices[2] {
		t.Fatal(r.Message)
	}
	if v, _ := e.s.Beehive(e.ctx, pid); v.Hive.Level != 2 || v.Capacity != beehive.Capacity[2] {
		t.Fatalf("%+v", v)
	}
	e.invariant()
}
