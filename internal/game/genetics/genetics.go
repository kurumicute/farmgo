// Package genetics 作物品質與突變的純規則：設定驗證、機率加成、抽取、售價與生長時間（對應 Python app/game/genetics.py）。
//
// 所有公式只在這裡定義，其他模組一律呼叫這些函式，避免重複套用倍率。
//   - 機率以萬分比（bp）整數表示，總和固定為 10000。
//   - 倍率以百分比整數表示；售價計算全程使用整數，最後無條件捨去（floor）到整數金幣。
package genetics

import (
	"fmt"
	"strings"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
)

// QualityOrder normal, good, rare, legendary（與 balance.QualityOrder 相同）。
var QualityOrder = balance.QualityOrder

// MutationOrder 突變判定順序（不含 none）。
var MutationOrder = []string{"giant", "golden", "fast", "variant"}

const TotalBP = 10_000

// 設定的合法範圍
var (
	QualityPriceRange  = balance.Range{Low: 50, High: 1000}
	MutationBPRange    = balance.Range{Low: 0, High: 2000}
	GiantRange         = balance.Range{Low: 1, High: 5}
	GoldenPctRange     = balance.Range{Low: 100, High: 1000}
	FastReductionRange = balance.Range{Low: 0, High: 7 * 24 * 3600}
	VariantPctRange    = balance.Range{Low: 100, High: 2000}
	LevelBonusRange    = balance.Range{Low: 0, High: 200}
	LevelBonusMaxRange = balance.Range{Low: 0, High: 2000}
)

const MutationTotalMaxBP = 5000

// pyG 等同 Python f"{x:g}"。
func pyG(x float64) string { return fmt.Sprintf("%.6g", x) }

func checkInt(value any, label string, r balance.Range, errs *[]string) int {
	n, ok := featurecfg.AsInt(value)
	if !ok {
		*errs = append(*errs, fmt.Sprintf("%s必須是整數。", label))
		return 0
	}
	if n < r.Low || n > r.High {
		*errs = append(*errs, fmt.Sprintf("%s必須在 %d–%d 之間（目前 %d）。", label, r.Low, r.High, n))
	}
	return n
}

func subMap(raw map[string]any, key string) map[string]any {
	if m, ok := featurecfg.AsMap(raw[key]); ok {
		return m
	}
	return map[string]any{}
}

// ValidateSettings 驗證並回傳正規化的設定；任何錯誤都會一次列出並拒絕套用。
// raw 為 JSON 形狀的設定（整數可為 int 或 json.Number；浮點數視為錯誤）。
func ValidateSettings(raw map[string]any) (balance.GeneticsSettings, error) {
	if raw == nil {
		return balance.GeneticsSettings{}, gameerr.Validationf("設定格式不正確。")
	}
	var errs []string
	qualityBP := subMap(raw, "quality_bp")
	qualityPct := subMap(raw, "quality_price_pct")
	mutationBP := subMap(raw, "mutation_bp")
	s := balance.GeneticsSettings{
		QualityBP:       map[string]int{},
		QualityPricePct: map[string]int{},
		MutationBP:      map[string]int{},
	}
	for _, q := range QualityOrder {
		s.QualityBP[q] = checkInt(qualityBP[q], balance.Qualities[q]+"機率", balance.Range{Low: 0, High: TotalBP}, &errs)
	}
	for _, q := range QualityOrder {
		s.QualityPricePct[q] = checkInt(qualityPct[q], balance.Qualities[q]+"售價倍率", QualityPriceRange, &errs)
	}
	for _, m := range MutationOrder {
		s.MutationBP[m] = checkInt(mutationBP[m], balance.Mutations[m]+"機率", MutationBPRange, &errs)
	}
	s.GiantMultiplier = checkInt(raw["giant_multiplier"], "巨型作物數量倍率", GiantRange, &errs)
	s.GoldenPricePct = checkInt(raw["golden_price_pct"], "黃金作物售價倍率", GoldenPctRange, &errs)
	s.FastReductionSeconds = checkInt(raw["fast_reduction_seconds"], "快速生長減少秒數", FastReductionRange, &errs)
	s.VariantPricePct = checkInt(raw["variant_price_pct"], "珍稀變種售價倍率", VariantPctRange, &errs)
	s.LevelBonusBP = checkInt(raw["level_bonus_bp"], "每級品質加成", LevelBonusRange, &errs)
	s.LevelBonusMaxBP = checkInt(raw["level_bonus_max_bp"], "品質加成上限", LevelBonusMaxRange, &errs)
	if len(errs) == 0 {
		total := sumValues(s.QualityBP)
		if total != TotalBP {
			errs = append(errs, fmt.Sprintf("品質機率總和必須為 100%%（目前 %s%%）。", pyG(float64(total)/100)))
		}
		if s.QualityBP["normal"] < s.LevelBonusMaxBP {
			errs = append(errs, "普通品質機率不可低於品質加成上限，否則加成後會出現負機率。")
		}
		others := 0
		for _, q := range QualityOrder[1:] {
			others += s.QualityBP[q]
		}
		if others == 0 && s.LevelBonusMaxBP > 0 {
			errs = append(errs, "優良／稀有／傳說機率全為 0 時，品質加成上限必須為 0。")
		}
		mutationTotal := sumValues(s.MutationBP)
		if mutationTotal > MutationTotalMaxBP {
			errs = append(errs, fmt.Sprintf("突變機率總和最多 %s%%（目前 %s%%）。",
				pyG(float64(MutationTotalMaxBP)/100), pyG(float64(mutationTotal)/100)))
		}
	}
	if len(errs) > 0 {
		return balance.GeneticsSettings{}, gameerr.Validationf("%s", "設定無效："+strings.Join(errs, "；"))
	}
	return s, nil
}

