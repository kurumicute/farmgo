package services

// 離線摘要補充（2026-11-18）：離開期間的自動收入，以及面板逾時時記下的最後操作時間。

import (
	"strings"
	"testing"
)

func TestWelcomeBackListsAutoIncomeButNotOwnActions(t *testing.T) {
	e := setup(t)
	pid := e.player()
	must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, pid)) // 打開面板
	e.grow(pid, 1, "wheat")
	e.clock.Advance(3600)
	e.ok(e.s.HarvestAll(e.ctx, pid))                                // 自己收成：不能算成「自動收割」
	if err := e.p.MarkSeen(e.ctx, pid, e.clock.Now()); err != nil { // 面板逾時
		t.Fatal(err)
	}
	e.clock.Advance(3600)
	e.exec(`INSERT INTO game_transactions (farm_id, event_type, quantity, gold_delta, description, created_at)
		SELECT id, 'sell', 5, 500, '自動販售機', $2 FROM farms WHERE player_id = $1`, pid, e.clock.Now())
	e.clock.Advance(60)
	text := strings.Join(must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, pid)).Lines, "\n")
	if !strings.Contains(text, "你離開了 1 小時") || !strings.Contains(text, "自動販售、自動釣魚賺了 500 金") || strings.Contains(text, "自動收割") {
		t.Fatal(text)
	}
}

func TestMarkSeenNeverMovesBackwards(t *testing.T) {
	e := setup(t)
	pid := e.player()
	must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, pid))
	e.clock.Advance(3600)
	must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, pid)) // 另一個面板較晚打開
	if err := e.p.MarkSeen(e.ctx, pid, e.clock.Now().Add(-3600e9)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(60)
	if l := must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, pid)).Lines; len(l) != 0 { // 仍以較新的時間計算：只離開 1 分鐘
		t.Fatal(l)
	}
}

func TestTodoSummaryForNewFarm(t *testing.T) {
	e := setup(t)
	pid := e.player()
	v := must[TodoSummary](t)(e.s.TodoSummary(e.ctx, pid))
	if v.CellarUnlocked || v.LabBuilt || v.BasketCount != 0 || v.BasketCapacity != 0 {
		t.Fatalf("%+v", v)
	}
}
