package services

import (
	"strings"
	"testing"

	"farmgm/internal/game/gameerr"
)

// 改名逐次加價（2026-11-19）：第 1 次免費，之後 1,000 起每次 ×2；30 天沒改名降一級。
func TestRenameFarmEscalates(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 10_000)
	cost := func() int { return must[int](t)(e.s.RenameCost(e.ctx, pid)) }
	if cost() != 0 {
		t.Fatal(cost())
	}
	if r := e.ok(e.s.RenameFarm(e.ctx, pid, "一號農場")); r.GoldDelta != 0 || !strings.Contains(r.Message, "免費") ||
		!strings.Contains(r.Message, "下次改名需要 1000 金幣") {
		t.Fatal(r.Message)
	}
	for i, want := range []int{1000, 2000, 4000} {
		if cost() != want {
			t.Fatal(i, cost())
		}
		if r := e.ok(e.s.RenameFarm(e.ctx, pid, []string{"二號", "三號", "四號"}[i])); r.GoldDelta != -want {
			t.Fatal(r.GoldDelta)
		}
	}
	if e.gold(pid) != 10_000-7000 || cost() != 8000 {
		t.Fatal(e.gold(pid), cost())
	}
	e.clock.Advance(31 * 86400) // 30 天沒改名降一級
	if cost() != 4000 {
		t.Fatal(cost())
	}
	e.setGoldRecorded(pid, 3999)
	_, err := e.s.RenameFarm(e.ctx, pid, "五號")
	wantKind(t, err, gameerr.InsufficientGold)
	if n := e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'rename_farm'"); n != 4 {
		t.Fatal(n)
	}
	e.invariant()
}

// 素材背包擴充（2026-11-19）：每級 +200 格，50,000 起每級 ×1.8，最多 10 級。
func TestExpandMaterialBag(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 1_000_000_000)
	base := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).MaterialCapacity
	var paid []int
	for range 10 {
		before := e.gold(pid)
		e.ok(e.s.ExpandMaterialBag(e.ctx, pid))
		paid = append(paid, before-e.gold(pid))
	}
	if paid[0] != 50_000 || paid[1] != 90_000 || paid[2] != 162_000 {
		t.Fatal(paid)
	}
	if c := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).MaterialCapacity; c != base+2000 {
		t.Fatal(base, c)
	}
	_, err := e.s.ExpandMaterialBag(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "最高等級")
	e.invariant()
}
