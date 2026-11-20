package weather

import (
	"time"

	"farmgm/internal/clock"
)

// 天氣奇遇（Go 版新增，2026-11-20，設計見「新玩法六項_流程圖.md」#4）：由連續幾天的天氣觸發。
// 天氣只由日期決定，所以奇遇也只由日期決定、不寫資料庫；「今天撿過沒」記在 farm_tools（LuckToolCode）。
const (
	LuckLightning = "lightning" // 昨天暴風 → 今天農田出現雷擊種子（每人每天 1 次，種下保證突變）
	LuckDrought   = "drought"   // 連續 DroughtDays 天晴天（含今天）→ 古代遺跡多一個「乾涸的古井」事件
	LuckDrizzle   = "drizzle"   // 連續 DrizzleDays 天雨天（含今天）→ 地下菇房每床多收、稀有機率提高
	LuckRainbow   = "rainbow"   // 昨天雨天、今天晴天 → 藏寶圖碎片掉率加倍，每人可以領 1 片（2026-11-20，#9）
)

const (
	DroughtDays       = 3
	DrizzleDays       = 2
	DrizzleExtraYield = 1  // 連綿細雨：每個菇床多收幾朵
	DrizzleRarePct    = 10 // 連綿細雨：稀有菇機率加幾個百分點
	// LuckToolCode farm_tools 的代碼：settings {"lightning_day": "YYYY-MM-DD"} 記錄撿過雷擊種子的日期。
	LuckToolCode = "weather_luck"
)

// LightningMutations 雷擊種子保證的突變（一般農田就有的突變，和賭博相同的三種），平均抽一種。
var LightningMutations = []string{"giant", "golden", "variant"}

// LuckOrder 顯示順序。
var LuckOrder = []string{LuckLightning, LuckRainbow, LuckDrought, LuckDrizzle}

// lookback 判定奇遇需要往前看的天數（含今天）。
const lookback = DroughtDays

// On 今天往後 offset 天（負數為往前）的天氣；Fixed 有值時每天都是那種天氣（測試用）。
func On(now time.Time, offset int) Kind {
	if k, ok := Kinds[Fixed]; ok {
		return k
	}
	return ForDate(now.In(clock.TaipeiTZ).AddDate(0, 0, offset))
}

// Forecast 從今天起連續 days 天的天氣（[0] 是今天）。
func Forecast(now time.Time, days int) []Kind {
	out := make([]Kind, days)
	for i := range out {
		out[i] = On(now, i)
	}
	return out
}

// LucksFrom 依天氣代碼（依時間順序，最後一個是當天）判定當天的奇遇，順序同 LuckOrder。
func LucksFrom(days []string) []string {
	n := len(days)
	streak := func(code string) int {
		c := 0
		for i := n - 1; i >= 0 && days[i] == code; i-- {
			c++
		}
		return c
	}
	var out []string
	if n >= 2 && days[n-2] == Storm {
		out = append(out, LuckLightning)
	}
	if n >= 2 && days[n-2] == Rainy && days[n-1] == Sunny {
		out = append(out, LuckRainbow)
	}
	if streak(Sunny) >= DroughtDays {
		out = append(out, LuckDrought)
	}
	if streak(Rainy) >= DrizzleDays {
		out = append(out, LuckDrizzle)
	}
	return out
}

// ForceLucks 不是 nil 時，今天（offset 0）的奇遇固定為這些（測試用；彩虹日無法只靠 Fixed 產生）。
var ForceLucks []string

// LucksOn 今天往後 offset 天的奇遇（0 = 今天）。
func LucksOn(now time.Time, offset int) []string {
	if offset == 0 && ForceLucks != nil {
		return append([]string(nil), ForceLucks...)
	}
	days := make([]string, lookback)
	for i := range days {
		days[i] = On(now, offset-(lookback-1)+i).Code
	}
	return LucksFrom(days)
}

// HasLuck 今天是否有這個奇遇。
func HasLuck(now time.Time, code string) bool {
	for _, c := range LucksOn(now, 0) {
		if c == code {
			return true
		}
	}
	return false
}

// DayKey 台灣日期（記錄每天一次的動作用）。
func DayKey(now time.Time) string { return now.In(clock.TaipeiTZ).Format("2006-01-02") }
