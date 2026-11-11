// Package pets 寵物（貓、狗）：規則與數值，不碰資料庫（對應 Python app/game/pets.py）。
//
//   - 購買後是 Lv0（出現在農場圖的左上／右上），加成 base_pct%；之後升級，每級再 +pct_per_level%（預設 Lv4 = 25%）。
//   - 價格與加成可在管理網站調整（featurecfg 的 "pets"），一律經 BuyPrice()／UpgradePrices()／BonusPct() 讀取。
//   - 貓：趕走偷吃作物的老鼠，主人收成時收成數量 +X%（小數部分依機率多 1 個，見 BonusQuantity）。
//   - 狗：趕走偷菜的小偷。防守設施（稻草人、警戒犬、防盜圍欄）都沒擋住時，狗再判定一次 X%；
//     不受防守設施 60% 整體上限限制。擋下時偷菜紀錄的 blocked_by = DogBlocker。
package pets

import (
	"fmt"
	"strconv"
	"strings"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

const (
	Cat        = "cat"
	Dog        = "dog"
	DogBlocker = "pet_dog" // steal_records.blocked_by（狗擋下時）
	LevelLimit = 10        // farm_pets.level 的 CHECK 上限：升級價格最多 10 個
	ConfigKey  = "pets"
)

func defaults() featurecfg.Value {
	return featurecfg.Value{
		"buy_price":      5_000,
		"upgrade_prices": []int{10_000, 20_000, 30_000, 40_000}, // Lv0→1、1→2、2→3、3→4
		"base_pct":       5,                                     // Lv0 的加成
		"pct_per_level":  5,                                     // 每升一級再加
	}
}

// ValidateConfig 驗證並正規化寵物設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	buy, err := featurecfg.IntValue(raw["buy_price"], "領養價格", 1, 100_000_000)
	if err != nil {
		return nil, err
	}
	prices, err := featurecfg.IntList(raw["upgrade_prices"], "升級價格", 1, 100_000_000, 1, LevelLimit)
	if err != nil {
		return nil, err
	}
	base, err := featurecfg.IntValue(raw["base_pct"], "Lv0 加成", 0, 100)
	if err != nil {
		return nil, err
	}
	per, err := featurecfg.IntValue(raw["pct_per_level"], "每級加成", 0, 100)
	if err != nil {
		return nil, err
	}
	top := base + len(prices)*per
	if top > 100 {
		return nil, gameerr.Validationf("最高等級的加成為 %d%%，不可超過 100%%。", top)
	}
	return featurecfg.Value{"buy_price": buy, "upgrade_prices": prices, "base_pct": base, "pct_per_level": per}, nil
}

func init() {
	featurecfg.Register(ConfigKey, "寵物", defaults, ValidateConfig)
}

func Config() featurecfg.Value { return featurecfg.Get(ConfigKey) }

func intOf(key string) int {
	n, _ := featurecfg.AsInt(Config()[key])
	return n
}

func BuyPrice() int { return intOf("buy_price") }

func UpgradePrices() []int {
	switch v := Config()["upgrade_prices"].(type) {
	case []int:
		return v
	default:
		list, _ := featurecfg.AsList(v)
		out := make([]int, 0, len(list))
		for _, x := range list {
			n, _ := featurecfg.AsInt(x)
			out = append(out, n)
		}
		return out
	}
}

func MaxLevel() int    { return len(UpgradePrices()) }
func BasePct() int     { return intOf("base_pct") }
func PctPerLevel() int { return intOf("pct_per_level") }

// Pet 寵物定義。
type Pet struct {
	Code   string
	Name   string
	Emoji  string
	Role   string // 簡介
	Effect string // 效果說明（{pct} 代入目前加成）
}

var PetOrder = []string{Cat, Dog}

var Pets = map[string]Pet{
	Cat: {Cat, "貓咪", "🐱", "抓老鼠：趕走偷吃農作物的老鼠", "收成數量 +{pct}%"},
	Dog: {Dog, "狗狗", "🐶", "保衛農田：趕走來偷菜的小偷", "防守設施沒擋住時，再有 {pct}% 機率擋下偷菜（所有農田）"},
}

// ItemCode 交易紀錄的 item_code（購買與升級都記 buy_item）。
func ItemCode(petCode string) string { return "pet_" + petCode }

// BonusPct level 為 nil 表示沒有這隻寵物。
func BonusPct(level *int) int {
	if level == nil {
		return 0
	}
	return BasePct() + max(0, min(*level, MaxLevel()))*PctPerLevel()
}

// UpgradePrice 從目前等級升到下一級的價格；已滿級時 ok = false。
func UpgradePrice(level int) (price int, ok bool) {
	prices := UpgradePrices()
	if level >= 0 && level < len(prices) {
		return prices[level], true
	}
	return 0, false
}

func EffectText(code string, level *int) string {
	return strings.ReplaceAll(Pets[code].Effect, "{pct}", strconv.Itoa(BonusPct(level)))
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
	return balance.Defenses[code].Name
}
