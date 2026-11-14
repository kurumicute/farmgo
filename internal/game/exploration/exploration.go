// Package exploration 山洞冒險：探索區域、掉落表、特殊事件、機器配方與等級門檻（純規則，不碰資料庫）。
// 對應 Python app/game/exploration.py。
//
// 探索的機率、數量與解鎖等級全部集中在本檔案；調整數值只改這裡（Migration 只寫死區域代碼）。
// 機率單位為萬分比（bp，10000 = 100%），亂數由呼叫端注入（Rng，回傳 [0, 1)），方便測試。
// 抽籤順序與 Python 完全相同：相同的亂數序列會得到相同結果。
package exploration

import (
	"strings"
)

// Rng 亂數來源，回傳 [0, 1) 的浮點數（對應 Python 的 random.random）。
type Rng func() float64

const TotalBP = 10_000

// ---------- 素材與特殊道具（存在 inventory_items，但放在獨立的素材背包，不佔倉庫空間；不能在商店購買或販售） ----------

// MaterialBagCapacity 素材背包上限（下列素材與假幣的總數）。模組層級可變狀態（Python MATERIAL_BAG_CAPACITY；測試會修改）。
var MaterialBagCapacity = 1000

// Material 名稱、稀有度、用途。
type Material struct {
	Name   string
	Rarity string
	Usage  string
}

// MaterialOrder 素材顯示順序（Python MATERIALS 的 dict 順序）。
var MaterialOrder = []string{
	"wood", "stone", "copper_ore", "iron_ore", "resin", "herb", "gear", "crystal", "ancient_core", "machine_core", Sawdust, "fake_coin",
}

var Materials = map[string]Material{
	"wood":         {"木材", "普通", "機器製作"},
	"stone":        {"石塊", "普通", "機器製作"},
	"copper_ore":   {"銅礦", "普通", "機器製作"},
	"iron_ore":     {"鐵礦", "稀有", "機器製作"},
	"resin":        {"樹脂", "普通", "機器製作"},
	"herb":         {"藥草", "普通", "作物培育"},
	"gear":         {"齒輪", "稀有", "自動化機器"},
	"crystal":      {"水晶", "稀有", "稀有作物培育"},
	"ancient_core": {"古木核心", "極稀有", "高級機器製作"},
	"machine_core": {"機械核心", "極稀有", "高級機器製作"},
	Sawdust:        {"木屑", "普通", "地下菇房"}, // Go 版新增（2026-11-12）
	"fake_coin":    {"假幣", "特殊", "兌換金幣"},
}

// Sawdust 木屑：每次探險（含古代遺跡）在原本的掉落之外固定再帶回 SawdustDrop 個，投入地下菇房提高稀有菇機率（Go 版新增）。
const Sawdust = "sawdust"

// SawdustDrop 各區域每次探險帶回的木屑數量 [low, high]：古老森林最多，其他區域差不多。
var SawdustDrop = map[string][2]int{
	"grassland": {2, 3},
	"forest":    {3, 4},
	"mine":      {2, 3},
	"ruins":     {2, 3},
}

// RollSawdust 依區域抽木屑數量並加進探索結果（在 Explore 之後呼叫，不影響原本掉落的抽籤順序）。
func RollSawdust(r *Result, rng Rng) {
	d, ok := Current().SawdustDrop[r.Area]
	if !ok {
		return
	}
	r.Add(Sawdust, d[0]+min(d[1]-d[0], int(rng()*float64(d[1]-d[0]+1))))
}

const (
	FakeCoin            = "fake_coin"
	FakeCoinValue       = 100 // 1 個假幣 = 100 金幣
	FakeCoinMaxExchange = 9999
)

// RareCrop 稀有作物：種子參考價、生長秒數、收成數量、單位售價、變種代碼與名稱（Migration 0014 寫入相同數值）。
type RareCrop struct {
	Code        string
	Name        string
	SeedPrice   int
	GrowSeconds int
	HarvestQty  int
	SellPrice   int
	VariantCode string
	VariantName string
}

// DefaultRareCrops 稀有種子是作物種子（crop_types.shop_buyable = false，商店不販售，只能探索或培育取得）。
func DefaultRareCrops() []RareCrop {
	return []RareCrop{
		{"amber_melon", "琥珀瓜", 200, 12 * 3600, 3, 120, "golden_amber_melon", "黃金琥珀瓜"},
		{"moonflower", "月光花", 500, 24 * 3600, 2, 450, "starflower", "星辰花"},
	}
}

