package services

import (
	"strings"
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
)

// TestUseFertilizerPlots 一次對多格施化肥（2026-11-15，面板的化肥選單可以複選）：同一筆交易，任何一格不能施時全部不施。
func TestUseFertilizerPlots(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.give(pid, "seed_strawberry", 3)
	for _, n := range []int{1, 2, 3} {
		e.ok(e.plantWatered(pid, n, "strawberry"))
	}
	e.give(pid, balance.ItemFertilizer, 2)
	_, err := e.s.UseFertilizerPlots(e.ctx, pid, []int{1, 2, 3})
	wantMsg(t, err, gameerr.InsufficientItems, "選了 3 格，只有 2 個")
	before := e.status(pid).Plots
	r := e.ok(e.s.UseFertilizerPlots(e.ctx, pid, []int{1, 3, 1}))
	if strings.Count(r.Message, "已對第") != 2 || !strings.Contains(r.Message, "第 1 格") || !strings.Contains(r.Message, "第 3 格") {
		t.Fatal(r.Message)
	}
	after := e.status(pid).Plots
	if !after[0].Fertilized || after[1].Fertilized || !after[2].Fertilized || e.qty(pid, balance.ItemFertilizer) != 0 {
		t.Fatalf("%+v", after)
	}
	if !after[0].ReadyAt.Before(*before[0].ReadyAt) {
		t.Fatal("施肥後應該提早成熟")
	}
	// 已經施過的格子：整批失敗，化肥不會扣
	e.give(pid, balance.ItemFertilizer, 2)
	_, err = e.s.UseFertilizerPlots(e.ctx, pid, []int{2, 3})
	wantMsg(t, err, gameerr.Validation, "已經施過")
	if e.qty(pid, balance.ItemFertilizer) != 2 || e.status(pid).Plots[1].Fertilized {
		t.Fatal("失敗時不應該扣化肥或施肥")
	}
	_, err = e.s.UseFertilizerPlots(e.ctx, pid, nil)
	wantKind(t, err, gameerr.Validation)
	e.invariant()
}
