// Package mushroom 地下菇房（Go 版新增，2026-11-12）：純規則，不碰資料庫。
//
//   - 農場 Lv.4 解鎖，不用建造；4 個菇床不用種植、不用澆水，每 GrowSeconds 長好一批，收成後立即開始下一批。
//   - 長出什麼菇由「上方農田目前種的作物」（養分）決定：每一朵依各養分的農田格數加權抽一種養分。
//   - 投入木屑（探險取得）提高稀有菇機率；投入 MaxSawdust 個時保證至少一半是稀有菇。收成後木屑歸零。
//   - 菇床狀態存在 farm_tools（tool_code = ToolCode）的 settings JSON，不需要 Migration。
//
// 亂數由呼叫端注入（回傳 [0, 1)），方便測試。
package mushroom

import (
	"fmt"
	"math"
	"time"
)

const (
	ToolCode    = "mushroom_cellar" // farm_tools.tool_code
	UnlockLevel = 4                 // 農場等級達到後自動開放
	BedCount    = 4                 // 菇床數
	GrowSeconds = 24 * 3600         // 每批生長時間
	YieldPerBed = 2                 // 每個菇床每批收成幾朵
	MaxSawdust  = 4                 // 每個菇床每批最多投入幾個木屑
)

// RarePct 投入 0..MaxSawdust 個木屑時，每一朵是稀有菇的機率（%）。投入 MaxSawdust 個時另外保證一半以上是稀有菇。
var RarePct = [MaxSawdust + 1]int{5, 15, 25, 35, 50}

// Species 菇類（代碼同時是背包物品與 balance.Goods 的代碼）。
type Species struct {
	Code  string
	Name  string
	Emoji string
	Rare  bool
}

var SpeciesOrder = []string{"shiitake", "button_mushroom", "enoki", "lingzhi", "truffle", "glowcap"}

var SpeciesMap = map[string]Species{
	"shiitake":        {"shiitake", "香菇", "🍄", false},
	"button_mushroom": {"button_mushroom", "洋菇", "🤍", false},
	"enoki":           {"enoki", "金針菇", "🌾", false},
	"lingzhi":         {"lingzhi", "靈芝", "🟤", true},
	"truffle":         {"truffle", "松露", "🟫", true},
	"glowcap":         {"glowcap", "發光菇", "💙", true},
}

// Nutrient 養分：上方農田的作物種類 → 長出的普通菇與稀有菇。
type Nutrient struct {
	Code   string
	Name   string
	Common string
	Rare   string
	Crops  []string
}

// DefaultNutrient 沒有種任何作物，或作物不在下列名單（例如管理員新增的作物）時使用的養分。
const DefaultNutrient = "grain"

var NutrientOrder = []string{"grain", "veggie", "fruit"}

var Nutrients = map[string]Nutrient{
	"grain":  {"grain", "穀物養分", "shiitake", "lingzhi", []string{"wheat", "corn"}},
	"veggie": {"veggie", "蔬菜養分", "button_mushroom", "truffle", []string{"carrot", "tomato", "pumpkin"}},
	"fruit":  {"fruit", "果香養分", "enoki", "glowcap", []string{"strawberry", "grape", "watermelon", "amber_melon", "moonflower"}},
}

// NutrientOf 作物代碼對應的養分。
func NutrientOf(cropCode string) string {
	if code, ok := Current().cropToNutrient[cropCode]; ok {
		return code
	}
	return DefaultNutrient
}

// Mix 依農田上的作物（每格一個代碼，空地不要傳）算各養分的格數；完全沒有作物時是 {DefaultNutrient: 1}。
func Mix(cropCodes []string) map[string]int {
	out := map[string]int{}
	for _, c := range cropCodes {
		out[NutrientOf(c)]++
	}
	if len(out) == 0 {
		out[DefaultNutrient] = 1
	}
	return out
}

// MixPct 各養分的比例（%，依 NutrientOrder，四捨五入），介面顯示用。
func MixPct(mix map[string]int) map[string]int {
	total := 0
	for _, n := range mix {
		total += n
	}
	out := map[string]int{}
	if total == 0 {
		return out
	}
	for code, n := range mix {
		out[code] = int(math.Round(float64(n) * 100 / float64(total)))
	}
	return out
}

// GuaranteedRare 投入 sawdust 個木屑、收成 yield 朵時保證的稀有菇數量。
func GuaranteedRare(sawdust, yield int) int {
	if sawdust >= MaxSawdust && Current().FullGuarantee {
		return (yield + 1) / 2
	}
	return 0
}

