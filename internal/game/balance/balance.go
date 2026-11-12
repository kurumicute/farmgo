// Package balance 遊戲平衡數值集中管理（對應 Python app/game/balance.py）。
//
// 作物的價格、生長時間與收成量存在資料庫 crop_types 表（由 Alembic 初始化），
// 調整作物數值請新增 Migration 或直接更新 crop_types；其餘農場層級的數值在此調整。
// Python dict 的順序有意義（顯示與判定順序），Go 以 *Order 切片保留。
package balance

import (
	"fmt"

	"farmgm/internal/numfmt"
)

const (
	InitialGold              = 500
	InitialPlots             = 4
	InitialLevel             = 1
	InitialWarehouseCapacity = 50
	// 倉庫擴大卷：每次 +50 格，價格依次遞增；最大 300 格
	WarehouseExpansionCode = "warehouse_expansion"
	WarehouseExpansionStep = 50
	WarehouseMaxCapacity   = 300
)

var WarehouseExpansionPrices = []int{4_000, 20_000, 50_000, 100_000, 200_000}

// WarehouseExpansionPrice 下一次擴大倉庫的價格；已達上限時 ok = false。
func WarehouseExpansionPrice(capacity int) (price int, ok bool) {
	if capacity >= WarehouseMaxCapacity {
		return 0, false
	}
	tier := max(0, numfmt.FloorDiv(capacity-InitialWarehouseCapacity, WarehouseExpansionStep))
	return WarehouseExpansionPrices[min(tier, len(WarehouseExpansionPrices)-1)], true
}

// 擴建土地：每次 +1 格，價格依目前格數遞增（第 5 格 500 … 第 16 格 180,000）
const (
	UpgradePlotsAdded = 1
	MaxPlots          = 16 // 每座農場最多土地數（Discord 農田圖片為 4×4）
)

var UpgradePrices = []int{500, 1_000, 2_000, 4_000, 8_000, 20_000, 40_000, 80_000, 120_000, 160_000, 170_000, 180_000}

// UpgradePrice 目前有 plots 格時，下一次擴建的價格；已達上限時 ok = false。
func UpgradePrice(plots int) (price int, ok bool) {
	if plots >= MaxPlots {
		return 0, false
	}
	tier := max(0, plots-InitialPlots)
	return UpgradePrices[min(tier, len(UpgradePrices)-1)], true
}

const (
	// 單次購買／販售數量上限，避免誤輸入過大數字
	MaxTradeQuantity = 9999

	PlayerNameMaxLen = 32
	FarmNameMaxLen   = 40
	RenameFarmCost   = 300 // 更改農場名稱每次的金幣費用
)

// CropSpec 初始作物設定。
type CropSpec struct {
	Code        string
	Name        string
	SeedPrice   int
	GrowSeconds int
	HarvestQty  int
	SellPrice   int
}

// InitialCrops 初始作物設定（供測試參考；遊戲執行時以資料庫 crop_types 為準）。
// Migration 0013 已把資料庫價格調整為：小麥 5/11、胡蘿蔔 12/15、番茄 25/22、草莓 60/34（種子價格/單位售價）
var InitialCrops = []CropSpec{
	{"wheat", "小麥", 10, 3600, 2, 15},
	{"carrot", "胡蘿蔔", 20, 7200, 3, 25},
	{"tomato", "番茄", 35, 14400, 4, 20},
	{"strawberry", "草莓", 60, 28800, 5, 25},
	// 以下 4 種由 Migration 0033 新增（數值就是寫進資料庫的值）
	{"corn", "玉米", 18, 3 * 3600, 3, 20},
	{"pumpkin", "南瓜", 80, 12 * 3600, 4, 55},
	{"grape", "葡萄", 120, 16 * 3600, 6, 45},
	{"watermelon", "西瓜", 150, 24 * 3600, 3, 130},
}

// Range 閉區間 [Low, High]。
type Range struct{ Low, High int }

