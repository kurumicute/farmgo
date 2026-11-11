package events

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/gameerr"
)

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func mustFail(t *testing.T, raw map[string]any, contains string) {
	t.Helper()
	_, err := Validate(raw)
	if !gameerr.Is(err, gameerr.Validation) || !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected %q, got %v", contains, err)
	}
}

// 移植自 tests/test_admin.py::test_event_validation
func TestEventValidation(t *testing.T) {
	day := now.Add(24 * time.Hour)
	mustFail(t, map[string]any{"name": "空", "schedule_type": "once", "starts_at": now, "ends_at": day}, "至少要設定一項")
	mustFail(t, map[string]any{"name": "x", "schedule_type": "daily", "start_minute": 600, "end_minute": 600,
		"sell_price_pct": 150}, "結束時刻")
	mustFail(t, map[string]any{"name": "x", "schedule_type": "weekly", "start_minute": 0, "end_minute": 60,
		"harvest_bonus_pct": 10}, "至少要選擇一天")
	mustFail(t, map[string]any{"name": "x", "schedule_type": "once", "starts_at": now, "ends_at": day,
		"sell_price_pct": 500}, "售價倍率")
	mustFail(t, map[string]any{"name": "x", "schedule_type": "once", "sell_price_pct": 150}, "單次事件必須設定")

	s, err := Validate(map[string]any{"name": " 週末 ", "schedule_type": "weekly", "start_minute": 0,
		"end_minute": 1440, "weekdays": 96, "sell_price_pct": 150})
	if err != nil || s.Name != "週末" || *s.Weekdays != 96 || !s.Enabled || s.Effects["sell_price_pct"] != 150 {
		t.Fatal(s, err)
	}
}

func ip(v int) *int { return &v }

func TestIsActiveAndBonus(t *testing.T) {
	// 2026-01-01 00:00 UTC = 台灣週四 08:00
	weekly := &Event{ID: 1, Name: "週四", Enabled: true, ScheduleType: "weekly", StartMinute: ip(8 * 60),
		EndMinute: ip(9 * 60), Weekdays: ip(1 << 3), SellPricePct: 150, QualityBonusBP: 2000}
	if !IsActive(weekly, now) || IsActive(weekly, now.Add(time.Hour)) {
		t.Fatal("weekly")
	}
	weekly.Weekdays = ip(1)
	if IsActive(weekly, now) {
		t.Fatal("monday only")
	}
	crop := int64(5)
	other := &Event{ID: 2, Name: "番茄", Enabled: true, ScheduleType: "once", CropTypeID: &crop,
		SellPricePct: 300, QualityBonusBP: 2000}
	b := BonusOf([]*Event{weekly, other}, "sell_price_pct", &crop)
	if b.Value != 200 || len(b.Applied) != 2 { // 50 + 200 → 上限 200
		t.Fatal(b)
	}
	b2 := BonusOf([]*Event{weekly, other}, "quality_bonus_bp", nil)
	if b2.Value != 2000 || len(b2.Applied) != 1 {
		t.Fatal(b2)
	}
	if got := MergeApplied(b, b2); len(got) != 2 || got[0].ID != 1 {
		t.Fatal(got)
	}
	if MergeApplied(Bonus{}) != nil {
		t.Fatal("empty")
	}
	if ApplySellBonus(15, 50) != 22 {
		t.Fatal("sell")
	}
}

func TestTexts(t *testing.T) {
	end := now.Add(48 * time.Hour)
	e := &Event{ScheduleType: "weekly", StartMinute: ip(0), EndMinute: ip(1440), Weekdays: ip(96), EndsAt: &end,
		SellPricePct: 150, QualityBonusBP: 250, FreeWater: 1}
	if got := ScheduleText(e); got != "每週六、日 00:00–24:00（即日起 ～ 2026-01-03 08:00）" {
		t.Fatal(got)
	}
	if got := EffectsText(e); got != "售價 ×1.5、品質加成 +2.5%、免澆水（種下立即生長）" {
		t.Fatal(got)
	}
}
