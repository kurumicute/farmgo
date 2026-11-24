// Package dailyprice 消耗品當天逐次加價（Go 版新增，2026-11-24，金幣回收）。數值可在管理網站「消耗品每日加價」調整
// （featurecfg key = ConfigKey）。
//
//   - 每種列在 Free 裡的道具，每座農場每天（台灣時間）前 Free[code] 個用原價，之後每多買 1 個，單價多 StepPct% 的原價，
//     最多到原價的 MaxPct%。例如原價 500、免加價 5 個、每個 +10%、最多 300%：第 6 個 550、第 7 個 600…第 25 個起 1,500。
//   - 沒有列在 Free 裡的道具不加價（例如魚飼料）。永久工具一次只能買 1 個，也不在這裡。
//   - 「今天買了幾個」由服務層從交易紀錄（buy_item）算出，不需要 Migration；台灣時間 00:00 歸零。
package dailyprice

import (
	"sync"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

const ConfigKey = "daily_price"

// Config 目前生效的設定。
type Config struct {
	StepPct int            // 超過免加價數量後，每多 1 個加原價的幾 %
	MaxPct  int            // 單價最多是原價的幾 %
	Free    map[string]int // 道具代碼 → 每天用原價的數量（沒列出的道具不加價）
}

// Codes 可以設定加價的道具：道具商店裡的消耗品。
func Codes() []string {
	var out []string
	for _, code := range balance.ItemOrder {
		if balance.Items[code].Kind == "consumable" {
			out = append(out, code)
		}
	}
	return out
}

func defaults() featurecfg.Value {
	free := map[string]int{}
	for code, n := range map[string]int{
		balance.ItemFertilizer: 20, balance.ItemEarthworm: 3, balance.ItemHourglass: 5,
		balance.ItemStormGuard: 2, balance.ItemSprinkler: 2, balance.ItemPatrolTicket: 3,
	} {
		if _, ok := balance.Items[code]; ok {
			free[code] = n
		}
	}
	return featurecfg.Value{"step_pct": 10, "max_pct": 300, "free": free}
}

func parse(raw featurecfg.Value) (*Config, error) {
	c := &Config{Free: map[string]int{}}
	var err error
	if c.StepPct, err = featurecfg.IntValue(raw["step_pct"], "每多買 1 個加價（%）", 0, 1_000); err != nil {
		return nil, err
	}
	if c.MaxPct, err = featurecfg.IntValue(raw["max_pct"], "單價上限（原價的 %）", 100, 10_000); err != nil {
		return nil, err
	}
	m, ok := featurecfg.AsMap(raw["free"])
	if raw["free"] != nil && !ok {
		return nil, gameerr.Validationf("每天用原價的數量格式不正確。")
	}
	allowed := map[string]bool{}
	for _, code := range Codes() {
		allowed[code] = true
	}
	for code, v := range m {
		if !allowed[code] {
			return nil, gameerr.Validationf("每天用原價的數量沒有「%s」這個道具。", code)
		}
		n, err := featurecfg.IntValue(v, balance.Items[code].Name+"每天用原價的數量", 0, 100_000)
		if err != nil {
			return nil, err
		}
		c.Free[code] = n
	}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	return featurecfg.Value{"step_pct": c.StepPct, "max_pct": c.MaxPct, "free": c.Free}, nil
}

var (
	cfgMu      sync.Mutex
	cfgVersion = -1
	cfgCache   *Config
)

// Current 目前生效的設定。
func Current() *Config {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if v := featurecfg.Version(); cfgCache == nil || cfgVersion != v {
		c, err := parse(featurecfg.Get(ConfigKey))
		if err != nil {
			c, _ = parse(defaults())
		}
		cfgCache, cfgVersion = c, v
	}
	return cfgCache
}

// Applies 這個道具會不會當天逐次加價。
func Applies(code string) bool {
	_, ok := Current().Free[code]
	return ok
}

// FreeOf 這個道具每天用原價的數量（不加價的道具回傳 -1）。
func FreeOf(code string) int {
	if n, ok := Current().Free[code]; ok {
		return n
	}
	return -1
}

// UnitPrice 今天第 nth 個（1 起算）的單價。
func UnitPrice(code string, base, nth int) int {
	c := Current()
	free, ok := c.Free[code]
	if !ok || nth <= free || base <= 0 {
		return base
	}
	pct := min(c.MaxPct, 100+c.StepPct*(nth-free))
	return max(base, numfmt.Round(float64(base)*float64(pct)/100))
}

// Total 今天已經買了 bought 個，再買 qty 個的總價。
func Total(code string, base, bought, qty int) int {
	total := 0
	for i := 1; i <= qty; i++ {
		total += UnitPrice(code, base, bought+i)
	}
	return total
}

// Next 今天已經買了 bought 個，下一個的單價。
func Next(code string, base, bought int) int { return UnitPrice(code, base, bought+1) }

// Affordable 今天已經買了 bought 個、持有 gold 金幣時，最多還能買幾個（不超過 limit）。
func Affordable(code string, base, bought, gold, limit int) int {
	n, spent := 0, 0
	for n < limit {
		p := UnitPrice(code, base, bought+n+1)
		if spent+p > gold {
			break
		}
		spent += p
		n++
	}
	return n
}

func init() {
	featurecfg.Register(ConfigKey, "消耗品每日加價", defaults, ValidateConfig)
}
