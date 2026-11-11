package services

// 對應 Python tests/test_admin.py 的服務層測試（純規則在 game/events、game/genetics；網站 API 在第 4 階段），
// 以及 tests/test_group_transfer.py::test_admin_transfer_requires_reason_and_records_bypass。

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/repotest"
)

const actor = "admin"

func newCrop() map[string]any {
	return map[string]any{"code": "eggplant", "name": "茄子", "seed_price": 30, "grow_seconds": 120, "harvest_quantity": 2,
		"sell_price": 40, "variant_code": "violet_eggplant", "variant_name": "紫晶茄子"}
}

func newAnimal() map[string]any {
	return map[string]any{"code": "duck", "name": "鴨", "price": 300, "feed_cost": 10, "cycle_seconds": 3600,
		"product_code": "duck_egg", "product_name": "鴨蛋", "product_quantity": 3, "product_price": 15}
}

// with Python {**base, **changes}。
func with(base map[string]any, kv ...any) map[string]any {
	out := maps.Clone(base)
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// onceEvent Python once_event：從一小時前到一小時後。
func (e *env) onceEvent(name string, kv ...any) map[string]any {
	now := e.clock.Now()
	return with(map[string]any{"name": name, "schedule_type": "once", "starts_at": now.Add(-time.Hour),
		"ends_at": now.Add(time.Hour)}, kv...)
}

func (e *env) auditActions() []string {
	e.t.Helper()
	var out []string
	repotestQuery(e.t, e, "SELECT action FROM admin_actions ORDER BY id", &out)
	return out
}

// appliedEvents 某種交易紀錄的 applied_events（依 id 排序；NULL 為 nil）。
func (e *env) appliedEvents(event string) [][]map[string]any {
	e.t.Helper()
	var raw []string
	repotestQuery(e.t, e, "SELECT coalesce(applied_events::text, 'null') FROM game_transactions WHERE event_type = '"+event+
		"' ORDER BY id", &raw)
	out := make([][]map[string]any, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal([]byte(r), &out[i]); err != nil {
			e.t.Fatal(err)
		}
	}
	return out
}

func eventID(view map[string]any) int64 { return view["id"].(int64) }

func (e *env) adminErr(err error, kind gameerr.Kind) {
	e.t.Helper()
	wantKind(e.t, err, kind)
}

// ---------- 限時事件整合 ----------

func TestSellEventAppliesAndIsRecorded(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	e.clock.Advance(60)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	wheat := must[CropCatalogEntry](t)(e.s.FindCropInfo(e.ctx, "wheat"))
	ev := must[map[string]any](t)(e.a.CreateEvent(e.ctx, actor, e.onceEvent("豐收祭", "sell_price_pct", 150)))
	must[map[string]any](t)(e.a.CreateEvent(e.ctx, actor, e.onceEvent("停用活動", "sell_price_pct", 300, "enabled", false)))
	must[map[string]any](t)(e.a.CreateEvent(e.ctx, actor, e.onceEvent("只限草莓", "sell_price_pct", 300, "crop_code", "strawberry")))
	if r := e.ok(e.s.SellCrop(e.ctx, pid, "wheat", 1, "normal", "none")); r.GoldDelta != wheat.SellPrice*150/100 {
		t.Fatal(r.GoldDelta)
	}
	if e.scalar("SELECT unit_price FROM game_transactions WHERE event_type = 'sell' ORDER BY id LIMIT 1") != 22 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'sell' AND description LIKE '%豐收祭%'") != 1 {
		t.Fatal("售價或說明不對")
	}
	applied := e.appliedEvents("sell")
	if len(applied[0]) != 1 || int64(applied[0][0]["id"].(float64)) != eventID(ev) || applied[0][0]["name"] != "豐收祭" {
		t.Fatal(applied)
	}
	// 停用後恢復原價
	must[map[string]any](t)(e.a.SetEventEnabled(e.ctx, actor, eventID(ev), false))
	if r := e.ok(e.s.SellCrop(e.ctx, pid, "wheat", 1, "normal", "none")); r.GoldDelta != wheat.SellPrice {
		t.Fatal(r.GoldDelta)
	}
	if applied := e.appliedEvents("sell"); applied[1] != nil {
		t.Fatal(applied)
	}
}

func TestPlantAndWaterEvents(t *testing.T) {
	e := setup(t)
	pid := e.player()
	harvest := must[map[string]any](t)(e.a.CreateEvent(e.ctx, actor, e.onceEvent("大豐收", "harvest_bonus_pct", 100)))
	grow := must[map[string]any](t)(e.a.CreateEvent(e.ctx, actor, e.onceEvent("生長加速", "grow_reduction_pct", 50)))
	e.ok(e.s.BuySeeds(e.ctx, pid, "carrot", 1))
	e.ok(e.s.PlantCrop(e.ctx, pid, 1, "carrot"))
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	p := e.plot(pid, 1)
	if *p.RemainingQuantity != 6 || p.RemainingSeconds != 90 { // 3 × (100% + 100%)；180 秒 × 50%
		t.Fatalf("%+v", p)
	}
	plant, water := e.appliedEvents("plant"), e.appliedEvents("water")
	if int64(plant[0][0]["id"].(float64)) != eventID(harvest) || int64(water[0][0]["id"].(float64)) != eventID(grow) {
		t.Fatal(plant, water)
	}
	// 事件結束後已決定的數量不變
	e.clock.Advance(3 * 3600)
	if evs := must[[]ActiveEventView](t)(e.s.ActiveEvents(e.ctx)); len(evs) != 0 {
		t.Fatal(evs)
	}
	if *e.plot(pid, 1).RemainingQuantity != 6 {
		t.Fatal("數量被改")
	}
}

