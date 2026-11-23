// Package pets 寵物（貓、狗）：規則與數值，不碰資料庫（對應 Python app/game/pets.py）。
//
//   - 購買後是 Lv0（出現在農場圖的左上／右上），加成 base_pct%；之後升級，每級再 +pct_per_level%（預設 Lv4 = 25%）。
//   - 價格與加成可在管理網站調整（featurecfg 的 "pets"，2026-11-22 起每種寵物分開設定），一律經 Of(code)／BuyPrice(code)／BonusPct(code, level) 讀取。
//   - 貓：趕走偷吃作物的老鼠，主人收成時收成數量 +X%（小數部分依機率多 1 個，見 BonusQuantity）。
//   - 狗：2026-11-23 起（Go 版，使用者要求）山洞探險時間 -X%（最多 DogExploreMaxPct%，古代遺跡除外，見 ExploreSeconds）。
//     原本（同 Python）是防守設施都沒擋住時再判定一次 X% 擋下偷菜（blocked_by = DogBlocker），防守設施與蜂箱夠多後幾乎沒用。
package pets

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

const (
	Cat        = "cat"
	Dog        = "dog"      // 2026-11-23 起：山洞探險時間 -X%（原本是防守設施沒擋住時再擋一次偷菜）
	Rabbit     = "rabbit"   // Go 版新增（2026-11-18，Migration 0045 放寬 pet_code）：菇房收成數量 +X%
	Bird       = "bird"     // Go 版新增：釣魚稀有魚機率提高
	Hedgehog   = "hedgehog" // Go 版新增：收成時 X% 機率品質提升一級（2026-11-22 起；原本是清除乾枯作物時救回 X%）
	DogBlocker = "pet_dog"  // steal_records.blocked_by（2026-11-22 以前狗擋下的紀錄；之後狗不再擋偷菜）
	LevelLimit = 10         // farm_pets.level 的 CHECK 上限：升級價格最多 10 個
	ConfigKey  = "pets"
)

// 2026-11-22 使用者要求：每種寵物分開設定（per_pet：寵物代碼 → 4 個數值）。最上層的 4 個數值是沒有個別設定時的預設，
// 所以之前存的快照（只有最上層）照常可用：每種寵物都套用最上層的數值。
func defaults() featurecfg.Value {
	return featurecfg.Value{
		"buy_price":      5_000,
		"upgrade_prices": []int{10_000, 20_000, 30_000, 40_000}, // Lv0→1、1→2、2→3、3→4
		"base_pct":       5,                                     // Lv0 的加成
		"pct_per_level":  5,                                     // 每升一級再加
	}
}

// Settings 一種寵物的數值。
type Settings struct {
	BuyPrice      int
	UpgradePrices []int // Lv0→1、1→2…
	BasePct       int   // Lv0 的加成
	PctPerLevel   int   // 每升一級再加
}

func (c Settings) value() featurecfg.Value {
	return featurecfg.Value{"buy_price": c.BuyPrice, "upgrade_prices": c.UpgradePrices, "base_pct": c.BasePct, "pct_per_level": c.PctPerLevel}
}

// parseSettings 驗證一組數值；label 放在錯誤訊息前面（例如「貓咪的」）。缺少的欄位用 fallback。
func parseSettings(raw featurecfg.Value, fallback featurecfg.Value, label string) (Settings, error) {
	get := func(k string) any {
		if v, ok := raw[k]; ok && v != nil {
			return v
		}
		return fallback[k]
	}
	var c Settings
	var err error
	if c.BuyPrice, err = featurecfg.IntValue(get("buy_price"), label+"領養價格", 1, 100_000_000); err != nil {
		return c, err
	}
	if c.UpgradePrices, err = featurecfg.IntList(get("upgrade_prices"), label+"升級價格", 1, 100_000_000, 1, LevelLimit); err != nil {
		return c, err
	}
	if c.BasePct, err = featurecfg.IntValue(get("base_pct"), label+"Lv0 加成", 0, 100); err != nil {
		return c, err
	}
	if c.PctPerLevel, err = featurecfg.IntValue(get("pct_per_level"), label+"每級加成", 0, 100); err != nil {
		return c, err
	}
	if top := c.BasePct + len(c.UpgradePrices)*c.PctPerLevel; top > 100 {
		return c, gameerr.Validationf("%s最高等級加成為 %d%%，不可超過 100%%。", label, top)
	}
	return c, nil
}

