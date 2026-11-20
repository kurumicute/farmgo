package services

import (
	"strings"
	"testing"

	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/npcorder"
	"farmgm/internal/game/treasure"
	"farmgm/internal/game/weather"
)

// setPieces 直接設定藏寶圖碎片（測試用）。
func (e *env) setPieces(pid int64, n, east, s, w int) {
	e.t.Helper()
	e.exec(`INSERT INTO farm_tools (farm_id, tool_code, enabled, acquired_at, settings)
		SELECT id, $2, true, now(), $3 FROM farms WHERE player_id = $1`, pid, treasure.ToolCode,
		map[string]any{"pieces": map[string]int{"n": n, "e": east, "s": s, "w": w}})
}

// 藏寶圖（2026-11-20）：換碎片、出發、開箱、彩虹碎片。
func TestTreasureMap(t *testing.T) {
	e := setup(t)
	defer func() { weather.ForceLucks = nil }()
	pid := e.player()
	e.setGoldRecorded(pid, 1_000)
	_, err := e.s.StartTreasureHunt(e.ctx, pid)
	wantKind(t, err, gameerr.InsufficientItems)

	e.setPieces(pid, 4, 1, 1, 0)
	_, err = e.s.ExchangePieces(e.ctx, pid, "e", "w")
	wantKind(t, err, gameerr.InsufficientItems) // 東只有 1 片
	e.ok(e.s.ExchangePieces(e.ctx, pid, "n", "w"))
	v, err := e.s.TreasureMap(e.ctx, pid)
	if err != nil || !v.Complete || v.Pieces["n"] != 1 || v.Pieces["w"] != 1 || e.gold(pid) != 1_000-treasure.ExchangeFee {
		t.Fatalf("%+v %v", v, err)
	}

	r := e.ok(e.s.StartTreasureHunt(e.ctx, pid))
	if !strings.Contains(r.Message, treasure.WeekSite(e.clock.Now()).Name) {
		t.Fatal(r.Message)
	}
	_, err = e.s.StartTreasureHunt(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "已經出發")
	_, err = e.s.OpenTreasure(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還在路上")
	e.clock.Advance(treasure.HuntSeconds)
	before := e.gold(pid)
	r = e.ok(e.s.OpenTreasure(e.ctx, pid))
	if r.GoldDelta <= 0 || e.gold(pid) != before+r.GoldDelta || !strings.Contains(r.Message, "第一次來到這裡") {
		t.Fatal(r.Message)
	}
	if v, _ := e.s.TreasureMap(e.ctx, pid); v.Hunting || v.Pieces["n"] != 0 || len(v.Found) != 1 {
		t.Fatalf("%+v", v)
	}
	if n := e.scalar("SELECT count(*)::int FROM game_transactions WHERE event_type = 'rumor_treasure'"); n != 1 {
		t.Fatal(n)
	}

	// 彩虹日：每天領 1 片
	_, err = e.s.ClaimRainbowPiece(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "不是彩虹日")
	weather.ForceLucks = []string{weather.LuckRainbow}
	e.ok(e.s.ClaimRainbowPiece(e.ctx, pid))
	_, err = e.s.ClaimRainbowPiece(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "已經領過")
	if v, _ := e.s.TreasureMap(e.ctx, pid); v.Pieces["n"]+v.Pieces["e"]+v.Pieces["s"]+v.Pieces["w"] != 1 {
		t.Fatalf("%+v", v.Pieces)
	}
	e.invariant()
}

// NPC 訂單（2026-11-20）：補單、交貨（先扣低品質）、換單費、碎片。
func TestNpcOrders(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 1_000)
	e.s.TreasureRng = func() float64 { return 0.99 } // 不是 VIP、不掉碎片
	v, err := e.r.NpcOrders(e.ctx, pid)
	if err != nil || len(v.Orders) != npcorder.Slots || v.RerollFee != 0 {
		t.Fatalf("%+v %v", v, err)
	}
	o := v.Orders[0]
	if o.Ready {
		t.Fatal("還沒有物品不能交貨")
	}
	_, err = e.r.DeliverNpcOrder(e.ctx, pid, o.ID)
	wantKind(t, err, gameerr.InsufficientItems)
	for _, l := range o.Items {
		e.give(pid, l.Code, l.Qty)
	}
	before := e.gold(pid)
	r := e.ok(e.r.DeliverNpcOrder(e.ctx, pid, o.ID))
	if r.GoldDelta != o.Reward || e.gold(pid) != before+o.Reward {
		t.Fatal(r.Message)
	}
	for _, l := range o.Items {
		if e.qty(pid, l.Code) != 0 {
			t.Fatalf("%s 沒扣掉", l.Code)
		}
	}
	_, err = e.r.DeliverNpcOrder(e.ctx, pid, o.ID)
	wantKind(t, err, gameerr.NotFound)

	// 換單：每天第一次免費，之後收費
	v, _ = e.r.NpcOrders(e.ctx, pid)
	if len(v.Orders) != npcorder.Slots-1 {
		t.Fatal("交過的訂單要等補單時間才補")
	}
	r = e.ok(e.r.RerollNpcOrder(e.ctx, pid, v.Orders[0].ID))
	if r.GoldDelta != 0 {
		t.Fatal(r.Message)
	}
	v, _ = e.r.NpcOrders(e.ctx, pid)
	r = e.ok(e.r.RerollNpcOrder(e.ctx, pid, v.Orders[0].ID))
	if r.GoldDelta != -npcorder.RerollFee {
		t.Fatal(r.Message)
	}

	// 交貨會掉碎片（亂數 0）
	e.s.TreasureRng = func() float64 { return 0 }
	v, _ = e.r.NpcOrders(e.ctx, pid)
	o = v.Orders[0]
	for _, l := range o.Items {
		e.give(pid, l.Code, l.Qty)
	}
	if r := e.ok(e.r.DeliverNpcOrder(e.ctx, pid, o.ID)); !strings.Contains(r.Message, "藏寶圖碎片") {
		t.Fatal(r.Message)
	}
	e.invariant()
}