func TestEventCrudAndAudit(t *testing.T) {
	e := setup(t)
	ev := must[map[string]any](t)(e.a.CreateEvent(e.ctx, actor, map[string]any{"name": "週末", "schedule_type": "weekly",
		"start_minute": 600, "end_minute": 720, "weekdays": 0b1100000, "quality_bonus_bp": 500}))
	if ev["schedule_text"] != "每週六、日 10:00–12:00" || ev["active_now"] != false {
		t.Fatal(ev)
	}
	// Python 把整個 view 加上修改送回；view 的時段欄位在 Go 是指標，這裡直接列出表單欄位
	changed := map[string]any{"name": "週末", "schedule_type": "weekly", "start_minute": 480, "end_minute": 720,
		"weekdays": 1 << 3, "quality_bonus_bp": 500, "crop_code": "tomato"}
	updated := must[map[string]any](t)(e.a.UpdateEvent(e.ctx, actor, eventID(ev), changed))
	if updated["active_now"] != true || updated["crop_type_id"].(*int64) == nil { // 測試時鐘：週四 08:00（台灣）
		t.Fatal(updated)
	}
	_, err := e.a.UpdateEvent(e.ctx, actor, 999, changed)
	e.adminErr(err, gameerr.NotFound)
	_, err = e.a.CreateEvent(e.ctx, actor, e.onceEvent("測試活動", "harvest_bonus_pct", 1, "crop_code", "nothing"))
	e.adminErr(err, gameerr.NotFound)
	if err := e.a.DeleteEvent(e.ctx, actor, eventID(ev)); err != nil {
		t.Fatal(err)
	}
	if evs := must[[]ActiveEventView](t)(e.s.ActiveEvents(e.ctx)); len(evs) != 0 {
		t.Fatal(evs)
	}
	if got := e.auditActions(); !slices.Equal(got, []string{"create_event", "update_event", "delete_event"}) {
		t.Fatal(got)
	}
}

// ---------- 作物種類 ----------

func marketCodes(t *testing.T, e *env) []string {
	var codes []string
	for _, c := range must[[]CropInfo](t)(e.s.GetMarketPrices(e.ctx)) {
		codes = append(codes, c.Code)
	}
	return codes
}

func TestCreateCropFullLifecycle(t *testing.T) {
	e := setup(t)
	pid := e.player()
	if crop := must[CropView](t)(e.a.CreateCrop(e.ctx, actor, newCrop())); !crop.Active {
		t.Fatal(crop)
	}
	if !slices.Contains(marketCodes(t, e), "eggplant") {
		t.Fatal("市場沒有新作物")
	}
	e.ok(e.s.BuySeeds(e.ctx, pid, "茄子", 1))
	e.ok(e.s.PlantCrop(e.ctx, pid, 1, "eggplant"))
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	e.clock.Advance(120)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	if r := e.ok(e.s.SellCrop(e.ctx, pid, "茄子", 2, "normal", "none")); r.GoldDelta != 80 {
		t.Fatal(r.GoldDelta)
	}
	// 珍稀變種使用資料庫中的代碼與名稱
	must[InventoryChange](t)(e.a.SetInventory(e.ctx, actor, pid, "violet_eggplant", "rare", "variant", 1))
	bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	if len(bp.Produce) != 1 || bp.Produce[0].Name != "紫晶茄子" || bp.Produce[0].Quality != "rare" {
		t.Fatalf("%+v", bp.Produce)
	}
	if r := e.ok(e.s.SellCrop(e.ctx, pid, "紫晶茄子", 1, "rare", "none")); r.GoldDelta != 40*4*150/100 {
		t.Fatal(r.GoldDelta)
	}
}

