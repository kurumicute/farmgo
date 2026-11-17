package weather

import (
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/events"
	"farmgm/internal/game/fishing"
)

// 同一個台灣日期一定是同一種天氣；跨過台灣時間 0 點才換。一年下來各種天氣都會出現，比例接近權重。
func TestForDateDeterministicAndWeighted(t *testing.T) {
	day := time.Date(2026, 11, 17, 0, 0, 0, 0, clock.TaipeiTZ)
	if ForDate(day).Code != ForDate(day.Add(23*time.Hour+59*time.Minute)).Code {
		t.Fatal("同一天的天氣不一樣")
	}
	// UTC 16:00 = 台灣隔天 0 點
	if ForDate(time.Date(2026, 11, 17, 15, 59, 0, 0, time.UTC)).Code != ForDate(day.Add(23*time.Hour)).Code {
		t.Fatal("應該以台灣日期決定")
	}
	counts := map[string]int{}
	const n = 4000
	for i := range n {
		counts[ForDate(day.AddDate(0, 0, i)).Code]++
	}
	for _, c := range Order {
		got, want := float64(counts[c])/n*100, float64(Kinds[c].Weight)
		if got < want-4 || got > want+4 {
			t.Errorf("%s 出現 %.1f%%，權重 %v%%", c, got, want)
		}
	}
}

func TestFixedAndEvents(t *testing.T) {
	defer func() { Fixed = "" }()
	Fixed = Cloudy
	if Today(time.Now()).Event() != nil {
		t.Fatal("陰天不應該有事件")
	}
	Fixed = Sunny
	ev := Today(time.Now()).Event()
	if ev == nil || ev.ID != EventID || events.BonusOf([]*events.Event{ev}, "grow_reduction_pct", nil).Value != 10 ||
		events.BonusOf([]*events.Event{ev}, "sell_price_pct", nil).Value != 0 {
		t.Fatalf("晴天事件不正確：%+v", ev)
	}
	Fixed = Rainy
	if events.BonusOf([]*events.Event{Today(time.Now()).Event()}, "free_water", nil).Value != 1 {
		t.Fatal("雨天應該免澆水")
	}
	Fixed = Storm
	if events.BonusOf([]*events.Event{Today(time.Now()).Event()}, "harvest_loss", nil).Value != 1 {
		t.Fatal("暴風應該收成減損 1")
	}
}

// 雨天的釣魚加成加在魚餌效果上，不改動原本的 map。
func TestRainFishEffects(t *testing.T) {
	bait := fishing.Effects{"rare_bp": 100}
	out := Kinds[Rainy].FishEffects(bait)
	if bait["rare_bp"] != 100 || out["rare_bp"] != 400 || out["epic_bp"] != 100 || out["legendary_bp"] != 30 {
		t.Fatalf("bait=%v out=%v", bait, out)
	}
	base := map[string]int{"common": 8000, "rare": 1500, "epic": 400, "legendary": 100}
	if fishing.RarityChances(base, out)["common"] >= fishing.RarityChances(base, bait)["common"] {
		t.Fatal("雨天普通魚機率應該下降")
	}
	if got := Kinds[Sunny].FishEffects(bait); got["rare_bp"] != 100 {
		t.Fatal("晴天不影響釣魚")
	}
}