func clampSawdust(n int) int { return max(0, min(MaxSawdust, n)) }

// pickNutrient 依 NutrientOrder 的順序加權抽養分（u ∈ [0, 1)）。
func pickNutrient(mix map[string]int, u float64) string {
	total := 0
	for _, code := range NutrientOrder {
		total += max(0, mix[code])
	}
	if total == 0 {
		return DefaultNutrient
	}
	point := u * float64(total)
	cum := 0
	last := DefaultNutrient
	for _, code := range NutrientOrder {
		w := max(0, mix[code])
		if w == 0 {
			continue
		}
		cum += w
		last = code
		if point < float64(cum) {
			return code
		}
	}
	return last
}

// Roll 擲骰決定一個菇床這一批收成的菇（長度 = yield）。每一朵先抽養分，再決定是不是稀有菇；
// 前 GuaranteedRare 朵一定是稀有菇（仍會抽養分）。
func Roll(mix map[string]int, sawdust, yield int, rng func() float64) []string {
	sawdust = clampSawdust(sawdust)
	guaranteed := GuaranteedRare(sawdust, yield)
	out := make([]string, 0, yield)
	for i := 0; i < yield; i++ {
		n := Nutrients[pickNutrient(mix, rng())]
		rare := i < guaranteed || rng()*100 < float64(Current().RarePct[sawdust])
		if rare {
			out = append(out, n.Rare)
		} else {
			out = append(out, n.Common)
		}
	}
	return out
}

// ---------- 菇床狀態（farm_tools.settings） ----------

// Bed 一個菇床：這一批開始生長的時間與已投入的木屑。
type Bed struct {
	StartedAt time.Time
	Sawdust   int
}

func (b Bed) ReadyAt() time.Time { return b.StartedAt.Add(time.Duration(Grow()) * time.Second) }

func (b Bed) Ready(now time.Time) bool { return !now.Before(b.ReadyAt()) }

// Progress 生長進度 0..1。
func (b Bed) Progress(now time.Time) float64 {
	p := now.Sub(b.StartedAt).Seconds() / float64(Grow())
	return max(0, min(1, p))
}

// NewBeds 剛開放時的菇床：全部從 now 開始生長、沒有木屑。
func NewBeds(now time.Time) []Bed {
	beds := make([]Bed, BedCount)
	for i := range beds {
		beds[i] = Bed{StartedAt: now}
	}
	return beds
}

// ParseBeds 從 settings 讀出菇床；欄位缺漏或格式錯誤的菇床視為從 now 開始（不讓壞資料卡住玩家）。
// 第二個回傳值表示是否有修補（呼叫端可寫回）。
func ParseBeds(settings map[string]any, now time.Time) ([]Bed, bool) {
	beds := NewBeds(now)
	raw, _ := settings["beds"].([]any)
	fixed := len(raw) != BedCount
	for i := range beds {
		if i >= len(raw) {
			break
		}
		m, ok := raw[i].(map[string]any)
		if !ok {
			fixed = true
			continue
		}
		s, _ := m["started_at"].(string)
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil || t.After(now) {
			fixed = true
			continue
		}
		beds[i].StartedAt = t.UTC()
		switch v := m["sawdust"].(type) {
		case float64:
			beds[i].Sawdust = clampSawdust(int(v))
		case int:
			beds[i].Sawdust = clampSawdust(v)
		}
	}
	return beds, fixed
}

// EncodeBeds 菇床寫回 settings 的格式（時間為 UTC RFC3339，微秒精度）。
func EncodeBeds(beds []Bed) map[string]any {
	list := make([]any, len(beds))
	for i, b := range beds {
		list[i] = map[string]any{"started_at": b.StartedAt.UTC().Format(time.RFC3339Nano), "sawdust": b.Sawdust}
	}
	return map[string]any{"beds": list}
}

// SpeciesName 菇類名稱（找不到時回傳代碼）。
func SpeciesName(code string) string {
	if s, ok := SpeciesMap[code]; ok {
		return s.Name
	}
	return code
}

// RareChanceText 介面顯示的稀有菇機率說明。
func RareChanceText(sawdust int) string {
	sawdust = clampSawdust(sawdust)
	if g := GuaranteedRare(sawdust, Yield()); g > 0 {
		return fmt.Sprintf("保證 %d 朵稀有菇，其餘 %d%%", g, Current().RarePct[sawdust])
	}
	return fmt.Sprintf("每朵 %d%% 稀有", Current().RarePct[sawdust])
}