// RareCrops 模組層級可變狀態（Python RARE_CROPS；測試會保留並重設）。修改後用 ResetRareCrops 還原。
var RareCrops = DefaultRareCrops()

// ResetRareCrops 把 RareCrops 還原為預設值（測試用，對應 conftest 的保留與重設）。
func ResetRareCrops() { RareCrops = DefaultRareCrops() }

const (
	AmberSeed = "seed_amber_melon"
	MoonSeed  = "seed_moonflower"
)

// RareSeedCodes 稀有作物種子的物品代碼（和素材一樣放素材背包，不佔倉庫）。
func RareSeedCodes() []string {
	out := make([]string, 0, len(RareCrops))
	for _, c := range RareCrops {
		out = append(out, "seed_"+c.Code)
	}
	return out
}

// ---------- 獎勵類型機率 ----------

var RewardTypes = []string{"common", "rare", "seed", "fake_coin", "event"}

var RewardTypeLabels = map[string]string{
	"common": "普通素材", "rare": "稀有素材", "seed": "稀有種子", "fake_coin": "假幣", "event": "特殊事件",
}

var RewardTypeBP = map[string]map[string]int{
	"normal":   {"common": 6500, "rare": 2000, "seed": 800, "fake_coin": 500, "event": 200},
	"advanced": {"common": 4500, "rare": 3000, "seed": 1200, "fake_coin": 800, "event": 500},
}

// Fallback 抽到的類型在目前等級沒有可掉落的物品時，依序改用下列類型（例如礦坑沒有種子 → 稀有素材）
var Fallback = map[string]string{"seed": "rare", "rare": "common", "event": "common", "fake_coin": "common", "common": "common"}

const FullPoolLevel = 16 // Lv.16：三個區域都改用高級探索（完整獎勵池）

// Drop 掉落項目。
type Drop struct {
	Item     string
	MinLevel int
	Weight   int
	QtyMin   int
	QtyMax   int
}

// Event 特殊事件。FakeCoins 為 [low, high]；Bonus 是額外抽幾次的獎勵類型（例如 {"rare"} = 再抽 1 個稀有素材）。
type Event struct {
	Code        string
	Name        string
	Description string
	MinLevel    int
	Weight      int
	FakeCoins   [2]int
	Bonus       []string
}

// LevelNote 介面顯示的等級解鎖說明。
type LevelNote struct {
	Level int
	Text  string
}

// PoolKinds 掉落池類型的順序（Python pools 的 dict 順序）。
var PoolKinds = []string{"common", "rare", "seed", "fake_coin"}

// Area 探索區域。
type Area struct {
	Code            string
	Emoji           string
	Name            string
	Description     string
	UnlockLevel     int
	DurationSeconds int // 探險時間：出發後經過這段時間才回來，期間不能去其他區域
	Rolls           int // 每次探索抽幾次獎勵
	AdvancedLevel   int // 達到此等級改用高級探索機率
	LevelNotes      []LevelNote
	Pools           map[string][]Drop
	Events          []Event
	// true：要所屬群組把古代遺跡修復完成才能進入（ruins 套件），等級只是另一個門檻
	NeedsRuins bool
}

func chest(fake [2]int) Event {
	return Event{"treasure_chest", "🎁 特殊寶箱", "發現一個上鎖的寶箱，裡面有假幣與稀有素材！", 1, 6, fake, []string{"rare"}}
}

