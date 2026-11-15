// Package hourglass 加速沙漏（Go 版新增，2026-11-15）：消耗品，在種子實驗室的研究槽或地下菇房使用，剩餘時間減少 CutPct%。
// 數值可在管理網站「加速沙漏」調整（featurecfg key = ConfigKey）。
package hourglass

import (
	"sync"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
)

const ConfigKey = "hourglass"

// Config 目前生效的設定。
type Config struct {
	Price      int // 商店價格
	CutPct     int // 剩餘時間減少的百分比
	MinSeconds int // 剩餘時間少於這個秒數時不能用（避免浪費）
}

func defaults() featurecfg.Value {
	return featurecfg.Value{"price": 500, "cut_pct": 50, "min_seconds": 60}
}

func parse(raw featurecfg.Value) (*Config, error) {
	c := &Config{}
	for _, f := range []struct {
		key, label string
		low, high  int
		ptr        *int
	}{
		{"price", "加速沙漏價格", 1, 100_000_000, &c.Price},
		{"cut_pct", "剩餘時間減少（%）", 1, 99, &c.CutPct},
		{"min_seconds", "剩餘時間少於幾秒不能用", 0, 86_400, &c.MinSeconds},
	} {
		n, err := featurecfg.IntValue(raw[f.key], f.label, f.low, f.high)
		if err != nil {
			return nil, err
		}
		*f.ptr = n
	}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	return featurecfg.Value{"price": c.Price, "cut_pct": c.CutPct, "min_seconds": c.MinSeconds}, nil
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

// Cut 使用一次後新的剩餘時間（四捨五入到秒）。
func Cut(remaining time.Duration) time.Duration {
	left := remaining * time.Duration(100-Current().CutPct) / 100
	return left.Round(time.Second)
}

// Usable 剩餘時間夠長才能用。
func Usable(remaining time.Duration) bool {
	return remaining > 0 && remaining >= time.Duration(Current().MinSeconds)*time.Second
}

func init() {
	featurecfg.Register(ConfigKey, "加速沙漏", defaults, ValidateConfig)
	balance.RegisterPrice(balance.ItemHourglass, func() int { return Current().Price })
}