func TestCropValidationAndDuplicates(t *testing.T) {
	e := setup(t)
	_, err := e.a.CreateCrop(e.ctx, actor, with(newCrop(), "code", "wheat"))
	e.adminErr(err, gameerr.AlreadyExists)
	_, err = e.a.CreateCrop(e.ctx, actor, with(newCrop(), "variant_code", "egg")) // 與動物產品重複
	e.adminErr(err, gameerr.AlreadyExists)
	_, err = e.a.CreateCrop(e.ctx, actor, with(newCrop(), "name", "小麥"))
	wantMsg(t, err, gameerr.AlreadyExists, "小麥")
	for _, bad := range [][]any{{"code", "Seed_x"}, {"code", "seed_eggplant"}, {"grow_seconds", 10}, {"seed_price", 0},
		{"variant_code", "eggplant"}, {"seed_price", true}, {"seed_price", 30.0}} {
		_, err := e.a.CreateCrop(e.ctx, actor, with(newCrop(), bad...))
		e.adminErr(err, gameerr.Validation)
	}
	updated := must[CropView](t)(e.a.UpdateCrop(e.ctx, actor, "wheat", with(newCrop(), "name", "大麥", "variant_name", "金大麥")))
	if updated.Name != "大麥" || updated.Code != "wheat" || updated.VariantCode != "amber_wheat" {
		t.Fatalf("%+v", updated)
	}
}

func TestDisableAndDeleteCrop(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "tomato")
	e.ok(e.s.BuySeeds(e.ctx, pid, "tomato", 1))
	must[CropView](t)(e.a.SetCropActive(e.ctx, actor, "tomato", false))
	if slices.Contains(marketCodes(t, e), "tomato") {
		t.Fatal("停用後市場仍有番茄")
	}
	_, err := e.s.BuySeeds(e.ctx, pid, "tomato", 1)
	wantMsg(t, err, gameerr.Validation, "停用")
	_, err = e.s.PlantCrop(e.ctx, pid, 2, "tomato")
	wantMsg(t, err, gameerr.Validation, "停用")
	// 已種下與已持有的仍可收成、販售
	e.clock.Advance(300)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	if r := e.ok(e.s.SellCrop(e.ctx, pid, "tomato", 1, "normal", "none")); r.GoldDelta != 20 {
		t.Fatal(r.GoldDelta)
	}
	wantMsg(t, e.a.DeleteCrop(e.ctx, actor, "tomato"), gameerr.Validation, "改為停用")
	wantMsg(t, e.a.DeleteCrop(e.ctx, actor, "wheat"), gameerr.Validation, "加工配方")
	must[CropView](t)(e.a.CreateCrop(e.ctx, actor, newCrop()))
	if err := e.a.DeleteCrop(e.ctx, actor, "eggplant"); err != nil {
		t.Fatal(err)
	}
	e.adminErr(e.a.DeleteCrop(e.ctx, actor, "eggplant"), gameerr.NotFound)
	if got := e.auditActions(); !slices.Equal(got, []string{"disable_crop", "create_crop", "delete_crop"}) {
		t.Fatal(got)
	}
}

// ---------- 動物種類 ----------

func TestAnimalTypeLifecycle(t *testing.T) {
	e := setup(t)
	pid := e.player()
	must[AnimalTypeView](t)(e.a.CreateAnimalType(e.ctx, actor, newAnimal()))
	must[BuildingSlot](t)(e.a.AddBuilding(e.ctx, actor, pid, "barn"))
	e.ok(e.r.BuyAnimals(e.ctx, pid, "鴨", 1))
	must[[]int](t)(e.a.AddAnimalsForPlayer(e.ctx, actor, pid, "duck", 1))
	e.ok(e.r.FeedAll(e.ctx, pid))
	e.clock.Advance(3600)
	e.ok(e.r.CollectAnimal(e.ctx, pid, 1))
	if r := e.ok(e.r.Sell(e.ctx, pid, "鴨蛋", 3, "normal", "none")); r.GoldDelta != 45 {
		t.Fatal(r.GoldDelta)
	}
	// 停用：不能再購買，已飼養的仍可餵食、收集
	must[AnimalTypeView](t)(e.a.SetAnimalTypeActive(e.ctx, actor, "duck", false))
	_, err := e.r.BuyAnimals(e.ctx, pid, "duck", 1)
	wantMsg(t, err, gameerr.NotFound, "停用")
	e.ok(e.r.FeedAnimal(e.ctx, pid, 1))
	e.clock.Advance(3600)
	e.ok(e.r.CollectAnimal(e.ctx, pid, 2))
	wantMsg(t, e.a.DeleteAnimalType(e.ctx, actor, "duck"), gameerr.Validation, "改為停用")
	updated := must[AnimalTypeView](t)(e.a.UpdateAnimalType(e.ctx, actor, "duck", with(newAnimal(), "product_price", 20)))
	if updated.ProductPrice != 20 {
		t.Fatalf("%+v", updated)
	}
	if r := e.ok(e.r.Sell(e.ctx, pid, "duck_egg", 1, "normal", "none")); r.GoldDelta != 20 {
		t.Fatal(r.GoldDelta)
	}
	_, err = e.a.CreateAnimalType(e.ctx, actor, with(newAnimal(), "code", "goose", "product_code", "wool"))
	e.adminErr(err, gameerr.AlreadyExists)
	must[AnimalTypeView](t)(e.a.CreateAnimalType(e.ctx, actor, with(newAnimal(), "code", "goose", "name", "鵝",
		"product_code", "goose_egg", "product_name", "鵝蛋")))
	if err := e.a.DeleteAnimalType(e.ctx, actor, "goose"); err != nil {
		t.Fatal(err)
	}
}

