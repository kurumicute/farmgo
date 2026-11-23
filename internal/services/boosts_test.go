package services

// 限時增益道具與新寵物（Go 版新增，2026-11-18）。

import (
	"context"
	"strings"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/weather"

	petrules "farmgm/internal/game/pets"
)

func TestSprinklerAndStormGuard(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	weather.Fixed = weather.Cloudy
	pid := e.player()
	e.setGoldRecorded(pid, 100_000)
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ItemSprinkler, 1)) // 道具商店買得到
	e.give(pid, balance.ItemStormGuard, 1)

	// 灑水器：陰天也有晴天的 -10%
	e.grow(pid, 1, "carrot")
	r := e.ok(e.s.UseBoost(e.ctx, pid, balance.ItemSprinkler))
	if !strings.Contains(r.Message, "已啟用灑水器") || !strings.Contains(r.Message, "剩餘 1 天") {
		t.Fatal(r.Message)
	}
	e.grow(pid, 2, "carrot")
	p1, p2 := e.plot(pid, 1), e.plot(pid, 2)
	if plain, sprinkled := p1.ReadyAt.Sub(*p1.WateredAt), p2.ReadyAt.Sub(*p2.WateredAt); sprinkled*10 != plain*9 {
		t.Fatalf("沒有灑水器 %v、有灑水器 %v", plain, sprinkled)
	}

	// 雨衣稻草人：暴風吹不壞（紅蘿蔔一次 3 個）
	e.clock.Advance(3 * 3600)
	weather.Fixed = weather.Storm
	e.ok(e.s.UseBoost(e.ctx, pid, balance.ItemStormGuard))
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); strings.Contains(r.Message, "大風颳走") || !strings.Contains(r.Message, "獲得 3 個") {
		t.Fatal(r.Message)
	}
	_, err := e.s.UseBoost(e.ctx, pid, balance.ItemStormGuard) // 用完了
	wantKind(t, err, gameerr.InsufficientItems)
	_, err = e.s.UseBoost(e.ctx, pid, "fertilizer") // 不是增益道具
	wantKind(t, err, gameerr.NotFound)

	list := must[[]BoostView](t)(e.s.Boosts(e.ctx, pid))
	if len(list) != len(balance.BoostOrder) || list[0].Code != balance.ItemStormGuard || list[0].ActiveUntil == nil ||
		list[0].Price != 300 || list[2].Recipe == "" || list[2].Price != 0 {
		t.Fatalf("%+v", list)
	}
	e.invariant()
}

func TestBoostExtendsWhileActive(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.give(pid, balance.ItemLuckySachet, 2)
	e.ok(e.s.UseBoost(e.ctx, pid, balance.ItemLuckySachet))
	r := e.ok(e.s.UseBoost(e.ctx, pid, balance.ItemLuckySachet))
	if !strings.Contains(r.Message, "已延長幸運香包") || !strings.Contains(r.Message, "剩餘 4 小時") {
		t.Fatal(r.Message)
	}
	if e.qty(pid, balance.ItemLuckySachet) != 0 {
		t.Fatal("兩個都用掉")
	}
}

func TestNewPetsAndTreat(t *testing.T) {
	e := setup(t)
	pid := e.petOwner(e.group("主群組"))
	for _, code := range []string{petrules.Rabbit, petrules.Bird, petrules.Hedgehog} { // Migration 0045 放寬的 CHECK
		e.ok(e.r.BuyPet(e.ctx, pid, code))
	}
	pct := func(code string) int {
		return must[int](t)(inTx(e.ctx, e.s, func(t *db.Tx) (int, error) {
			farm, err := e.s.farmOf(context.Background(), t, pid)
			if err != nil {
				return 0, err
			}
			return e.s.petPct(e.ctx, t, farm.ID, code)
		}))
	}
	if pct(petrules.Bird) != 5 || pct(petrules.Cat) != 0 {
		t.Fatal(pct(petrules.Bird), pct(petrules.Cat))
	}
	e.give(pid, balance.ItemPetTreat, 1)
	e.ok(e.s.UseBoost(e.ctx, pid, balance.ItemPetTreat))
	if pct(petrules.Bird) != 7 { // 5% ×1.5 = 7.5，無條件捨去
		t.Fatal(pct(petrules.Bird))
	}

	// 刺蝟（2026-11-22 起）：收成時品質提升一級（機率 0 → 一定中）；清除乾枯作物不再救回
	e.s.PetRng = func() float64 { return 0 }
	e.grow(pid, 1, "strawberry")
	e.clock.Advance(20 * 3600)
	if r := e.ok(e.s.ClearWithered(e.ctx, pid, 1)); strings.Contains(r.Message, "刺蝟") {
		t.Fatal(r.Message)
	}
	e.grow(pid, 1, "strawberry")
	e.clock.Advance(28800 + 60)
	e.exec("UPDATE farm_plots SET quality = 'normal' WHERE plot_number = 1")
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); !strings.Contains(r.Message, "刺蝟吃掉害蟲，品質提升為優良") {
		t.Fatal(r.Message)
	}
	if got := e.scalar("SELECT coalesce(sum(quantity), 0) FROM inventory_items WHERE item_code = 'strawberry' AND quality = 'good'"); got == 0 {
		t.Fatal("沒有優良品質的草莓")
	}
	e.s.PetRng = func() float64 { return 0.99 } // 沒中：品質不變
	e.grow(pid, 1, "strawberry")
	e.clock.Advance(28800 + 60)
	e.exec("UPDATE farm_plots SET quality = 'normal' WHERE plot_number = 1")
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); strings.Contains(r.Message, "刺蝟") {
		t.Fatal(r.Message)
	}
	// 一鍵收成的摘要也要顯示刺蝟的效果（2026-11-22）
	e.s.PetRng = func() float64 { return 0 }
	e.grow(pid, 1, "strawberry")
	e.clock.Advance(28800 + 60)
	e.exec("UPDATE farm_plots SET quality = 'normal' WHERE plot_number = 1")
	if r := e.ok(e.s.HarvestAll(e.ctx, pid)); !strings.Contains(r.Message, "🦔 刺蝟吃掉害蟲，1 格品質提升一級") {
		t.Fatal(r.Message)
	}
	e.invariant()
}

func TestBirdRaisesRareFishChance(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	weather.Fixed = weather.Cloudy
	pid := e.petOwner(e.group("主群組"))
	before := e.ok2fish(pid)
	e.ok(e.r.BuyPet(e.ctx, pid, petrules.Bird))
	after := e.ok2fish(pid)
	if after["common"] >= before["common"] || after["rare"] <= before["rare"] || after["legendary"] <= before["legendary"] {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestMushroomRollWithExtraRare(t *testing.T) {
	mix := map[string]int{"grain": 1}
	all := mushroom.RollWith(mix, 0, 6, 100, func() float64 { return 0.5 })
	for _, code := range all {
		if !mushroom.SpeciesMap[code].Rare {
			t.Fatal("稀有機率 +100% 時全部都是稀有菇", all)
		}
	}
	if got := mushroom.Roll(mix, 0, 6, func() float64 { return 0.99 }); mushroom.SpeciesMap[got[0]].Rare {
		t.Fatal("沒有加成時照木屑的機率", got)
	}
}
