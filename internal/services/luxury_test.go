package services

// 對應 Python tests/test_luxury_items.py：自動播種機、自動販售機、倉庫擴大卷，以及免澆水／大風颳走活動。

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/repositories/repotest"
)

func (e *env) rich() int64 {
	pid := e.player()
	e.setGoldRecorded(pid, 5_000_000)
	return pid
}

// invariant 全服金幣 = 交易紀錄加總，且排行統計與交易紀錄一致。
func (e *env) invariant() {
	e.t.Helper()
	if !e.goldMatchesTransactions() {
		e.t.Fatal("金幣與交易紀錄不一致")
	}
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		r, err := farmstats.Check(ctx, tx, 20)
		if err == nil && r.FarmStatsMismatches != 0 {
			e.t.Fatalf("%+v", r)
		}
		return err
	})
}

// event 建立一個進行中的單次活動（Python admin.create_event）。
// createEvent Python event(admin, clock, **effects)：一次性活動，從一小時前到一天後。
func (e *env) createEvent(name string, effects map[string]any) (map[string]any, error) {
	now := e.clock.Now()
	raw := map[string]any{"name": name, "schedule_type": "once", "starts_at": now.Add(-time.Hour), "ends_at": now.Add(24 * time.Hour)}
	for k, v := range effects {
		raw[k] = v
	}
	return e.a.CreateEvent(e.ctx, "root", raw)
}

func (e *env) event(name string, freeWater, harvestLoss int) map[string]any {
	e.t.Helper()
	view, err := e.createEvent(name, map[string]any{"free_water": freeWater, "harvest_loss": harvestLoss})
	if err != nil {
		e.t.Fatal(err)
	}
	return view
}

func (e *env) plotCodes(pid int64) (empty int, planted []string) {
	for _, p := range e.status(pid).Plots {
		if p.State == rules.PlotEmpty {
			empty++
		} else {
			planted = append(planted, *p.CropCode)
		}
	}
	slices.Sort(planted)
	return
}

func TestAutoPlanterFillsEmptyPlotsFromBackpack(t *testing.T) {
	e := setup(t)
	pid := e.rich()
	e.ok(e.s.BuySeeds(e.ctx, pid, "carrot", 2))
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	if r := e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoPlanter, 1)); !strings.Contains(r.Message, "自動播種機種下 3 格") {
		t.Fatal(r.Message)
	}
	if _, planted := e.plotCodes(pid); !slices.Equal(planted, []string{"carrot", "carrot", "wheat"}) || // 只會用背包裡的種子
		len(must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).Seeds) != 0 {
		t.Fatal(planted)
	}
	// 收成後立即補種；沒有種子時說明原因，空地保留
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	e.clock.Advance(700)
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); !strings.Contains(r.Message, "沒有種子可以種") {
		t.Fatal(r.Message)
	}
	e.ok(e.s.BuySeeds(e.ctx, pid, "tomato", 5)) // 買種子也會觸發，把 2 格空地種滿
	seeds := 0
	for _, x := range must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).Seeds {
		seeds += x.Quantity
	}
	if empty, _ := e.plotCodes(pid); empty != 0 || seeds != 3 {
		t.Fatal(empty, seeds)
	}
	e.ok(e.s.SetToolEnabled(e.ctx, pid, "auto_planter", false))
	e.invariant()
}

func TestAutoSellerSellsHarvestWithoutUsingWarehouse(t *testing.T) {
	e := setup(t)
	pid := e.rich()
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoSeller, 1))
	gold := e.gold(pid)
	e.grow(pid, 1, "carrot")
	e.clock.Advance(180)
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); !strings.Contains(r.Message, "自動販售機賣出（單價 25），金幣 +75") {
		t.Fatal(r.Message)
	}
	if e.gold(pid) != gold-20+75 || len(must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).Produce) != 0 ||
		e.scalar(`SELECT count(*) FROM game_transactions WHERE event_type = 'sell' AND description LIKE '自動販售機：%' AND unit_price = 25`) != 1 {
		t.Fatal("自動販售結果不對")
	}
	e.invariant()
}

func TestAutoSellerIgnoresFullWarehouse(t *testing.T) {
	e := setup(t)
	pid := e.rich()
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoSeller, 1))
	e.grow(pid, 1, "wheat")
	e.give(pid, "seed_carrot", balance.InitialWarehouseCapacity) // 倉庫全滿（Python admin.set_inventory）
	e.clock.Advance(60)
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); !strings.Contains(r.Message, "自動販售機賣出") {
		t.Fatal(r.Message)
	}
}

func TestWarehouseExpansionPricesAndCap(t *testing.T) {
	e := setup(t)
	pid := e.rich()
	var paid []int
	for range 5 {
		before := e.gold(pid)
		e.ok(e.s.ExpandWarehouse(e.ctx, pid))
		paid = append(paid, before-e.gold(pid))
	}
	if !slices.Equal(paid, []int{4_000, 20_000, 50_000, 100_000, 200_000}) || e.status(pid).WarehouseCapacity != 300 {
		t.Fatal(paid)
	}
	_, err := e.s.ExpandWarehouse(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "最大容量")
	e.invariant()
}

func TestWarehouseExpansionNeedsGold(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.s.ExpandWarehouse(e.ctx, pid)
	wantKind(t, err, gameerr.InsufficientGold)
	if e.status(pid).WarehouseCapacity != 50 {
		t.Fatal("容量被改")
	}
}

func TestFreeWaterEventStartsGrowingOnPlant(t *testing.T) {
	e := setup(t)
	pid := e.player()
	if view := e.event("測試活動", 1, 0); !strings.Contains(view["effects_text"].(string), "免澆水") {
		t.Fatal(view)
	}
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	if r := e.ok(e.s.PlantCrop(e.ctx, pid, 1, "wheat")); !strings.Contains(r.Message, "已自動澆水") {
		t.Fatal(r.Message)
	}
	if p := e.plot(pid, 1); p.State != rules.PlotGrowing || *p.CropCode != "wheat" {
		t.Fatalf("%+v", p)
	}
}

func TestWindEventBlowsAwayButKeepsOne(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.event("測試活動", 0, 1)
	e.grow(pid, 1, "carrot")
	e.grow(pid, 2, "carrot")
	e.clock.Advance(180)
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); !strings.Contains(r.Message, "大風颳走 1 個") || !strings.Contains(r.Message, "獲得 2 個") {
		t.Fatal(r.Message)
	}
	e.event("超強風", 0, 10)
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 2)); !strings.Contains(r.Message, "獲得 1 個") { // 至少保留 1 個
		t.Fatal(r.Message)
	}
	e.invariant()
}

func TestEarthwormLastsThreeDays(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.Buy(e.ctx, pid, "蚯蚓", 1))
	if r := e.ok(e.s.UseItem(e.ctx, pid, "earthworm", nil)); !strings.Contains(r.Message, "剩餘 3 天") {
		t.Fatal(r.Message)
	}
}

func TestNewEventFieldsValidated(t *testing.T) {
	e := setup(t)
	_, err := e.createEvent("測試活動", map[string]any{"harvest_loss": 11})
	wantKind(t, err, gameerr.Validation)
	_, err = e.createEvent("測試活動", map[string]any{"free_water": 2})
	wantKind(t, err, gameerr.Validation)
}
