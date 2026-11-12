package mushroom

// 可在管理網站調整的地下菇房設定（featurecfg key = ConfigKey，存在 feature_settings，不需要 Migration）。
//
//   - 可調整：開放等級、每批生長時間、每床每批收成朵數、投入 0..MaxSawdust 個木屑時的稀有機率、
//     投入滿 MaxSawdust 個時是否保證一半稀有、各養分對應的作物。
//   - 固定：菇床數（圖片版面依 BedCount 繪製）、每床最多木屑數（機率表的長度）、菇的種類與售價。
//   - 改生長時間會立即套用到正在生長的菇床（成熟時間 = 開始時間 + 目前的生長時間）。

import (
	"fmt"
	"regexp"
	"sync"

	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

const ConfigKey = "mushroom"

var cropCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Config 目前生效的設定（唯讀）。
type Config struct {
	UnlockLevel    int
	GrowSeconds    int
	YieldPerBed    int
	RarePct        [MaxSawdust + 1]int
	FullGuarantee  bool                // 投入滿 MaxSawdust 個時保證至少一半稀有
	NutrientCrops  map[string][]string // 養分代碼 → 作物代碼
	cropToNutrient map[string]string
}

func defaultConfig() *Config {
	c := &Config{UnlockLevel: UnlockLevel, GrowSeconds: GrowSeconds, YieldPerBed: YieldPerBed, RarePct: RarePct,
		FullGuarantee: true, NutrientCrops: map[string][]string{}}
	for _, code := range NutrientOrder {
		c.NutrientCrops[code] = append([]string(nil), Nutrients[code].Crops...)
	}
	c.index()
	return c
}

func (c *Config) index() {
	c.cropToNutrient = map[string]string{}
	for _, code := range NutrientOrder {
		for _, crop := range c.NutrientCrops[code] {
			if _, dup := c.cropToNutrient[crop]; !dup {
				c.cropToNutrient[crop] = code
			}
		}
	}
}

var (
	cfgMu      sync.Mutex
	cfgVersion = -1
	cfgCache   *Config
	cfgDefault = defaultConfig()
)

// Current 目前生效的設定。
func Current() *Config {
	if featurecfg.IsDefault(ConfigKey) {
		return cfgDefault
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if v := featurecfg.Version(); cfgCache == nil || cfgVersion != v {
		c, err := parseConfig(featurecfg.Get(ConfigKey))
		if err != nil {
			c = cfgDefault
		}
		cfgCache, cfgVersion = c, v
	}
	return cfgCache
}

func encodeConfig(c *Config) featurecfg.Value {
	rare := make([]any, len(c.RarePct))
	for i, p := range c.RarePct {
		rare[i] = p
	}
	crops := map[string]any{}
	for _, code := range NutrientOrder {
		list := []any{}
		for _, crop := range c.NutrientCrops[code] {
			list = append(list, crop)
		}
		crops[code] = list
	}
	return featurecfg.Value{"unlock_level": c.UnlockLevel, "grow_seconds": c.GrowSeconds, "yield_per_bed": c.YieldPerBed,
		"rare_pct": rare, "full_guarantee": c.FullGuarantee, "nutrient_crops": crops}
}

func parseConfig(raw featurecfg.Value) (*Config, error) {
	c := &Config{NutrientCrops: map[string][]string{}}
	var err error
	if c.UnlockLevel, err = featurecfg.IntValue(raw["unlock_level"], "開放等級", 1, 100); err != nil {
		return nil, err
	}
	if c.GrowSeconds, err = featurecfg.IntValue(raw["grow_seconds"], "生長時間（秒）", 60, 7*24*3600); err != nil {
		return nil, err
	}
	if c.YieldPerBed, err = featurecfg.IntValue(raw["yield_per_bed"], "每床每批收成朵數", 1, 20); err != nil {
		return nil, err
	}
	rare, err := featurecfg.IntList(raw["rare_pct"], "稀有機率", 0, 100, MaxSawdust+1, MaxSawdust+1)
	if err != nil {
		return nil, err
	}
	copy(c.RarePct[:], rare)
	switch v := raw["full_guarantee"].(type) {
	case bool:
		c.FullGuarantee = v
	case nil:
		c.FullGuarantee = true
	default:
		return nil, gameerr.Validationf("「投入滿 %d 個保證一半稀有」必須是 true 或 false。", MaxSawdust)
	}
	crops, ok := featurecfg.AsMap(raw["nutrient_crops"])
	if !ok {
		return nil, gameerr.Validationf("養分對應的作物格式不正確。")
	}
	seen := map[string]string{}
	for _, code := range NutrientOrder {
		name := Nutrients[code].Name
		var list []any
		if crops[code] != nil {
			if list, ok = featurecfg.AsList(crops[code]); !ok || len(list) > 50 {
				return nil, gameerr.Validationf("%s的作物最多 50 種。", name)
			}
		}
		c.NutrientCrops[code] = []string{}
		for _, v := range list {
			crop, _ := v.(string)
			if !cropCodeRe.MatchString(crop) {
				return nil, gameerr.Validationf("%s的作物代碼「%v」格式不正確。", name, v)
			}
			if other, dup := seen[crop]; dup {
				return nil, gameerr.Validationf("作物「%s」同時出現在%s與%s。", crop, Nutrients[other].Name, name)
			}
			seen[crop] = code
			c.NutrientCrops[code] = append(c.NutrientCrops[code], crop)
		}
	}
	c.index()
	return c, nil
}

// ValidateConfig 驗證並正規化菇房設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	return encodeConfig(c), nil
}

func init() {
	featurecfg.Register(ConfigKey, "地下菇房", func() featurecfg.Value { return encodeConfig(defaultConfig()) }, ValidateConfig)
}

// Unlock 目前的開放等級。
func Unlock() int { return Current().UnlockLevel }

// Grow 目前每批的生長秒數。
func Grow() int { return Current().GrowSeconds }

// Yield 目前每床每批收成幾朵。
func Yield() int { return Current().YieldPerBed }

// GrowHoursText 生長時間的顯示文字（例如「24 小時」「1 小時 30 分鐘」）。
func GrowHoursText() string {
	s := Grow()
	h, m := s/3600, (s%3600)/60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d 小時 %d 分鐘", h, m)
	case h > 0:
		return fmt.Sprintf("%d 小時", h)
	default:
		return fmt.Sprintf("%d 分鐘", max(1, m))
	}
}
