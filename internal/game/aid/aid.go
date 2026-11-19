// Package aid 送禮與互助（Go 版新增，2026-11-17）：同群組的玩家每天可以互送作物、互相幫忙拔草（2026-11-19 由幫忙澆水改成），
// 雙方都拿少量金幣獎勵，
// 用正面互動平衡偷菜。數值可在管理網站「送禮與互助」調整（featurecfg key = ConfigKey）。
//
// 防止小號洗錢：
//   - 雙方都要達到等級門檻（MinLevel），農場也要建立滿 MinFarmDays 天。
//   - 送禮只能送普通品質、沒有突變的作物，一份最多 GiftMaxQuantity 個、總價值（作物基本售價 × 數量）最多 GiftMaxValue 金。
//   - 每天的次數上限：送出、收到各自計算；同一對玩家同一種互助一天只能一次；每人每天只能被拔草一次。日期以台灣時間計算。
package aid

import (
	"sync"

	"farmgm/internal/game/featurecfg"
)

const ConfigKey = "aid"

const (
	// WeedReceivePerDay 每人每天最多被幾位玩家幫忙拔草（固定，不開放調整）。
	WeedReceivePerDay = 1
	// WeedGrowPct 幫忙拔草時，對方生長中作物的剩餘生長時間減少幾 %。
	WeedGrowPct = 10
	// WeedTimeFactor 剩餘生長時間的倍率（1 - WeedGrowPct%）。
	WeedTimeFactor = 1 - float64(WeedGrowPct)/100
)

// Config 目前生效的設定。
type Config struct {
	MinLevel          int // 雙方的農場等級門檻
	MinFarmDays       int // 雙方的農場至少建立幾天
	GiftSendPerDay    int // 每天最多送出幾份禮物
	GiftReceivePerDay int // 每天最多收到幾份禮物
	GiftMaxQuantity   int // 一份禮物最多幾個作物
	GiftMaxValue      int // 一份禮物的總價值上限（作物基本售價 × 數量）
	GiftRewardGold    int // 送禮時雙方各得的金幣
	// 幫忙拔草；設定鍵沿用 water_*（原本是幫忙澆水），已儲存的設定不會失效
	WeedSendPerDay int // 每天最多幫幾位玩家拔草
	WeedRewardGold int // 幫忙拔草時雙方各得的金幣
}

func defaults() featurecfg.Value {
	return featurecfg.Value{
		"min_level": 5, "min_farm_days": 3,
		"gift_send_per_day": 1, "gift_receive_per_day": 2, "gift_max_quantity": 10, "gift_max_value": 1500, "gift_reward_gold": 30,
		"water_send_per_day": 3, "water_reward_gold": 20,
	}
}

type field struct {
	key, label string
	low, high  int
	ptr        func(*Config) *int
}

var fields = []field{
	{"min_level", "等級門檻", 1, 100, func(c *Config) *int { return &c.MinLevel }},
	{"min_farm_days", "農場至少建立幾天", 0, 365, func(c *Config) *int { return &c.MinFarmDays }},
	{"gift_send_per_day", "每天最多送出幾份禮物", 0, 20, func(c *Config) *int { return &c.GiftSendPerDay }},
	{"gift_receive_per_day", "每天最多收到幾份禮物", 0, 20, func(c *Config) *int { return &c.GiftReceivePerDay }},
	{"gift_max_quantity", "一份禮物最多幾個作物", 1, 100, func(c *Config) *int { return &c.GiftMaxQuantity }},
	{"gift_max_value", "一份禮物的價值上限", 1, 1_000_000, func(c *Config) *int { return &c.GiftMaxValue }},
	{"gift_reward_gold", "送禮時雙方各得的金幣", 0, 10_000, func(c *Config) *int { return &c.GiftRewardGold }},
	{"water_send_per_day", "每天最多幫幾位玩家拔草", 0, 20, func(c *Config) *int { return &c.WeedSendPerDay }},
	{"water_reward_gold", "幫忙拔草時雙方各得的金幣", 0, 10_000, func(c *Config) *int { return &c.WeedRewardGold }},
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

// GiftLimit 這種作物一份最多可以送幾個（同時受數量與價值上限限制；售價高於價值上限時為 0）。
func GiftLimit(unitPrice int) int {
	c := Current()
	if unitPrice <= 0 {
		return c.GiftMaxQuantity
	}
	return min(c.GiftMaxQuantity, c.GiftMaxValue/unitPrice)
}

func init() {
	featurecfg.Register(ConfigKey, "送禮與互助", defaults, ValidateConfig)
}
