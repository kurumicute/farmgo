package services

// Go 版新增的果醬（2026-11-11，Python 沒有）：果醬工坊可以把番茄、葡萄、南瓜、西瓜加工成果醬，
// 可以販售，世界 Boss 預設設定也收（醫療補給）。

import (
	"strings"
	"testing"

	"farmgm/internal/game/balance"
	wb "farmgm/internal/game/worldboss"
)

var newJams = []struct {
	recipe, alias, crop, name string
	price, seconds, points    int
}{
	{"tomato_jam", "番茄醬", "tomato", "番茄醬", 195, 45 * 60, 20},
	{"grape_jam", "葡萄果醬", "grape", "葡萄果醬", 395, 75 * 60, 40},
	{"pumpkin_jam", "南瓜醬", "pumpkin", "南瓜果醬", 485, 90 * 60, 49},
	{"watermelon_jam", "西瓜果醬", "watermelon", "西瓜果醬", 1145, 120 * 60, 115},
}

func TestNewJamRecipesCraftAndSell(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "果醬工坊"))
	pid := f
	recipes := must[[]RecipeView](t)(e.r.Recipes(e.ctx, &pid))
	jamWorkshop := 0
	for _, r := range recipes {
		if r.BuildingCode == "jam_workshop" {
			jamWorkshop++
		}
	}
	if jamWorkshop != 5 { // 草莓＋新增的 4 種
		t.Fatal(jamWorkshop)
	}
	total := 0
	for _, j := range newJams {
		rc, g := balance.Recipes[j.recipe], balance.Goods[j.recipe]
		if rc.Building != "jam_workshop" || rc.Inputs[j.crop] != 3 || rc.Seconds != j.seconds || g.SellPrice != j.price || g.Name != j.name {
			t.Fatalf("%s：%+v %+v", j.recipe, rc, g)
		}
		e.give(f, j.crop, 3)
		r := e.ok(e.r.StartCraft(e.ctx, f, j.alias, 1)) // 以名稱或別名開始加工；同一座工坊依序排隊
		if !strings.Contains(r.Message, "1 份"+j.name) {
			t.Fatal(r.Message)
		}
		total += j.seconds
	}
	e.clock.Advance(float64(total))
	for _, o := range e.orders(f, false) {
		e.ok(e.r.CollectCraft(e.ctx, f, o.OrderID))
	}
	for _, j := range newJams {
		if e.qty(f, j.recipe) != 1 || e.qty(f, j.crop) != 0 {
			t.Fatal(j.recipe, e.qty(f, j.recipe), e.qty(f, j.crop))
		}
		r := e.ok(e.r.SellGoods(e.ctx, f, j.name, 1))
		if r.GoldDelta != j.price {
			t.Fatal(j.recipe, r.GoldDelta)
		}
	}
}

func TestNewJamsAreMedicalSuppliesForWorldBoss(t *testing.T) {
	s := wb.DefaultSettings()
	for _, j := range newJams {
		if it, ok := s.Items[j.recipe]; !ok || it.Supply != "medical" || it.Points != j.points {
			t.Fatal(j.recipe, it)
		}
	}
	if len(wb.DefaultItemOrder) != len(s.Items) {
		t.Fatal(len(wb.DefaultItemOrder), len(s.Items))
	}
}
