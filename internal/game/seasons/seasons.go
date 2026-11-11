// Package seasons 季節與時段限定（純規則）：部分作物只能在特定季節種植，部分魚只在特定季節或時段釣得到。
// 對應 Python app/game/seasons.py。
//
// 季節依台灣時間的真實月份（使用者選擇）：春 3–5 月、夏 6–8 月、秋 9–11 月、冬 12–2 月。
// 時段也是台灣時間的整點區間 [開始, 結束)，可以跨午夜（例如 18–6 = 晚上 6 點到早上 6 點）。
// 沒有設定（nil）就是不限制。作物只在「種下」的當下檢查，種下後過了季節照樣會長大、收成；
// 魚在「拋竿」的當下檢查。
package seasons

import (
	"fmt"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

var Seasons = []string{"spring", "summer", "autumn", "winter"}

var SeasonNames = map[string]string{"spring": "春", "summer": "夏", "autumn": "秋", "winter": "冬"}

var SeasonEmoji = map[string]string{"spring": "🌸", "summer": "☀️", "autumn": "🍂", "winter": "❄️"}

var monthSeason = map[time.Month]string{
	3: "spring", 4: "spring", 5: "spring", 6: "summer", 7: "summer", 8: "summer",
	9: "autumn", 10: "autumn", 11: "autumn", 12: "winter", 1: "winter", 2: "winter",
}

// 預設限定（作物與 0033 的魚由 Migration 0034 寫入，0035 的特殊魚由 0035 寫入，數值相同）

// CropSeasons 小麥四季都能種（不列出）；其他每種兩個季節，每季都有 4–5 種作物可以種（2026-11-09 使用者要求）
var CropSeasons = map[string][]string{
	"carrot":      {"autumn", "winter"},
	"tomato":      {"winter", "spring"},
	"strawberry":  {"winter", "spring"},
	"corn":        {"spring", "summer"},
	"watermelon":  {"spring", "summer"},
	"pumpkin":     {"summer", "autumn"},
	"grape":       {"summer", "autumn"},
	"amber_melon": {"autumn", "winter"},
	"moonflower":  {"winter", "spring"},
}

// Hours 台灣時間整點區間 [Start, End)。
type Hours struct{ Start, End int }

// FishLimit 魚的季節（nil = 不限）與時段（nil = 不限）。
type FishLimit struct {
	Seasons []string
	Hours   *Hours
}

func h(start, end int) *Hours { return &Hours{start, end} }

// FishLimits 魚：code → 季節／時段限定。
var FishLimits = map[string]FishLimit{
	"pond_smelt":       {[]string{"winter"}, nil},
	"mullet":           {[]string{"winter"}, nil},
	"mackerel":         {[]string{"autumn"}, nil},
	"yellowfin_tuna":   {[]string{"summer"}, nil},
	"swordfish":        {[]string{"autumn"}, nil},
	"dragon_king_carp": {[]string{"spring"}, nil},
	"bluegill":         {nil, h(6, 18)},
	"snakehead":        {nil, h(18, 6)},
	"eel":              {nil, h(18, 6)},
	"moon_ray":         {nil, h(20, 4)},
	// Migration 0035 的特殊魚：季節＋時段（部分還要指定魚餌，見 fishing.FISH_REQUIRED_BAITS）
	"dawn_trout":        {nil, h(4, 7)},
	"snow_icefish":      {[]string{"winter"}, h(5, 9)},
	"firefly_squid":     {[]string{"spring"}, h(20, 4)},
	"lanternfish":       {[]string{"summer"}, h(20, 4)},
	"maple_salmon":      {[]string{"autumn"}, h(16, 19)},
	"rainbow_wrasse":    {[]string{"summer"}, h(10, 16)},
	"midnight_angler":   {nil, h(0, 3)},
	"spring_koi":        {[]string{"spring"}, h(6, 10)},
	"ghost_shark":       {[]string{"autumn"}, h(21, 3)},
	"frost_dragon_fish": {[]string{"winter"}, h(22, 2)},
}

func SeasonOf(now time.Time) string {
	return monthSeason[now.In(clock.TaipeiTZ).Month()]
}

// InHours start／end 為 nil 或相同時不限制。
func InHours(now time.Time, start, end *int) bool {
	if start == nil || end == nil || *start == *end {
		return true
	}
	hour := now.In(clock.TaipeiTZ).Hour()
	if *start < *end {
		return *start <= hour && hour < *end
	}
	return hour >= *start || hour < *end
}

// Available seasons 為空（nil）表示不限季節。
func Available(seasons []string, start, end *int, now time.Time) bool {
	if len(seasons) > 0 && !contains(seasons, SeasonOf(now)) {
		return false
	}
	return InHours(now, start, end)
}

// Describe 顯示用：「🍂秋季限定」「🕒 18:00–06:00」，沒有限制時回傳空字串。
func Describe(seasons []string, start, end *int) string {
	var parts []string
	if len(seasons) > 0 {
		var emoji, names []string
		for _, s := range seasons {
			emoji = append(emoji, SeasonEmoji[s])
			names = append(names, SeasonNames[s])
		}
		parts = append(parts, strings.Join(emoji, "")+strings.Join(names, "、")+"季限定")
	}
	if start != nil && end != nil && *start != *end {
		parts = append(parts, fmt.Sprintf("🕒 %02d:00–%02d:00", *start, *end))
	}
	return strings.Join(parts, "　")
}

func NowText(now time.Time) string {
	s := SeasonOf(now)
	return fmt.Sprintf("%s 現在是%s季　🕒 台灣時間 %s", SeasonEmoji[s], SeasonNames[s], now.In(clock.TaipeiTZ).Format("15:04"))
}

// Validate 管理網站送來的設定：季節清單（空 = 不限）、開始與結束整點（0–23，兩個都填或都不填）。
// seasons 可為 nil、[]string 或 []any；start／end 可為 nil、int 或 json.Number。
// 回傳的季節依 Seasons 順序排列，空清單回傳 nil。
func Validate(seasons any, start, end any) ([]string, *int, *int, error) {
	var out []string
	if seasons != nil {
		var list []string
		switch v := seasons.(type) {
		case []string:
			list = v
		case []any:
			for _, x := range v {
				s, ok := x.(string)
				if !ok {
					return nil, nil, nil, gameerr.Validationf("季節只能是 spring、summer、autumn、winter。")
				}
				list = append(list, s)
			}
		default:
			return nil, nil, nil, gameerr.Validationf("季節只能是 spring、summer、autumn、winter。")
		}
		for _, s := range list {
			if !contains(Seasons, s) {
				return nil, nil, nil, gameerr.Validationf("季節只能是 spring、summer、autumn、winter。")
			}
		}
		for _, s := range Seasons {
			if contains(list, s) {
				out = append(out, s)
			}
		}
	}
	if (start == nil) != (end == nil) {
		return nil, nil, nil, gameerr.Validationf("時段的開始與結束要一起設定（或都不設定）。")
	}
	var hours [2]*int
	for i, v := range []any{start, end} {
		if v == nil {
			continue
		}
		n, ok := featurecfg.AsInt(v)
		if !ok || n < 0 || n > 23 {
			return nil, nil, nil, gameerr.Validationf("時段必須是 0–23 的整點。")
		}
		hours[i] = &n
	}
	if hours[0] != nil && *hours[0] == *hours[1] {
		return nil, nil, nil, gameerr.Validationf("時段的開始與結束不能相同。")
	}
	return out, hours[0], hours[1], nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
