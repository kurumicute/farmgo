// Package upgrades 設施升級與道具價格（Go 版新增，2026-11-21）。數值可在管理網站「設施與道具價格」調整
// （featurecfg key = ConfigKey），預設值與原本寫在程式裡的常數相同。
//
//   - 加工設施升級：升到第 1、2、3 級的價格（BuildingPrices）與各級縮短的加工時間 %（BuildingCutPct）。
//     farm_buildings.level 有 CHECK（0～3），所以最多 BuildingLevelLimit 級；降低等級數時，已超過的設施以新的最高等級計算。
//   - 蜂箱：各級價格（第 1 項是放置費）、突變加成、叮小偷機率（萬分比），搬家費，產蜜間隔、每次幾罐、存量上限。
//     等級存在 farm_tools.settings（沒有 CHECK），最多 BeehiveLevelLimit 級；降低等級數時同樣以新的最高等級計算。
//   - 道具、防守設施、建築的價格：沒有列在設定裡的代碼用 balance 的原始價格。
//     自動釣魚機、加速沙漏的價格在各自的設定（autofish、hourglass），不在這裡。
package upgrades

import (
	"fmt"
	"sync"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

const (
	ConfigKey          = "upgrades"
	BuildingLevelLimit = 3  // farm_buildings.level 的 CHECK 上限
	BeehiveLevelLimit  = 10 // 蜂箱最多幾級
	priceMax           = 1_000_000_000
)

// ItemCodes 可在這裡調整價格的道具（自動釣魚機、加速沙漏有自己的設定）。
func ItemCodes() []string {
	var out []string
	for _, code := range balance.ItemOrder {
		if code != balance.ToolAutoFisher && code != balance.ItemHourglass {
			out = append(out, code)
		}
	}
	return out
}

// Config 目前生效的設定（唯讀）。
type Config struct {
	BuildingPrices []int // 升到第 1、2…級的價格
	BuildingCutPct []int // 第 1、2…級縮短的加工時間 %

	BeehivePrices     []int // 第 1 項 = 放置費，第 2 項起 = 升到第 2、3…級的價格
	BeehiveMutationBP []int // 各級（第 1 級起）的突變加成（萬分比）
	BeehiveStingBP    []int // 各級的叮小偷機率（萬分比）
	BeehiveMoveFee    int
	BeehiveHours      int // 每幾小時產一次
	BeehiveHoney      int // 每次幾罐
	BeehiveCapacity   int // 存量上限

	ItemPrices     map[string]int
	DefensePrices  map[string]int
	BuildingBuilds map[string]int // 建造價格
}

func defaults() featurecfg.Value {
	items := map[string]int{}
	for _, code := range ItemCodes() {
		items[code] = balance.Items[code].Price
	}
	defenses := map[string]int{}
	for _, code := range balance.DefenseOrder {
		defenses[code] = balance.Defenses[code].Price
	}
	builds := map[string]int{}
	for _, code := range balance.BuildingOrder {
		builds[code] = balance.Buildings[code].Price
	}
	return featurecfg.Value{
		"building_prices":     []int{15_000, 50_000, 100_000},
		"building_cut_pct":    []int{15, 40, 60},
		"beehive_prices":      []int{8_000, 20_000, 50_000, 100_000, 200_000},
		"beehive_mutation_bp": []int{100, 150, 200, 250, 300},
		"beehive_sting_bp":    []int{1500, 2000, 2500, 3000, 3500},
		"beehive_move_fee":    500,
		"beehive_hours":       12,
		"beehive_honey":       2,
		"beehive_capacity":    10,
		"item_prices":         items,
		"defense_prices":      defenses,
		"building_build":      builds,
	}
}

// priceMap 讀「代碼 → 價格」；只接受 fallback 裡的代碼，沒列出的用 fallback。
func priceMap(raw any, label string, name func(string) string, fallback map[string]int) (map[string]int, error) {
	m, ok := featurecfg.AsMap(raw)
	if raw != nil && !ok {
		return nil, gameerr.Validationf("%s格式不正確。", label)
	}
	out := map[string]int{}
	for k, v := range fallback {
		out[k] = v
	}
	for k, v := range m {
		if _, known := fallback[k]; !known {
			return nil, gameerr.Validationf("%s沒有「%s」這個項目。", label, k)
		}
		n, err := featurecfg.IntValue(v, fmt.Sprintf("%s的價格", name(k)), 1, priceMax)
		if err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, nil
}

func parse(raw featurecfg.Value) (*Config, error) {
	def := defaults()
	merged := featurecfg.Value{}
	for k, v := range def {
		merged[k] = v
	}
	for k, v := range raw { // 之後新增的設定：之前存的快照沒有時用預設值
		merged[k] = v
	}
	c := &Config{}
	var err error
	if c.BuildingPrices, err = featurecfg.IntList(merged["building_prices"], "加工設施升級價格", 1, priceMax, 1, BuildingLevelLimit); err != nil {
		return nil, err
	}
	if c.BuildingCutPct, err = featurecfg.IntList(merged["building_cut_pct"], "加工設施各級縮短的加工時間", 0, 90, 1, BuildingLevelLimit); err != nil {
		return nil, err
	}
	if len(c.BuildingCutPct) != len(c.BuildingPrices) {
		return nil, gameerr.Validationf("加工設施的升級價格有 %d 級、縮短時間有 %d 級，數量要一樣。", len(c.BuildingPrices), len(c.BuildingCutPct))
	}
	if c.BeehivePrices, err = featurecfg.IntList(merged["beehive_prices"], "蜂箱各級價格", 1, priceMax, 1, BeehiveLevelLimit); err != nil {
		return nil, err
	}
	if c.BeehiveMutationBP, err = featurecfg.IntList(merged["beehive_mutation_bp"], "蜂箱各級的突變加成", 0, 5_000, 1, BeehiveLevelLimit); err != nil {
		return nil, err
	}
	if c.BeehiveStingBP, err = featurecfg.IntList(merged["beehive_sting_bp"], "蜂箱各級的叮小偷機率", 0, 10_000, 1, BeehiveLevelLimit); err != nil {
		return nil, err
	}
	if n := len(c.BeehivePrices); len(c.BeehiveMutationBP) != n || len(c.BeehiveStingBP) != n {
		return nil, gameerr.Validationf("蜂箱的價格、突變加成、叮小偷機率的級數要一樣（目前 %d／%d／%d 級）。",
			n, len(c.BeehiveMutationBP), len(c.BeehiveStingBP))
	}
	for _, f := range []struct {
		key, label string
		low, high  int
		ptr        *int
	}{
		{"beehive_move_fee", "蜂箱搬家費", 0, priceMax, &c.BeehiveMoveFee},
		{"beehive_hours", "蜂箱每幾小時產一次蜂蜜", 1, 168, &c.BeehiveHours},
		{"beehive_honey", "蜂箱每次產幾罐蜂蜜", 1, 100, &c.BeehiveHoney},
		{"beehive_capacity", "蜂箱最多存幾罐蜂蜜", 1, 1_000, &c.BeehiveCapacity},
	} {
		if *f.ptr, err = featurecfg.IntValue(merged[f.key], f.label, f.low, f.high); err != nil {
			return nil, err
		}
	}
	defItems, _ := def["item_prices"].(map[string]int)
	if c.ItemPrices, err = priceMap(merged["item_prices"], "道具價格",
		func(k string) string { return balance.Items[k].Name }, defItems); err != nil {
		return nil, err
	}
	defDef, _ := def["defense_prices"].(map[string]int)
	if c.DefensePrices, err = priceMap(merged["defense_prices"], "防守設施價格",
		func(k string) string { return balance.Defenses[k].Name }, defDef); err != nil {
		return nil, err
	}
	defBuild, _ := def["building_build"].(map[string]int)
	if c.BuildingBuilds, err = priceMap(merged["building_build"], "建築價格",
		func(k string) string { return balance.Buildings[k].Name }, defBuild); err != nil {
		return nil, err
	}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	return featurecfg.Value{
		"building_prices": c.BuildingPrices, "building_cut_pct": c.BuildingCutPct,
		"beehive_prices": c.BeehivePrices, "beehive_mutation_bp": c.BeehiveMutationBP, "beehive_sting_bp": c.BeehiveStingBP,
		"beehive_move_fee": c.BeehiveMoveFee, "beehive_hours": c.BeehiveHours, "beehive_honey": c.BeehiveHoney,
		"beehive_capacity": c.BeehiveCapacity,
		"item_prices":      c.ItemPrices, "defense_prices": c.DefensePrices, "building_build": c.BuildingBuilds,
	}, nil
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

// ---------- 加工設施升級 ----------

// BuildingMaxLevel 加工設施最高等級。
func BuildingMaxLevel() int { return len(Current().BuildingPrices) }

// BuildingUpgradePrice 從 level 升到下一級的價格；已滿級時是 0（呼叫端先用 BuildingMaxLevel 判斷）。
func BuildingUpgradePrice(level int) int {
	p := Current().BuildingPrices
	if level < 0 || level >= len(p) {
		return 0
	}
	return p[level]
}

// BuildingCutPct 這個等級縮短的加工時間 %（0 級 = 0；超過目前最高等級時以最高等級計算）。
func BuildingCutPct(level int) int {
	cut := Current().BuildingCutPct
	level = min(level, len(cut))
	if level <= 0 {
		return 0
	}
	return cut[level-1]
}

// CraftSeconds 依設施等級縮短後的加工秒數（無條件進位，至少 1 秒）。
func CraftSeconds(seconds, level int) int {
	if seconds <= 0 {
		return seconds
	}
	pct := BuildingCutPct(level)
	return max(1, (seconds*(100-pct)+99)/100)
}

func init() {
	featurecfg.Register(ConfigKey, "設施與道具價格", defaults, ValidateConfig)
	for _, code := range ItemCodes() {
		code := code
		balance.RegisterPrice(code, func() int {
			if p, ok := Current().ItemPrices[code]; ok {
				return p
			}
			return balance.Items[code].Price
		})
	}
	balance.SetPriceHooks(
		func(code string) (int, bool) { p, ok := Current().DefensePrices[code]; return p, ok },
		func(code string) (int, bool) { p, ok := Current().BuildingBuilds[code]; return p, ok },
	)
}