func TestQuestItemFilterUsesDBProducts(t *testing.T) {
	e := setup(t)
	pid := e.player()
	must[AnimalTypeView](t)(e.a.CreateAnimalType(e.ctx, actor, newAnimal()))
	now := e.clock.Now()
	q := NewQuest{GroupID: e.home(pid).ID, Name: "鴨蛋任務", QuestType: "animal", Target: 10, RewardMode: "fixed",
		RewardType: "gold", RewardAmount: 10, StartsAt: now, EndsAt: now.Add(24 * time.Hour), ItemFilter: strPtr("鴨蛋")}
	quest := must[QuestView](t)(e.a.CreateQuest(e.ctx, actor, q))
	if deref(quest.ItemFilter) != "duck_egg" || !strings.Contains(quest.GoalText, "鴨蛋") {
		t.Fatalf("%+v", quest)
	}
	q.ItemFilter = strPtr("nope")
	_, err := e.a.CreateQuest(e.ctx, actor, q)
	e.adminErr(err, gameerr.Validation)
}

// ---------- 調整玩家 ----------

func TestSetInventoryMaterialsUseMaterialBag(t *testing.T) {
	e := setup(t)
	pid := e.player()
	must[InventoryChange](t)(e.a.SetInventory(e.ctx, actor, pid, "egg", "normal", "none", balance.InitialWarehouseCapacity)) // 倉庫全滿
	must[InventoryChange](t)(e.a.SetInventory(e.ctx, actor, pid, "wood", "normal", "none", 10))                              // 素材放素材背包
	if bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)); bp.MaterialUsed != 10 {
		t.Fatal(bp.MaterialUsed)
	}
	_, err := e.a.SetInventory(e.ctx, actor, pid, "stone", "normal", "none", exploration.MaterialBagCapacity)
	wantMsg(t, err, gameerr.WarehouseFull, "素材背包")
}

func TestSetInventoryAddEditDelete(t *testing.T) {
	e := setup(t)
	pid := e.player()
	set := func(code, q, m string, n int) InventoryChange {
		t.Helper()
		return must[InventoryChange](t)(e.a.SetInventory(e.ctx, actor, pid, code, q, m, n))
	}
	set("fertilizer", "normal", "none", 3)
	set("wheat", "legendary", "golden", 2)
	set("fertilizer", "normal", "none", 1)
	if set("fertilizer", "normal", "none", 1).Changed {
		t.Fatal("沒有變更")
	}
	inv := map[string]int{}
	for _, it := range must[Inventory](t)(e.s.GetInventory(e.ctx, pid)).Items {
		inv[it.ItemCode+"|"+it.DisplayName] = it.Quantity
	}
	if !maps.Equal(inv, map[string]int{"fertilizer|化肥": 1, "wheat|小麥【傳說・黃金作物】": 2}) {
		t.Fatal(inv)
	}
	set("wheat", "legendary", "golden", 0)
	if items := must[Inventory](t)(e.s.GetInventory(e.ctx, pid)).Items; len(items) != 1 || items[0].ItemCode != "fertilizer" {
		t.Fatalf("%+v", items)
	}
	var qty []string
	repotestQuery(t, e, "SELECT quantity::text FROM game_transactions WHERE event_type = 'admin_adjust' ORDER BY id", &qty)
	if !slices.Equal(qty, []string{"3", "2", "2", "2"}) ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'admin_adjust' AND gold_delta <> 0") != 0 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'admin_adjust' AND description LIKE '管理員調整%'") != 4 {
		t.Fatal(qty)
	}
	_, err := e.a.SetInventory(e.ctx, actor, pid, "egg", "normal", "none", balance.InitialWarehouseCapacity)
	e.adminErr(err, gameerr.WarehouseFull)
	_, err = e.a.SetInventory(e.ctx, actor, pid, "gold_bar", "normal", "none", 1)
	e.adminErr(err, gameerr.NotFound)
	for _, bad := range [][3]string{{"seed_wheat", "rare", "none"}, {"wheat", "normal", "variant"}, {"amber_wheat", "normal", "none"}} {
		_, err = e.a.SetInventory(e.ctx, actor, pid, bad[0], bad[1], bad[2], 1)
		e.adminErr(err, gameerr.Validation)
	}
	_, err = e.a.SetInventory(e.ctx, actor, 999, "wheat", "normal", "none", 1)
	e.adminErr(err, gameerr.NotFound)
}