var (
	CropGrowSecondsRange     = Range{60, 7 * 24 * 3600} // 網站可設定的生長時間範圍
	CropSeedPriceRange       = Range{1, 1_000_000}
	CropHarvestQuantityRange = Range{1, 100}
	CropSellPriceRange       = Range{0, 1_000_000}
)

const (
	CatalogCodePattern = `^[a-z][a-z0-9_]{1,31}$` // 作物、變種、動物、產品代碼（建立後不可修改）
	CatalogNameMaxLen  = 32
)

// ---------- 澆水、乾枯 ----------

// WitherSeconds 成熟後超過此秒數未收成即乾枯，乾枯作物不能收成或被偷，需用 clear 清除
const WitherSeconds = 16 * 3600

// ---------- 偷菜 ----------

const StealMaxPerAction = 2

// StealMaxPerCrop 個別作物每次最多偷幾個（沒列出的用 StealMaxPerAction）
var StealMaxPerCrop = map[string]int{"wheat": 1}

func StealMax(cropCode string) int {
	if v, ok := StealMaxPerCrop[cropCode]; ok {
		return v
	}
	return StealMaxPerAction
}

const (
	// 每位偷菜者每天（台灣時間）對同一位玩家只能成功偷 1 格土地；被防守設施阻止的不算
	StealPlotsPerVictimPerDay = 1
	// 每位玩家每天（台灣時間）最多被幾位不同的人成功偷到；被防守設施阻止的不算
	StealThievesPerVictimPerDay = 3
	GroupNameMaxLen             = 40
	// 農場轉移所屬群組的冷卻時間（首次建立農場後的第一次轉移不需等待）
	GroupTransferCooldownSeconds = 7 * 24 * 3600
	GroupTransferReasonMaxLen    = 200
)

// ---------- 道具 ----------

const (
	FertilizerTimeFactor     = 0.85          // 化肥：剩餘生長時間減少 15%（2026-11-06 由 25% 調降）
	EarthwormDurationSeconds = 3 * 24 * 3600 // 蚯蚓效果持續 3 天；有效期間再次使用再延長 3 天
	EarthwormGrowMultiplier  = 0.85          // 蚯蚓有效期間澆水的作物，生長時間 ×0.85（2026-11-06 由 ×0.75 調降）

	ItemFertilizer   = "fertilizer"
	ItemEarthworm    = "earthworm"
	ToolAutoWaterer  = "auto_waterer"
	ToolAutoPlanter  = "auto_planter"
	ToolAutoSeller   = "auto_seller"
	ToolThiefGlove   = "thief_glove"
	EffectEarthworm  = "earthworm"
	ItemPatrolTicket = "patrol_ticket"
)

// FertilizerPct 說明文字用（Python round((1 - FERTILIZER_TIME_FACTOR) * 100)）。
var FertilizerPct = numfmt.Round((1 - FertilizerTimeFactor) * 100)

var LuxuryTools = []string{ToolAutoPlanter, ToolAutoSeller, ToolThiefGlove}

// Item 道具：名稱、價格、種類（consumable／tool）、說明、別名。
type Item struct {
	Name    string
	Price   int
	Kind    string
	Desc    string
	Aliases []string
}

// ItemOrder 道具顯示順序（Python ITEMS 的 dict 順序）。
var ItemOrder = []string{ItemFertilizer, ItemEarthworm, ToolAutoWaterer, ToolAutoPlanter, ToolAutoSeller, ToolThiefGlove, ItemPatrolTicket}

