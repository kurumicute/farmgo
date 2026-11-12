package services

// 對應 Python tests/test_bulk_actions.py：一鍵操作（種滿、全部澆水、全部收成、全部清除）在同一筆交易中完成。

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
)

func (e *env) plotStates(pid int64) []rules.PlotState {
	e.t.Helper()
	var out []rules.PlotState
	for _, p := range e.status(pid).Plots {
		out = append(out, p.State)
	}
	return out
}

func (e *env) allStates(pid int64, want rules.PlotState) bool {
	e.t.Helper()
	for _, s := range e.plotStates(pid) {
		if s != want {
			return false
		}
	}
	return true
}

func (e *env) setInv(pid int64, code, quality, mutation string, qty int) {
	e.t.Helper()
	if _, err := e.a.SetInventory(e.ctx, "root", pid, code, quality, mutation, qty); err != nil {
		e.t.Fatal(err)
	}
}

func TestPlantWaterHarvestAll(t *testing.T) {
	e := setup(t)
	pid := e.player()
	if m := e.ok(e.s.PlantAll(e.ctx, pid, "wheat")).Message; !strings.Contains(m, "在 4 格種下小麥（購買 4 顆種子）") {
		t.Fatal(m)
	}
	if !e.allStates(pid, rules.PlotWaitingWater) {
		t.Fatal(e.plotStates(pid))
	}
	if m := e.ok(e.s.WaterAll(e.ctx, pid)).Message; !strings.Contains(m, "已為 4 格澆水") {
		t.Fatal(m)
	}
	e.clock.Advance(61)
	if m := e.ok(e.s.HarvestAll(e.ctx, pid)).Message; !strings.Contains(m, "收成 4 格，共 8 個作物") {
		t.Fatal(m)
	}
	if !e.allStates(pid, rules.PlotEmpty) {
		t.Fatal(e.plotStates(pid))
	}
	_, err := e.s.HarvestAll(e.ctx, pid)
	wantKind(t, err, gameerr.Validation)
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestPlantAllLimitedByGold(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 25)
	if m := e.ok(e.s.PlantAll(e.ctx, pid, "wheat")).Message; !strings.Contains(m, "金幣不夠，還有 2 格空著") {
		t.Fatal(m)
	}
	e.setGoldRecorded(pid, 0)
	_, err := e.s.PlantAll(e.ctx, pid, "carrot")
	wantKind(t, err, gameerr.InsufficientGold)
}

func TestHarvestAllStopsWhenWarehouseFull(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.PlantAll(e.ctx, pid, "strawberry")) // 每格 5 個
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.clock.Advance(600)
	e.setInv(pid, "carrot", "normal", "none", balance.InitialWarehouseCapacity-12)
	m := e.ok(e.s.HarvestAll(e.ctx, pid)).Message
	if !strings.Contains(m, "收成 2 格，共 10 個作物") || !strings.Contains(m, "還有 2 格沒收") {
		t.Fatal(m)
	}
	if e.allPlots(pid, rules.PlotReady) != 2 { // 放不下的留在農田，不會遺失
		t.Fatal(e.plotStates(pid))
	}
	e.setInv(pid, "carrot", "normal", "none", balance.InitialWarehouseCapacity-10)
	_, err := e.s.HarvestAll(e.ctx, pid)
	wantKind(t, err, gameerr.WarehouseFull)
}

func TestClearAllWithered(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.clock.Advance(60 + balance.WitherSeconds + 1)
	if m := e.ok(e.s.ClearAllWithered(e.ctx, pid)).Message; !strings.Contains(m, "已清除 4 格") {
		t.Fatal(m)
	}
	if !e.allStates(pid, rules.PlotEmpty) {
		t.Fatal(e.plotStates(pid))
	}
}

// 一鍵收成時任務清單只查一次：中途達標完成後，後面幾格不再計入（與逐格重查的結果相同）。
func TestHarvestAllQuestCompletesMidLoop(t *testing.T) {
	e := setup(t)
	pid := e.player()
	home := e.home(pid).ID
	now := e.clock.Now()
	must[QuestView](t)(e.a.CreateQuest(e.ctx, "root", NewQuest{GroupID: home, Name: "收成任務", QuestType: "harvest",
		Target: 3, RewardMode: "fixed", RewardType: "gold", RewardAmount: 10, StartsAt: now, EndsAt: now.Add(24 * time.Hour)}))
	svc := e.a.InGroupContext(&home)
	e.ok(svc.PlantAll(e.ctx, pid, "wheat")) // 4 格，每格 2 個
	e.ok(svc.WaterAll(e.ctx, pid))
	e.clock.Advance(61)
	if r := e.ok(svc.HarvestAll(e.ctx, pid)); r.Quantity != 8 {
		t.Fatal(r.Quantity)
	}
	q := must[[]QuestView](t)(e.a.GroupQuests(e.ctx, home, pid))[0]
	if q.CompletedAt == nil || q.Progress != 4 { // 第 2 格達標，第 3、4 格不計
		t.Fatalf("%+v", q)
	}
}