func TestPlotsAddDeleteAndStealHistoryKept(t *testing.T) {
	e := setup(t)
	g := e.group("群")
	thief, victim := e.farmer("小偷", g), e.farmer("苦主", g)
	e.grow(victim, 4, "wheat")
	e.clock.Advance(60)
	e.steal(g, thief, victim, 4, 1)
	wantMsg(t, e.a.DeletePlot(e.ctx, actor, victim, 4), gameerr.Validation, "還有作物")
	if err := e.a.ClearPlotForPlayer(e.ctx, actor, victim, 4); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DeletePlot(e.ctx, actor, victim, 4); err != nil {
		t.Fatal(err)
	}
	if e.scalar("SELECT count(*) FROM steal_records WHERE plot_id IS NULL AND plot_number = 4") != 1 {
		t.Fatal("偷菜紀錄沒有保留")
	}
	var nums []int
	for _, p := range e.status(victim).Plots {
		nums = append(nums, p.PlotNumber)
	}
	if !slices.Equal(nums, []int{1, 2, 3}) {
		t.Fatal(nums)
	}
	added := must[PlotsAdded](t)(e.a.AddPlots(e.ctx, actor, victim, 2))
	if !slices.Equal(added.Added, []int{4, 5}) || added.Total != 5 {
		t.Fatalf("%+v", added)
	}
	_, err := e.a.AddPlots(e.ctx, actor, victim, balance.AdminMaxPlots)
	e.adminErr(err, gameerr.Validation)
	e.adminErr(e.a.DeletePlot(e.ctx, actor, victim, 99), gameerr.NotFound)
}

func TestPlantForPlayerReadyNow(t *testing.T) {
	e := setup(t)
	pid := e.player()
	if r := must[PlantedForPlayer](t)(e.a.PlantForPlayer(e.ctx, actor, pid, 2, "strawberry", "rare", "giant", false, true)); r.Quantity != 10 {
		t.Fatalf("%+v", r)
	}
	if h := e.ok(e.s.HarvestCrop(e.ctx, pid, 2)); h.Quantity != 10 || !strings.Contains(h.Message, "稀有・巨型作物") {
		t.Fatal(h.Message)
	}
	must[PlantedForPlayer](t)(e.a.PlantForPlayer(e.ctx, actor, pid, 3, "wheat", "normal", "none", true, false))
	if st := e.plot(pid, 3).State; st != rules.PlotGrowing {
		t.Fatal(st)
	}
	must[PlantedForPlayer](t)(e.a.PlantForPlayer(e.ctx, actor, pid, 1, "wheat", "normal", "variant", false, false))
	_, err := e.a.PlantForPlayer(e.ctx, actor, pid, 1, "wheat", "normal", "none", false, false)
	wantMsg(t, err, gameerr.Validation, "已經有作物")
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	e.clock.Advance(60)
	if h := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); h.ItemCode != "amber_wheat" {
		t.Fatal(h.ItemCode)
	}
	must[CropView](t)(e.a.SetCropActive(e.ctx, actor, "carrot", false))
	_, err = e.a.PlantForPlayer(e.ctx, actor, pid, 1, "carrot", "normal", "none", false, false)
	wantMsg(t, err, gameerr.Validation, "停用")
	e.adminErr(e.a.ClearPlotForPlayer(e.ctx, actor, pid, 1), gameerr.Validation)
}

func TestBuildingsAndAnimals(t *testing.T) {
	e := setup(t)
	pid := e.player()
	must[BuildingSlot](t)(e.a.AddBuilding(e.ctx, actor, pid, "barn"))
	must[[]int](t)(e.a.AddAnimalsForPlayer(e.ctx, actor, pid, "chicken", 4))
	_, err := e.a.AddAnimalsForPlayer(e.ctx, actor, pid, "cow", 1)
	wantMsg(t, err, gameerr.Validation, "容量不足")
	wantMsg(t, e.a.DeleteBuilding(e.ctx, actor, pid, "barn", 1), gameerr.Validation, "請先刪除動物")
	if err := e.a.DeleteAnimalForPlayer(e.ctx, actor, pid, 4); err != nil {
		t.Fatal(err)
	}
	if n := len(must[[]AnimalView](t)(e.r.ListAnimals(e.ctx, pid))); n != 3 {
		t.Fatal(n)
	}
	e.adminErr(e.a.DeleteAnimalForPlayer(e.ctx, actor, pid, 4), gameerr.NotFound)
	// 加工中的設施不能刪除
	must[BuildingSlot](t)(e.a.AddBuilding(e.ctx, actor, pid, "mill"))
	_, err = e.a.AddBuilding(e.ctx, actor, pid, "mill")
	e.adminErr(err, gameerr.AlreadyExists)
	must[InventoryChange](t)(e.a.SetInventory(e.ctx, actor, pid, "wheat", "normal", "none", 3))
	e.ok(e.r.StartCraft(e.ctx, pid, "flour", 1))
	wantMsg(t, e.a.DeleteBuilding(e.ctx, actor, pid, "mill", 1), gameerr.Validation, "未領取")
	e.clock.Advance(1800)
	e.ok(e.r.CollectCraft(e.ctx, pid, int64(e.scalar("SELECT id FROM craft_orders"))))
	if err := e.a.DeleteBuilding(e.ctx, actor, pid, "mill", 1); err != nil {
		t.Fatal(err)
	}
	_, err = e.r.Sell(e.ctx, pid, "flour", 2, "normal", "none")
	e.adminErr(err, gameerr.InsufficientItems)
	got := e.auditActions()
	for _, want := range []string{"add_building", "add_animals", "delete_animal", "delete_building"} {
		if !slices.Contains(got, want) {
			t.Fatal(got)
		}
	}
}