var Areas = map[string]Area{
	"grassland": {
		Code: "grassland", Emoji: "🌿", Name: "青青草原",
		Description: "微風吹拂的草原，適合收集木材、石塊與藥草，偶爾能撿到琥珀種子。",
		UnlockLevel: 1, DurationSeconds: 8 * 3600, Rolls: 3, AdvancedLevel: FullPoolLevel,
		LevelNotes: []LevelNote{{2, "草原進階素材：銅礦"}, {4, "草原稀有發現：鐵礦、稀有發現事件"},
			{15, "極稀有探索事件"}, {16, "完整獎勵池（高級探索）"}},
		Pools: map[string][]Drop{
			"common": {{"wood", 1, 3, 2, 4}, {"stone", 1, 3, 2, 4}, {"herb", 1, 2, 1, 3},
				{"copper_ore", 2, 1, 1, 2}},
			"rare":      {{"iron_ore", 4, 1, 1, 2}},
			"seed":      {{AmberSeed, 1, 1, 1, 1}},
			"fake_coin": {{FakeCoin, 1, 1, 1, 2}},
		},
		Events: []Event{
			chest([2]int{2, 4}),
			{"rare_find", "🔍 草原稀有發現", "在草叢深處發現一處隱密的寶地。", 4, 3, [2]int{1, 1}, []string{"rare", "seed"}},
			{"ancient_ruins", "🏛️ 古代遺跡", "草原下竟然埋著古代遺跡！", 15, 1, [2]int{5, 10}, []string{"rare", "rare"}},
		},
	},
	"forest": {
		Code: "forest", Emoji: "🌲", Name: "古老森林",
		Description: "千年古樹林立的森林，盛產樹脂與齒輪，深處藏著古木核心與月光種子。",
		UnlockLevel: 5, DurationSeconds: 16 * 3600, Rolls: 4, AdvancedLevel: 9,
		LevelNotes: []LevelNote{{6, "森林進階素材：鐵礦"}, {7, "森林稀有發現：古木核心、稀有發現事件"},
			{9, "森林高級探索獎勵"}, {14, "高級機器素材：古木核心機率提高"}, {15, "極稀有探索事件"}},
		Pools: map[string][]Drop{
			"common": {{"wood", 5, 3, 3, 5}, {"resin", 5, 3, 2, 4}, {"herb", 5, 2, 2, 3},
				{"iron_ore", 6, 1, 1, 2}},
			"rare":      {{"gear", 5, 3, 1, 2}, {"ancient_core", 7, 1, 1, 1}, {"ancient_core", 14, 1, 1, 1}},
			"seed":      {{MoonSeed, 5, 1, 1, 1}},
			"fake_coin": {{FakeCoin, 5, 1, 1, 3}},
		},
		Events: []Event{
			chest([2]int{2, 5}),
			{"rare_find", "🔍 森林稀有發現", "在古樹樹洞裡找到前人留下的寶藏。", 7, 3, [2]int{1, 2}, []string{"rare", "seed"}},
			{"ancient_ruins", "🏛️ 森林古代遺跡", "藤蔓後方出現一座古代神殿！", 15, 1, [2]int{5, 10}, []string{"rare", "rare"}},
		},
	},
	"mine": {
		Code: "mine", Emoji: "⛏️", Name: "神秘礦坑",
		Description: "深不見底的礦坑，有銅礦、鐵礦與水晶，運氣好能挖到機械核心。",
		UnlockLevel: 10, DurationSeconds: 24 * 3600, Rolls: 5, AdvancedLevel: FullPoolLevel,
		LevelNotes: []LevelNote{{11, "礦坑進階素材：鐵礦"}, {13, "礦坑稀有發現：機械核心機率提高、稀有發現事件"},
			{14, "高級機器素材：機械核心機率再提高"}, {15, "極稀有探索事件"},
			{16, "完整獎勵池（高級探索）"}},
		Pools: map[string][]Drop{
			"common": {{"stone", 10, 3, 3, 5}, {"copper_ore", 10, 3, 2, 4}, {"iron_ore", 11, 2, 1, 3}},
			"rare": {{"iron_ore", 10, 2, 1, 2}, {"gear", 10, 2, 1, 2}, {"crystal", 10, 2, 1, 2},
				{"machine_core", 10, 1, 1, 1}, {"machine_core", 13, 1, 1, 1},
				{"machine_core", 14, 1, 1, 1}},
			"seed":      {},
			"fake_coin": {{FakeCoin, 10, 1, 2, 4}},
		},
		Events: []Event{
			chest([2]int{3, 6}),
			{"rare_find", "🔍 礦坑稀有發現", "鎬子敲開一條閃閃發光的礦脈！", 13, 3, [2]int{1, 2}, []string{"rare", "rare"}},
			{"ancient_ruins", "🏛️ 地底遺跡", "礦坑最深處通往一座失落的地底城市！", 15, 1, [2]int{5, 10},
				[]string{"rare", "rare", "rare"}},
		},
	},
}

