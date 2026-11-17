package services

import (
	"strings"
	"testing"

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
