package services

import (
	"slices"
	"strings"
	"testing"

	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/weather"
)

// 每日天氣（Go 版新增，2026-11-17）：晴天加速、雨天種下自動澆水、暴風收成減損且防守設施能減輕。
func TestWeatherEffects(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	pid := e.player()
	e.setGoldRecorded(pid, 100_000)

	// 晴天：澆水後生長時間 -10%
	e.grow(pid, 1, "carrot") // 陰天
	weather.Fixed = weather.Sunny
	e.grow(pid, 2, "carrot")
	p1, p2 := e.plot(pid, 1), e.plot(pid, 2)
	cloudy, sunny := p1.ReadyAt.Sub(*p1.WateredAt), p2.ReadyAt.Sub(*p2.WateredAt)
	if sunny*10 != cloudy*9 {
		t.Fatalf("陰天 %v、晴天 %v", cloudy, sunny)
	}

	// 雨天：種下自動澆水
	weather.Fixed = weather.Rainy
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	if r := e.ok(e.s.PlantCrop(e.ctx, pid, 3, "wheat")); !strings.Contains(r.Message, "已自動澆水") {
		t.Fatal(r.Message)
	}
	if p := e.plot(pid, 3); p.State != rules.PlotGrowing {
		t.Fatalf("%+v", p)
	}

	// 暴風：每格少 1 個（紅蘿蔔一次 3 個 → 剩 2 個）
	e.clock.Advance(3 * 3600)
	weather.Fixed = weather.Storm
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); !strings.Contains(r.Message, "大風颳走 1 個") || !strings.Contains(r.Message, "獲得 2 個") {
		t.Fatal(r.Message)
	}
	// 有警戒犬（守護全部土地）就整個擋下
	e.ok(e.so.BuyDefense(e.ctx, pid, "警戒犬"))
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 2)); strings.Contains(r.Message, "大風颳走") ||
		!strings.Contains(r.Message, "防守設施擋下暴風 1 個") || !strings.Contains(r.Message, "獲得 3 個") {
		t.Fatal(r.Message)
	}
	e.invariant()
}

// 雨天釣魚：釣魚中心顯示的稀有度機率包含天氣加成。
func TestWeatherRainFishingChances(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	pid := e.player()
	before := e.ok2fish(pid)
	weather.Fixed = weather.Rainy
	after := e.ok2fish(pid)
	if after["common"] >= before["common"] || after["rare"] <= before["rare"] {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func (e *env) ok2fish(pid int64) map[string]float64 {
	e.t.Helper()
	ov, err := e.f.FishingOverview(e.ctx, pid)
	if err != nil {
		e.t.Fatal(err)
	}
	return ov.Chances
}

// 天氣奇遇・雷擊種子（2026-11-20）：暴風隔天每天撿 1 次，種在第一格空地，突變固定。
func TestLightningSeed(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	pid := e.player()
	_, err := e.s.PickLightningSeed(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "今天沒有雷擊種子")
	if e.status(pid).LightningReady {
		t.Fatal("陰天不能撿")
	}

	weather.Fixed = weather.Storm // 每天都暴風：昨天也是暴風
	if !e.status(pid).LightningReady {
		t.Fatal("暴風隔天應該可以撿")
	}
	r := e.ok(e.s.PickLightningSeed(e.ctx, pid))
	if !strings.Contains(r.Message, "撿起雷擊種子") || !strings.Contains(r.Message, "第 1 格") {
		t.Fatal(r.Message)
	}
	p := e.plot(pid, 1)
	if p.CropCode == nil || p.Mutation == nil || !slices.Contains(weather.LightningMutations, *p.Mutation) {
		t.Fatalf("%+v", p)
	}
	_, err = e.s.PickLightningSeed(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "已經撿過")
	if e.status(pid).LightningReady {
		t.Fatal("撿過之後不再顯示")
	}
	if n := e.scalar("SELECT count(*)::int FROM farm_tools WHERE tool_code = $1", weather.LuckToolCode); n != 1 {
		t.Fatal(n)
	}

	// 隔天又可以撿；沒有空地時撿不起來，也不算撿過
	e.clock.Advance(24 * 3600)
	if !e.status(pid).LightningReady {
		t.Fatal("隔天應該可以再撿")
	}
	for n := 2; n <= len(e.status(pid).Plots); n++ {
		e.grow(pid, n, "wheat")
	}
	if e.status(pid).EmptyPlots() == 0 {
		_, err = e.s.PickLightningSeed(e.ctx, pid)
		wantKind(t, err, gameerr.PlotOccupied)
		if !e.status(pid).LightningReady {
			t.Fatal("沒撿成功不能算撿過")
		}
	}
	e.invariant()
}

// 天氣奇遇・連綿細雨（2026-11-20）：連續雨天時菇房每床多收、稀有機率提高。
func TestDrizzleMushrooms(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	pid := e.rancher()
	e.exec("UPDATE farms SET level = 4 WHERE player_id = $1", pid)
	e.cellar(pid)
	e.grow(pid, 1, "strawberry")
	e.s.MushroomRng = func() float64 { return 0.99 } // 沒有木屑：5% + 10% 仍抽不到稀有
	e.clock.Advance(mushroom.GrowSeconds)
	weather.Fixed = weather.Rainy
	r := e.ok(e.r.HarvestMushrooms(e.ctx, pid))
	if !strings.Contains(r.Message, "連綿細雨") {
		t.Fatal(r.Message)
	}
	want := mushroom.BedCount * (mushroom.Yield() + weather.DrizzleExtraYield)
	if got := e.qty(pid, "enoki") + e.qty(pid, "glowcap"); got != want {
		t.Fatalf("收成 %d 朵，應為 %d", got, want)
	}
}