func TestSetGoldAndLevel(t *testing.T) {
	e := setup(t)
	pid := e.player()
	gold, level := 12345, 5
	if r := must[FarmValues](t)(e.a.SetFarmValues(e.ctx, actor, pid, &gold, &level)); r != (FarmValues{12345, 5, true}) {
		t.Fatalf("%+v", r)
	}
	st := e.status(pid)
	if st.Gold != 12345 || st.Level != 5 || len(st.Plots) != balance.InitialPlots { // 土地不變
		t.Fatalf("%+v", st)
	}
	if qt := must[QualityTable](t)(e.s.QualityTable(e.ctx, &pid)); *qt.Level != 5 { // 品質加成依新等級計算
		t.Fatal(*qt.Level)
	}
	if must[FarmValues](t)(e.a.SetFarmValues(e.ctx, actor, pid, &gold, nil)).Changed {
		t.Fatal("沒有變更")
	}
	hundred := 100
	must[FarmValues](t)(e.a.SetFarmValues(e.ctx, actor, pid, &hundred, nil))
	// 金幣差額記入交易紀錄，所有 gold_delta 加總仍等於目前金幣
	if e.scalar("SELECT sum(gold_delta) FROM game_transactions") != 100 {
		t.Fatal("交易紀錄加總不對")
	}
	var deltas []string
	repotestQuery(t, e, "SELECT gold_delta::text FROM game_transactions WHERE event_type = 'admin_adjust' ORDER BY id", &deltas)
	if !slices.Equal(deltas, []string{"11845", "-12245"}) {
		t.Fatal(deltas)
	}
	neg, zero, over := -1, 0, balance.AdminMaxLevel+1
	for _, bad := range [][2]*int{{&neg, nil}, {nil, &zero}, {nil, &over}, {nil, nil}} {
		_, err := e.a.SetFarmValues(e.ctx, actor, pid, bad[0], bad[1])
		e.adminErr(err, gameerr.Validation)
	}
	one := 1
	_, err := e.a.SetFarmValues(e.ctx, actor, 999, &one, nil)
	e.adminErr(err, gameerr.NotFound)
	if !slices.Contains(e.auditActions(), "set_farm_values") {
		t.Fatal("沒有管理紀錄")
	}
}

func TestAdminAdjustDoesNotScoreQuests(t *testing.T) {
	e := setup(t)
	pid := e.player()
	g := e.home(pid).ID
	now := e.clock.Now()
	quest := must[QuestView](t)(e.a.CreateQuest(e.ctx, actor, NewQuest{GroupID: g, Name: "收成任務", QuestType: "harvest",
		Target: 5, RewardMode: "fixed", RewardType: "gold", RewardAmount: 10, StartsAt: now, EndsAt: now.Add(24 * time.Hour)}))
	must[InventoryChange](t)(e.a.InGroupContext(&g).SetInventory(e.ctx, actor, pid, "wheat", "normal", "none", 10))
	if qs := must[[]QuestView](t)(e.a.GroupQuests(e.ctx, g, pid)); qs[0].Progress != 0 || quest.Progress != 0 {
		t.Fatal(qs[0].Progress)
	}
}

// ---------- 管理員轉移所屬群組（test_group_transfer.py 與 test_admin.py 的 API 測試改寫為服務層） ----------

func TestAdminTransferRequiresReasonAndRecordsBypass(t *testing.T) {
	e := setup(t)
	a, b, c := e.groups3()
	alice := e.farmer("Alice", a)
	_, err := e.a.AdminTransferFarm(e.ctx, "root", alice, b, "  ", false)
	wantMsg(t, err, gameerr.Validation, "原因")
	// 管理員轉移會自動加入目標群組
	if r := must[AdminTransferResult](t)(e.a.AdminTransferFarm(e.ctx, "root", alice, b, "玩家申請", false)); !r.Changed || r.CooldownBypassed {
		t.Fatalf("%+v", r)
	}
	_, err = e.a.AdminTransferFarm(e.ctx, "root", alice, c, "再轉一次", false)
	wantMsg(t, err, gameerr.HomeGroupLocked, "冷卻中")
	if r := must[AdminTransferResult](t)(e.a.AdminTransferFarm(e.ctx, "root", alice, c, "客服處理", true)); !r.CooldownBypassed {
		t.Fatalf("%+v", r)
	}
	if h := e.home(alice); h.ID != c || h.Name != "C 群" {
		t.Fatalf("%+v", h)
	}
	// 略過冷卻只在該交易有效：之後玩家自己轉移仍受冷卻限制
	must[bool](t)(e.so.AddMember(e.ctx, a, alice))
	_, err = e.s.TransferHomeGroup(e.ctx, alice, a)
	wantKind(t, err, gameerr.HomeGroupLocked)
	var reasons, bypassed, before []string
	const q = "FROM admin_actions WHERE action = 'transfer_farm_group' ORDER BY id"
	repotestQuery(t, e, "SELECT details->>'reason' "+q, &reasons)
	repotestQuery(t, e, "SELECT details->>'cooldown_bypassed' "+q, &bypassed)
	repotestQuery(t, e, "SELECT details->'before'->>'home_group_id' "+q, &before)
	if !slices.Equal(reasons, []string{"玩家申請", "客服處理"}) || bypassed[1] != "true" || before[1] != strconvItoa64(b) {
		t.Fatal(reasons, bypassed, before)
	}
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		var v string
		if err := tx.QueryRow(ctx, "SELECT coalesce(current_setting('farmgm.admin_group_transfer', true), '')").Scan(&v); err != nil {
			return err
		}
		if v != "" {
			t.Fatalf("SET LOCAL 洩漏到其他交易：%q", v)
		}
		return nil
	})
	if _, err := e.a.AdminTransferFarm(e.ctx, "root", alice, c, "同群組", true); err == nil {
		t.Fatal("已經屬於這個群組")
	}
	e.invariant()
}

