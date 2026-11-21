// Package expansion 改名與後段擴充的價格（Go 版新增，2026-11-19，金幣回收第二波）。數值可在管理網站「改名與擴充」調整
// （featurecfg key = ConfigKey）。
//
//   - 改名逐次加價：第 1 次免費（RenameFirstFree），之後 RenameBase × 2^(級數-1)，上限 RenameMax；
//     每 RenameDecayDays 天沒有改名降一級（最低 1 級，不會再回到免費）。次數由交易紀錄（rename_farm）計算。
//   - 土地第 17 格以上（第二塊農田）：前 16 格照 balance.UpgradePrices；之後第 n 格 = LandBase × (LandGrowthPct/100)^(n-17)，
//     最多 LandMax 格（16～32；農田圖第二塊也是 4×4）。
//   - 倉庫後段：300 格以內照 balance.WarehouseExpansionPrices；之後每次 +50 格，價格 WarehouseLateBase × (WarehouseLateGrowthPct/100)^k，
//     最多 WarehouseMax 格。
//   - 2026-11-21 起前段也可以調整：土地第 5～16 格的價格（LandPrices，預設 balance.UpgradePrices）、
//     倉庫 300 格以內各次的價格（WarehousePrices，預設 balance.WarehouseExpansionPrices）與每次擴充的格數（WarehouseStep）。
//     清單比級數短時，之後都用最後一項的價格。
//   - 素材背包擴充（farms.material_bag_level，Migration 0046）：每級 +MaterialBagStep 格（基本容量見 exploration 設定），
//     第 1 級 MaterialBagBase，之後每級 ×(MaterialBagGrowthPct/100)，最多 MaterialBagLevels 級（資料庫上限 10）。
package expansion

import (
	"math"
	"slices"
	"sync"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/numfmt"
)

const ConfigKey = "expansion"

// MaxLandLimit 土地上限的上限：第二塊農田最多 16 格（農田圖 4×4）。
const MaxLandLimit = balance.MaxPlots * 2

// Config 目前生效的設定（唯讀）。
type Config struct {
	RenameFirstFree bool
	RenameBase      int
	RenameMax       int
	RenameDecayDays int

	LandMax       int
	LandBase      int
	LandGrowthPct int

	WarehouseMax           int
	WarehouseLateBase      int
	WarehouseLateGrowthPct int

	LandPrices      []int // 第 5～16 格（初始 4 格之後）每格的價格
	WarehousePrices []int // 倉庫 300 格以內每次擴充的價格
	WarehouseStep   int   // 倉庫每次擴充幾格

	MaterialBagLevels    int
	MaterialBagStep      int
	MaterialBagBase      int
	MaterialBagGrowthPct int
}

// MaxMaterialBagLevel farms.material_bag_level 的 CHECK 上限。
const MaxMaterialBagLevel = 10

func defaults() featurecfg.Value {
	return featurecfg.Value{
		"rename_first_free": 1, "rename_base": 1_000, "rename_max": 100_000, "rename_decay_days": 30,
		"land_max": 32, "land_base": 300_000, "land_growth_pct": 130,
		"warehouse_max": 600, "warehouse_late_base": 400_000, "warehouse_late_growth_pct": 160,
		"land_prices": slices.Clone(balance.UpgradePrices), "warehouse_prices": slices.Clone(balance.WarehouseExpansionPrices),
		"warehouse_step":      balance.WarehouseExpansionStep,
		"material_bag_levels": 10, "material_bag_step": 200, "material_bag_base": 50_000, "material_bag_growth_pct": 180,
	}
}

type field struct {
	key, label string
	low, high  int
	ptr        func(*Config) *int
}

var fields = []field{
	{"rename_base", "改名第 2 次的價格", 1, 100_000_000, func(c *Config) *int { return &c.RenameBase }},
	{"rename_max", "改名價格上限", 1, 1_000_000_000, func(c *Config) *int { return &c.RenameMax }},
	{"rename_decay_days", "幾天沒改名降一級", 1, 365, func(c *Config) *int { return &c.RenameDecayDays }},
	{"land_max", "土地上限（格）", balance.MaxPlots, MaxLandLimit, func(c *Config) *int { return &c.LandMax }},
	{"land_base", "第 17 格的價格", 1, 1_000_000_000, func(c *Config) *int { return &c.LandBase }},
	{"land_growth_pct", "第 17 格以後每格的倍率（%）", 100, 1000, func(c *Config) *int { return &c.LandGrowthPct }},
	{"warehouse_step", "倉庫每次擴充幾格", 1, 1_000, func(c *Config) *int { return &c.WarehouseStep }},
	{"warehouse_max", "倉庫上限（格）", balance.WarehouseMaxCapacity, 5_000, func(c *Config) *int { return &c.WarehouseMax }},
	{"warehouse_late_base", "倉庫超過 300 格後第 1 次擴充的價格", 1, 1_000_000_000, func(c *Config) *int { return &c.WarehouseLateBase }},
	{"warehouse_late_growth_pct", "倉庫後段每次的倍率（%）", 100, 1000, func(c *Config) *int { return &c.WarehouseLateGrowthPct }},
	{"material_bag_levels", "素材背包可以擴充幾級", 0, MaxMaterialBagLevel, func(c *Config) *int { return &c.MaterialBagLevels }},
	{"material_bag_step", "素材背包每級增加幾格", 1, 10_000, func(c *Config) *int { return &c.MaterialBagStep }},
	{"material_bag_base", "素材背包第 1 級的價格", 1, 1_000_000_000, func(c *Config) *int { return &c.MaterialBagBase }},
	{"material_bag_growth_pct", "素材背包每級的倍率（%）", 100, 1000, func(c *Config) *int { return &c.MaterialBagGrowthPct }},
}

