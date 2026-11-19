// Package reputation 農場聲望（Go 版新增，2026-11-19，金幣回收第二波）：在「🏛️ 農會」捐金幣，1 金 = 1 聲望點，
// 捐出的金幣完全消失（不算進「淨賺金幣」排行）。等級沒有上限，需要的累積聲望 = LevelBase × 等級^(LevelExpPct/100)；
// 每 5 級給外觀獎勵（稱號或名牌，見 Rewards），不給數值。數值可在管理網站「農場聲望」調整（featurecfg key = ConfigKey）。
package reputation

import (
	"math"
	"sync"

	"farmgm/internal/game/featurecfg"
)

const ConfigKey = "reputation"

// Config 目前生效的設定（唯讀）。
type Config struct {
	MinDonation int // 一次最少捐多少
	DailyCap    int // 每天最多捐多少（0 = 不限）
	LevelBase   int // Lv.1 需要的聲望
	LevelExpPct int // 等級的指數（150 = 1.5 次方）
}

func defaults() featurecfg.Value {
	return featurecfg.Value{"min_donation": 1_000, "daily_cap": 1_000_000, "level_base": 10_000, "level_exp_pct": 150}
}

type field struct {
	key, label string
	low, high  int
	ptr        func(*Config) *int
}

var fields = []field{
	{"min_donation", "一次最少捐多少", 1, 1_000_000_000, func(c *Config) *int { return &c.MinDonation }},
	{"daily_cap", "每天最多捐多少（0 = 不限）", 0, 1_000_000_000_000, func(c *Config) *int { return &c.DailyCap }},
	{"level_base", "Lv.1 需要的聲望", 1, 1_000_000_000, func(c *Config) *int { return &c.LevelBase }},
	{"level_exp_pct", "等級的指數（%，150 = 1.5 次方）", 100, 300, func(c *Config) *int { return &c.LevelExpPct }},
}

func parse(raw featurecfg.Value) (*Config, error) {
	c := &Config{}
	for _, f := range fields {
		n, err := featurecfg.IntValue(raw[f.key], f.label, f.low, f.high)
		if err != nil {
			return nil, err
		}
		*f.ptr(c) = n
	}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	out := featurecfg.Value{}
	for _, f := range fields {
		out[f.key] = *f.ptr(c)
	}
	return out, nil
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

// Need 升到 level 需要的累積聲望。
func Need(level int) int {
	if level <= 0 {
		return 0
	}
	c := Current()
	return int(math.Round(float64(c.LevelBase) * math.Pow(float64(level), float64(c.LevelExpPct)/100)))
}

// Level 累積聲望對應的等級。
func Level(points int) int {
	lv := 0
	for Need(lv+1) <= points {
		lv++
		if lv > 100_000 { // 防呆：設定極端時不會無限迴圈
			break
		}
	}
	return lv
}

// Reward 等級獎勵：Title 或 Badge（名牌代碼，見 game/cosmetics）其中之一。
type Reward struct {
	Level int
	Title string
	Badge string
}

// Rewards 等級獎勵（只給外觀），大約每 5～25 級一個。
var Rewards = []Reward{
	{Level: 5, Title: "農會會員"},
	{Level: 10, Badge: "wheat"},
	{Level: 15, Title: "農會理事"},
	{Level: 20, Title: "熱心捐款人"},
	{Level: 25, Badge: "temple"},
	{Level: 30, Title: "農會長老"},
	{Level: 40, Badge: "sparkle"},
	{Level: 50, Title: "農會會長"},
	{Level: 75, Title: "農會傳奇"},
}

// RewardsBetween 從 from 級（不含）升到 to 級（含）可以拿到的獎勵。
func RewardsBetween(from, to int) []Reward {
	var out []Reward
	for _, r := range Rewards {
		if r.Level > from && r.Level <= to {
			out = append(out, r)
		}
	}
	return out
}

// NextReward 下一個還沒拿到的獎勵（沒有時 ok = false）。
func NextReward(level int) (Reward, bool) {
	for _, r := range Rewards {
		if r.Level > level {
			return r, true
		}
	}
	return Reward{}, false
}

// Titles 聲望獎勵的稱號（檢查與其他稱號重複用）。
func Titles() []string {
	var out []string
	for _, r := range Rewards {
		if r.Title != "" {
			out = append(out, r.Title)
		}
	}
	return out
}

func init() {
	featurecfg.Register(ConfigKey, "農場聲望（農會）", defaults, ValidateConfig)
}
