// Package seedlab 種子實驗室的純規則（Go 版新增，2026-11-13；設計見專案根目錄「種子實驗室流程圖.md」）。
//
//   - 農場達到開放等級（預設 Lv.5）後付金幣建造實驗室；一般種子經「鑑定」變成有 4 個基因（1–5 級）的特殊種子。
//   - 雜交：同品種一定相容（改良基因）；不同品種只有 Hybrids 表中的組合可以雜交，成功時得到雜交作物，失敗時得到母本品種。
//   - 每個基因各自 50% 繼承母本或父本，再擲升級／維持／退化；催化劑（水晶）提高指定基因的升級機率。
//   - 突變研究：消耗水晶，成功時種子帶上突變（含實驗室限定的夜光、豐收、水晶），突變會隨種子傳承。
//   - 數值都在 config.go（管理網站可調整），這裡一律經 Current() 讀取。
//
// 機率以萬分比（bp）整數表示；亂數由呼叫端傳入 u ∈ [0, 1)，方便測試。
package seedlab

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// MaxLevel 研究等級上限（研究槽數量 = 研究等級）。
const MaxLevel = 3

// Unlock 目前的開放等級。
func Unlock() int { return Current().UnlockLevel }

// Slots 研究槽數量（Lv.1 1 個、Lv.2 2 個、Lv.3 3 個）。
func Slots(level int) int { return max(1, min(level, MaxLevel)) }

// LevelFor 累積經驗對應的研究等級。
func LevelFor(exp int) int {
	c := Current()
	level := 1
	for l := 2; l <= MaxLevel; l++ {
		if exp >= c.LevelExp[l] {
			level = l
		}
	}
	return level
}

// NextLevelExp 下一級需要的累積經驗；已滿級時回傳 0。
func NextLevelExp(level int) int {
	if level >= MaxLevel {
		return 0
	}
	return Current().LevelExp[level+1]
}

// IdentifyFee 鑑定一顆種子的費用。
func IdentifyFee(seedPrice int) int {
	c := Current()
	return max(c.IdentifyFeeMin, seedPrice*c.IdentifyFeeMultiplier)
}

// ---------- 基因 ----------

const (
	GeneMin = 1
	GeneMax = 5
)

// Gene 代碼與名稱（依 GeneOrder 顯示與處理）。
const (
	GeneGrowth   = "growth"
	GeneYield    = "yield"
	GeneQuality  = "quality"
	GeneMutation = "mutation"
)

var (
	GeneOrder = []string{GeneGrowth, GeneYield, GeneQuality, GeneMutation}
	GeneNames = map[string]string{GeneGrowth: "生長", GeneYield: "產量", GeneQuality: "品質", GeneMutation: "突變"}
	GeneEmoji = map[string]string{GeneGrowth: "⏱️", GeneYield: "🧺", GeneQuality: "⭐", GeneMutation: "🧬"}
)

// Genes 4 個基因等級（1–5）。
type Genes struct {
	Growth   int `json:"growth"`
	Yield    int `json:"yield"`
	Quality  int `json:"quality"`
	Mutation int `json:"mutation"`
}

// Get 依代碼取得等級。
func (g Genes) Get(code string) int {
	switch code {
	case GeneGrowth:
		return g.Growth
	case GeneYield:
		return g.Yield
	case GeneQuality:
		return g.Quality
	case GeneMutation:
		return g.Mutation
	}
	return 0
}

// Set 依代碼設定等級（限制在 1–5）。
func (g *Genes) Set(code string, level int) {
	level = max(GeneMin, min(GeneMax, level))
	switch code {
	case GeneGrowth:
		g.Growth = level
	case GeneYield:
		g.Yield = level
	case GeneQuality:
		g.Quality = level
	case GeneMutation:
		g.Mutation = level
	}
}

// Total 4 個基因等級總和（4–20）。
func (g Genes) Total() int { return g.Growth + g.Yield + g.Quality + g.Mutation }

// Text 例如「生長 3／產量 5／品質 4／突變 2」。
func (g Genes) Text() string {
	parts := make([]string, len(GeneOrder))
	for i, code := range GeneOrder {
		parts[i] = fmt.Sprintf("%s %d", GeneNames[code], g.Get(code))
	}
	return strings.Join(parts, "／")
}