func parse(raw featurecfg.Value) (*Config, error) {
	c := &Config{}
	merged := defaults() // 之後新增的設定：之前存的快照沒有時用預設值（不改到傳入的 raw）
	for k, v := range raw {
		merged[k] = v
	}
	raw = merged
	free, err := featurecfg.IntValue(raw["rename_first_free"], "第 1 次改名免費（0／1）", 0, 1)
	if err != nil {
		return nil, err
	}
	c.RenameFirstFree = free == 1
	for _, f := range fields {
		n, err := featurecfg.IntValue(raw[f.key], f.label, f.low, f.high)
		if err != nil {
			return nil, err
		}
		*f.ptr(c) = n
	}
	var err2 error
	if c.LandPrices, err2 = featurecfg.IntList(raw["land_prices"], "土地第 5～16 格的價格", 1, 1_000_000_000, 1, balance.MaxPlots-balance.InitialPlots); err2 != nil {
		return nil, err2
	}
	if c.WarehousePrices, err2 = featurecfg.IntList(raw["warehouse_prices"], "倉庫 300 格以內各次擴充的價格", 1, 1_000_000_000, 1, 50); err2 != nil {
		return nil, err2
	}
	if c.RenameMax < c.RenameBase {
		c.RenameMax = c.RenameBase
	}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	free := 0
	if c.RenameFirstFree {
		free = 1
	}
	out := featurecfg.Value{"rename_first_free": free, "land_prices": c.LandPrices, "warehouse_prices": c.WarehousePrices}
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

// grow base × (pct/100)^k，四捨五入到百位，超過 int 上限時截在 1e12。
func grow(base, pct, k int) int {
	v := float64(base) * math.Pow(float64(pct)/100, float64(k))
	if v > 1e12 {
		return 1e12
	}
	return int(math.Round(v/100) * 100)
}

// RenameLevel 依過去的改名時間（由舊到新）計算下一次的級數：0 = 免費，1 = RenameBase，之後每級 ×2。
func RenameLevel(times []time.Time, now time.Time) int {
	c := Current()
	decay := func(from, to time.Time) int {
		return int(to.Sub(from) / (time.Duration(c.RenameDecayDays) * 24 * time.Hour))
	}
	level := 0
	if !c.RenameFirstFree {
		level = 1
	}
	for i, at := range times {
		if i > 0 {
			level = max(1, level-decay(times[i-1], at))
		}
		level++
	}
	if len(times) > 0 {
		level = max(1, level-decay(times[len(times)-1], now))
	}
	return level
}

// RenamePrice 這個級數的改名價格。
func RenamePrice(level int) int {
	if level <= 0 {
		return 0
	}
	c := Current()
	if level > 40 {
		return c.RenameMax
	}
	return min(c.RenameMax, c.RenameBase<<(level-1))
}

// MaxPlots 目前設定的土地上限。
func MaxPlots() int { return Current().LandMax }

// LandPrice 目前有 plots 格時，下一次擴建的價格；已達上限時 ok = false。
func LandPrice(plots int) (price int, ok bool) {
	c := Current()
	if plots >= c.LandMax {
		return 0, false
	}
	if plots < balance.MaxPlots {
		tier := max(0, plots-balance.InitialPlots)
		return c.LandPrices[min(tier, len(c.LandPrices)-1)], true
	}
	return grow(c.LandBase, c.LandGrowthPct, plots-balance.MaxPlots), true
}

// WarehouseMax 目前設定的倉庫上限。
func WarehouseMax() int { return Current().WarehouseMax }

// WarehousePrice 下一次擴大倉庫的價格；已達上限時 ok = false。
func WarehousePrice(capacity int) (price int, ok bool) {
	c := Current()
	if capacity >= c.WarehouseMax {
		return 0, false
	}
	if capacity < balance.WarehouseMaxCapacity {
		tier := max(0, numfmt.FloorDiv(capacity-balance.InitialWarehouseCapacity, c.WarehouseStep))
		return c.WarehousePrices[min(tier, len(c.WarehousePrices)-1)], true
	}
	k := (capacity - balance.WarehouseMaxCapacity) / c.WarehouseStep
	return grow(c.WarehouseLateBase, c.WarehouseLateGrowthPct, k), true
}

// WarehouseStep 倉庫每次擴充幾格。
func WarehouseStep() int { return Current().WarehouseStep }

// MaterialBagCapacity 素材背包容量：base（exploration 設定的基本容量）+ 等級 × 每級格數（等級超過設定時以設定為準）。
func MaterialBagCapacity(level, base int) int {
	c := Current()
	return base + min(max(level, 0), c.MaterialBagLevels)*c.MaterialBagStep
}

// MaterialBagPrice 從目前等級升一級的價格；已滿級時 ok = false。
func MaterialBagPrice(level int) (price int, ok bool) {
	c := Current()
	if level < 0 || level >= c.MaterialBagLevels {
		return 0, false
	}
	return grow(c.MaterialBagBase, c.MaterialBagGrowthPct, level), true
}

func init() {
	featurecfg.Register(ConfigKey, "改名與擴充", defaults, ValidateConfig)
}