// SettingsMap 把設定轉成 JSON 形狀的 map（可再交給 ValidateSettings；測試與 API 用）。
func SettingsMap(s balance.GeneticsSettings) map[string]any {
	toAny := func(m map[string]int) map[string]any {
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return map[string]any{
		"quality_bp":             toAny(s.QualityBP),
		"quality_price_pct":      toAny(s.QualityPricePct),
		"mutation_bp":            toAny(s.MutationBP),
		"giant_multiplier":       s.GiantMultiplier,
		"golden_price_pct":       s.GoldenPricePct,
		"fast_reduction_seconds": s.FastReductionSeconds,
		"variant_price_pct":      s.VariantPricePct,
		"level_bonus_bp":         s.LevelBonusBP,
		"level_bonus_max_bp":     s.LevelBonusMaxBP,
	}
}

func sumValues(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

func copyMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// LevelBonusBP 農場等級加成：每高於 1 級加 level_bonus_bp，上限 level_bonus_max_bp。
func LevelBonusBP(s balance.GeneticsSettings, farmLevel int) int {
	return min(s.LevelBonusMaxBP, s.LevelBonusBP*max(0, farmLevel-1))
}

// EffectiveQualityBP 把加成（等級＋限時事件）從「普通」依其他品質的基礎機率比例重新分配；結果總和恆為 10000。
//
// 加成總和不超過普通品質的基礎機率，因此不會出現負機率。回傳新的 map。
func EffectiveQualityBP(s balance.GeneticsSettings, farmLevel, eventBonusBP int) map[string]int {
	base := s.QualityBP
	bonus := min(base["normal"], LevelBonusBP(s, farmLevel)+max(0, eventBonusBP))
	others := QualityOrder[1:]
	othersTotal := 0
	for _, q := range others {
		othersTotal += base[q]
	}
	result := copyMap(base)
	if bonus <= 0 || othersTotal == 0 {
		return result
	}
	result["normal"] = base["normal"] - bonus
	given := 0
	for _, q := range others {
		share := bonus * base[q] / othersTotal // 皆為非負數，等同 Python //
		result[q] = base[q] + share
		given += share
	}
	result[others[0]] += bonus - given // 捨去的餘數給第一個較高品質，維持總和
	return result
}

// RollQuality u ∈ [0, 1)；依 QualityOrder 切分區間（普通在最前面）。
func RollQuality(qualityBP map[string]int, u float64) string {
	point := int(u * TotalBP)
	cumulative := 0
	for _, q := range QualityOrder {
		cumulative += qualityBP[q]
		if point < cumulative {
			return q
		}
	}
	return QualityOrder[len(QualityOrder)-1]
}

// EffectiveMutationBP 限時事件的突變加成依各突變的基礎機率比例分配；總和不超過 10000。基礎機率全為 0 時不加成。
func EffectiveMutationBP(s balance.GeneticsSettings, eventBonusBP int) map[string]int {
	base := s.MutationBP
	total := sumValues(base)
	bonus := min(max(0, eventBonusBP), TotalBP-total)
	result := copyMap(base)
	if bonus <= 0 || total == 0 {
		return result
	}
	given := 0
	for _, m := range MutationOrder {
		share := bonus * base[m] / total
		result[m] += share
		given += share
	}
	for _, m := range MutationOrder {
		if base[m] > 0 {
			result[m] += bonus - given // 捨去的餘數給第一個有機率的突變，維持總和
			break
		}
	}
	return result
}

// RollMutation u ∈ [0, 1)；突變區間放在最上方 [1 - 總機率, 1)，每批最多一個主要突變。
func RollMutation(s balance.GeneticsSettings, u float64, eventBonusBP int) string {
	mutationBP := EffectiveMutationBP(s, eventBonusBP)
	point := int(u * TotalBP)
	start := TotalBP - sumValues(mutationBP)
	if point < start {
		return "none"
	}
	cumulative := start
	for _, m := range MutationOrder {
		cumulative += mutationBP[m]
		if point < cumulative {
			return m
		}
	}
	return MutationOrder[len(MutationOrder)-1]
}

// HarvestQuantity 收成數量：巨型作物先乘倍率（豐收作物 +50%，至少 +1），再套用限時事件加成（無條件捨去，至少為原數量）。
func HarvestQuantity(baseQuantity int, mutation string, s balance.GeneticsSettings, eventBonusPct int) int {
	quantity := baseQuantity
	switch mutation {
	case "giant":
		quantity = baseQuantity * s.GiantMultiplier
	case "bountiful":
		quantity = baseQuantity + max(1, baseQuantity*balance.BountifulBonusPct/100)
	}
	return numfmt.FloorDiv(quantity*(100+max(0, eventBonusPct)), 100)
}

// GrowSeconds 生長時間依序套用：蚯蚓 → 限時事件（減少 %）→ 快速生長（減少固定秒數）→ 最短時間。不會變成 0 或負數。
func GrowSeconds(baseSeconds int, mutation string, earthwormActive bool, s balance.GeneticsSettings, eventReductionPct int) int {
	seconds := rules.GrowSecondsWithEffects(baseSeconds, earthwormActive)
	// 加速後的下限：最短生長時間；若蚯蚓後已低於最短時間，事件不會反而拉長時間
	floor := min(balance.MinGrowSeconds, seconds)
	if eventReductionPct > 0 {
		seconds = max(floor, numfmt.FloorDiv(seconds*(100-min(eventReductionPct, 100)), 100))
	}
	if mutation == "fast" {
		seconds = max(balance.MinGrowSeconds, seconds-s.FastReductionSeconds)
	}
	return seconds
}

// BasePrice 基本售價：珍稀變種是獨立作物，基本售價 = 原作物售價 × 變種倍率（捨去）。
func BasePrice(cropSellPrice int, mutation string, s balance.GeneticsSettings) int {
	if mutation == "variant" {
		return numfmt.FloorDiv(cropSellPrice*s.VariantPricePct, 100)
	}
	return cropSellPrice
}

// MutationPricePct 突變售價倍率：只有黃金、夜光作物影響單價；巨型、豐收影響數量、快速生長影響時間、
// 變種已反映在基本售價、水晶在收成時額外給水晶。
func MutationPricePct(mutation string, s balance.GeneticsSettings) int {
	switch mutation {
	case "golden":
		return s.GoldenPricePct
	case "glow":
		return balance.GlowPricePct
	}
	return 100
}

// UnitPrice 最終單價 = 基本售價 × 品質倍率 × 突變售價倍率，整數運算後無條件捨去。
func UnitPrice(cropSellPrice int, quality, mutation string, s balance.GeneticsSettings) int {
	base := BasePrice(cropSellPrice, mutation, s)
	return numfmt.FloorDiv(base*s.QualityPricePct[quality]*MutationPricePct(mutation, s), 10_000)
}

// ProduceCode 收成後的農產品代碼：珍稀變種使用作物設定的獨立代碼（variantCode 為 "" 表示 None）。
func ProduceCode(code, variantCode, mutation string) string {
	if mutation == "variant" {
		return variantCode
	}
	return code
}

// ProduceName 收成後的農產品名稱（mutation 為 "" 表示 None）。
func ProduceName(name, variantName, mutation string) string {
	if mutation == "variant" {
		return variantName
	}
	return name
}

// ParseQuality 接受代碼或中文名稱（例如 rare／稀有）；無法辨識時回傳 ""（Python None）。
func ParseQuality(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	for _, code := range balance.QualityOrder {
		if token == code || token == balance.Qualities[code] {
			return code
		}
	}
	return ""
}

// ParseMutation 接受代碼、中文名稱或前兩字（例如 golden／黃金作物／黃金）；無法辨識時回傳 ""（Python None）。
func ParseMutation(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	for _, code := range balance.MutationOrder {
		name := balance.Mutations[code]
		if token == code || token == name || token == firstRunes(name, 2) {
			return code
		}
	}
	return ""
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