var Items = map[string]Item{
	ItemFertilizer: {"化肥", 30, "consumable",
		fmt.Sprintf("用於生長中的農田，剩餘生長時間減少 %d%%（每次種植限用一次）", FertilizerPct), []string{"化肥"}},
	ItemEarthworm: {"人工養殖蚯蚓", 300, "consumable",
		fmt.Sprintf("使用時正在生長的作物剩餘時間 ×%v，之後 3 天內澆水的作物生長時間也 ×%v（效果期間再次使用可再延長 3 天）",
			EarthwormGrowMultiplier, EarthwormGrowMultiplier),
		[]string{"蚯蚓", "人工養殖蚯蚓"}},
	ToolAutoWaterer: {"自動澆水器", 3500, "tool", "永久工具：啟用時種植後自動澆水", []string{"澆水器", "自動澆水器"}},
	ToolAutoPlanter: {"自動播種機", 30_000, "tool",
		"奢侈品：啟用時，收成、清除乾枯、擴建或購買種子後，自動從背包隨機挑選種子把空著的農田種滿", []string{"播種機", "自動播種機"}},
	ToolAutoSeller: {"自動販售機", 150_000, "tool",
		"奢侈品：啟用時，收成的農作物依當下價目表自動賣出（不佔倉庫）", []string{"販售機", "自動販售機"}},
	ToolThiefGlove: {"傳說扒手手套", 500_000, "tool",
		"奢侈品：啟用時偷菜不受每日限制，可以偷同群組所有人的所有成熟農田，不限人數與次數" +
			"（每株作物仍只能偷一次、每次最多 2 個，小麥 1 個；防守設施照常判定）",
		[]string{"手套", "扒手手套", "傳說扒手手套"}},
	ItemPatrolTicket: {"巡查券", 60, "consumable",
		"使用後查看自己農場近 7 天所有偷菜嘗試（成功與被阻止，含偷菜者）", []string{"巡查券", "patrol"}},
}

// ---------- 賭場 ----------
// 系統從 1–10 擲一個數字：比大（6–10）／比小（1–5）贏了價值 ×2；猜中指定數字價值 ×5；輸了沒收下注的作物

const (
	GambleMin             = 1
	GambleMax             = 10
	GambleBigFrom         = 6
	GambleHiloMultiplier  = 2
	GambleExactMultiplier = 5
)

// 只有這些品質或突變的作物可以下注
var (
	GambleQualities = []string{"rare", "legendary"}
	GambleMutations = []string{"giant", "golden", "variant"}
	GambleModeOrder = []string{"big", "small", "exact"}
	GambleModes     = map[string]string{"big": "比大（6–10）", "small": "比小（1–5）", "exact": "猜數字"}
)

// ---------- 防守設施 ----------

// Defense 名稱、價格、每座農場上限、阻止機率、守護格數（nil = 全部土地）、別名。
type Defense struct {
	Name        string
	Price       int
	MaxPerFarm  int
	BlockChance float64
	Range       *int
	Aliases     []string
}

func intPtr(v int) *int { return &v }

var Defenses = map[string]Defense{
	"scarecrow": {"稻草人", 400, 2, 0.25, intPtr(3), []string{"稻草人"}},
	"guard_dog": {"警戒犬", 1200, 1, 0.30, nil, []string{"警戒犬", "dog", "狗"}},
	"fence":     {"防盜圍欄", 800, 1, 0.20, intPtr(4), []string{"防盜圍欄", "圍欄"}},
}

// DefenseOrder 判定順序固定：稻草人 → 警戒犬 → 防盜圍欄；同種設施重疊守護同一格只計算一次
var DefenseOrder = []string{"scarecrow", "guard_dog", "fence"}

const (
	// DefenseMaxBlockChance 每次偷菜整體阻止機率上限
	DefenseMaxBlockChance = 0.60
	PatrolDays            = 7
	PatrolLimit           = 20
)

// ---------- 動物養殖 ----------

// AnimalSpec 動物種類與產品（Migration 0008 寫入的初始值）。
type AnimalSpec struct {
	Code         string
	Name         string
	BuyPrice     int
	FeedCost     int
	CycleSeconds int
	ProductCode  string
	ProductName  string
	ProductQty   int
	ProductPrice int
}

var InitialAnimals = []AnimalSpec{
	{"chicken", "雞", 500, 20, 24 * 3600, "egg", "雞蛋", 2, 25},
	{"cow", "牛", 2000, 50, 24 * 3600, "milk", "牛奶", 2, 60},
	{"sheep", "羊", 1500, 35, 48 * 3600, "wool", "羊毛", 1, 120},
}

