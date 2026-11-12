package services

// 地下菇房（Go 版新增，2026-11-12，Python 沒有）：Lv.4 開放、投入木屑、依上方作物擲骰收成、倉庫不足時部分收成、
// 菇類販售與烘焙坊料理，以及探險帶回木屑。

import (
	"strings"
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
)

func (e *env) sawdust(pid int64) int {
	e.t.Helper()
	return e.scalar(`SELECT COALESCE(sum(i.quantity), 0)::int FROM inventory_items i JOIN farms f ON f.id = i.farm_id
		WHERE f.player_id = $1 AND i.item_code = $2`, pid, exploration.Sawdust)
}

func (e *env) cellar(pid int64) MushroomCellarView {
	e.t.Helper()
	return must[MushroomCellarView](e.t)(e.r.MushroomCellar(e.ctx, pid))
}

func TestMushroomCellarUnlockAndHarvest(t *testing.T) {
	e := setup(t)
	pid := e.rancher()

	// Lv.3：還沒開放，也不會建立菇房
	e.exec("UPDATE farms SET level = 3 WHERE player_id = $1", pid)
	if v := e.cellar(pid); v.Unlocked || v.UnlockLevel != 4 {
		t.Fatalf("%+v", v)
	}
	_, err := e.r.HarvestMushrooms(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "Lv.4")
	if n := e.scalar("SELECT count(*)::int FROM farm_tools WHERE tool_code = $1", mushroom.ToolCode); n != 0 {
		t.Fatal(n)
	}

	// Lv.4：打開就能用，4 個菇床同時開始生長
	e.exec("UPDATE farms SET level = 4 WHERE player_id = $1", pid)
	v := e.cellar(pid)
	if !v.Unlocked || len(v.Beds) != mushroom.BedCount || v.ReadyCount != 0 || v.NextReadyAt == nil {
		t.Fatalf("%+v", v)
	}
	e.cellar(pid) // 再打開一次不會重複建立
	if n := e.scalar("SELECT count(*)::int FROM farm_tools WHERE tool_code = $1", mushroom.ToolCode); n != 1 {
		t.Fatal(n)
	}
	_, err = e.r.HarvestMushrooms(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒長好")

	// 投入木屑：單一菇床上限 4 個；「全部補滿」依序補到木屑用完
	_, err = e.r.AddSawdust(e.ctx, pid, 1, 4)
	wantKind(t, err, gameerr.InsufficientItems)
	e.give(pid, exploration.Sawdust, 10)
	e.ok(e.r.AddSawdust(e.ctx, pid, 1, 4))
	_, err = e.r.AddSawdust(e.ctx, pid, 1, 1)
	wantMsg(t, err, gameerr.Validation, "最多 4 個")
	_, err = e.r.AddSawdust(e.ctx, pid, 5, 1)
	wantKind(t, err, gameerr.Validation)
	e.ok(e.r.AddSawdust(e.ctx, pid, 0, 0))
	v = e.cellar(pid)
	got := []int{v.Beds[0].Sawdust, v.Beds[1].Sawdust, v.Beds[2].Sawdust, v.Beds[3].Sawdust}
	if got[0] != 4 || got[1] != 4 || got[2] != 2 || got[3] != 0 || e.sawdust(pid) != 0 || v.Sawdust != 0 {
		t.Fatal(got, e.sawdust(pid))
	}

	// 上方只種草莓（果香養分）：只會長金針菇或發光菇；亂數 0.99 → 只有保證的稀有菇
	e.grow(pid, 1, "strawberry")
	v = e.cellar(pid)
	if v.Planted != 1 || len(v.Nutrients) != 1 || v.Nutrients[0].Code != "fruit" || v.Nutrients[0].Pct != 100 {
		t.Fatalf("%+v", v.Nutrients)
	}
	e.s.MushroomRng = func() float64 { return 0.99 }
	e.clock.Advance(mushroom.GrowSeconds)
	r := e.ok(e.r.HarvestMushrooms(e.ctx, pid))
	if !strings.Contains(r.Message, "收成 4 個菇床") || !strings.Contains(r.Message, "稀有菇 2 朵") {
		t.Fatal(r.Message)
	}
	if e.qty(pid, "glowcap") != 2 || e.qty(pid, "enoki") != 6 || e.qty(pid, "shiitake") != 0 {
		t.Fatal(e.qty(pid, "glowcap"), e.qty(pid, "enoki"))
	}
	v = e.cellar(pid)
	for _, b := range v.Beds { // 立即開始下一批，木屑歸零
		if b.Ready || b.Sawdust != 0 {
			t.Fatalf("%+v", b)
		}
	}
	_, err = e.r.HarvestMushrooms(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒長好")
	if n := e.scalar(`SELECT count(*)::int FROM game_transactions WHERE event_type = 'craft_collect' AND description LIKE '地下菇房%'`); n != 2 {
		t.Fatal(n)
	}

	// 菇類可以直接賣
	if r := e.ok(e.r.SellGoods(e.ctx, pid, "發光菇", 1)); r.GoldDelta != balance.Goods["glowcap"].SellPrice || r.GoldDelta != 900 {
		t.Fatal(r.GoldDelta)
	}
	if balance.Goods["shiitake"].SellPrice != 200 {
		t.Fatal(balance.Goods["shiitake"])
	}
}

func TestMushroomHarvestPartialWhenWarehouseFull(t *testing.T) {
	e := setup(t)
	pid := e.rancher()
	e.exec("UPDATE farms SET level = 4 WHERE player_id = $1", pid)
	e.cellar(pid)
	e.clock.Advance(mushroom.GrowSeconds)
	e.s.MushroomRng = func() float64 { return 0.5 }
	// 倉庫只剩 3 格：只能收 1 個菇床（2 朵）
	e.exec("UPDATE farms SET warehouse_capacity = 3 WHERE player_id = $1", pid)
	r := e.ok(e.r.HarvestMushrooms(e.ctx, pid))
	if !strings.Contains(r.Message, "收成 1 個菇床") || !strings.Contains(r.Message, "還有 3 個菇床沒收成") {
		t.Fatal(r.Message)
	}
	if e.qty(pid, "shiitake") != 2 { // 沒有作物 → 穀物養分 → 香菇
		t.Fatal(e.qty(pid, "shiitake"))
	}
	_, err := e.r.HarvestMushrooms(e.ctx, pid)
	wantMsg(t, err, gameerr.WarehouseFull, "菇會留在菇床上")
	if v := e.cellar(pid); v.ReadyCount != 3 {
		t.Fatal(v.ReadyCount)
	}
}

func TestMushroomBakeryRecipes(t *testing.T) {
	e := setup(t)
	pid := e.rancher()
	e.ok(e.r.Build(e.ctx, pid, "烘焙坊"))
	e.give(pid, "glowcap", 2)
	e.give(pid, "flour", 2)
	r := e.ok(e.r.StartCraft(e.ctx, pid, "發光菇派", 1))
	if !strings.Contains(r.Message, "1 份發光菇派") {
		t.Fatal(r.Message)
	}
	e.clock.Advance(float64(balance.Recipes["glowcap_pie"].Seconds))
	for _, o := range e.orders(pid, false) {
		e.ok(e.r.CollectCraft(e.ctx, pid, o.OrderID))
	}
	if e.qty(pid, "glowcap_pie") != 1 || e.qty(pid, "glowcap") != 0 {
		t.Fatal(e.qty(pid, "glowcap_pie"))
	}
	if r := e.ok(e.r.SellGoods(e.ctx, pid, "發光菇派", 1)); r.GoldDelta != balance.Goods["glowcap_pie"].SellPrice {
		t.Fatal(r.GoldDelta)
	}
	for _, code := range []string{"shiitake_bread", "mushroom_soup", "enoki_pancake", "lingzhi_cake", "truffle_bread", "glowcap_pie"} {
		rc := balance.Recipes[code]
		if rc.Building != "bakery" || balance.Goods[rc.Output].SellPrice == 0 {
			t.Fatal(code, rc)
		}
		for _, in := range rc.InputOrder {
			if _, ok := balance.Goods[in]; !ok {
				t.Fatal(code, in)
			}
		}
	}
}

func TestExplorationBringsSawdust(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.s.exploreRng = func() float64 { return 0.999 } // 木屑取上限
	out := must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "青青草原"))
	if out.Trip == nil {
		t.Fatalf("%+v", out)
	}
	e.clock.Advance(float64(exploration.Areas["grassland"].DurationSeconds))
	rw := must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid))
	found := false
	for _, it := range rw.Items {
		if it.Code == exploration.Sawdust {
			found = it.Quantity == exploration.SawdustDrop["grassland"][1]
		}
	}
	if !found || e.sawdust(pid) != exploration.SawdustDrop["grassland"][1] {
		t.Fatalf("%+v %d", rw.Items, e.sawdust(pid))
	}
	// 森林最多
	if exploration.SawdustDrop["forest"][1] <= exploration.SawdustDrop["grassland"][1] {
		t.Fatal(exploration.SawdustDrop)
	}
}
