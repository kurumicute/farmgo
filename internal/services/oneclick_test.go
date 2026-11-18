package services

// 一鍵收成並重種（Go 版新增，2026-11-18）。

import (
	"strings"
	"testing"

	"farmgm/internal/game/rules"
)

func TestHarvestAndReplantKeepsCropsAndBuysSeeds(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 1_000)
	e.grow(pid, 1, "wheat")
	e.grow(pid, 2, "wheat")
	e.grow(pid, 3, "carrot")
	e.give(pid, "seed_wheat", 1) // 小麥種子有 1 顆，另一顆要補買
	e.clock.Advance(2*3600 + 60) // 小麥、胡蘿蔔都成熟，還沒乾枯
	gold := e.status(pid).Gold
	m := e.ok(e.s.HarvestAndReplant(e.ctx, pid)).Message
	if !strings.Contains(m, "🌱 重種：") || !strings.Contains(m, "在 2 格種下小麥（購買 1 顆種子）") || !strings.Contains(m, "在 1 格種下胡蘿蔔（購買 1 顆種子）") {
		t.Fatal(m)
	}
	st := e.status(pid)
	want := map[int]string{1: "wheat", 2: "wheat", 3: "carrot"}
	for _, p := range st.Plots {
		code, ok := want[p.PlotNumber]
		if !ok {
			if p.State != rules.PlotEmpty { // 原本空著的土地不會被種
				t.Fatal(p.PlotNumber, p.State)
			}
			continue
		}
		if p.CropCode == nil || *p.CropCode != code || p.State == rules.PlotEmpty || p.State == rules.PlotReady {
			t.Fatalf("第 %d 格：%+v", p.PlotNumber, p)
		}
	}
	if n := e.qty(pid, "seed_wheat"); n != 0 {
		t.Fatal("小麥種子應該用完", n)
	}
	if st.Gold >= gold {
		t.Fatal("應該扣了補買種子的錢", gold, st.Gold)
	}
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestHarvestAndReplantWithoutGoldStillHarvests(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	e.clock.Advance(2*3600 + 60) // 小麥、胡蘿蔔都成熟，還沒乾枯
	e.setGoldRecorded(pid, 0)
	m := e.ok(e.s.HarvestAndReplant(e.ctx, pid)).Message
	if !strings.Contains(m, "收成 1 格") || !strings.Contains(m, "金幣不足") {
		t.Fatal(m)
	}
	if p := e.status(pid).Plots[0]; p.State != rules.PlotEmpty {
		t.Fatal(p.State)
	}
}