const AnimalMaxBuy = 12

var (
	AnimalPriceRange           = Range{1, 1_000_000}
	AnimalFeedRange            = Range{0, 100_000}
	AnimalCycleRange           = Range{60, 7 * 24 * 3600}
	AnimalProductQuantityRange = Range{1, 100}
	AnimalProductPriceRange    = Range{0, 1_000_000}
)

// ---------- 建築（畜舍與加工設施） ----------

// Building 名稱、建造價格、每座農場上限、別名。
type Building struct {
	Name       string
	Price      int
	MaxPerFarm int
	Aliases    []string
}

var BuildingOrder = []string{"barn", "mill", "bakery", "jam_workshop"}

var Buildings = map[string]Building{
	"barn":         {"畜舍", 1000, 3, []string{"畜舍"}},
	"mill":         {"磨坊", 2000, 1, []string{"磨坊"}},
	"bakery":       {"烘焙坊", 3500, 1, []string{"烘焙坊"}},
	"jam_workshop": {"果醬工坊", 2500, 1, []string{"果醬工坊"}},
}

const BarnSlots = 4 // 每座畜舍可安置的動物數（任一動物佔 1 欄位）

// ---------- 加工配方 ----------

// Recipe 名稱、所需設施、{原料代碼: 每份數量}、成品代碼、每份產量、每份加工秒數、別名。
type Recipe struct {
	Name       string
	Building   string
	Inputs     map[string]int
	InputOrder []string
	Output     string
	OutputQty  int
	Seconds    int
	Aliases    []string
}

var RecipeOrder = []string{"flour", "bread", "strawberry_jam", "tomato_jam", "grape_jam", "pumpkin_jam", "watermelon_jam",
	"shiitake_bread", "mushroom_soup", "enoki_pancake", "lingzhi_cake", "truffle_bread", "glowcap_pie"}

var Recipes = map[string]Recipe{
	"flour":          {"麵粉", "mill", map[string]int{"wheat": 3}, []string{"wheat"}, "flour", 1, 30 * 60, []string{"麵粉"}},
	"bread":          {"麵包", "bakery", map[string]int{"flour": 2}, []string{"flour"}, "bread", 1, 60 * 60, []string{"麵包"}},
	"strawberry_jam": {"草莓果醬", "jam_workshop", map[string]int{"strawberry": 3}, []string{"strawberry"}, "strawberry_jam", 1, 60 * 60, []string{"草莓果醬", "果醬", "jam"}},
	// Go 版新增（2026-11-11）：果醬工坊另外可以加工番茄、葡萄、南瓜、西瓜；售價約為原料市價的 2.9 倍（與草莓果醬相同比例）
	"tomato_jam":     {"番茄醬", "jam_workshop", map[string]int{"tomato": 3}, []string{"tomato"}, "tomato_jam", 1, 45 * 60, []string{"番茄醬", "番茄果醬"}},
	"grape_jam":      {"葡萄果醬", "jam_workshop", map[string]int{"grape": 3}, []string{"grape"}, "grape_jam", 1, 75 * 60, []string{"葡萄果醬", "葡萄醬"}},
	"pumpkin_jam":    {"南瓜果醬", "jam_workshop", map[string]int{"pumpkin": 3}, []string{"pumpkin"}, "pumpkin_jam", 1, 90 * 60, []string{"南瓜果醬", "南瓜醬"}},
	"watermelon_jam": {"西瓜果醬", "jam_workshop", map[string]int{"watermelon": 3}, []string{"watermelon"}, "watermelon_jam", 1, 120 * 60, []string{"西瓜果醬", "西瓜醬"}},
	// Go 版新增（2026-11-12）：烘焙坊用地下菇房的菇類做料理；售價約為原料售價的 1.2 倍（菇類本身已經很值錢）
	"shiitake_bread": {"香菇麵包", "bakery", map[string]int{"shiitake": 2, "flour": 1}, []string{"shiitake", "flour"}, "shiitake_bread", 1, 60 * 60, []string{"香菇麵包"}},
	"mushroom_soup":  {"洋菇濃湯", "bakery", map[string]int{"button_mushroom": 2, "flour": 1}, []string{"button_mushroom", "flour"}, "mushroom_soup", 1, 60 * 60, []string{"洋菇濃湯", "蘑菇濃湯"}},
	"enoki_pancake":  {"金針菇煎餅", "bakery", map[string]int{"enoki": 2, "flour": 1}, []string{"enoki", "flour"}, "enoki_pancake", 1, 75 * 60, []string{"金針菇煎餅"}},
	"lingzhi_cake":   {"靈芝養生糕", "bakery", map[string]int{"lingzhi": 2, "flour": 2}, []string{"lingzhi", "flour"}, "lingzhi_cake", 1, 120 * 60, []string{"靈芝養生糕", "靈芝糕"}},
	"truffle_bread":  {"松露麵包", "bakery", map[string]int{"truffle": 2, "flour": 2}, []string{"truffle", "flour"}, "truffle_bread", 1, 150 * 60, []string{"松露麵包"}},
	"glowcap_pie":    {"發光菇派", "bakery", map[string]int{"glowcap": 2, "flour": 2}, []string{"glowcap", "flour"}, "glowcap_pie", 1, 180 * 60, []string{"發光菇派"}},
}