// Stars 例如「⏱️3 🧺5 ⭐4 🧬2」（下拉選單用的短格式）。
func (g Genes) Stars() string {
	parts := make([]string, len(GeneOrder))
	for i, code := range GeneOrder {
		parts[i] = fmt.Sprintf("%s%d", GeneEmoji[code], g.Get(code))
	}
	return strings.Join(parts, " ")
}

// ---------- 基因在種植時的效果（1 級沒有加成） ----------

// GrowthReductionPct 生長基因：生長時間減少的百分比（與溫室、限時活動相加；下限沿用 balance.MinGrowSeconds）。
func GrowthReductionPct(level int) int { return Current().GrowthPctPerLevel * max(0, level-1) }

// YieldBonus 產量基因：每次收成增加的數量 = 作物原本收成數量 × 每級 % × (等級 − 1)，無條件進位
// （2026-11-14 改成百分比：原本每 2 級 +1，鑑定出的 1–2 級完全沒有加成）。baseQuantity 是作物目錄的收成數量。
func YieldBonus(level, baseQuantity int) int {
	pct := Current().YieldPctPerLevel * max(0, level-1) * max(0, baseQuantity)
	return (pct + 99) / 100
}

// QualityBonusBP 品質基因：品質加成（與農場等級、限時活動的加成相加）。
func QualityBonusBP(level int) int { return Current().QualityBPPerLevel * max(0, level-1) }

// MutationBonusBP 突變基因：種植時突變機率加成（種子本身沒有突變時才擲骰）。
func MutationBonusBP(level int) int { return Current().MutationBPPerLevel * max(0, level-1) }

// ---------- 鑑定 ----------

// RollIdentify 鑑定：一般作物每個基因 1–2 級；稀有作物（商店不賣的，例如琥珀瓜、月光花）2–3 級。u < 0.5 時多 1 級。
func RollIdentify(rare bool, rng func() float64) Genes {
	low := 1
	if rare {
		low = 2
	}
	var g Genes
	for _, code := range GeneOrder {
		level := low
		if rng() < 0.5 {
			level++
		}
		g.Set(code, level)
	}
	return g
}

// ---------- 雜交 ----------

// Hybrid 一個雜交組合：雙親（順序不影響）→ 雜交作物代碼。SuccessBP、Minutes 是目前設定的值（FindHybrid 填入）。
type Hybrid struct {
	A, B      string
	Code      string
	SuccessBP int
	Minutes   int
}

// Hybrids 12 種雜交作物的組合（固定；成功率與時間見 Current().Hybrids）。作物本身由 Migration 0039 寫入 crop_types，商店不賣。
var Hybrids = []Hybrid{
	{A: "wheat", B: "corn", Code: "golden_grain"},
	{A: "carrot", B: "tomato", Code: "rainbow_carrot"},
	{A: "strawberry", B: "grape", Code: "purple_berry"},
	{A: "pumpkin", B: "watermelon", Code: "honey_melon"},
	{A: "amber_melon", B: "pumpkin", Code: "lantern_gourd"},
	{A: "moonflower", B: "strawberry", Code: "starry_night_flower"},
	{A: "corn", B: "tomato", Code: "ruby_corn"},
	{A: "wheat", B: "strawberry", Code: "sakura_wheat"},
	{A: "carrot", B: "pumpkin", Code: "sunset_squash"},
	{A: "grape", B: "watermelon", Code: "jade_grape"},
	{A: "amber_melon", B: "watermelon", Code: "dawn_melon"},
	{A: "moonflower", B: "tomato", Code: "crimson_moon_flower"},
}

// CurrentHybrids 雜交組合與目前的成功率、時間（依 Hybrids 順序）。
func CurrentHybrids() []Hybrid {
	c := Current()
	out := make([]Hybrid, len(Hybrids))
	for i, h := range Hybrids {
		s := c.Hybrids[h.Code]
		h.SuccessBP, h.Minutes = s.SuccessBP, s.Minutes
		out[i] = h
	}
	return out
}

// HybridPrice 雜交作物的基礎價格 = 雙親價格總和 × 1.5（四捨五入）。
func HybridPrice(a, b int) int { return ((a+b)*3 + 1) / 2 }

// HybridCodes 雜交作物代碼（依 Hybrids 順序）。
func HybridCodes() []string {
	out := make([]string, len(Hybrids))
	for i, h := range Hybrids {
		out[i] = h.Code
	}
	return out
}