// 古代遺跡：所屬群組修好後的「額外」探索，不佔一次一趟的名額，按下立即拿到獎勵，冷卻 1 天。
// 掉落以古老森林為模板（等級門檻配合 Lv.4 開放而提前），每次再多抽 RuinsExtraRolls 份（2026-11-09 使用者要求）。
const (
	RuinsUnlockLevel     = 4
	RuinsCooldownSeconds = 24 * 3600
)

// RuinsExtraRolls 遺跡探索每次額外多抽的份數範圍 [low, high]。
var RuinsExtraRolls = [2]int{1, 2}

func ruinsArea(forest Area) Area {
	shift := RuinsUnlockLevel - forest.UnlockLevel
	lv := func(level int) int { return max(RuinsUnlockLevel, level+shift) }

	notes := []LevelNote{{RuinsUnlockLevel, "所屬群組修復完成後即可額外探索（每天一次，不佔一般探險）"}}
	for _, n := range forest.LevelNotes {
		notes = append(notes, LevelNote{lv(n.Level), strings.ReplaceAll(n.Text, "森林", "遺跡")})
	}
	pools := make(map[string][]Drop, len(forest.Pools))
	for kind, pool := range forest.Pools {
		out := make([]Drop, len(pool))
		for i, d := range pool {
			d.MinLevel = lv(d.MinLevel) // 等同 Python dataclasses.replace
			out[i] = d
		}
		pools[kind] = out
	}
	return Area{
		Code: "ruins", Emoji: "🏛️", Name: "古代遺跡",
		Description: "群組合力修復的古代遺跡，藏著和古老森林一樣豐富的資源，而且每次都會多找到一些東西。",
		UnlockLevel: RuinsUnlockLevel, DurationSeconds: RuinsCooldownSeconds, Rolls: forest.Rolls,
		AdvancedLevel: lv(forest.AdvancedLevel),
		LevelNotes:    notes,
		Pools:         pools,
		Events: []Event{
			chest([2]int{2, 5}),
			{"rare_find", "🔍 遺跡稀有發現", "在倒塌的石柱下找到前人留下的寶藏。", lv(7), 3, [2]int{1, 2},
				[]string{"rare", "seed"}},
			{"ancient_ruins", "👑 失落王座", "遺跡最深處的王座下藏著古代王國的寶藏！", lv(15), 1, [2]int{5, 10},
				[]string{"rare", "rare"}},
		},
		NeedsRuins: true,
	}
}

func init() {
	Areas["ruins"] = ruinsArea(Areas["forest"])
}

var AreaOrder = []string{"grassland", "forest", "mine", "ruins"}

// ---------- 機器（存在 farm_tools；製作一次永久持有，可啟用／停用） ----------

// Machine 機器。MaterialOrder 保留 Python dict 的素材順序。
type Machine struct {
	Code          string
	Emoji         string
	Name          string
	UnlockLevel   int
	Materials     map[string]int
	MaterialOrder []string
	Description   string
}

var Machines = map[string]Machine{
	"seed_maker": {"seed_maker", "🌱", "種子製造機", 3,
		map[string]int{"wood": 10, "stone": 10, "copper_ore": 5, "gear": 3}, []string{"wood", "stone", "copper_ore", "gear"},
		"把收成的作物做成種子：1 個作物 = 2 顆對應種子（從「🌱 農田」→「🌱 種子製造」選作物與數量）"},
	"auto_harvester": {"auto_harvester", "🚜", "自動收割機", 5,
		map[string]int{"wood": 20, "iron_ore": 10, "gear": 5}, []string{"wood", "iron_ore", "gear"},
		"開啟面板時自動收成成熟的作物（含成熟後才乾枯的作物）"},
	"auto_processor": {"auto_processor", "🏭", "自動加工機", 8,
		map[string]int{"stone": 15, "copper_ore": 15, "iron_ore": 10, "machine_core": 1},
		[]string{"stone", "copper_ore", "iron_ore", "machine_core"},
		"開啟面板時自動領取完成的加工品，並用背包原料開始設定的加工項目"},
	"rare_incubator": {"rare_incubator", "🧬", "稀有作物培育器", 12,
		map[string]int{"wood": 20, "herb": 15, "crystal": 5, "ancient_core": 1},
		[]string{"wood", "herb", "crystal", "ancient_core"},
		"用普通作物與探索素材培育稀有作物種子"},
}