// parse 回傳最上層的預設與每種寵物的數值。
func parse(raw featurecfg.Value) (Settings, map[string]Settings, error) {
	shared, err := parseSettings(raw, defaults(), "")
	if err != nil {
		return Settings{}, nil, err
	}
	per := map[string]Settings{}
	m, ok := featurecfg.AsMap(raw["per_pet"])
	if raw["per_pet"] != nil && !ok {
		return Settings{}, nil, gameerr.Validationf("每種寵物的設定格式不正確。")
	}
	for k := range m {
		if _, known := Pets[k]; !known {
			return Settings{}, nil, gameerr.Validationf("沒有「%s」這種寵物。", k)
		}
	}
	for _, code := range PetOrder {
		sub, _ := featurecfg.AsMap(m[code])
		if per[code], err = parseSettings(sub, shared.value(), Pets[code].Name+"的"); err != nil {
			return Settings{}, nil, err
		}
	}
	return shared, per, nil
}

// ValidateConfig 驗證並正規化寵物設定（輸出一律包含每種寵物的完整數值）。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	shared, per, err := parse(raw)
	if err != nil {
		return nil, err
	}
	out := shared.value()
	pm := map[string]any{}
	for code, c := range per {
		pm[code] = map[string]any(c.value())
	}
	out["per_pet"] = pm
	return out, nil
}

func init() {
	featurecfg.Register(ConfigKey, "寵物", defaults, ValidateConfig)
}

func Config() featurecfg.Value { return featurecfg.Get(ConfigKey) }

var (
	cfgMu      sync.Mutex
	cfgVersion = -1
	cfgCache   map[string]Settings
)

// Of 這種寵物目前的數值（未知的代碼用最上層的預設）。
func Of(code string) Settings {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if v := featurecfg.Version(); cfgCache == nil || cfgVersion != v {
		shared, per, err := parse(Config())
		if err != nil {
			shared, per, _ = parse(defaults())
		}
		per[""] = shared
		cfgCache, cfgVersion = per, v
	}
	if c, ok := cfgCache[code]; ok {
		return c
	}
	return cfgCache[""]
}

func BuyPrice(code string) int        { return Of(code).BuyPrice }
func UpgradePrices(code string) []int { return Of(code).UpgradePrices }
func MaxLevel(code string) int        { return len(Of(code).UpgradePrices) }
func BasePct(code string) int         { return Of(code).BasePct }
func PctPerLevel(code string) int     { return Of(code).PctPerLevel }

// Pet 寵物定義。
type Pet struct {
	Code   string
	Name   string
	Emoji  string
	Role   string // 簡介
	Effect string // 效果說明（{pct} 代入目前加成）
}

var PetOrder = []string{Cat, Dog, Rabbit, Bird, Hedgehog}

var Pets = map[string]Pet{
	Cat:      {Cat, "貓咪", "🐱", "抓老鼠：趕走偷吃農作物的老鼠", "收成數量 +{pct}%"},
	Dog:      {Dog, "狗狗", "🐶", "帶路探險：熟悉山洞裡的路，探險少繞遠路", "山洞探險時間 -{pct}%（最多 -50%，古代遺跡除外）"},
	Rabbit:   {Rabbit, "兔子", "🐰", "照顧菇床：幫忙翻動木屑、照看地下菇房", "地下菇房每個菇床收成數量 +{pct}%"},
	Bird:     {Bird, "小鳥", "🐦", "找魚群：在水面上空盤旋，帶你找到好魚", "釣魚（含自動釣魚機）稀有以上的魚機率提高（{pct}% 加成）"},
	Hedgehog: {Hedgehog, "刺蝟", "🦔", "吃害蟲：把啃食作物的害蟲吃掉，作物長得更好", "收成時每格有 {pct}% 機率品質提升一級（最高傳說）"},
}

