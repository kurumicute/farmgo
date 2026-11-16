package services

// 管理網站：Go 版新增功能的唯讀檢視、金幣與物品的增減／數值檢查／原因（2026-11-16）。

import (
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
)

func TestAdminGoFeaturesPreviewDoesNotWrite(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.s.AutoFishRng = func() float64 { return 0.3 }
	e.autoBait(pid, 1000)
	e.setGoldRecorded(pid, 200_000)
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoFisher, 1))
	e.clock.Advance((12 * time.Minute).Seconds())
	gold := e.gold(pid)

	v := must[*AdminGoFeaturesView](t)(e.a.AdminGoFeatures(e.ctx, pid))
	if len(v.Errors) != 0 || v.Lab == nil || v.Cellar == nil || v.Exploration == nil || v.FishBasket == nil || len(v.Machines) == 0 {
		t.Fatalf("%+v", v)
	}
	// 檢視時模擬自動釣魚機補算（2 條），但交易回滾：資料庫沒有魚、魚餌沒有減少
	if fb := v.FishBasket; fb.Count != 2 || fb.AutoFish.Caught != 2 || !fb.AutoFish.Owned || !fb.AutoFish.Enabled {
		t.Fatalf("%+v", fb)
	}
	if e.basketCount(pid) != 0 || e.baitLeft(pid) != 1000 || e.gold(pid) != gold {
		t.Fatal(e.basketCount(pid), e.baitLeft(pid), e.gold(pid))
	}
	// 玩家自己打開面板時才真的結算
	if st := e.autoFish(pid); st.Caught != 2 || e.basketCount(pid) != 2 {
		t.Fatalf("%+v", st)
	}
	if v := must[*AdminGoFeaturesView](t)(e.a.AdminGoFeatures(e.ctx, 999_999)); v != nil {
		t.Fatal("沒有農場的玩家應該回傳 nil")
	}
}

func TestAdminGroupExtras(t *testing.T) {
	e := setup(t)
	g := e.group("遺跡群")
	empty := e.group("空群組")
	e.farmer("小華", g)
	v := must[*AdminGroupExtrasView](t)(e.a.AdminGroupExtras(e.ctx, g))
	if v.Ruins == nil || v.Ruins.Completed || v.Ruins.Stages == 0 || len(v.Ruins.Materials) == 0 || v.News == nil || len(v.Errors) != 0 {
		t.Fatalf("%+v", v)
	}
	if v := must[*AdminGroupExtrasView](t)(e.a.AdminGroupExtras(e.ctx, empty)); v.Ruins != nil || v.Errors["ruins"] == "" {
		t.Fatalf("%+v", v)
	}
	if v := must[*AdminGroupExtrasView](t)(e.a.AdminGroupExtras(e.ctx, 999_999)); v != nil {
		t.Fatal("不存在的群組應該回傳 nil")
	}
	// 檢視不會建立遺跡資料列
	if n := e.scalar("SELECT count(*)::int FROM group_ruins"); n != 0 {
		t.Fatal(n)
	}
}

