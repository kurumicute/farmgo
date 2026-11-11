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
	var got []string
	for _, m := range []int{3, 6, 9, 12, 2} {
		got = append(got, SeasonOf(taipei(m, 1, 12)))
	}
	if !reflect.DeepEqual(got, []string{"spring", "summer", "autumn", "winter", "winter"}) {
		t.Fatal(got)
	}
	if SeasonOf(time.Date(2026, 2, 28, 16, 0, 0, 0, time.UTC)) != "spring" { // 台灣時間 3/1 00:00
		t.Fatal("3/1")
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