// IsHybrid 是否為雜交作物。
func IsHybrid(code string) bool {
	return slices.ContainsFunc(Hybrids, func(h Hybrid) bool { return h.Code == code })
}

// FindHybrid 兩個不同品種的雜交組合（含目前的成功率與時間）；沒有時回傳 nil。
func FindHybrid(a, b string) *Hybrid {
	for _, h := range Hybrids {
		if (h.A == a && h.B == b) || (h.A == b && h.B == a) {
			s := Current().Hybrids[h.Code]
			h.SuccessBP, h.Minutes = s.SuccessBP, s.Minutes
			return &h
		}
	}
	return nil
}

// HybridParents 雜交作物的雙親；不是雜交作物時回傳 ("", "")。
func HybridParents(code string) (string, string) {
	for _, h := range Hybrids {
		if h.Code == code {
			return h.A, h.B
		}
	}
	return "", ""
}

// Compatible 兩個品種能否配對：同品種一定可以；不同品種要在雜交表中。
func Compatible(a, b string) bool { return a == b || FindHybrid(a, b) != nil }

// 培育材料
const (
	MatResin       = "resin"        // 樹脂（花粉）：提高雜交成功率
	MatHerb        = "herb"         // 藥草（營養液）：縮短培育時間
	MatCrystal     = "crystal"      // 水晶（催化劑）：指定 1 個基因升級機率提高；突變研究必要
	MatAncientCore = "ancient_core" // 古代核心：突變研究機率加倍
	MinJobMinutes  = 1
)

// CrossPlan 一次雜交的設定。
type CrossPlan struct {
	Mother, Father string // 作物代碼
	Resin, Herb    bool
	Catalyst       string // 基因代碼；"" = 不使用
}

// Result 雜交結果的品種與成功率；同品種時成功率 100%。
func (p CrossPlan) Result() (code string, successBP int, ok bool) {
	if p.Mother == p.Father {
		return p.Mother, 10_000, true
	}
	h := FindHybrid(p.Mother, p.Father)
	if h == nil {
		return "", 0, false
	}
	bp := h.SuccessBP
	if p.Resin {
		bp += Current().ResinSuccessBP
	}
	return h.Code, min(10_000, bp), true
}

// Duration 培育時間。
func (p CrossPlan) Duration() time.Duration {
	c := Current()
	minutes := c.SameSpeciesMinutes
	if p.Mother != p.Father {
		if h := FindHybrid(p.Mother, p.Father); h != nil {
			minutes = h.Minutes
		}
	}
	if p.Herb {
		minutes = minutes * (100 - c.HerbTimePct) / 100
	}
	return time.Duration(max(MinJobMinutes, minutes)) * time.Minute
}

// Materials 這次雜交消耗的素材。
func (p CrossPlan) Materials() map[string]int {
	out := map[string]int{}
	if p.Resin {
		out[MatResin] = 1
	}
	if p.Herb {
		out[MatHerb] = 1
	}
	if p.Catalyst != "" {
		out[MatCrystal] = 1
	}
	return out
}

// Parent 雜交雙親的資料。
type Parent struct {
	Genes      Genes
	Generation int
	Mutation   string // "none" = 沒有
}

// Child 雜交或繼承後的子代。
type Child struct {
	Genes      Genes
	Generation int
	Mutation   string
}

// CrossMutations 雜交時自然產生的突變（只有一般種植就有的四種；實驗室限定的突變要用突變研究取得）。
var CrossMutations = []string{"giant", "golden", "fast", "variant"}

// LabMutations 突變研究可能得到的突變（依此順序擲骰，各占相同機率）。
var LabMutations = []string{"giant", "golden", "fast", "variant", "glow", "bountiful", "crystal"}

