package services

// Go 版新增（2026-11-18）：乳品工坊（起司、奶油、美乃滋）、烘焙坊的蛋糕與草莓蛋糕、果醬工坊的綜合果醬。

import (
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
)

func TestDairyAndCakeChainCraftAndSell(t *testing.T) {
	e := setup(t)
	f := e.rancher() // 100,000 金
	_, err := e.r.StartCraft(e.ctx, f, "起司", 1)
	wantKind(t, err, gameerr.Validation) // 還沒建造乳品工坊
	e.ok(e.r.Build(e.ctx, f, "乳品工坊"))
	e.ok(e.r.Build(e.ctx, f, "烘焙坊"))
	e.ok(e.r.Build(e.ctx, f, "果醬工坊"))
	if st := must[WorkshopStatus](t)(e.r.WorkshopStatus(e.ctx, f)); st.Gold != 100_000-3_000-3_500-2_500 {
		t.Fatal(st.Gold)
	}

	e.give(f, "milk", 6)
	e.give(f, "egg", 5)
	e.give(f, "flour", 2)
	e.give(f, "strawberry_jam", 1)
	e.give(f, "strawberry", 2)
	e.give(f, "grape", 2)
	e.ok(e.r.StartCraft(e.ctx, f, "起司", 1))  // 牛奶 -3
	e.ok(e.r.StartCraft(e.ctx, f, "奶油", 1))  // 牛奶 -2
	e.ok(e.r.StartCraft(e.ctx, f, "美乃滋", 1)) // 雞蛋 -3
	_, err = e.r.StartCraft(e.ctx, f, "美乃滋", 1)
	wantKind(t, err, gameerr.InsufficientItems) // 雞蛋只剩 2
	e.ok(e.r.StartCraft(e.ctx, f, "蛋糕", 1))     // 麵粉 -2、雞蛋 -2、牛奶 -1
	e.ok(e.r.StartCraft(e.ctx, f, "綜合果醬", 1))   // 草莓 -2、葡萄 -2
	for _, code := range []string{"milk", "egg", "flour", "strawberry", "grape"} {
		if n := e.qty(f, code); n != 0 {
			t.Fatal(code, n)
		}
	}
	_, err = e.r.StartCraft(e.ctx, f, "草莓蛋糕", 1)
	wantKind(t, err, gameerr.InsufficientItems) // 蛋糕還沒做好

	e.clock.Advance(float64((90 + 60 + 45) * 60)) // 乳品工坊 3 張依序排隊，最晚的一張最後完成
	for _, o := range e.orders(f, false) {
		e.ok(e.r.CollectCraft(e.ctx, f, o.OrderID))
	}
	e.ok(e.r.StartCraft(e.ctx, f, "草莓蛋糕", 1)) // 蛋糕 -1、草莓果醬 -1
	e.clock.Advance(120 * 60)
	for _, o := range e.orders(f, false) {
		e.ok(e.r.CollectCraft(e.ctx, f, o.OrderID))
	}
	for _, code := range []string{"cake", "strawberry_jam"} {
		if n := e.qty(f, code); n != 0 {
			t.Fatal(code, n)
		}
	}
	for _, c := range []struct {
		code, name string
		price      int
	}{{"cheese", "起司", 290}, {"butter", "奶油", 190}, {"mayonnaise", "美乃滋", 120}, {"mixed_jam", "綜合果醬", 520},
		{"strawberry_cake", "草莓蛋糕", 900}} {
		if n := e.qty(f, c.code); n != 1 {
			t.Fatal(c.code, n)
		}
		// 2026-11-19 起加工品售價浮動（總成本 -50%～+100%），c.price 是內建的參考售價，實際以這一期的價格計算
		if r := e.ok(e.r.SellGoods(e.ctx, f, c.name, 1)); r.GoldDelta != e.goodPrice(c.code) {
			t.Fatal(c.code, r.GoldDelta)
		}
	}
}

func TestDairyIsUpgradableAndListsRecipes(t *testing.T) {
	if !balance.Upgradable("dairy") {
		t.Fatal("乳品工坊應該可以升級")
	}
	for _, code := range balance.RecipeOrder {
		rc, ok := balance.Recipes[code]
		if !ok {
			t.Fatal("RecipeOrder 有不存在的配方", code)
		}
		if _, ok := balance.Buildings[rc.Building]; !ok {
			t.Fatal(code, "的設施不存在", rc.Building)
		}
		if _, ok := balance.Goods[rc.Output]; !ok {
			t.Fatal(code, "的成品沒有售價", rc.Output)
		}
		if len(rc.InputOrder) != len(rc.Inputs) {
			t.Fatal(code, "InputOrder 與 Inputs 不一致")
		}
	}
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "乳品工坊"))
	e.ok(e.r.UpgradeBuilding(e.ctx, f, "dairy"))
	pid := f
	n := 0
	for _, r := range must[[]RecipeView](t)(e.r.Recipes(e.ctx, &pid)) {
		if r.BuildingCode != "dairy" {
			continue
		}
		n++
		if !r.Built || r.Seconds != balance.CraftSeconds(balance.Recipes[r.Code].Seconds, 1) {
			t.Fatalf("%+v", r)
		}
	}
	if n != 4 { // 起司、奶油、美乃滋＋蜂蜜牛奶（2026-11-20）
		t.Fatal(n)
	}
}