var MachineOrder = []string{"seed_maker", "auto_harvester", "auto_processor", "rare_incubator"}

// 種子製造機：1 個作物 = 2 顆種子；只接受下列品質與突變（做出來的種子一律是普通種子）
const (
	SeedMakerRatio = 2
	SeedMakerMax   = 999
)

var (
	// Go 版放寬（2026-11-14）：原本只接受普通品質、沒有突變的作物，玩家收成大多是優良，選單常常是空的；
	// 品質開放到優良為止（稀有、傳說不行），突變不限
	SeedMakerQualities = []string{"normal", "good"}
	SeedMakerMutations = []string{"none", "giant", "golden", "fast", "variant"}
	// SeedMakerRareCrops true：琥珀瓜、月光花等稀有作物也能製造種子（模組層級可變狀態，測試會修改）
	SeedMakerRareCrops = false
)

// IncubatorRecipe 稀有作物培育器的配方（以加工訂單 craft_orders 執行，building_code = rare_incubator）。
type IncubatorRecipe struct {
	Name       string
	Inputs     map[string]int
	InputOrder []string
	Output     string
	OutputQty  int
	Seconds    int
	Aliases    []string
}

var IncubatorRecipeOrder = []string{"incubate_amber", "incubate_moon"}

var IncubatorRecipes = map[string]IncubatorRecipe{
	"incubate_amber": {"琥珀瓜種子培育", map[string]int{"wheat": 10, "herb": 3}, []string{"wheat", "herb"},
		AmberSeed, 1, 4 * 3600, []string{"琥珀瓜", "琥珀"}},
	"incubate_moon": {"月光花種子培育", map[string]int{"strawberry": 5, "herb": 5, "crystal": 1},
		[]string{"strawberry", "herb", "crystal"}, MoonSeed, 1, 8 * 3600, []string{"月光花", "月光"}},
}

const IncubatorMaxQuantity = 5

// LevelUnlocks 等級解鎖總覽（介面顯示用；實際判斷以 UnlockLevel／MinLevel 為準）。
var LevelUnlocks = []LevelNote{
	{1, "青青草原"}, {2, "草原進階素材"}, {3, "種子製造機"}, {4, "草原稀有發現、古代遺跡（群組修復後）"},
	{5, "古老森林、自動收割機"}, {6, "森林進階素材"}, {7, "森林稀有發現"}, {8, "自動加工機"},
	{9, "森林高級探索獎勵"}, {10, "神秘礦坑"},
	{11, "礦坑進階素材"}, {12, "稀有作物培育器"}, {13, "礦坑稀有發現"}, {14, "高級機器素材"},
	{15, "極稀有探索事件"}, {16, "三個區域完整探索獎勵池"},
}

// ---------- 規則 ----------

// FindArea 以代碼（不分大小寫）或中文名稱找區域；找不到回傳 ""。
func FindArea(key string) string {
	key = strings.TrimSpace(key)
	for _, code := range AreaOrder {
		if strings.ToLower(key) == code || key == Areas[code].Name {
			return code
		}
	}
	return ""
}

func IsAdvanced(area Area, level int) bool { return level >= area.AdvancedLevel }

func Eligible(pool []Drop, level int) []Drop {
	var out []Drop
	for _, d := range pool {
		if level >= d.MinLevel && d.Weight > 0 {
			out = append(out, d)
		}
	}
	return out
}

// PossibleItems 這個區域可能掉落的所有物品代碼；level 為 nil 表示所有等級。
func PossibleItems(area Area, level *int) map[string]bool {
	lv := 10_000
	if level != nil {
		lv = *level
	}
	items := map[string]bool{}
	for _, pool := range area.Pools {
		for _, d := range Eligible(pool, lv) {
			items[d.Item] = true
		}
	}
	if d, ok := Current().SawdustDrop[area.Code]; ok && d[1] > 0 { // 每次探險固定帶回木屑
		items[Sawdust] = true
	}
	for _, e := range area.Events {
		if lv >= e.MinLevel && e.FakeCoins[1] > 0 {
			items[FakeCoin] = true
			break
		}
	}
	return items
}

