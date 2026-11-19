// Package craftprice 加工品的浮動售價（2026-11-19 使用者要求）：每 PeriodDays 天（台灣日期）抽一次價格，
// 範圍是加工品總成本的 -DownPct%～+UpPct%（例如成本 102 → 51～204）。
//
//   - 總成本：一份成品的原料成本，原料若也是加工品則一路往下算到最基本的原料（作物用資料庫的基本售價、不含季節加成；
//     動物產品用資料庫售價；菇類與其他非加工品用程式內建售價）。不能賣的原料（例如藥草）沒有價格，成本算 0。
//   - 抽價：以「期數＋商品代碼」雜湊決定，同一期內所有程序（網站、Bot）算出的價格都一樣，不需要存資料庫；
//     換期時自動換價。
//   - 只有加工配方（balance.Recipes）的成品是加工品；菇類（地下菇房採收）維持固定售價。
package craftprice

import (
	"hash/fnv"
	"strconv"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
)

const (
	PeriodDays = 3   // 幾天抽一次價格（台灣時間 00:00 換期）
	DownPct    = 50  // 價格下限：總成本 -DownPct%
	UpPct      = 100 // 價格上限：總成本 +UpPct%
)

// byOutput 成品代碼 → 配方。
var byOutput = func() map[string]balance.Recipe {
	out := map[string]balance.Recipe{}
	for _, rc := range balance.Recipes {
		out[rc.Output] = rc
	}
	return out
}()

// IsCrafted 是否為加工品（某個加工配方的成品）。
func IsCrafted(code string) bool {
	_, ok := byOutput[code]
	return ok
}

// epochDay 台灣日期距 1970-01-01 的天數。
func epochDay(now time.Time) int64 {
	l := now.In(clock.TaipeiTZ)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// Period 目前是第幾期（每 PeriodDays 天一期）。
func Period(now time.Time) int64 {
	d := epochDay(now)
	if d < 0 {
		return (d - PeriodDays + 1) / PeriodDays
	}
	return d / PeriodDays
}

// PeriodEnd 這一期結束（下一次換價）的時間：台灣時間 00:00。
func PeriodEnd(now time.Time) time.Time {
	day := (Period(now) + 1) * PeriodDays
	return time.Date(1970, 1, 1+int(day), 0, 0, 0, 0, clock.TaipeiTZ)
}

// Costs 每個加工品一份的總成本（四捨五入）；base 是非加工品的單價（沒有的視為 0）。
func Costs(base map[string]int) map[string]int {
	memo := map[string]float64{}
	var cost func(code string, depth int) float64
	cost = func(code string, depth int) float64 {
		if v, ok := memo[code]; ok {
			return v
		}
		rc, ok := byOutput[code]
		if !ok || depth > 10 { // 不是加工品；depth 防止配方循環
			return float64(base[code])
		}
		total := 0.0
		for _, item := range rc.InputOrder {
			total += cost(item, depth+1) * float64(rc.Inputs[item])
		}
		v := total / float64(max(rc.OutputQty, 1))
		memo[code] = v
		return v
	}
	out := map[string]int{}
	for code := range byOutput {
		out[code] = int(cost(code, 0) + 0.5)
	}
	return out
}

// Range 總成本對應的價格範圍（至少 1 金）。
func Range(cost int) (low, high int) {
	low = max(1, cost*(100-DownPct)/100)
	high = max(low, cost*(100+UpPct)/100)
	return low, high
}

// Price 某一期的售價：在 Range(cost) 之間依雜湊均勻抽一個整數。
func Price(code string, cost int, period int64) int {
	low, high := Range(cost)
	h := fnv.New64a()
	_, _ = h.Write([]byte("craftprice:" + code + ":" + strconv.FormatInt(period, 10)))
	return low + int(h.Sum64()%uint64(high-low+1))
}

// Quote 一個加工品目前的價格資訊。
type Quote struct {
	Cost, Low, High, Price int
}

// Quotes 所有加工品在 now 這一期的成本、範圍與售價。總成本為 0（原料都不能賣）時沿用程式內建售價，不浮動。
func Quotes(base map[string]int, now time.Time) map[string]Quote {
	period := Period(now)
	out := map[string]Quote{}
	for code, cost := range Costs(base) {
		if cost <= 0 {
			p := balance.Goods[code].SellPrice
			out[code] = Quote{cost, p, p, p}
			continue
		}
		low, high := Range(cost)
		out[code] = Quote{cost, low, high, Price(code, cost, period)}
	}
	return out
}