const (
	CraftMaxQuantity = 10 // 單張訂單最多份數
	CraftQueueMax    = 10 // 每種設施最多同時有幾張未完成的訂單（排隊依序加工，前一張完成才開始下一張）
)

// ---------- 加工品（可販售商品；動物產品在資料庫 animal_types） ----------

// Good 名稱、單位售價、別名。
type Good struct {
	Name      string
	SellPrice int
	Aliases   []string
}

var GoodOrder = []string{"flour", "bread", "strawberry_jam", "tomato_jam", "grape_jam", "pumpkin_jam", "watermelon_jam",
	"shiitake", "button_mushroom", "enoki", "lingzhi", "truffle", "glowcap",
	"shiitake_bread", "mushroom_soup", "enoki_pancake", "lingzhi_cake", "truffle_bread", "glowcap_pie"}

var Goods = map[string]Good{
	"flour":          {"麵粉", 45, []string{"麵粉"}},
	"bread":          {"麵包", 200, []string{"麵包"}},
	"strawberry_jam": {"草莓果醬", 300, []string{"草莓果醬", "果醬", "jam"}},
	"tomato_jam":     {"番茄醬", 195, []string{"番茄醬", "番茄果醬"}},
	"grape_jam":      {"葡萄果醬", 395, []string{"葡萄果醬", "葡萄醬"}},
	"pumpkin_jam":    {"南瓜果醬", 485, []string{"南瓜果醬", "南瓜醬"}},
	"watermelon_jam": {"西瓜果醬", 1145, []string{"西瓜果醬", "西瓜醬"}},
	// Go 版新增（2026-11-12）：地下菇房的菇類（game/mushroom），可以直接販售；香菇 200 為基準，其他菇在這基礎上加價
	"shiitake":        {"香菇", 200, []string{"香菇"}},
	"button_mushroom": {"洋菇", 230, []string{"洋菇", "蘑菇"}},
	"enoki":           {"金針菇", 260, []string{"金針菇"}},
	"lingzhi":         {"靈芝", 520, []string{"靈芝"}},
	"truffle":         {"松露", 700, []string{"松露"}},
	"glowcap":         {"發光菇", 900, []string{"發光菇"}},
	// 菇類料理（烘焙坊）
	"shiitake_bread": {"香菇麵包", 560, []string{"香菇麵包"}},
	"mushroom_soup":  {"洋菇濃湯", 620, []string{"洋菇濃湯", "蘑菇濃湯"}},
	"enoki_pancake":  {"金針菇煎餅", 680, []string{"金針菇煎餅"}},
	"lingzhi_cake":   {"靈芝養生糕", 1350, []string{"靈芝養生糕", "靈芝糕"}},
	"truffle_bread":  {"松露麵包", 1800, []string{"松露麵包"}},
	"glowcap_pie":    {"發光菇派", 2300, []string{"發光菇派"}},
}