func weighted[T any](entries []T, weight func(T) int, u float64) T {
	total := 0
	for _, e := range entries {
		total += weight(e)
	}
	point := u * float64(total)
	cumulative := 0
	for _, e := range entries {
		cumulative += weight(e)
		if point < float64(cumulative) {
			return e
		}
	}
	return entries[len(entries)-1]
}

func RollType(bp map[string]int, u float64) string {
	point := int(u * TotalBP)
	cumulative := 0
	for _, t := range RewardTypes {
		cumulative += bp[t]
		if point < cumulative {
			return t
		}
	}
	return RewardTypes[len(RewardTypes)-1]
}

// Result 一次探索的結果。Items 為物品代碼 → 數量，ItemOrder 保留取得順序。
type Result struct {
	Area      string
	Advanced  bool
	Items     map[string]int
	ItemOrder []string
	Events    []string // 觸發的特殊事件名稱
	Types     []string // 每次抽到（含改用）的獎勵類型
}

func NewResult(area string, advanced bool) *Result {
	return &Result{Area: area, Advanced: advanced, Items: map[string]int{}}
}

func (r *Result) Add(item string, qty int) {
	if qty <= 0 {
		return
	}
	if _, ok := r.Items[item]; !ok {
		r.ItemOrder = append(r.ItemOrder, item)
	}
	r.Items[item] += qty
}

func (r *Result) Total() int {
	total := 0
	for _, v := range r.Items {
		total += v
	}
	return total
}

func qty(drop Drop, rng Rng) int {
	return drop.QtyMin + min(drop.QtyMax-drop.QtyMin, int(rng()*float64(drop.QtyMax-drop.QtyMin+1)))
}

func dropWeight(d Drop) int   { return d.Weight }
func eventWeight(e Event) int { return e.Weight }

// rollItem 抽一個指定類型的物品；目前等級沒有可掉落的物品時依 Fallback 改用其他類型。
func rollItem(area Area, kind string, level int, rng Rng, result *Result) {
	seen := map[string]bool{}
	for !seen[kind] {
		seen[kind] = true
		entries := Eligible(area.Pools[kind], level)
		if len(entries) > 0 {
			drop := weighted(entries, dropWeight, rng())
			result.Add(drop.Item, qty(drop, rng))
			result.Types = append(result.Types, kind)
			return
		}
		kind = Fallback[kind]
	}
}

// Explore 產生一次探索的全部獎勵（不檢查解鎖與是否正在探險，由服務層驗證）。extraRolls：額外多抽幾份（守護者獎勵）。
func Explore(areaCode string, level int, rng Rng, extraRolls int) *Result {
	cfg := Current()
	area := cfg.Areas[areaCode]
	advanced := IsAdvanced(area, level)
	bp := cfg.RewardTypeBP["normal"]
	if advanced {
		bp = cfg.RewardTypeBP["advanced"]
	}
	result := NewResult(areaCode, advanced)
	for i := 0; i < area.Rolls+max(0, extraRolls); i++ {
		kind := RollType(bp, rng())
		if kind == "event" {
			var events []Event
			for _, e := range area.Events {
				if level >= e.MinLevel {
					events = append(events, e)
				}
			}
			if len(events) > 0 {
				event := weighted(events, eventWeight, rng())
				result.Events = append(result.Events, event.Name)
				result.Types = append(result.Types, "event")
				low, high := event.FakeCoins[0], event.FakeCoins[1]
				if high > 0 {
					result.Add(FakeCoin, low+min(high-low, int(rng()*float64(high-low+1))))
				}
				for _, bonus := range event.Bonus {
					rollItem(area, bonus, level, rng, result)
				}
				continue
			}
			kind = Fallback["event"]
		}
		rollItem(area, kind, level, rng, result)
	}
	return result
}

// MissingMaterials 缺少的素材 → 還差幾個。
func MissingMaterials(machine Machine, have map[string]int) map[string]int {
	out := map[string]int{}
	for code, need := range machine.Materials {
		if have[code] < need {
			out[code] = need - have[code]
		}
	}
	return out
}

func MaterialName(code string) string {
	if m, ok := Materials[code]; ok {
		return m.Name
	}
	return code
}
