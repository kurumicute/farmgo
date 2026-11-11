package specialshop

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/featurecfg"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, clock.TaipeiTZ)
}

func TestWeekStartAndOffersRotate(t *testing.T) {
	featurecfg.Reset()
	// 2026-01-01（週四）台灣時間 → 該週週一 2025-12-29；週日 23:59 仍是同一週
	if !WeekStart(date(2026, 1, 1)).Equal(date(2025, 12, 29)) {
		t.Fatal(WeekStart(date(2026, 1, 1)))
	}
	if !WeekStart(time.Date(2026, 1, 4, 23, 59, 0, 0, clock.TaipeiTZ)).Equal(date(2025, 12, 29)) {
		t.Fatal("sunday")
	}
	if !WeekStart(date(2026, 1, 5)).Equal(date(2026, 1, 5)) {
		t.Fatal("monday")
	}
	// UTC 週日 16:00 = 台灣週一 00:00
	if !WeekStart(time.Date(2026, 1, 4, 16, 0, 0, 0, time.UTC)).Equal(date(2026, 1, 5)) {
		t.Fatal("utc")
	}
	if !WeekEndsAt(date(2026, 1, 5)).Equal(date(2026, 1, 12)) {
		t.Fatal("ends")
	}
	crops := []CropRef{
		{"wheat", "wheat", 5, true}, {"carrot", "carrot", 12, true}, {"tomato", "tomato", 25, true},
		{"strawberry", "strawberry", 40, true}, {"amber_melon", "amber_melon", 200, false},
		{"moonflower", "moonflower", 500, false},
	}
	codes := func(os []Offer) string {
		var s []string
		for _, o := range os {
			s = append(s, o.CropCode)
		}
		return strings.Join(s, ",")
	}
	w0 := WeeklyOffers(date(2026, 1, 5), crops)
	w1 := WeeklyOffers(date(2026, 1, 12), crops)
	if codes(w0) != "wheat,carrot,amber_melon" || codes(w1) != "tomato,strawberry,moonflower" {
		t.Fatal(codes(w0), codes(w1))
	}
	if w0[1].Price != 9 || w0[1].Limit != ConfigInt("normal_limit") { // 12 × 0.7 = 8.4 → 9
		t.Fatal(w0[1])
	}
	if w1[2].Price != 600 || w1[2].Limit != ConfigInt("rare_limit") || !w1[2].Rare {
		t.Fatal(w1[2])
	}
	// 第 0 週之前（負的週次）也要和 Python 一樣輪替
	if got := codes(WeeklyOffers(date(2025, 12, 29), crops)); got != "tomato,strawberry,moonflower" {
		t.Fatal(got)
	}
}

func TestGreenhouseAndTitles(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	if GreenhouseMaxLevel() != 5 || GreenhousePct(3) != 15 || GreenhousePct(99) != 25 {
		t.Fatal("greenhouse")
	}
	if p, ok := GreenhousePrice(0); !ok || p != 5_000 {
		t.Fatal(p)
	}
	if _, ok := GreenhousePrice(5); ok {
		t.Fatal("max")
	}
	if p, ok := TitlePrice("黃金地主"); !ok || p != 100_000 {
		t.Fatal(p)
	}
	base := `"normal_deals":2,"normal_discount_pct":30,"normal_limit":50,"rare_price_pct":120,"rare_limit":3`
	raw, _ := featurecfg.Decode([]byte(`{"greenhouse_prices":[100,200],"greenhouse_pct_per_level":10,` + base +
		`,"titles":[{"title":"測試大亨","price":123}]}`))
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if GreenhouseMaxLevel() != 2 {
		t.Fatal("level")
	}
	if p, ok := TitlePrice("測試大亨"); !ok || p != 123 {
		t.Fatal(p)
	}
	head := `{"greenhouse_prices":[100],"greenhouse_pct_per_level":1,` + base
	for js, want := range map[string]string{
		`{"greenhouse_prices":[100,200],"greenhouse_pct_per_level":50,` + base + `,"titles":[]}`: "不可超過 90%",
		head + `,"titles":[{"title":"a","price":1},{"title":"a","price":2}]}`:                    "不可重複",
		head + `,"titles":[{"title":" ","price":1}]}`:                                            "不可空白",
		head + `,"titles":[{"price":1}]}`:                                                        "不可空白",
		head + `,"titles":[{"title":"x","price":0}]}`:                                            "稱號「x」的價格",
		head + `}`: "最多 20 個稱號",
	} {
		r, _ := featurecfg.Decode([]byte(js))
		if _, err := featurecfg.Validate(ConfigKey, r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}