// BirdFishBP 小鳥的釣魚加成（萬分比，從普通魚扣除）：加成 pct% 時稀有 +pct×10、史詩 +pct×4、傳說 +pct。
func BirdFishBP(pct int) map[string]int {
	if pct <= 0 {
		return nil
	}
	return map[string]int{"rare": pct * 10, "epic": pct * 4, "legendary": pct}
}

// UpgradeQuality 刺蝟（2026-11-22 使用者要求，取代清除乾枯作物時救回）：u < pct% 時品質提升一級；
// 已是最高品質或沒中時原樣回傳、ok = false。
func UpgradeQuality(quality string, pct int, u float64) (string, bool) {
	if pct <= 0 || u >= float64(pct)/100 {
		return quality, false
	}
	if quality == "" {
		quality = "normal"
	}
	for i, q := range balance.QualityOrder[:len(balance.QualityOrder)-1] {
		if q == quality {
			return balance.QualityOrder[i+1], true
		}
	}
	return quality, false
}

// ItemCode 交易紀錄的 item_code（購買與升級都記 buy_item）。
func ItemCode(petCode string) string { return "pet_" + petCode }

// BonusPct level 為 nil 表示沒有這隻寵物。
func BonusPct(code string, level *int) int {
	if level == nil {
		return 0
	}
	c := Of(code)
	return c.BasePct + max(0, min(*level, len(c.UpgradePrices)))*c.PctPerLevel
}

// UpgradePrice 從目前等級升到下一級的價格；已滿級時 ok = false。
func UpgradePrice(code string, level int) (price int, ok bool) {
	prices := UpgradePrices(code)
	if level >= 0 && level < len(prices) {
		return prices[level], true
	}
	return 0, false
}

func EffectText(code string, level *int) string {
	return strings.ReplaceAll(Pets[code].Effect, "{pct}", strconv.Itoa(BonusPct(code, level)))
}

// BonusQuantity 貓的收成加成：quantity × pct%，整數部分一定給，小數部分以機率 u ∈ [0, 1) 決定是否多 1 個。
func BonusQuantity(quantity, pct int, u float64) int {
	if quantity <= 0 || pct <= 0 {
		return 0
	}
	extra := float64(quantity) * float64(pct) / 100
	whole := int(extra)
	if u < extra-float64(whole) {
		return whole + 1
	}
	return whole
}

// OddsText 貓咪對「每格收成 quantity 個」的實際效果（與 BonusQuantity 相同算法）。
func OddsText(quantity, pct int) string {
	extra := float64(quantity) * float64(pct) / 100
	sure := int(extra)
	chance := numfmt.RoundN((extra-float64(sure))*100, 1)
	chanceText := strconv.FormatFloat(chance, 'g', -1, 64) + "%" // Python f"{chance:g}"
	switch {
	case sure != 0 && chance != 0:
		return fmt.Sprintf("固定 +%d，另有 %s 機率再 +1", sure, chanceText)
	case sure != 0:
		return fmt.Sprintf("固定 +%d", sure)
	case chance != 0:
		return chanceText + " 機率 +1"
	}
	return "不會多"
}

// BlockerName 偷菜被阻止時的阻止者名稱（防守設施或狗狗）。
func BlockerName(code string) string {
	if code == DogBlocker {
		return Pets[Dog].Name
	}
	if code == "beehive" { // beehive.Blocker（蜂箱，2026-11-20；不 import beehive，避免循環）
		return "蜂箱"
	}
	return balance.DefenseOf(code).Name
}

// DogExploreMaxPct 狗狗縮短探險時間的上限（%）。
const DogExploreMaxPct = 50

// ExploreSeconds 狗狗（2026-11-23 使用者要求，取代多擋偷菜）：探險時間 × (100 − pct)%，pct 最多 DogExploreMaxPct，捨去。
func ExploreSeconds(base, pct int) int {
	pct = max(0, min(pct, DogExploreMaxPct))
	return base * (100 - pct) / 100
}
