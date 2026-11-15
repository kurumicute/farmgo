// Package autofish 自動釣魚機與魚簍升級的規則（Go 版新增，2026-11-15）。數值可在管理網站「自動釣魚與魚簍」調整
// （featurecfg key = ConfigKey，存在 feature_settings）。
//
//   - 自動釣魚機（balance.ToolAutoFisher，工具）：啟用時每 IntervalMinutes 分鐘釣 1 條魚放進魚簍，魚簍滿了就暫停
//     （不會累積）；同時啟用自動販售機且 AutoSellFish 時，釣到的魚直接賣掉，最多補算 AutoSellMaxHours 小時。
//     釣到的魚只會是不需要特定魚餌、當下季節與時段有的魚，稀有度依釣魚設定的基礎機率（不受魚餌加成）。
//   - 魚簍升級（farms.fish_basket_level）：第 1、2…級的容量與價格；0 級用釣魚設定的 basket_capacity。
//   - 自動釣魚機的價格由 init 掛到 balance.PriceOverride，商店與購買都讀 balance.ItemPrice。
package autofish

import (
	"sync"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

const ConfigKey = "autofish"

// MaxBasketLevel farms.fish_basket_level 的 CHECK 上限。
const MaxBasketLevel = 10

// Config 目前生效的設定（唯讀）。
type Config struct {
	MachinePrice     int
	IntervalMinutes  int
	AutoSellFish     bool
	AutoSellMaxHours int
	BasketCapacities []int // 第 1、2…級的容量
	BasketPrices     []int // 升到第 1、2…級的價格
}

func defaults() featurecfg.Value {
	return featurecfg.Value{
		"machine_price":       100_000,
		"interval_minutes":    5,
		"auto_sell_fish":      1,
		"auto_sell_max_hours": 24,
		"basket_capacities":   []int{40, 60, 80, 100},
		"basket_prices":       []int{30_000, 50_000, 100_000, 200_000},
	}
}

func parse(raw featurecfg.Value) (*Config, error) {
	c := &Config{}
	ints := []struct {
		key, label string
		low, high  int
		ptr        *int
	}{
		{"machine_price", "自動釣魚機價格", 1, 1_000_000_000, &c.MachinePrice},
		{"interval_minutes", "自動釣魚間隔（分鐘）", 1, 1440, &c.IntervalMinutes},
		{"auto_sell_max_hours", "自動販售最多補算（小時）", 1, 720, &c.AutoSellMaxHours},
	}
	for _, f := range ints {
		n, err := featurecfg.IntValue(raw[f.key], f.label, f.low, f.high)
		if err != nil {
			return nil, err
		}
		*f.ptr = n
	}
	sell, err := featurecfg.IntValue(raw["auto_sell_fish"], "自動販售機賣自動釣到的魚（0／1）", 0, 1)
	if err != nil {
		return nil, err
	}
	c.AutoSellFish = sell == 1
	if c.BasketCapacities, err = featurecfg.IntList(raw["basket_capacities"], "魚簍各級容量", 5, 500, 0, MaxBasketLevel); err != nil {
		return nil, err
	}
	if c.BasketPrices, err = featurecfg.IntList(raw["basket_prices"], "魚簍升級價格", 1, 1_000_000_000, 0, MaxBasketLevel); err != nil {
		return nil, err
	}
	if len(c.BasketCapacities) != len(c.BasketPrices) {
		return nil, gameerr.Validationf("魚簍各級容量與升級價格的數量要一樣（目前 %d 個容量、%d 個價格）。", len(c.BasketCapacities), len(c.BasketPrices))
	}
	for i := 1; i < len(c.BasketCapacities); i++ {
		if c.BasketCapacities[i] <= c.BasketCapacities[i-1] {
			return nil, gameerr.Validationf("魚簍容量要一級比一級大（第 %d 級 %d 格不大於第 %d 級 %d 格）。",
				i+1, c.BasketCapacities[i], i, c.BasketCapacities[i-1])
		}
	}
	return c, nil
}

func encode(c *Config) featurecfg.Value {
	sell := 0
	if c.AutoSellFish {
		sell = 1
	}
	return featurecfg.Value{"machine_price": c.MachinePrice, "interval_minutes": c.IntervalMinutes, "auto_sell_fish": sell,
		"auto_sell_max_hours": c.AutoSellMaxHours, "basket_capacities": c.BasketCapacities, "basket_prices": c.BasketPrices}
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	return encode(c), nil
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

// Interval 自動釣魚的間隔。
func Interval() time.Duration { return time.Duration(Current().IntervalMinutes) * time.Minute }

// BasketCapacity 魚簍容量：0 級（或設定的等級數比目前少時超過的部分）用 base（釣魚設定的 basket_capacity）。
func BasketCapacity(level, base int) int {
	caps := Current().BasketCapacities
	if level <= 0 || len(caps) == 0 {
		return base
	}
	return max(base, caps[min(level, len(caps))-1])
}

// NextBasket 從目前等級升到下一級的容量與價格；已滿級時 ok = false。
func NextBasket(level int) (capacity, price int, ok bool) {
	c := Current()
	if level < 0 || level >= len(c.BasketCapacities) {
		return 0, 0, false
	}
	return c.BasketCapacities[level], c.BasketPrices[level], true
}

// MaxBasket 設定的最高等級。
func MaxBasket() int { return len(Current().BasketCapacities) }

func init() {
	featurecfg.Register(ConfigKey, "自動釣魚與魚簍", defaults, ValidateConfig)
	balance.PriceOverride = func(code string) (int, bool) {
		if code == balance.ToolAutoFisher {
			return Current().MachinePrice, true
		}
		return 0, false
	}
}