// ---------- 群組共同任務 ----------

// QuestType 名稱與可用計量方式；amount 只適用販售（金額）。
type QuestType struct {
	Name    string
	Metrics []string
}

var QuestTypeOrder = []string{"harvest", "sell", "build", "animal", "defense"}

var QuestTypes = map[string]QuestType{
	"harvest": {"共同收成", []string{"quantity"}},
	"sell":    {"共同販售", []string{"quantity", "amount"}},
	"build":   {"農場建設", []string{"quantity"}},
	"animal":  {"動物生產", []string{"quantity"}},
	"defense": {"防守任務", []string{"quantity"}},
}

var (
	QuestRewardTypes     = []string{"gold", "fertilizer", "earthworm", "patrol_ticket"}
	QuestRewardModeOrder = []string{"fixed", "proportional"}
	QuestRewardModes     = map[string]string{"fixed": "每位貢獻者固定獎勵", "proportional": "獎池依貢獻比例分配"}
)

const (
	QuestMaxTarget     = 1_000_000
	QuestMaxGoldReward = 1_000_000
	QuestMaxItemReward = 100
	EventMaxDays       = 90 // 任務與競賽最長期間
)

// ---------- 群組競賽 ----------

// CompetitionType 名稱與計分說明。
type CompetitionType struct {
	Name string
	Desc string
}

var CompetitionTypeOrder = []string{"harvest", "sales", "development", "defense"}

var CompetitionTypes = map[string]CompetitionType{
	"harvest":     {"群組收成大賽", "有效收成數量（每收成 1 個農產品 1 分）"},
	"sales":       {"群組銷售大賽", "成功販售收入（每 1 金幣 1 分）"},
	"development": {"群組農場發展大賽", "土地擴建每次 10 分、建造設施每座 5 分"},
	"defense":     {"群組防守大賽", "成功阻止偷菜每次 1 分"},
}

var DevelopmentPoints = map[string]int{"upgrade": 10, "build": 5}

const (
	CompetitionRewardRanks = 3 // 前幾名群組有獎勵
	CompetitionMaxReward   = 100_000
	// 截止後再等待此秒數才結算，確保截止前開始的操作都已提交
	CompetitionSettleGraceSeconds = 60
)

// ---------- 作物品質與突變 ----------

// 依序排列（判定時依此順序切分機率區間）
var (
	QualityOrder  = []string{"normal", "good", "rare", "legendary"}
	Qualities     = map[string]string{"normal": "普通", "good": "優良", "rare": "稀有", "legendary": "傳說"}
	MutationOrder = []string{"none", "giant", "golden", "fast", "variant"}
	Mutations     = map[string]string{"none": "無", "giant": "巨型作物", "golden": "黃金作物", "fast": "快速生長", "variant": "珍稀變種"}
)

// Variant 珍稀變種代碼與名稱。
type Variant struct{ Code, Name string }

// InitialVariants 初始作物的變種（供測試參考；實際存在 crop_types.variant_code／variant_name）。
var InitialVariants = map[string]Variant{
	"wheat":      {"amber_wheat", "琥珀小麥"},
	"carrot":     {"amethyst_carrot", "紫晶胡蘿蔔"},
	"tomato":     {"black_tomato", "黑鑽番茄"},
	"strawberry": {"snow_strawberry", "白雪草莓"},
	"corn":       {"golden_corn", "金粒玉米"},
	"pumpkin":    {"moon_pumpkin", "月影南瓜"},
	"grape":      {"crystal_grape", "水晶葡萄"},
	"watermelon": {"starry_watermelon", "星紋西瓜"},
}

const MinGrowSeconds = 60 // 任何加速後的最短生長時間

