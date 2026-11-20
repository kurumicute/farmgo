package services

import (
	"testing"

	"farmgm/internal/game/questboard"
)

// 委託板（2026-11-20）：每週出題不重複、新手貢獻加倍、完成後群組增益。
func TestQuestBoard(t *testing.T) {
	e := setup(t)
	pid := e.player() // Lv.1、在「主群組」
	e.setGoldRecorded(pid, 10_000)
	if n, err := e.s.EnsureWeeklyQuestBoards(e.ctx); err != nil || n != len(questboard.Tiers) {
		t.Fatal(n, err)
	}
	if n, err := e.s.EnsureWeeklyQuestBoards(e.ctx); err != nil || n != 0 {
		t.Fatal("同一週不能重複出題", n, err)
	}
	if n := e.scalar("SELECT count(*)::int FROM quests WHERE created_by LIKE 'questboard:%'"); n != 3 {
		t.Fatal(n)
	}

	// 新手（Lv.3 以下）收成的貢獻加倍
	e.grow(pid, 1, "wheat")
	e.clock.Advance(3600)
	gid := int64(e.scalar("SELECT home_group_id::int FROM farms WHERE id = (SELECT id FROM farms LIMIT 1)"))
	r := e.ok(e.s.InGroupContext(&gid).HarvestCrop(e.ctx, pid, 1)) // 任務只計入在群組內的操作
	harvested := r.Quantity
	if n := e.scalar(`SELECT count(*)::int FROM quests WHERE created_by LIKE 'questboard:%' AND quest_type = 'harvest'
		AND progress <> $1`, 2*harvested); n != 0 {
		t.Fatalf("收成 %d 個，委託進度應為 %d", harvested, 2*harvested)
	}

	// 🟢 委託完成後 24 小時：澆水後生長時間 -5%
	e.grow(pid, 2, "carrot")
	e.exec("UPDATE quests SET status = 'completed', completed_at = $1 WHERE created_by = 'questboard:easy'", e.clock.Now())
	e.grow(pid, 3, "carrot")
	p2, p3 := e.plot(pid, 2), e.plot(pid, 3)
	before, after := p2.ReadyAt.Sub(*p2.WateredAt), p3.ReadyAt.Sub(*p3.WateredAt)
	if after*100 != before*95 {
		t.Fatalf("沒有增益 %v、有增益 %v", before, after)
	}
	e.clock.Advance(questboard.BuffHours * 3600)
	e.grow(pid, 4, "carrot")
	if p4 := e.plot(pid, 4); p4.ReadyAt.Sub(*p4.WateredAt) != before {
		t.Fatal("24 小時後增益要結束")
	}
	e.invariant()
}
