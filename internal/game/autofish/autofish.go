// Package autofish 自動釣魚機與魚簍升級的規則（Go 版新增，2026-11-15）。數值可在管理網站「自動釣魚與魚簍」調整
// （featurecfg key = ConfigKey，存在 feature_settings）。
//
//   - 自動釣魚機（balance.ToolAutoFisher，工具）：啟用時每 IntervalMinutes 分鐘釣 1 條魚放進魚簍，魚簍滿了就暫停
//     （不會累積）；同時啟用自動販售機且 AutoSellFish 時，釣到的魚直接賣掉，最多補算 AutoSellMaxHours 小時。
//     每釣 1 條消耗 BaitPerCatch 個裝備中的魚餌（ConsumeBait），沒有魚餌就暫停；釣到的魚跟手動釣魚一樣依魚餌決定
//     （稀有度加成、只有特定魚餌才釣得到的魚），而且是當下季節與時段有的魚。
//   - 加速（farm_tools.settings.speed_level）：各級的間隔與價格。
//   - 魚簍升級（farms.fish_basket_level）：第 1、2…級的容量與價格；0 級用釣魚設定的 basket_capacity。
//   - 自動釣魚機的價格由 init 以 balance.RegisterPrice 登記，商店與購買都讀 balance.ItemPrice。
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
	// 2026-11-15 第二版：每釣 1 條消耗裝備中的魚餌（沒有魚餌就暫停）、加速等級（各級的間隔與價格）
	ConsumeBait    bool
	BaitPerCatch   int
	SpeedIntervals []int // 加速第 1、2…級的間隔（分鐘），要一級比一級短、而且比 IntervalMinutes 短
	SpeedPrices    []int
}

func defaults() featurecfg.Value {
	return featurecfg.Value{
		"machine_price":       100_000,
		"interval_minutes":    5,
		"auto_sell_fish":      1,
		"auto_sell_max_hours": 24,
		"basket_capacities":   []int{40, 60, 80, 100},
		"basket_prices":       []int{30_000, 50_000, 100_000, 200_000},
		"consume_bait":        1,
		"bait_per_catch":      1,
		"speed_intervals":     []int{4, 3},
		"speed_prices":        []int{50_000, 120_000},
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
	// 第二版新增的設定：之前存的快照沒有時用預設值
	def := defaults()
	get := func(key string) any {
		if v, ok := raw[key]; ok {
			return v
		}
		return def[key]
	}
	bait, err := featurecfg.IntValue(get("consume_bait"), "自動釣魚消耗魚餌（0／1）", 0, 1)
	if err != nil {
		return nil, err
	}
	c.ConsumeBait = bait == 1
	if c.BaitPerCatch, err = featurecfg.IntValue(get("bait_per_catch"), "每釣 1 條消耗的魚餌", 1, 10); err != nil {
		return nil, err
	}
	if c.SpeedIntervals, err = featurecfg.IntList(get("speed_intervals"), "加速各級的間隔（分鐘）", 1, 1440, 0, MaxSpeedLevel); err != nil {
		return nil, err
	}
	if c.SpeedPrices, err = featurecfg.IntList(get("speed_prices"), "加速各級的價格", 1, 1_000_000_000, 0, MaxSpeedLevel); err != nil {
		return nil, err
	}
	if len(c.SpeedIntervals) != len(c.SpeedPrices) {
		return nil, gameerr.Validationf("加速各級的間隔與價格的數量要一樣（目前 %d 個間隔、%d 個價格）。", len(c.SpeedIntervals), len(c.SpeedPrices))
	}
	prev := c.IntervalMinutes
	for i, m := range c.SpeedIntervals {
		if m >= prev {
			return nil, gameerr.Validationf("加速第 %d 級的間隔 %d 分鐘要比前一級的 %d 分鐘短。", i+1, m, prev)
		}
		prev = m
	}
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
	bait := 0
	if c.ConsumeBait {
		bait = 1
	}
	return featurecfg.Value{"machine_price": c.MachinePrice, "interval_minutes": c.IntervalMinutes, "auto_sell_fish": sell,
		"auto_sell_max_hours": c.AutoSellMaxHours, "basket_capacities": c.BasketCapacities, "basket_prices": c.BasketPrices,
		"consume_bait": bait, "bait_per_catch": c.BaitPerCatch, "speed_intervals": c.SpeedIntervals, "speed_prices": c.SpeedPrices}
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

// MaxSpeedLevel 加速最多幾級。
const MaxSpeedLevel = 5

// Interval 自動釣魚的間隔（speedLevel 0 = 沒有加速；超過目前設定的級數時以最高級計算）。
func Interval(speedLevel int) time.Duration {
	c := Current()
	m := c.IntervalMinutes
	if speedLevel > 0 && len(c.SpeedIntervals) > 0 {
		m = c.SpeedIntervals[min(speedLevel, len(c.SpeedIntervals))-1]
	}
	return time.Duration(m) * time.Minute
}

// NextSpeed 下一級加速的間隔（分鐘）與價格；已滿級時 ok = false。
func NextSpeed(level int) (minutes, price int, ok bool) {
	c := Current()
	if level < 0 || level >= len(c.SpeedIntervals) {
		return 0, 0, false
	}
	return c.SpeedIntervals[level], c.SpeedPrices[level], true
}

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
	balance.RegisterPrice(balance.ToolAutoFisher, func() int { return Current().MachinePrice })
}
