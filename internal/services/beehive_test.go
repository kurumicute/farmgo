package services

import (
	"slices"
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
	if r.GoldDelta != -beehive.Price(1) {
		t.Fatal(r.Message)
	}
	r = e.ok(e.s.PlaceBeehive(e.ctx, pid, 2)) // 搬家
	if r.GoldDelta != -beehive.MoveFee() || !strings.Contains(r.Message, "搬到第 2 格") {
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

	// 2026-11-21 起產蜜固定：每 12 小時 2 罐，不分等級、天氣
	_, err = e.s.CollectHoney(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒有蜂蜜")
	e.clock.Advance(float64(2 * beehive.IntervalSeconds()))
	e.ok(e.s.CollectHoney(e.ctx, pid))
	if e.qty(pid, beehive.Honey) != 2*beehive.HoneyPerInterval() {
		t.Fatal(e.qty(pid, beehive.Honey))
	}

	r = e.ok(e.s.UpgradeBeehive(e.ctx, pid))
	if r.GoldDelta != -beehive.Price(2) {
		t.Fatal(r.Message)
	}
	if v, _ := e.s.Beehive(e.ctx, pid); v.Hive.Level != 2 || v.Capacity != beehive.Capacity() {
		t.Fatalf("%+v", v)
	}
	// 2026-11-21 起最高 Lv.5
	e.setGoldRecorded(pid, 400_000)
	for lv := 3; lv <= beehive.MaxLevel(); lv++ {
		if r := e.ok(e.s.UpgradeBeehive(e.ctx, pid)); r.GoldDelta != -beehive.Price(lv) {
			t.Fatal(lv, r.Message)
		}
	}
	_, err = e.s.UpgradeBeehive(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "最高等級（Lv.5）")
	if v, _ := e.s.Beehive(e.ctx, pid); v.Hive.Level != 5 {
		t.Fatalf("%+v", v)
	}
	e.invariant()
}

// 拆除蜂箱（2026-11-20）：蜂蜜先收進倉庫、倉庫放不下時不拆、拆完那一格可以種、重新放置要再付放置費。
func TestRemoveBeehive(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 100_000)
	e.exec("UPDATE farms SET level = 3 WHERE player_id = $1", pid)
	_, err := e.s.RemoveBeehive(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒有蜂箱")

	e.ok(e.s.PlaceBeehive(e.ctx, pid, 1))
	e.ok(e.s.UpgradeBeehive(e.ctx, pid))
	e.clock.Advance(float64(2 * beehive.IntervalSeconds())) // 每次 2 罐，共 4 罐

	// 倉庫放不下全部蜂蜜：不拆
	e.exec("UPDATE farms SET warehouse_capacity = 2 WHERE player_id = $1", pid)
	_, err = e.s.RemoveBeehive(e.ctx, pid)
	wantMsg(t, err, gameerr.WarehouseFull, "還有 4 罐蜂蜜")
	if v, _ := e.s.Beehive(e.ctx, pid); !v.Hive.Placed() {
		t.Fatal("失敗時蜂箱要保留")
	}
	e.exec("UPDATE farms SET warehouse_capacity = 200 WHERE player_id = $1", pid)

	gold := e.gold(pid)
	r := e.ok(e.s.RemoveBeehive(e.ctx, pid))
	if !strings.Contains(r.Message, "已拆除第 1 格") || !strings.Contains(r.Message, "4 罐蜂蜜") || e.qty(pid, beehive.Honey) != 4 {
		t.Fatal(r.Message, e.qty(pid, beehive.Honey))
	}
	if e.gold(pid) != gold {
		t.Fatal("拆除不退費也不收費")
	}
	if v, _ := e.s.Beehive(e.ctx, pid); v.Hive.Placed() || !slices.Contains(v.EmptyPlots, 1) {
		t.Fatalf("%+v", v)
	}
	st := e.status(pid)
	if st.Beehive != nil || st.Plots[0].State != rules.PlotEmpty {
		t.Fatalf("%+v", st.Plots[0])
	}
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	e.ok(e.s.PlantCrop(e.ctx, pid, 1, "wheat")) // 那一格可以種了
	_, err = e.s.RemoveBeehive(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒有蜂箱")

	// 重新放置要再付放置費，等級從 Lv.1 開始
	r = e.ok(e.s.PlaceBeehive(e.ctx, pid, 2))
	if r.GoldDelta != -beehive.Price(1) {
		t.Fatal(r.Message)
	}
	if v, _ := e.s.Beehive(e.ctx, pid); v.Hive.Level != 1 || v.Hive.Honey != 0 {
		t.Fatalf("%+v", v.Hive)
	}
	e.invariant()
}