func TestAdjustFarmValuesDeltaExpectAndReason(t *testing.T) {
	e := setup(t)
	pid := e.player()
	start := e.gold(pid)
	ptr := func(n int) *int { return &n }

	r := must[FarmValues](t)(e.a.AdjustFarmValues(e.ctx, actor, pid, FarmValuesAdjust{GoldDelta: ptr(250), Reason: "  補償活動獎勵  "}))
	if !r.Changed || r.Gold != start+250 || e.gold(pid) != start+250 {
		t.Fatalf("%+v", r)
	}
	if e.text("SELECT details->>'reason' FROM admin_actions WHERE action = 'set_farm_values' ORDER BY id DESC LIMIT 1") != "補償活動獎勵" {
		t.Fatal("原因要記在管理操作紀錄")
	}
	if n := e.scalar("SELECT gold_delta FROM game_transactions WHERE event_type = 'admin_adjust' ORDER BY id DESC LIMIT 1"); n != 250 {
		t.Fatal(n)
	}
	// 減少超過持有：拒絕
	_, err := e.a.AdjustFarmValues(e.ctx, actor, pid, FarmValuesAdjust{GoldDelta: ptr(-(start + 251))})
	wantMsg(t, err, gameerr.Validation, "調整後會是")
	// 畫面上的數值過期：拒絕，不蓋掉
	_, err = e.a.AdjustFarmValues(e.ctx, actor, pid, FarmValuesAdjust{Gold: ptr(10), ExpectGold: ptr(start)})
	wantMsg(t, err, gameerr.Validation, "請重新整理")
	_, err = e.a.AdjustFarmValues(e.ctx, actor, pid, FarmValuesAdjust{Level: ptr(3), ExpectLevel: ptr(9)})
	wantMsg(t, err, gameerr.Validation, "農場等級")
	if e.gold(pid) != start+250 {
		t.Fatal(e.gold(pid))
	}
	// 設為＋正確的目前數值：成功
	if r := must[FarmValues](t)(e.a.AdjustFarmValues(e.ctx, actor, pid, FarmValuesAdjust{Gold: ptr(10), ExpectGold: ptr(start + 250)})); r.Gold != 10 {
		t.Fatalf("%+v", r)
	}
	// 設為與增減不能同時使用；原因過長
	_, err = e.a.AdjustFarmValues(e.ctx, actor, pid, FarmValuesAdjust{Gold: ptr(1), GoldDelta: ptr(1)})
	wantKind(t, err, gameerr.Validation)
	long := make([]rune, AdminReasonMaxLen+1)
	for i := range long {
		long[i] = '字'
	}
	_, err = e.a.AdjustFarmValues(e.ctx, actor, pid, FarmValuesAdjust{GoldDelta: ptr(1), Reason: string(long)})
	wantMsg(t, err, gameerr.Validation, "原因")
}

func TestAdjustInventoryDeltaAndExpect(t *testing.T) {
	e := setup(t)
	pid := e.player()
	ptr := func(n int) *int { return &n }
	adj := func(in InventoryAdjust) (InventoryChange, error) {
		return e.a.AdjustInventory(e.ctx, actor, pid, "fertilizer", "normal", "none", in)
	}
	if r := must[InventoryChange](t)(adj(InventoryAdjust{Delta: ptr(5), Reason: "測試"})); r.Quantity != 5 {
		t.Fatalf("%+v", r)
	}
	if r := must[InventoryChange](t)(adj(InventoryAdjust{Delta: ptr(-2)})); r.Quantity != 3 {
		t.Fatalf("%+v", r)
	}
	_, err := adj(InventoryAdjust{Delta: ptr(-4)})
	wantMsg(t, err, gameerr.Validation, "調整後會是")
	_, err = adj(InventoryAdjust{Quantity: ptr(9), Expect: ptr(5)})
	wantMsg(t, err, gameerr.Validation, "請重新整理")
	if r := must[InventoryChange](t)(adj(InventoryAdjust{Quantity: ptr(0), Expect: ptr(3)})); r.Quantity != 0 {
		t.Fatalf("%+v", r)
	}
	if n := e.scalar("SELECT count(*)::int FROM inventory_items i JOIN farms f ON f.id = i.farm_id WHERE f.player_id = $1 "+
		"AND i.item_code = 'fertilizer'", pid); n != 0 {
		t.Fatal("數量 0 要刪除這筆庫存", n)
	}
	_, err = adj(InventoryAdjust{})
	wantKind(t, err, gameerr.Validation)
	if e.text("SELECT details->>'reason' FROM admin_actions WHERE action = 'set_inventory' ORDER BY id LIMIT 1") != "測試" {
		t.Fatal("原因要記在管理操作紀錄")
	}
}