// Inherit 雜交的基因繼承：每個基因 50% 繼承母本或父本，再擲升級／維持／退化（催化劑提高指定基因的升級機率），
// 世代 = 較大的世代 + 1；雙親有突變時有機會遺傳，否則依子代突變基因擲自然突變。
func Inherit(mother, father Parent, catalyst string, rng func() float64) Child {
	c := Current()
	var g Genes
	for _, code := range GeneOrder {
		level := mother.Genes.Get(code)
		if rng() >= 0.5 {
			level = father.Genes.Get(code)
		}
		up := c.GeneUpBP
		if code == catalyst {
			up += c.CatalystUpBP
		}
		u := int(rng() * 10_000)
		switch {
		case u < up:
			level++
		case u < up+c.GeneDownBP:
			level--
		}
		g.Set(code, level)
	}
	child := Child{Genes: g, Generation: max(mother.Generation, father.Generation) + 1, Mutation: "none"}
	var inherited []string
	for _, m := range []string{mother.Mutation, father.Mutation} {
		if m != "" && m != "none" && !slices.Contains(inherited, m) {
			inherited = append(inherited, m)
		}
	}
	u := rng()
	if len(inherited) > 0 {
		if int(u*10_000) < c.InheritMutationBP {
			child.Mutation = inherited[min(len(inherited)-1, int(rng()*float64(len(inherited))))]
		}
		return child
	}
	if int(u*10_000) < c.CrossMutationPerLvBP*g.Mutation {
		child.Mutation = CrossMutations[min(len(CrossMutations)-1, int(rng()*float64(len(CrossMutations))))]
	}
	return child
}

// Success 依成功率擲骰。
func Success(successBP int, u float64) bool { return int(u*10_000) < successBP }

// ---------- 突變研究 ----------

// MutateChanceBP 突變研究成功率：基礎 + 突變基因每級加成，使用古代核心時加倍，不超過上限。
func MutateChanceBP(geneMutation int, ancientCore bool) int {
	c := Current()
	bp := c.MutateBaseBP + c.MutatePerLevelBP*max(0, geneMutation-1)
	if ancientCore {
		bp *= 2
	}
	return min(c.MutateMaxBP, bp)
}

// RollLabMutation 突變研究成功時得到的突變（不會和目前的突變相同）。
func RollLabMutation(current string, u float64) string {
	var pool []string
	for _, m := range LabMutations {
		if m != current {
			pool = append(pool, m)
		}
	}
	return pool[min(len(pool)-1, int(u*float64(len(pool))))]
}

// MutateDuration 突變研究的時間。
func MutateDuration() time.Duration { return time.Duration(Current().MutateMinutes) * time.Minute }

// CropSpec 雜交作物的目錄資料（測試夾具用來重設；正式資料由 Migration 0039 寫入）。售價由雙親依 HybridPrice 計算。
type CropSpec struct {
	Code, Name               string
	GrowSeconds, HarvestQty  int
	VariantCode, VariantName string
	Seasons                  []string // nil = 不限
}

// HybridCrops 12 種雜交作物（依 Hybrids 順序）。
var HybridCrops = []CropSpec{
	{"golden_grain", "黃金穀物", 7200, 3, "sun_grain", "太陽穀", nil},
	{"rainbow_carrot", "彩虹蘿蔔", 14400, 4, "aurora_carrot", "極光蘿蔔", []string{"spring", "autumn", "winter"}},
	{"purple_berry", "紫莓", 43200, 5, "midnight_berry", "午夜紫莓", nil},
	{"honey_melon", "蜜糖瓜", 64800, 4, "sugar_crystal_melon", "糖晶瓜", []string{"spring", "summer", "autumn"}},
	{"lantern_gourd", "燈籠瓜", 43200, 4, "golden_lantern_gourd", "金燈籠瓜", []string{"summer", "autumn", "winter"}},
	{"starry_night_flower", "星夜花", 86400, 3, "galaxy_flower", "銀河花", []string{"spring", "winter"}},
	{"ruby_corn", "紅寶玉米", 14400, 4, "blaze_corn", "烈焰玉米", []string{"spring", "summer", "winter"}},
	{"sakura_wheat", "櫻花麥", 18000, 4, "night_sakura_wheat", "夜櫻麥", nil},
	{"sunset_squash", "夕陽瓜", 28800, 4, "afterglow_squash", "晚霞瓜", []string{"summer", "autumn", "winter"}},
	{"jade_grape", "翡翠葡萄", 72000, 5, "imperial_jade_grape", "帝王翡翠葡萄", []string{"spring", "summer", "autumn"}},
	{"dawn_melon", "曙光瓜", 72000, 3, "corona_melon", "日冕瓜", nil},
	{"crimson_moon_flower", "赤月花", 64800, 3, "blood_moon_flower", "血月花", []string{"spring", "winter"}},
}