// ---------- 魚種、釣具、釣魚設定與季節限定（test_fishing_api.py、test_seasons.py、test_new_catalog.py 改寫為服務層） ----------

func (e *env) text(sql string, args ...any) string {
	e.t.Helper()
	var s string
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&s)
	})
	return s
}

func TestAdminFishTypeLifecycle(t *testing.T) {
	e := setup(t)
	body := map[string]any{"code": "test_fish", "name": "測試魚", "emoji": "🐟", "rarity": "rare", "min_price": 21,
		"max_price": 30, "min_weight_g": 100, "max_weight_g": 500, "difficulty": 3}
	if f := must[FishTypeView](t)(e.a.CreateFishType(e.ctx, actor, body)); f.RarityName != "稀有" {
		t.Fatalf("%+v", f)
	}
	_, err := e.a.CreateFishType(e.ctx, actor, with(body, "code", "bad_fish", "name", "壞魚", "min_price", 31)) // 最低高於最高
	e.adminErr(err, gameerr.Validation)
	// 售價不受稀有度限制
	must[FishTypeView](t)(e.a.CreateFishType(e.ctx, actor, with(body, "code", "rich_fish", "name", "富魚", "rarity", "common",
		"min_price", 5000, "max_price", 90000)))
	_, err = e.a.CreateFishType(e.ctx, actor, with(body, "name", "另一條"))
	e.adminErr(err, gameerr.AlreadyExists)
	upd := with(body, "name", "改名魚", "rarity", "epic", "min_price", 51, "max_price", 70)
	delete(upd, "code")
	if f := must[FishTypeView](t)(e.a.UpdateFishType(e.ctx, actor, "test_fish", upd)); f.Rarity != "epic" || f.Name != "改名魚" {
		t.Fatalf("%+v", f)
	}
	if f := must[FishTypeView](t)(e.a.SetFishTypeActive(e.ctx, actor, "test_fish", false)); f.Active {
		t.Fatal("沒有停用")
	}
	got := e.auditActions()
	for _, want := range []string{"create_fish_type", "update_fish_type", "disable_fish_type"} {
		if !slices.Contains(got, want) {
			t.Fatal(got)
		}
	}
	if e.text("SELECT details->'after'->>'name' FROM admin_actions WHERE action = 'update_fish_type'") != "改名魚" {
		t.Fatal("管理紀錄的 details 不對")
	}
}

func TestAdminGearAndSettings(t *testing.T) {
	e := setup(t)
	gear := map[string]any{"code": "gold_rod", "kind": "rod", "name": "黃金魚竿", "emoji": "🎣", "price": 9999,
		"required_level": 5, "stock": 3, "effects": map[string]any{"reel_power": 16, "control_pct": 20, "reaction_bonus_ms": 1000}}
	must[GearView](t)(e.a.CreateFishingGear(e.ctx, actor, gear))
	_, err := e.a.CreateFishingGear(e.ctx, actor, with(gear, "code", "bad_rod", "name", "壞魚竿",
		"effects", map[string]any{"max_tension": 120}))
	e.adminErr(err, gameerr.Validation)
	upd := with(gear, "price", 5000, "stock", nil)
	delete(upd, "code")
	delete(upd, "kind")
	if g := must[GearView](t)(e.a.UpdateFishingGear(e.ctx, actor, "gold_rod", upd)); g.Price != 5000 || g.Stock != nil {
		t.Fatalf("%+v", g)
	}
	if e.text("SELECT effects::text FROM fishing_gear WHERE code = 'gold_rod'") == "" ||
		e.scalar("SELECT count(*) FROM fishing_gear WHERE code = 'gold_rod' AND stock IS NULL AND (effects->>'reel_power')::int = 16") != 1 {
		t.Fatal("釣具沒有寫入")
	}
	if g := must[GearView](t)(e.a.SetFishingGearActive(e.ctx, actor, "gold_rod", false)); g.Active {
		t.Fatal("沒有停用")
	}
	settings := settingsWith(func(s *fishing.Settings) {
		s.RarityBP = map[string]int{"common": 7000, "rare": 2000, "epic": 800, "legendary": 200}
	})
	if v := must[int](t)(e.a.UpdateFishingSettings(e.ctx, actor, settings)); v != 1 {
		t.Fatal(v)
	}
	wrong := settingsWith(func(s *fishing.Settings) {
		s.RarityBP = map[string]int{"common": 7000, "rare": 2000, "epic": 800, "legendary": 100}
	})
	_, err = e.a.UpdateFishingSettings(e.ctx, actor, wrong)
	wantMsg(t, err, gameerr.Validation, "100%")
	if e.scalar("SELECT (details->>'from_version')::int FROM admin_actions WHERE action = 'update_fishing_settings'") != 0 ||
		e.scalar("SELECT (details->'after'->'rarity_bp'->>'legendary')::int FROM admin_actions WHERE action = 'update_fishing_settings'") != 200 {
		t.Fatal("管理紀錄的 details 不對")
	}
}