// DefaultGenetics 預設設定（資料庫中沒有設定時使用）。機率單位為萬分比（bp）；倍率單位為百分比。
// 每次呼叫回傳新的副本，呼叫端可自由修改。
func DefaultGenetics() GeneticsSettings {
	return GeneticsSettings{
		QualityBP:            map[string]int{"normal": 7000, "good": 2000, "rare": 800, "legendary": 200},
		QualityPricePct:      map[string]int{"normal": 100, "good": 120, "rare": 150, "legendary": 200},
		MutationBP:           map[string]int{"giant": 300, "golden": 100, "fast": 300, "variant": 100},
		GiantMultiplier:      2,
		GoldenPricePct:       300,
		FastReductionSeconds: 7200,
		VariantPricePct:      400,
		LevelBonusBP:         50,
		LevelBonusMaxBP:      500,
	}
}

// GeneticsSettings 品質與突變設定（JSON 欄位名稱與 Python／資料庫相同）。
type GeneticsSettings struct {
	QualityBP            map[string]int `json:"quality_bp"`
	QualityPricePct      map[string]int `json:"quality_price_pct"`
	MutationBP           map[string]int `json:"mutation_bp"`
	GiantMultiplier      int            `json:"giant_multiplier"`
	GoldenPricePct       int            `json:"golden_price_pct"`
	FastReductionSeconds int            `json:"fast_reduction_seconds"`
	VariantPricePct      int            `json:"variant_price_pct"`
	LevelBonusBP         int            `json:"level_bonus_bp"`
	LevelBonusMaxBP      int            `json:"level_bonus_max_bp"`
}

// ---------- 限時全服事件 ----------

var (
	EventScheduleTypeOrder = []string{"once", "daily", "weekly"}
	EventScheduleTypes     = map[string]string{"once": "單次", "daily": "每日", "weekly": "每週"}
	WeekdayNames           = []string{"一", "二", "三", "四", "五", "六", "日"} // 位元 0 = 週一
)

const (
	EventNameMaxLen = 60
	EventDescMaxLen = 300
)

// 單一事件的設定範圍（與資料庫 CHECK 一致）
var (
	EventSellPricePctRange     = Range{100, 300} // 售價倍率（%）
	EventGrowReductionPctRange = Range{0, 90}    // 生長時間減少（%）
	EventQualityBonusBPRange   = Range{0, 3000}  // 品質加成（萬分比，從普通移到較高品質）
	EventMutationBonusBPRange  = Range{0, 3000}  // 突變機率加成（萬分比，依各突變比例分配）
	EventHarvestBonusPctRange  = Range{0, 200}   // 收成數量加成（%）
	EventFreeWaterRange        = Range{0, 1}     // 1 = 免澆水：活動期間種下的作物立即開始生長
	EventHarvestLossRange      = Range{0, 10}    // 大風颳走：收成時每格少 N 個（至少保留 1 個）
)

// 多個事件同時生效時加總，但不超過下列上限
const (
	EventMaxSellBonusPct     = 200 // 最多 ×3
	EventMaxGrowReductionPct = 75
	EventMaxQualityBonusBP   = 3000
	EventMaxMutationBonusBP  = 3000
	EventMaxHarvestBonusPct  = 200
	EventMaxFreeWater        = 1
	EventMaxHarvestLoss      = 10
)

// ---------- 管理員調整 ----------

const (
	AdminMaxPlots        = MaxPlots // 管理員新增土地也不能超過上限
	AdminMaxItemQuantity = 100_000
	AdminMaxGold         = 1_000_000_000_000
	AdminMaxLevel        = 100 // 只影響品質加成，不會自動增減土地
)

// ---------- 排名獎勵（每日／每週／每月結算） ----------

const (
	RankingNameMaxLen         = 60
	RankingMaxRanks           = 10 // 每條規則最多設定前幾名的獎勵
	RankingMaxGold            = 10_000_000
	RankingMaxItems           = 100
	RankingSettleGraceSeconds = 60  // 期間結束後再等待此秒數才結算
	RankingMaxCatchupPeriods  = 400 // 服務長時間停止後，一次最多補結算的期間數
)
