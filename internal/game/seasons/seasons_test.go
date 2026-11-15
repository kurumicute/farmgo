package seasons

import (
	"reflect"
	"testing"
	"time"

	"farmgm/internal/game/gameerr"
)

func taipei(month, day, hour int) time.Time {
	return time.Date(2026, time.Month(month), day, hour, 0, 0, 0, time.UTC).Add(-8 * time.Hour)
}

func ip(v int) *int { return &v }

// 移植自 tests/test_seasons.py::test_rules_real_seasons_and_hours
func TestRulesRealSeasonsAndHours(t *testing.T) {
	// 2026-11-15 起每 14 天輪替：冬 → 春 → 夏 → 秋（之前的時間往回推算）
	var got []string
	for _, d := range []int{15, 28, 29, 13 + 30, 27 + 30, 10 + 61, 14 + 30} { // 11/15、11/28、11/29、12/13、12/27、1/10、12/14
		got = append(got, SeasonOf(taipei(11, d, 12)))
	}
	if !reflect.DeepEqual(got, []string{"winter", "winter", "spring", "summer", "autumn", "winter", "summer"}) {
		t.Fatal(got)
	}
	if SeasonOf(time.Date(2026, 11, 28, 15, 59, 0, 0, time.UTC)) != "winter" || SeasonOf(time.Date(2026, 11, 28, 16, 0, 0, 0, time.UTC)) != "spring" {
		t.Fatal("台灣時間 11/29 00:00 換季")
	}
	if SeasonOf(taipei(11, 14, 12)) != "autumn" || SeasonOf(taipei(11, 1, 12)) != "autumn" || SeasonOf(taipei(10, 31, 12)) != "summer" {
		t.Fatal("Epoch 之前往回推算")
	}
	if n := NextChange(taipei(11, 20, 12)); !n.Equal(time.Date(2026, 11, 28, 16, 0, 0, 0, time.UTC)) {
		t.Fatal(n)
	}
	if s := NowText(taipei(11, 20, 12)); s != "❄️ 現在是冬季（11/29 換成🌸春季）　🕒 台灣時間 12:00" {
		t.Fatal(s)
	}
	if !InHours(taipei(1, 1, 20), ip(18), ip(6)) || !InHours(taipei(1, 1, 5), ip(18), ip(6)) {
		t.Fatal("overnight")
	}
	if InHours(taipei(1, 1, 6), ip(18), ip(6)) || InHours(taipei(1, 1, 12), ip(18), ip(6)) {
		t.Fatal("overnight out")
	}
	if !InHours(taipei(1, 1, 6), ip(6), ip(18)) || InHours(taipei(1, 1, 18), ip(6), ip(18)) {
		t.Fatal("day")
	}
	if !Available(nil, nil, nil, taipei(1, 1, 12)) || Available([]string{"summer"}, nil, nil, taipei(1, 1, 12)) {
		t.Fatal("available")
	}
	if Describe([]string{"autumn"}, nil, nil) != "🍂秋季限定" || Describe(nil, ip(18), ip(6)) != "🕒 18:00–06:00" ||
		Describe(nil, nil, nil) != "" {
		t.Fatal("describe")
	}
	s, a, b, err := Validate([]any{"winter", "spring"}, nil, nil)
	if err != nil || !reflect.DeepEqual(s, []string{"spring", "winter"}) || a != nil || b != nil {
		t.Fatal(s, err)
	}
	s, a, b, err = Validate([]any{}, 18, 6)
	if err != nil || s != nil || *a != 18 || *b != 6 {
		t.Fatal(s, err)
	}
	bads := [][3]any{{[]any{"rainy"}, nil, nil}, {nil, 18, nil}, {nil, 5, 5}, {nil, 24, 1}, {nil, true, 1}}
	for i, bad := range bads {
		if _, _, _, err := Validate(bad[0], bad[1], bad[2]); !gameerr.Is(err, gameerr.Validation) {
			t.Errorf("bad %d: %v", i, err)
		}
	}
}

// 每季至少 4 種作物可以種、所有作物都是兩個季節（test_migration_constants_match_rules 中不需要 Migration 的部分）。
func TestCropSeasonCoverage(t *testing.T) {
	for code, s := range CropSeasons {
		if len(s) != 2 {
			t.Fatal(code)
		}
	}
	for _, season := range Seasons {
		n := 0
		for _, s := range CropSeasons {
			if contains(s, season) {
				n++
			}
		}
		if n < 4 {
			t.Fatal(season, n)
		}
	}
}