func TestAdminLimitsKeptWhenNotSent(t *testing.T) {
	e := setup(t)
	crop := map[string]any{"name": "茄子", "variant_name": "紫晶茄子", "seed_price": 10, "grow_seconds": 600,
		"harvest_quantity": 2, "sell_price": 5}
	must[CropView](t)(e.a.CreateCrop(e.ctx, "root", with(crop, "code", "eggplant", "variant_code", "violet_eggplant",
		"limits", map[string]any{"seasons": []any{"summer"}, "hour_start": 6, "hour_end": 18})))
	kept := must[CropView](t)(e.a.UpdateCrop(e.ctx, "root", "eggplant", with(crop, "sell_price", 7, "limits", nil)))
	if !slices.Equal(kept.Seasons, []string{"summer"}) || *kept.HourStart != 6 || *kept.HourEnd != 18 || kept.SellPrice != 7 {
		t.Fatalf("%+v", kept)
	}
	if e.text("SELECT seasons::text FROM crop_types WHERE code = 'eggplant'") != `["summer"]` {
		t.Fatal("季節沒有寫入")
	}
	cleared := must[CropView](t)(e.a.UpdateCrop(e.ctx, "root", "eggplant", with(crop, "limits", map[string]any{})))
	if len(cleared.Seasons) != 0 || cleared.Seasons == nil || cleared.HourStart != nil {
		t.Fatalf("%+v", cleared)
	}
	if e.scalar("SELECT count(*) FROM crop_types WHERE code = 'eggplant' AND seasons IS NULL AND hour_start IS NULL") != 1 {
		t.Fatal("取消限制要寫成 NULL")
	}
	_, err := e.a.UpdateCrop(e.ctx, "root", "eggplant", with(crop, "limits", map[string]any{"hour_start": 3}))
	e.adminErr(err, gameerr.Validation)
}

func TestAdminCanSetAndKeepRequiredBaits(t *testing.T) {
	e := setup(t)
	base := map[string]any{"name": "測試魚", "emoji": "🐟", "rarity": "epic", "min_price": 1, "max_price": 2,
		"min_weight_g": 10, "max_weight_g": 20, "difficulty": 3, "description": "", "sort_order": 0}
	_, err := e.a.CreateFishType(e.ctx, "root", with(base, "code", "test_fish", "required_baits", []any{"bamboo_rod"}))
	wantMsg(t, err, gameerr.Validation, "不是魚餌")
	made := must[FishTypeView](t)(e.a.CreateFishType(e.ctx, "root", with(base, "code", "test_fish",
		"required_baits", []any{"dragon_bait", "dragon_bait"})))
	if !slices.Equal(made.RequiredBaits, []string{"dragon_bait"}) { // 重複的只留一個
		t.Fatal(made.RequiredBaits)
	}
	// 網站表單沒送：不變更
	kept := must[FishTypeView](t)(e.a.UpdateFishType(e.ctx, "root", "test_fish", with(base, "max_price", 5, "required_baits", nil)))
	if !slices.Equal(kept.RequiredBaits, []string{"dragon_bait"}) || kept.MaxPrice != 5 {
		t.Fatalf("%+v", kept)
	}
	if e.text("SELECT required_baits::text FROM fish_types WHERE code = 'test_fish'") != `["dragon_bait"]` {
		t.Fatal("指定魚餌沒有寫入")
	}
	cleared := must[FishTypeView](t)(e.a.UpdateFishType(e.ctx, "root", "test_fish", with(base, "required_baits", []any{})))
	if len(cleared.RequiredBaits) != 0 || e.scalar("SELECT count(*) FROM fish_types WHERE code = 'test_fish' AND required_baits IS NULL") != 1 {
		t.Fatalf("%+v", cleared)
	}
}
