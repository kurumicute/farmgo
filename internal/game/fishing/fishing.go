// Package fishing 釣魚小遊戲的純規則：稀有度、設定驗證、釣具效果、魚種抽選與拉扯博弈。不依賴資料庫或 Discord。
// 對應 Python app/game/fishing.py。
//
// 重要設計（需求：稀有度機率與釣魚成功率是兩個獨立設定）：
//   - 稀有度只決定「出現哪一種魚」與「拉扯有多難」（體力、起始距離、掙扎機率），不會降低成功率。
//   - 基礎成功率（預設 90%）在拋竿時獨立判定：沒通過時，魚會在拉扯開始後「脫鉤」（與稀有度、裝備無關）。
//   - 拉扯中魚的下一個動作會事先顯示（💬 提示），依提示操作（衝刺前放線、平靜時收線）一定能把魚拉上來；
//     因此依提示操作的玩家，各稀有度的整體成功率都接近基礎成功率。不看提示亂按才會斷線或讓魚逃走。
//   - 魚竿（收線力、穩定度、反應時間）與魚線（最大張力）提高操作容錯；魚餌提高稀有魚的出現機率並縮短等待。
//   - 裝備再好也不能只靠一直收線：連續收線的額外張力依魚線上限按比例增加（好魚線一樣會斷）、
//     魚還有力氣時拉到水邊也拉不上來（LandStaminaRatio）、魚會隨機往左或右竄，要按同方向壓竿（Dodge*）。
//   - 防腳本（Guard*）：定期與操作時間異常時要通過人機確認，答錯暫停釣魚；只影響能不能拋竿，不扣任何資產。
//
// 所有數值可由管理員調整（fishing_settings 版本紀錄）；拋竿時把設定與裝備效果存進釣魚 Session，
// 之後修改設定不影響已經開始的釣魚。
//
// 亂數一律以參數注入（RNG 對應 Python 的 rng()，回傳 [0, 1)），呼叫順序與 Python 相同。
package fishing

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

// RNG 回傳 [0, 1) 的亂數（對應 Python random.random）。
type RNG func() float64

// Effects 釣具效果（JSON 物件，值皆為整數）；缺欄位時讀到 0，與 Python dict.get(key, 0) 相同。
type Effects = map[string]int

func getOr(e Effects, key string, def int) int {
	if v, ok := e[key]; ok {
		return v
	}
	return def
}

var (
	Rarities    = []string{"common", "rare", "epic", "legendary"}
	RarityNames = map[string]string{"common": "普通", "rare": "稀有", "epic": "史詩", "legendary": "傳說"}
	RarityEmoji = map[string]string{"common": "⚪", "rare": "🔵", "epic": "🟣", "legendary": "🟡"}
)

// 每種魚的售價由管理員自由設定，不受稀有度限制；上限只是避免超出資料庫整數欄位
const (
	PriceMin = 0
	PriceMax = 1_000_000_000
)

// PriceRow 計算售價範圍用的魚種欄位。
type PriceRow struct {
	Rarity   string
	MinPrice int
	MaxPrice int
}

// PriceSpan 某稀有度目前魚種的售價範圍（最低售價的最小值～最高售價的最大值），沒有魚時 ok = false。
func PriceSpan(fishList []PriceRow, rarity string) (low, high int, ok bool) {
	for _, f := range fishList {
		if f.Rarity != rarity {
			continue
		}
		if !ok {
			low, high, ok = f.MinPrice, f.MaxPrice, true
			continue
		}
		low = min(low, f.MinPrice)
		high = max(high, f.MaxPrice)
	}
	return low, high, ok
}

var (
	GearKinds     = []string{"rod", "line", "bait"}
	GearKindNames = map[string]string{"rod": "魚竿", "line": "魚線", "bait": "魚餌"}
	GearKindEmoji = map[string]string{"rod": "🎋", "line": "🪢", "bait": "🪱"}

	SteerActions = []string{"left", "right"}
	Actions      = []string{"reel", "release", "wait", "left", "right"}
	ActionNames  = map[string]string{"reel": "收線", "release": "放線", "wait": "等待", "left": "左壓", "right": "右壓"}

	// Session 狀態：casting（等魚上鉤，含咬鉤反應時間）、fighting（拉扯中）；其餘為結束狀態
	ActiveStatuses = []string{"casting", "fighting"}
	EndStatuses    = []string{"landed", "escaped", "line_broken", "slipped", "missed", "timeout", "abandoned"}
	StatusNames    = map[string]string{
		"casting": "等待上鉤", "fighting": "拉扯中", "landed": "成功釣起", "escaped": "魚逃走了", "line_broken": "斷線",
		"slipped": "脫鉤", "missed": "沒有及時拉竿", "timeout": "操作逾時", "abandoned": "放棄／中斷",
	}
	FailStatuses = []string{"escaped", "line_broken", "slipped", "missed", "timeout", "abandoned"}
)

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

const (
	EscapeDistance       = 100 // 距離達到上限：魚逃走
	BaseMaxTension       = 100 // 沒有魚線加成時的最大張力
	StartTension         = 20
	ReactionGraceSeconds = 1.5     // 網路延遲的寬限（不顯示給玩家）
	SessionMaxSeconds    = 15 * 60 // 單次釣魚最長時間（避免殘留）
	TiredRatio           = 0.3     // 體力低於 30% 時，掙扎與衝刺機率減半
)

// SlipTurns 基礎成功率沒通過時，在第幾次拉扯操作脫鉤
var SlipTurns = []int{1, 2}

// FishDifficultyStamina 魚種掙扎難度 → 體力倍率
var FishDifficultyStamina = map[int]float64{1: 0.8, 2: 0.9, 3: 1.0, 4: 1.15, 5: 1.3}

// PlayerAction 玩家操作的效果（距離為負 = 拉近）；SpikeMul：這回合魚的動作造成的張力增加倍率（放線時魚線鬆，
// 衝刺不易繃緊）；DashMul：魚衝刺拉開的距離倍率（等待＝穩住魚竿，魚只能拉走一半距離）
type PlayerAction struct {
	Tension  float64
	Stamina  float64
	Distance float64
	SpikeMul float64
	DashMul  float64
}

var PlayerActions = map[string]PlayerAction{
	"reel":    {14, -5, 0, 1.0, 1.0}, // 距離由魚竿收線力決定
	"release": {-30, 0, 6, 0.5, 1.0},
	"wait":    {-12, 3, 0, 0.8, 0.5},
}

// FishIntent 魚的動作：張力、距離（依剩餘體力比例縮放 50%–100%）、體力消耗
type FishIntent struct {
	Tension  float64
	Distance float64
	Stamina  float64
}

var FishIntents = map[string]FishIntent{
	"calm":     {-6, 0, 0},
	"struggle": {12, 4, -4},
	"surge":    {22, 9, -12},
	"strong":   {32, 13, -18},
	"tired":    {-8, 0, 0},
}

var IntentText = map[string]string{
	"calm":     "🌊 魚很平靜，現在收線正好！",
	"struggle": "💦 魚在掙扎，小心張力。",
	"surge":    "⚡ 魚準備衝刺！",
	"strong":   "💥 魚正在猛烈掙扎，就要強力衝刺了！",
	"tired":    "😮‍💨 魚累了，快收線！",
}

// 拉扯（2026-11-10 簡化）：每條魚要「成功拉幾下」才上岸，依稀有度在範圍內隨機（成功收線或壓對方向各算 1 下）。
// 防連點：每場一定有魚竄向一邊（ForcedDodges 次，在隨機的進度出現），只有按同方向壓竿才能過關；
// 按其他任何鍵都不算進度、張力暴增，而且魚會繼續竄（換方向）直到壓對，所以只按收線一定會斷線。
var (
	Pulls        = map[string][2]int{"common": {2, 4}, "rare": {3, 5}, "epic": {4, 6}, "legendary": {5, 7}}
	ForcedDodges = map[string]int{"common": 1, "rare": 1, "epic": 2, "legendary": 2}
)

const (
	// 連續收線：每多連續一次，額外增加「魚線最大張力的 N%」；放線或壓中方向歸零，等待減 ReelStreakWaitDecay 次
	ReelStreakTensionPct = 4
	ReelSpikeMul         = 0.5 // 收線時魚衝擊造成的張力只算一半（魚竿穩定度再降低）
	ReelStreakWaitDecay  = 2
	SteerIdleTension     = 8   // 魚沒有竄動時亂壓竿
	ReleaseSlack         = 6   // 放線讓魚游遠一點
	WaitTension          = -25 // 等待：張力下降（魚的衝擊不再增加張力）
)

// DodgeHit 壓中：張力下降、算 1 下
var DodgeHit = struct{ Tension float64 }{-15}

// DodgeMiss 沒壓中（按錯方向或其他操作）：張力暴增、魚被拉遠、不算進度
var DodgeMiss = struct{ Tension, Distance float64 }{18, 8}

var DodgeText = map[string]string{"left": "⬅️ 魚猛然往左邊竄！快往同方向壓竿！", "right": "➡️ 魚猛然往右邊竄！快往同方向壓竿！"}

// 防腳本：只影響能不能拋竿，不扣金幣或魚。
// GuardEnabled 測試預設關閉（ManualClock 的操作間隔都是 0），防腳本測試自行開啟。
var GuardEnabled = true

// GuardCheckEveryCasts 每隔這麼多次拋竿（隨機）要通過一次人機確認
var GuardCheckEveryCasts = [2]int{25, 45}

const (
	GuardChallengeSeconds    = 180 // 題目有效時間；過期換一題（不算答錯）
	GuardLockMinutes         = 5   // 答錯暫停釣魚；連續答錯加倍
	GuardLockMaxMinutes      = 60
	GuardSuspicionLimit      = 6    // 可疑分數到這裡，下一次拋竿就要確認（通過後歸零）
	GuardFastActionSeconds   = 0.5  // 畫面更新後到按下一步的最短合理時間（含 Discord 來回與人的反應）
	GuardFastHookSeconds     = 0.6  // 上鉤後多久內拉竿不合理（面板本身就要 0.4 秒後才更新畫面）
	GuardRegularMinSamples   = 6    // 一場至少這麼多次操作才判斷節奏
	GuardRegularStdevSeconds = 0.08 // 操作間隔的標準差低於這個值：節奏規律得不像人
	GuardGapsKept            = 30   // 一場最多保留幾個操作間隔
	GuardOptions             = 5
)

var GuardScore = map[string]int{"fast_action": 1, "fast_hook": 2, "regular": 3}

// GuardCreature 題目：只寫中文名稱、按鈕只有 emoji，單純比對文字的腳本答不出來
type GuardCreature struct{ Emoji, Name string }

var GuardCreatureOrder = []string{"puffer", "tropical", "shrimp", "crab", "octopus", "squid", "whale", "shark", "turtle", "lobster"}

var GuardCreatures = map[string]GuardCreature{
	"puffer": {"🐡", "河豚"}, "tropical": {"🐠", "熱帶魚"}, "shrimp": {"🦐", "蝦子"}, "crab": {"🦀", "螃蟹"},
	"octopus": {"🐙", "章魚"}, "squid": {"🦑", "魷魚"}, "whale": {"🐳", "鯨魚"}, "shark": {"🦈", "鯊魚"},
	"turtle": {"🐢", "烏龜"}, "lobster": {"🦞", "龍蝦"},
}

// Difficulty 某稀有度的拉扯難度（JSON 欄位名稱與 Python 相同）。
type Difficulty struct {
	Stamina     int `json:"stamina"`
	Distance    int `json:"distance"`
	StrugglePct int `json:"struggle_pct"`
	SurgePct    int `json:"surge_pct"`
	StrongPct   int `json:"strong_pct"`
	ChainPct    int `json:"chain_pct"`
}

func (d Difficulty) get(key string) int {
	switch key {
	case "stamina":
		return d.Stamina
	case "distance":
		return d.Distance
	case "struggle_pct":
		return d.StrugglePct
	case "surge_pct":
		return d.SurgePct
	case "strong_pct":
		return d.StrongPct
	case "chain_pct":
		return d.ChainPct
	}
	return 0
}

// Settings 釣魚設定（fishing_settings 的 JSON；欄位名稱與 Python 相同）。
type Settings struct {
	RarityBP             map[string]int        `json:"rarity_bp"`
	BaseSuccessPct       int                   `json:"base_success_pct"`
	BiteWaitMinSeconds   int                   `json:"bite_wait_min_seconds"`
	BiteWaitMaxSeconds   int                   `json:"bite_wait_max_seconds"`
	ReactionSeconds      int                   `json:"reaction_seconds"`
	ActionTimeoutSeconds int                   `json:"action_timeout_seconds"`
	BasketCapacity       int                   `json:"basket_capacity"`
	Difficulty           map[string]Difficulty `json:"difficulty"`
}

func (s *Settings) setInt(key string, v int) {
	switch key {
	case "base_success_pct":
		s.BaseSuccessPct = v
	case "bite_wait_min_seconds":
		s.BiteWaitMinSeconds = v
	case "bite_wait_max_seconds":
		s.BiteWaitMaxSeconds = v
	case "reaction_seconds":
		s.ReactionSeconds = v
	case "action_timeout_seconds":
		s.ActionTimeoutSeconds = v
	case "basket_capacity":
		s.BasketCapacity = v
	}
}

// Raw 轉成 JSON 物件（整數為 json.Number），可再交給 ValidateSettings。
func (s Settings) Raw() map[string]any {
	data, _ := json.Marshal(s)
	v, _ := featurecfg.Decode(data)
	return v
}

// DefaultDifficulty 預設拉扯難度（每次回傳新的副本）。
func DefaultDifficulty() map[string]Difficulty {
	return map[string]Difficulty{
		"common":    {Stamina: 40, Distance: 40, StrugglePct: 30, SurgePct: 10, StrongPct: 0, ChainPct: 0},
		"rare":      {Stamina: 60, Distance: 50, StrugglePct: 30, SurgePct: 20, StrongPct: 5, ChainPct: 0},
		"epic":      {Stamina: 80, Distance: 55, StrugglePct: 25, SurgePct: 25, StrongPct: 15, ChainPct: 20},
		"legendary": {Stamina: 100, Distance: 55, StrugglePct: 20, SurgePct: 25, StrongPct: 25, ChainPct: 40},
	}
}

// DefaultSettings 預設設定（每次回傳新的副本）。
func DefaultSettings() Settings {
	return Settings{
		RarityBP:             map[string]int{"common": 8000, "rare": 1500, "epic": 400, "legendary": 100}, // 萬分比，合計 10000
		BaseSuccessPct:       90,
		BiteWaitMinSeconds:   5,
		BiteWaitMaxSeconds:   12,
		ReactionSeconds:      5,
		ActionTimeoutSeconds: 20,
		BasketCapacity:       20,
		Difficulty:           DefaultDifficulty(),
	}
}

// Limit 有名稱的整數範圍（保留 Python dict 的順序）。
type Limit struct {
	Key       string
	Low, High int
}

var DifficultyLimits = []Limit{
	{"stamina", 10, 300}, {"distance", 20, 90}, {"struggle_pct", 0, 100}, {"surge_pct", 0, 100},
	{"strong_pct", 0, 100}, {"chain_pct", 0, 90},
}

var SettingLimits = []Limit{
	{"base_success_pct", 50, 100},
	{"bite_wait_min_seconds", 1, 30},
	{"bite_wait_max_seconds", 1, 60},
	{"reaction_seconds", 2, 15},
	{"action_timeout_seconds", 5, 60},
	{"basket_capacity", 5, 200},
}

// GearEffectLimits 釣具效果欄位與範圍（管理員可調整）
var GearEffectLimits = map[string][]Limit{
	"rod":  {{"reel_power", 8, 30}, {"control_pct", 0, 60}, {"reaction_bonus_ms", 0, 5000}},
	"line": {{"max_tension", 80, 250}},
	"bait": {{"rare_bp", 0, 3000}, {"epic_bp", 0, 2000}, {"legendary_bp", 0, 1000}, {"bite_speed_pct", 0, 80}},
}

var GearEffectNames = map[string]string{
	"reel_power": "收線力", "control_pct": "穩定度（減少張力 %）", "reaction_bonus_ms": "反應時間加成（毫秒）",
	"max_tension": "最大張力", "rare_bp": "稀有機率加成（萬分比）", "epic_bp": "史詩機率加成（萬分比）",
	"legendary_bp": "傳說機率加成（萬分比）", "bite_speed_pct": "上鉤速度加快 %",
}

var (
	GearPriceRange    = [2]int{1, 1_000_000}
	GearLevelRange    = [2]int{1, 100}
	GearStockRange    = [2]int{0, 1_000_000}
	FishWeightRangeG  = [2]int{10, 500_000} // 10 公克 – 500 公斤
	FishDifficultyRng = [2]int{1, 5}
)

const (
	BaitMaxHold          = 999
	BaitMaxBuy           = 99
	SellMaxPerAction     = 200
	ShareCooldownSeconds = 10 // 同一隻魚連續分享的最短間隔（避免洗版）
)

// FishSpec 初始魚種：code, 名稱, emoji, 稀有度, 最低價, 最高價, 最小重量 g, 最大重量 g, 掙扎難度, 說明
type FishSpec struct {
	Code       string
	Name       string
	Emoji      string
	Rarity     string
	MinPrice   int
	MaxPrice   int
	MinWeightG int
	MaxWeightG int
	Difficulty int
	Desc       string
}

// InitialFish 初始魚種（與 Migration 0016、0033、0035 相同；測試用來重設目錄。正式資料以資料庫為準）
var InitialFish = []FishSpec{
	{"pale_chub", "溪哥", "🐟", "common", 3, 18, 20, 120, 1, "溪流裡成群游動的小魚。"},
	{"loach", "泥鰍", "🐟", "common", 5, 23, 30, 200, 1, "喜歡躲在泥巴裡，滑溜溜的。"},
	{"crucian_carp", "鯽魚", "🐟", "common", 3, 27, 100, 800, 2, "池塘裡最常見的魚。"},
	{"tilapia", "吳郭魚", "🐟", "common", 8, 35, 300, 1500, 2, "適應力超強，到處都釣得到。"},
	{"common_carp", "鯉魚", "🐟", "common", 18, 45, 1000, 8000, 3, "力氣不小的大型淡水魚。"},
	{"sweetfish", "香魚", "🐠", "rare", 68, 113, 100, 400, 2, "只住在清澈溪流，帶有淡淡香氣。"},
	{"sea_bass", "鱸魚", "🐠", "rare", 48, 90, 1000, 6000, 3, "會突然衝刺的掠食魚。"},
	{"rainbow_trout", "虹鱒", "🐠", "rare", 57, 102, 800, 4000, 3, "身上有彩虹般的光澤。"},
	{"catfish", "鯰魚", "🐠", "rare", 48, 86, 1500, 12000, 4, "夜行性，咬餌後會往深處鑽。"},
	{"salmon", "鮭魚", "🐡", "epic", 116, 180, 2000, 10000, 3, "逆流而上的勇者。"},
	{"mahi_mahi", "鬼頭刀", "🐡", "epic", 125, 203, 2000, 15000, 4, "速度飛快，會連續跳出水面。"},
	{"grouper", "石斑魚", "🐡", "epic", 135, 215, 3000, 25000, 4, "被釣到會拚命往礁石裡躲。"},
	{"formosan_salmon", "櫻花鉤吻鮭", "🌸", "legendary", 270, 428, 200, 1500, 3, "冰河時期留下的國寶魚。"},
	{"golden_koi", "黃金錦鯉", "🎏", "legendary", 315, 450, 3000, 12000, 4, "傳說釣到就會好運連連。"},
	{"oarfish", "皇帶魚", "🐉", "legendary", 228, 405, 20000, 150000, 5, "深海中的神秘巨魚。"},
	// 以下 20 種由 Migration 0033 新增（普通 6、稀有 6、史詩 5、傳說 3；部分史詩／傳說魚要用指定的高級魚餌）
	{"goby", "鰕虎", "🐟", "common", 5, 21, 10, 80, 1, "趴在溪底石頭上的小魚，一點也不怕人。"},
	{"pond_smelt", "公魚", "🐟", "common", 3, 14, 10, 60, 1, "冬天湖面上最受歡迎的小魚。"},
	{"bluegill", "藍鰓太陽魚", "🐟", "common", 5, 23, 50, 400, 1, "鰓蓋上有一抹藍色，貪吃又好釣。"},
	{"milkfish", "虱目魚", "🐟", "common", 9, 32, 300, 2000, 2, "魚塭裡的國民魚，刺很多但很好吃。"},
	{"mullet", "烏魚", "🐟", "common", 12, 36, 500, 3000, 2, "冬至前後成群洄游，烏魚子就是牠的。"},
	{"snakehead", "鱧魚", "🐟", "common", 18, 45, 1000, 6000, 3, "俗稱雷魚，咬餌兇猛，力氣比外表大。"},
	{"mackerel", "鯖魚", "🐠", "rare", 48, 80, 200, 1200, 2, "背上有波浪花紋，成群在海面附近游動。"},
	{"flounder", "比目魚", "🐠", "rare", 48, 86, 300, 3000, 2, "兩隻眼睛長在同一邊，平貼著海底。"},
	{"yellowfin_seabream", "黃鰭鯛", "🐠", "rare", 50, 90, 300, 2000, 2, "河口常見的鯛魚，鰭帶著亮黃色。"},
	{"black_seabream", "黑鯛", "🐠", "rare", 54, 95, 400, 3000, 3, "警戒心很強，磯釣客的夢幻對手。"},
	{"amberjack", "紅魽", "🐠", "rare", 57, 102, 1500, 9000, 3, "會繞著礁石狂奔的大型掠食魚。"},
	{"eel", "鰻魚", "🐍", "rare", 68, 113, 500, 3000, 4, "滑溜又有力，咬到餌就往石縫裡鑽。"},
	{"lionfish", "獅子魚", "🦁", "epic", 125, 192, 300, 1200, 3, "華麗的鰭條藏著毒刺，小心別被扎到。"},
	{"napoleon_fish", "曲紋唇魚", "🐡", "epic", 135, 215, 5000, 40000, 4, "俗稱蘇眉，額頭隆起的珊瑚礁巨魚。"},
	{"arowana", "金龍魚", "🐉", "epic", 147, 215, 1000, 6000, 4, "鱗片閃著金光，被視為招財的象徵。"},
	{"yellowfin_tuna", "黃鰭鮪", "🐟", "epic", 270, 405, 10000, 80000, 5, "深海的速度之王，只吃深海誘餌以上的魚餌。"},
	{"swordfish", "旗魚", "🗡️", "epic", 293, 450, 30000, 200000, 5, "用長劍般的上顎突擊獵物，只吃深海誘餌以上的魚餌。"},
	{"moon_ray", "月光魟", "🌙", "legendary", 495, 788, 10000, 80000, 5,
		"月圓時才浮上海面的發光魟魚，要用深海誘餌以上的魚餌。"},
	{"coelacanth", "腔棘魚", "🦴", "legendary", 563, 900, 30000, 90000, 5, "活化石，只會被龍涎香餌吸引。"},
	{"dragon_king_carp", "龍王鯉", "🐲", "legendary", 675, 1125, 5000, 30000, 5, "躍過龍門的鯉魚王，只吃龍涎香餌。"},
	// 以下 10 種特殊魚由 Migration 0035 新增：都有季節或時段限定（seasons.FishLimits），部分還要指定魚餌
	{"dawn_trout", "黎明鱒", "🌅", "rare", 80, 135, 300, 2500, 3, "只在天剛亮（04–07 點）時出現的鱒魚。"},
	{"snow_icefish", "雪精靈冰魚", "❄️", "rare", 90, 147, 50, 300, 2, "冬天清晨才會游到淺水的透明小魚。"},
	{"firefly_squid", "螢火魷", "✨", "epic", 225, 360, 50, 200, 3,
		"春天夜裡發出藍光的魷魚，要用深海誘餌以上的魚餌。"},
	{"lanternfish", "燈籠魚", "🏮", "epic", 203, 338, 100, 600, 3, "夏夜提著小燈籠浮上海面的深海魚。"},
	{"maple_salmon", "楓紅鮭", "🍁", "epic", 248, 383, 2000, 12000, 4, "秋天黃昏逆流而上，身上像楓葉一樣紅。"},
	{"rainbow_wrasse", "虹彩隆頭魚", "🌈", "epic", 215, 350, 200, 2000, 3,
		"夏天正午的珊瑚礁上閃著七彩光芒。"},
	{"midnight_angler", "午夜鮟鱇", "🌑", "legendary", 720, 1170, 5000, 40000, 5,
		"只在午夜 0–3 點提著燈出沒，要用深海誘餌以上的魚餌。"},
	{"spring_koi", "春神錦鯉", "🌸", "legendary", 788, 1350, 3000, 15000, 5,
		"春天早晨才會現身的神之錦鯉，只吃龍涎香餌。"},
	{"ghost_shark", "幽靈鯊", "👻", "legendary", 900, 1463, 20000, 150000, 5,
		"秋天深夜的海面下有一道半透明的影子，要用深海誘餌以上的魚餌。"},
	{"frost_dragon_fish", "霜龍魚", "🧊", "legendary", 1013, 1575, 10000, 60000, 5,
		"冬天深夜冰海中的龍形巨魚，只吃龍涎香餌。"},
}

// FishRequiredBaits 要用指定魚餌才釣得到的魚（code → 可用的魚餌代碼）；沒列在這裡的魚任何魚餌都能釣到。
// 拋竿時只從「目前裝備的魚餌釣得到」的魚種中挑；該稀有度都釣不到時往較低的稀有度找（PickFish）
var FishRequiredBaits = map[string][]string{
	"yellowfin_tuna":    {"deep_sea_bait", "dragon_bait"},
	"swordfish":         {"deep_sea_bait", "dragon_bait"},
	"moon_ray":          {"deep_sea_bait", "dragon_bait"},
	"coelacanth":        {"dragon_bait"},
	"dragon_king_carp":  {"dragon_bait"},
	"firefly_squid":     {"deep_sea_bait", "dragon_bait"},
	"midnight_angler":   {"deep_sea_bait", "dragon_bait"},
	"spring_koi":        {"dragon_bait"},
	"ghost_shark":       {"deep_sea_bait", "dragon_bait"},
	"frost_dragon_fish": {"dragon_bait"},
}

// GearSpec 初始釣具：code, 類型, 名稱, emoji, 價格, 需要等級, 說明, 效果
type GearSpec struct {
	Code    string
	Kind    string
	Name    string
	Emoji   string
	Price   int
	Level   int
	Desc    string
	Effects Effects
}

var InitialGear = []GearSpec{
	{"bamboo_rod", "rod", "竹製魚竿", "🎋", 100, 1, "新手入門的魚竿。",
		Effects{"reel_power": 10, "control_pct": 0, "reaction_bonus_ms": 0}},
	{"wooden_rod", "rod", "木製魚竿", "🎋", 600, 2, "比竹竿穩一點。",
		Effects{"reel_power": 11, "control_pct": 5, "reaction_bonus_ms": 500}},
	{"iron_rod", "rod", "鐵製魚竿", "🎣", 2000, 4, "收線更有力，魚衝刺時比較穩。",
		Effects{"reel_power": 13, "control_pct": 10, "reaction_bonus_ms": 1000}},
	{"pro_rod", "rod", "高級魚竿", "🎣", 6000, 7, "專業釣手愛用。",
		Effects{"reel_power": 15, "control_pct": 18, "reaction_bonus_ms": 1500}},
	{"legend_rod", "rod", "傳說魚竿", "✨", 20000, 10, "傳說魚也拉得動的頂級魚竿。",
		Effects{"reel_power": 18, "control_pct": 25, "reaction_bonus_ms": 2000}},
	{"basic_line", "line", "普通魚線", "🪢", 50, 1, "一般的尼龍線。", Effects{"max_tension": 100}},
	{"strong_line", "line", "強化魚線", "🪢", 800, 3, "比較不容易斷。", Effects{"max_tension": 115}},
	{"heavy_line", "line", "高強度魚線", "🪢", 3000, 6, "大魚的衝擊也撐得住。", Effects{"max_tension": 130}},
	{"worm_bait", "bait", "普通蚯蚓", "🪱", 3, 1, "最普通的魚餌（每次拋竿用掉 1 個）。",
		Effects{"rare_bp": 0, "epic_bp": 0, "legendary_bp": 0, "bite_speed_pct": 0}},
	{"premium_bait", "bait", "高級魚餌", "🦐", 6, 1, "稀有魚比較愛吃，上鉤也比較快。",
		Effects{"rare_bp": 300, "epic_bp": 100, "legendary_bp": 30, "bite_speed_pct": 20}},
	{"special_bait", "bait", "特殊魚餌", "🌟", 12, 4, "特製香餌，吸引史詩與傳說魚。",
		Effects{"rare_bp": 500, "epic_bp": 300, "legendary_bp": 150, "bite_speed_pct": 40}},
	// 以下由 Migration 0033 新增：部分史詩／傳說魚只吃這些魚餌（FishRequiredBaits）。
	// 所有魚餌的需要等級已由 Migration 0034 調低 2 級（最低 Lv.1），這裡是調整後的值
	{"deep_sea_bait", "bait", "深海誘餌", "🦑", 40, 6, "深海魚才會咬的誘餌：黃鰭鮪、旗魚、月光魟要用它（或龍涎香餌）。",
		Effects{"rare_bp": 400, "epic_bp": 500, "legendary_bp": 200, "bite_speed_pct": 30}},
	{"dragon_bait", "bait", "龍涎香餌", "🐉", 120, 8, "傳說中的頂級香餌：腔棘魚、龍王鯉只吃它，深海魚也愛吃。",
		Effects{"rare_bp": 300, "epic_bp": 600, "legendary_bp": 400, "bite_speed_pct": 20}},
}

// intValue 對應 Python _int：訊息的範圍不加千分位。
func intValue(value any, label string, low, high int) (int, error) {
	n, ok := featurecfg.AsInt(value)
	if !ok || n < low || n > high {
		return 0, gameerr.Validationf("%s必須是 %d–%d 之間的整數。", label, low, high)
	}
	return n, nil
}

// pyG 對應 Python f"{x:g}"。
func pyG(x float64) string { return strconv.FormatFloat(x, 'g', 6, 64) }

func sameKeys(m map[string]any, keys []string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// ---------- 設定 ----------

var SettingLabels = map[string]string{
	"base_success_pct":       "基礎釣魚成功率（%）",
	"bite_wait_min_seconds":  "上鉤等待時間下限（秒）",
	"bite_wait_max_seconds":  "上鉤等待時間上限（秒）",
	"reaction_seconds":       "拉竿反應時間（秒）",
	"action_timeout_seconds": "拉扯操作逾時（秒）",
	"basket_capacity":        "魚簍容量",
}

var DifficultyLabels = map[string]string{
	"stamina": "體力", "distance": "起始距離", "struggle_pct": "掙扎機率（%）", "surge_pct": "衝刺機率（%）",
	"strong_pct": "強力掙扎機率（%）", "chain_pct": "連續衝刺機率（%）",
}

// ValidateSettings 檢查並整理管理員送出的釣魚設定；稀有度機率合計必須是 100%（10000 萬分比）。
// raw 是 JSON 物件（數值可為 int 或 json.Number）。
func ValidateSettings(raw map[string]any) (Settings, error) {
	var out Settings
	if raw == nil {
		return out, gameerr.Validationf("設定格式不正確。")
	}
	bp, ok := featurecfg.AsMap(raw["rarity_bp"])
	if !ok || !sameKeys(bp, Rarities) {
		return out, gameerr.Validationf("稀有度機率必須包含普通、稀有、史詩、傳說四種。")
	}
	out.RarityBP = map[string]int{}
	total := 0
	for _, r := range Rarities {
		n, err := intValue(bp[r], RarityNames[r]+"出現機率", 0, 10000)
		if err != nil {
			return Settings{}, err
		}
		out.RarityBP[r] = n
		total += n
	}
	if total != 10000 {
		return Settings{}, gameerr.Validationf("稀有度出現機率合計必須是 100%%（目前 %s%%）。", pyG(float64(total)/100))
	}
	for _, l := range SettingLimits {
		n, err := intValue(raw[l.Key], SettingLabels[l.Key], l.Low, l.High)
		if err != nil {
			return Settings{}, err
		}
		out.setInt(l.Key, n)
	}
	if out.BiteWaitMaxSeconds < out.BiteWaitMinSeconds {
		return Settings{}, gameerr.Validationf("上鉤等待時間的上限不可小於下限。")
	}
	diff, ok := featurecfg.AsMap(raw["difficulty"])
	if !ok || !sameKeys(diff, Rarities) {
		return Settings{}, gameerr.Validationf("拉扯難度必須包含四種稀有度。")
	}
	out.Difficulty = map[string]Difficulty{}
	for _, r := range Rarities {
		d, ok := diffMap(diff[r])
		if !ok {
			return Settings{}, gameerr.Validationf("%s的拉扯難度格式不正確。", RarityNames[r])
		}
		values := map[string]int{}
		for _, l := range DifficultyLimits {
			n, err := intValue(d[l.Key], RarityNames[r]+DifficultyLabels[l.Key], l.Low, l.High)
			if err != nil {
				return Settings{}, err
			}
			values[l.Key] = n
		}
		if values["struggle_pct"]+values["surge_pct"]+values["strong_pct"] > 100 {
			return Settings{}, gameerr.Validationf("%s的掙扎、衝刺、強力掙扎機率合計不可超過 100%%。", RarityNames[r])
		}
		out.Difficulty[r] = Difficulty{
			Stamina: values["stamina"], Distance: values["distance"], StrugglePct: values["struggle_pct"],
			SurgePct: values["surge_pct"], StrongPct: values["strong_pct"], ChainPct: values["chain_pct"],
		}
	}
	return out, nil
}

func diffMap(v any) (map[string]any, bool) {
	if d, ok := v.(Difficulty); ok {
		m := map[string]any{}
		for _, l := range DifficultyLimits {
			m[l.Key] = d.get(l.Key)
		}
		return m, true
	}
	return featurecfg.AsMap(v)
}

// ---------- 釣具 ----------

// ValidateGearEffects 檢查釣具效果；回傳該類型的所有欄位（缺欄位為錯誤）。
func ValidateGearEffects(kind string, raw map[string]any) (Effects, error) {
	limits, ok := GearEffectLimits[kind]
	if !ok {
		return nil, gameerr.Validationf("釣具類型必須是魚竿、魚線或魚餌。")
	}
	if raw == nil {
		return nil, gameerr.Validationf("釣具效果格式不正確。")
	}
	var unknown []string
	for k := range raw {
		known := false
		for _, l := range limits {
			if l.Key == k {
				known = true
				break
			}
		}
		if !known {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, gameerr.Validationf("%s沒有這些效果欄位：%s。", GearKindNames[kind], strings.Join(unknown, "、"))
	}
	out := Effects{}
	for _, l := range limits {
		n, err := intValue(raw[l.Key], GearEffectNames[l.Key], l.Low, l.High)
		if err != nil {
			return nil, err
		}
		out[l.Key] = n
	}
	return out, nil
}

// GearEffectsText 釣具效果的說明文字。
func GearEffectsText(kind string, effects Effects) string {
	if kind == "rod" {
		parts := []string{fmt.Sprintf("收線力 %d", effects["reel_power"])}
		if effects["control_pct"] != 0 {
			parts = append(parts, fmt.Sprintf("魚的衝擊張力 -%d%%", effects["control_pct"]))
		}
		if effects["reaction_bonus_ms"] != 0 {
			parts = append(parts, fmt.Sprintf("拉竿時間 +%s 秒", pyG(float64(effects["reaction_bonus_ms"])/1000)))
		}
		return strings.Join(parts, "、")
	}
	if kind == "line" {
		return fmt.Sprintf("最大張力 %d", getOr(effects, "max_tension", BaseMaxTension))
	}
	var parts []string
	for _, r := range []string{"rare", "epic", "legendary"} {
		if v := effects[r+"_bp"]; v != 0 {
			parts = append(parts, fmt.Sprintf("%s%s +%s%%", RarityEmoji[r], RarityNames[r], pyG(float64(v)/100)))
		}
	}
	if v := effects["bite_speed_pct"]; v != 0 {
		parts = append(parts, fmt.Sprintf("上鉤快 %d%%", v))
	}
	if len(parts) == 0 {
		return "一般魚餌，沒有額外效果"
	}
	return strings.Join(parts, "、")
}

// ---------- 魚種 ----------

// FishValues 驗證後的魚種數值（JSON 欄位名稱與 Python 相同）。
type FishValues struct {
	Rarity     string `json:"rarity"`
	MinPrice   int    `json:"min_price"`
	MaxPrice   int    `json:"max_price"`
	MinWeightG int    `json:"min_weight_g"`
	MaxWeightG int    `json:"max_weight_g"`
	Difficulty int    `json:"difficulty"`
}

// ValidateFish 魚種數值：售價由管理員自由設定（不受稀有度限制，只要求最低不高於最高）；重量以公克儲存。
func ValidateFish(raw map[string]any) (FishValues, error) {
	rarity, _ := raw["rarity"].(string)
	if !contains(Rarities, rarity) {
		return FishValues{}, gameerr.Validationf("稀有度必須是普通、稀有、史詩或傳說。")
	}
	minPrice, err := intValue(raw["min_price"], "最低售價", PriceMin, PriceMax)
	if err != nil {
		return FishValues{}, err
	}
	maxPrice, err := intValue(raw["max_price"], "最高售價", PriceMin, PriceMax)
	if err != nil {
		return FishValues{}, err
	}
	if minPrice > maxPrice {
		return FishValues{}, gameerr.Validationf("最低售價不可高於最高售價。")
	}
	minW, err := intValue(raw["min_weight_g"], "最小重量（公克）", FishWeightRangeG[0], FishWeightRangeG[1])
	if err != nil {
		return FishValues{}, err
	}
	maxW, err := intValue(raw["max_weight_g"], "最大重量（公克）", FishWeightRangeG[0], FishWeightRangeG[1])
	if err != nil {
		return FishValues{}, err
	}
	if minW > maxW {
		return FishValues{}, gameerr.Validationf("最小重量不可大於最大重量。")
	}
	d, err := intValue(raw["difficulty"], "掙扎難度", FishDifficultyRng[0], FishDifficultyRng[1])
	if err != nil {
		return FishValues{}, err
	}
	return FishValues{rarity, minPrice, maxPrice, minW, maxW, d}, nil
}

func rarityWeights(rarityBP map[string]int, bait Effects) map[string]int {
	weights := map[string]int{}
	for _, r := range Rarities {
		w := rarityBP[r]
		if r != "common" {
			w += bait[r+"_bp"]
		}
		weights[r] = w
	}
	return weights
}

// RollRarity 依設定的稀有度機率（加上魚餌加成，從普通扣除）抽稀有度。
func RollRarity(rarityBP map[string]int, bait Effects, rng RNG) string {
	weights := rarityWeights(rarityBP, bait)
	extra := 0
	for _, r := range Rarities {
		extra += weights[r] - rarityBP[r]
	}
	weights["common"] = max(0, rarityBP["common"]-extra)
	total := 0
	for _, r := range Rarities {
		total += weights[r]
	}
	point := rng() * float64(total)
	for _, r := range Rarities {
		point -= float64(weights[r])
		if point < 0 {
			return r
		}
	}
	return "common"
}

// RarityChances 顯示用：套用魚餌後各稀有度的實際機率（%）。
func RarityChances(rarityBP map[string]int, bait Effects) map[string]float64 {
	weights := rarityWeights(rarityBP, bait)
	extra := 0
	for _, r := range Rarities[1:] {
		extra += weights[r] - rarityBP[r]
	}
	weights["common"] = max(0, rarityBP["common"]-extra)
	total := 0
	for _, r := range Rarities {
		total += weights[r]
	}
	if total == 0 {
		total = 1
	}
	out := map[string]float64{}
	for _, r := range Rarities {
		out[r] = float64(weights[r]) * 100 / float64(total)
	}
	return out
}

// BaitAllows 這條魚能不能用目前的魚餌釣到（requiredBaits 為空 = 任何魚餌都可以；baitCode "" 表示沒有魚餌）。
func BaitAllows(requiredBaits []string, baitCode string) bool {
	return len(requiredBaits) == 0 || (baitCode != "" && contains(requiredBaits, baitCode))
}

// PickFish 在抽到的稀有度中平均挑一種魚；該稀有度沒有啟用中的魚時，往較低的稀有度找（再往較高的找）。
func PickFish[T any](fishByRarity map[string][]T, rarity string, rng RNG) (T, bool) {
	idx := 0
	for i, r := range Rarities {
		if r == rarity {
			idx = i
		}
	}
	var order []string
	for i := idx; i >= 0; i-- {
		order = append(order, Rarities[i])
	}
	order = append(order, Rarities[idx+1:]...)
	for _, r := range order {
		pool := fishByRarity[r]
		if len(pool) > 0 {
			return pool[min(len(pool)-1, int(rng()*float64(len(pool))))], true
		}
	}
	var zero T
	return zero, false
}

// RollWeight 重量偏向較輕（大魚比較少見）。
func RollWeight(minG, maxG int, rng RNG) int {
	return minG + numfmt.Round(float64(maxG-minG)*math.Pow(rng(), 1.6))
}

// FishPrice 售價依重量在魚種的價格區間內線性換算（越重越值錢），不會超出區間。
func FishPrice(minPrice, maxPrice, minG, maxG, weightG int) int {
	if maxG <= minG {
		return maxPrice
	}
	ratio := min(1.0, max(0.0, float64(weightG-minG)/float64(maxG-minG)))
	return minPrice + numfmt.Round(float64(maxPrice-minPrice)*ratio)
}

func WeightText(weightG int) string {
	if weightG >= 1000 {
		return fmt.Sprintf("%.2f kg", float64(weightG)/1000)
	}
	return fmt.Sprintf("%d g", weightG)
}

// ---------- 拉扯博弈 ----------

// Last 上一次操作的結果（服務層只寫 text 時 action／intent 省略，與 Python 相同）。
type Last struct {
	Action string `json:"action,omitempty"`
	Intent string `json:"intent,omitempty"`
	Text   string `json:"text"`
}

// State 拉扯狀態，存在 fishing_sessions.state（JSON）。
//
// 選用帶 JSON tag 的 struct（而非 map[string]any）：欄位名稱與 Python dict 相同；數值欄位用 float64，
// 因為 Python 的 tension、reel_streak 在計算中會變成小數，Go 的 float64 整數值序列化成 JSON 時仍是 `40` 這樣的整數，
// Python 讀回不受影響。舊 Session 缺少的欄位（reel_streak、dodge、gaps）解碼後為零值，等同 Python .get 的預設值
// （0、None、[]）。2026-11-10 以前的拉扯沒有 need 等欄位（Need 為 0），Step 開始時以 upgradeLegacy 補上（Python
// _upgrade_legacy）。gaps 由服務層（防腳本）寫入，step 不改。Python 端若加入其他未知欄位，經 Go 讀寫後會遺失。
type State struct {
	Stamina       float64   `json:"stamina"`
	MaxStamina    float64   `json:"max_stamina"`
	Distance      float64   `json:"distance"`
	StartDistance float64   `json:"start_distance"`
	Tension       float64   `json:"tension"`
	MaxTension    float64   `json:"max_tension"`
	Turn          int       `json:"turn"`
	SlipTurn      *int      `json:"slip_turn"`
	Intent        string    `json:"intent"`
	Last          *Last     `json:"last"`
	ReelStreak    float64   `json:"reel_streak"`
	Dodge         *string   `json:"dodge"`
	Need          int       `json:"need"`     // 要成功拉幾下才上岸
	Progress      int       `json:"progress"` // 已成功拉幾下
	DodgeAt       []int     `json:"dodge_at"` // 在這些進度魚會竄向一邊（壓中後移除）
	Slack         float64   `json:"slack"`    // 放線、沒壓中方向讓魚多游遠的距離
	Gaps          []float64 `json:"gaps,omitempty"`
}

// upgradeLegacy 升級時進行中的舊拉扯（沒有 need 欄位）：再成功拉 2 下就上岸，不再有強制竄動（Python _upgrade_legacy）。
func (s *State) upgradeLegacy() {
	if s.Need == 0 {
		if s.StartDistance == 0 {
			s.StartDistance = max(1, s.Distance)
		}
		s.Need = 2
	}
	if s.DodgeAt == nil {
		s.DodgeAt = []int{}
	}
}

// PullsLeft 還要成功拉幾下才上岸（舊狀態沒有 need 時回傳 0）。
func PullsLeft(s State) int {
	return max(0, s.Need-s.Progress)
}

// sync 魚的體力與距離跟著進度下降（放線、沒壓中方向會讓魚游遠：slack）。
func (s *State) sync() {
	left := float64(PullsLeft(*s))
	s.Stamina = clamp(s.MaxStamina*left/float64(s.Need), 0, s.MaxStamina)
	s.Distance = clamp(s.StartDistance*left/float64(s.Need)+s.Slack, 0, EscapeDistance)
}

// DodgeDir 目前竄動方向（"" 表示沒有）。
func (s State) DodgeDir() string {
	if s.Dodge == nil {
		return ""
	}
	return *s.Dodge
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func BiteWaitSeconds(settings Settings, bait Effects, rng RNG) float64 {
	low, high := float64(settings.BiteWaitMinSeconds), float64(settings.BiteWaitMaxSeconds)
	wait := low + (high-low)*rng()
	return max(1.0, wait*float64(100-bait["bite_speed_pct"])/100)
}

func ReactionSeconds(settings Settings, rod Effects) float64 {
	return float64(settings.ReactionSeconds) + float64(rod["reaction_bonus_ms"])/1000
}

// NewFight 拋竿時建立拉扯初始狀態：要拉幾下（Pulls）、在哪幾個進度魚會竄向一邊；基礎成功率沒通過時記下脫鉤的回合。
// 亂數的使用順序與 Python 相同：脫鉤 → 拉幾下 → 竄動的進度 → 第一下的竄動方向 → 魚的第一個動作。
func NewFight(settings Settings, rarity string, fishDifficulty int, line Effects, rng RNG) State {
	diff := settings.Difficulty[rarity]
	mul, ok := FishDifficultyStamina[fishDifficulty]
	if !ok {
		mul = 1.0
	}
	stamina := max(1, numfmt.Round(float64(diff.Stamina)*mul))
	var slipTurn *int
	if rng()*100 >= float64(settings.BaseSuccessPct) {
		t := SlipTurns[min(len(SlipTurns)-1, int(rng()*float64(len(SlipTurns))))]
		slipTurn = &t
	}
	pulls := Pulls[rarity]
	low, high := pulls[0], pulls[1]
	need := min(high, low+int(rng()*float64(high-low+1)))
	spots := make([]int, need)
	for i := range spots {
		spots[i] = i
	}
	dodgeAt := []int{}
	for range min(ForcedDodges[rarity], need) {
		i := min(len(spots)-1, int(rng()*float64(len(spots))))
		dodgeAt = append(dodgeAt, spots[i])
		spots = append(spots[:i], spots[i+1:]...)
	}
	slices.Sort(dodgeAt)
	state := State{
		Stamina: float64(stamina), MaxStamina: float64(stamina), Distance: float64(diff.Distance),
		StartDistance: float64(diff.Distance), Tension: StartTension,
		MaxTension: float64(getOr(line, "max_tension", BaseMaxTension)), Turn: 0,
		SlipTurn: slipTurn, Intent: "calm", Last: nil, ReelStreak: 0, Dodge: nil,
		Need: need, Progress: 0, DodgeAt: dodgeAt, Slack: 0,
	}
	if slices.Contains(dodgeAt, 0) {
		state.Dodge = strPtr(dodgeDirection(rng))
	}
	state.Intent = NextIntent(state, diff, "", rng)
	return state
}

func dodgeDirection(rng RNG) string {
	if rng() < 0.5 {
		return SteerActions[0]
	}
	return SteerActions[1]
}

// NextIntent 決定魚的下一個動作；previous "" 表示沒有上一個動作。
func NextIntent(state State, diff Difficulty, previous string, rng RNG) string {
	if state.Stamina <= 0 {
		return "tired"
	}
	if (previous == "surge" || previous == "strong") && rng()*100 < float64(diff.ChainPct) {
		return previous // 連續衝刺
	}
	scale := 1.0
	if state.Stamina < state.MaxStamina*TiredRatio {
		scale = 0.5
	}
	point := rng() * 100
	for _, it := range []struct {
		intent string
		pct    int
	}{{"strong", diff.StrongPct}, {"surge", diff.SurgePct}, {"struggle", diff.StrugglePct}} {
		point -= float64(it.pct) * scale
		if point < 0 {
			return it.intent
		}
	}
	return "calm"
}

// clamp 對應 Python _clamp：限制範圍後以銀行家捨入轉成整數。
func clamp(value, low, high float64) float64 {
	return float64(numfmt.Round(min(high, max(low, value))))
}

// FishSpike 魚的動作造成的張力變化（魚竿穩定度降低衝擊）。
func FishSpike(intent string, rod Effects) float64 {
	t := FishIntents[intent].Tension
	if t > 0 {
		return t * float64(100-rod["control_pct"]) / 100
	}
	return t
}

// ReelTension 這次收線會增加多少張力：連續收線越多次越高（依魚線上限按比例）。
func ReelTension(state State) float64 {
	extra := state.ReelStreak * state.MaxTension * ReelStreakTensionPct / 100
	return PlayerActions["reel"].Tension + extra
}

// LandStamina 相容舊介面：新規則以「拉幾下」決定上岸，不再看體力門檻（Python land_stamina 也固定回傳 0）。
func LandStamina(State) int { return 0 }

var ActionResultText = map[string]string{"reel": "🎣 你用力收線。", "release": "↩️ 你放出一些線。", "wait": "⏳ 你穩住魚竿觀察。"}

const SteerMissText = "🎣 你壓了一下竿。"

var IntentResult = map[string]string{
	"calm": "魚游得很平穩。", "struggle": "魚左右掙扎！", "surge": "魚突然衝刺！", "strong": "魚強力衝刺！",
	"tired": "魚累得游不動了。",
}

// Step 執行一次拉扯操作，回傳 (新狀態, 結果)；結果 "" 表示繼續，否則為 landed／escaped／line_broken／slipped。
//
// 魚竄向一邊時只有同方向壓竿算 1 下，其他操作都是失誤（不算進度、張力與距離增加，魚繼續竄）；
// 沒有竄動時，收線（張力撐得住）算 1 下，等待與放線用來降張力。拉滿 need 下就上岸。
func Step(state State, action string, diff Difficulty, rod Effects, rng RNG) (State, string, error) {
	if !contains(Actions, action) {
		return state, "", gameerr.Validationf("沒有這個操作。")
	}
	s := state
	s.upgradeLegacy()
	s.DodgeAt = slices.Clone(s.DodgeAt)
	s.Turn++
	intent := s.Intent
	dodge := s.DodgeDir()
	s.Dodge = nil
	streak := s.ReelStreak
	if s.SlipTurn != nil && *s.SlipTurn != 0 && s.Turn >= *s.SlipTurn {
		s.Last = &Last{Action: action, Intent: intent, Text: "🪝 魚鉤沒有咬牢，魚掙脫逃走了！"}
		return s, "slipped", nil
	}
	missed := false
	var text string
	switch {
	case dodge != "" && action == dodge: // 壓中：算 1 下
		s.DodgeAt = slices.DeleteFunc(s.DodgeAt, func(x int) bool { return x == s.Progress })
		s.Progress++
		s.ReelStreak = 0
		s.Tension = max(0, s.Tension+DodgeHit.Tension)
		text = "🎯 壓對方向！魚被你帶回來了。"
	case dodge != "": // 沒壓中：不算進度，魚繼續竄
		missed = true
		extra := 0.0
		if action == "reel" {
			extra = ReelTension(s)
			s.ReelStreak = streak + 1
		} else {
			s.ReelStreak = 0
		}
		s.Tension = max(0, s.Tension+extra+DodgeMiss.Tension)
		s.Slack += DodgeMiss.Distance
		text = "⚠️ 沒壓對方向，魚線被猛扯了一下！"
	default:
		spike := FishSpike(intent, rod)
		switch action {
		case "reel":
			hit := spike
			if spike > 0 {
				hit = spike * ReelSpikeMul
			}
			s.Tension = max(0, s.Tension+ReelTension(s)+hit)
			s.ReelStreak = streak + 1
			if s.Tension <= s.MaxTension {
				s.Progress++
			}
		case "wait":
			s.Tension = max(0, s.Tension+WaitTension) // 穩住魚竿：魚的衝擊不會再加張力
			s.ReelStreak = max(0, streak-ReelStreakWaitDecay)
		case "release":
			s.Tension = max(0, s.Tension+PlayerActions["release"].Tension+max(spike, 0)*0.5)
			s.Slack += ReleaseSlack
			s.ReelStreak = 0
		default: // 魚沒有竄動時亂壓竿
			s.Tension = max(0, s.Tension+SteerIdleTension+spike)
			s.ReelStreak = 0
		}
		if contains(SteerActions, action) {
			text = SteerMissText
		} else {
			text = ActionResultText[action]
		}
		text += "　" + IntentResult[intent]
	}
	s.Tension = clamp(s.Tension, 0, 999)
	s.Last = &Last{Action: action, Intent: intent, Text: text}
	if s.Tension > s.MaxTension {
		s.Last.Text = "💥 張力太大，魚線斷了！"
		return s, "line_broken", nil
	}
	s.sync()
	if s.Progress >= s.Need {
		s.Distance = 0
		s.Last.Text = "🎉 收線成功，把魚拉出水面了！"
		return s, "landed", nil
	}
	if s.Distance >= EscapeDistance {
		s.Last.Text = "🌊 魚游得太遠，掙脫逃走了！"
		return s, "escaped", nil
	}
	s.Intent = NextIntent(s, diff, intent, rng)
	if missed || slices.Contains(s.DodgeAt, s.Progress) {
		s.Dodge = strPtr(dodgeDirection(rng))
	}
	return s, "", nil
}

// GuardChallenge 人機確認題目（JSON 欄位名稱與 Python 相同）。
type GuardChallenge struct {
	Answer  string   `json:"answer"`
	Options []string `json:"options"`
}

// NewGuardChallenge 出一題人機確認；pick(序列, 數量) 取不重複的選項、shuffle 打亂順序（由服務層傳入）。
func NewGuardChallenge(pick func([]string, int) []string, shuffle func([]string)) GuardChallenge {
	options := append([]string(nil), pick(append([]string(nil), GuardCreatureOrder...), GuardOptions)...)
	answer := options[0]
	shuffle(options)
	return GuardChallenge{Answer: answer, Options: options}
}

func GuardLockMinutesFor(consecutiveFailures int) int {
	m := GuardLockMinutes
	for i := 1; i < consecutiveFailures; i++ {
		m *= 2
		if m >= GuardLockMaxMinutes {
			return GuardLockMaxMinutes
		}
	}
	return min(GuardLockMaxMinutes, m)
}

// RegularRhythm 一場的操作間隔是否規律得不像人（真人的間隔會隨畫面內容明顯變動）。
func RegularRhythm(gaps []float64) bool {
	if len(gaps) < GuardRegularMinSamples {
		return false
	}
	sum := 0.0
	for _, g := range gaps {
		sum += g
	}
	mean := sum / float64(len(gaps))
	v := 0.0
	for _, g := range gaps {
		v += (g - mean) * (g - mean)
	}
	return math.Sqrt(v/float64(len(gaps))) < GuardRegularStdevSeconds
}

// ---------- 模擬（測試與調整數值用） ----------

// PolicyKeys 模擬策略可以看到的狀態欄位。
var PolicyKeys = []string{"intent", "tension", "distance", "stamina", "max_stamina", "max_tension", "dodge", "reel_streak", "turn"}

// Policy 模擬策略：依狀態決定下一個操作。
type Policy func(state State, diff Difficulty, rod Effects) string

// SmartAction 依提示操作的玩家（margin 4）。
func SmartAction(state State, diff Difficulty, rod Effects) string {
	return SmartActionMargin(state, diff, rod, 4)
}

// SmartActionMargin 依提示操作的玩家：魚竄動時壓同方向；收線撐得住就收線，否則等待，等待也撐不住才放線。
func SmartActionMargin(state State, diff Difficulty, rod Effects, margin float64) string {
	if d := state.DodgeDir(); d != "" {
		return d
	}
	t, mt := state.Tension, state.MaxTension
	spike := FishSpike(state.Intent, rod)
	hit := spike
	if spike > 0 {
		hit = spike * ReelSpikeMul
	}
	if t+ReelTension(state)+hit <= mt-margin {
		return "reel"
	}
	if t+WaitTension <= mt-margin {
		return "wait"
	}
	return "release"
}

// NaiveAction 不看提示的玩家：一直收線，張力超過七成才放線（不看魚往哪邊竄）。
func NaiveAction(state State, diff Difficulty, rod Effects) string {
	if state.Tension > state.MaxTension*0.7 {
		return "release"
	}
	return "reel"
}

// ReelOnlyAction 最簡單的連點腳本：只按收線。
func ReelOnlyAction(State, Difficulty, Effects) string { return "reel" }

// AlternateAction 固定節奏的腳本：收線、等待輪流按，不看畫面。
func AlternateAction(state State, diff Difficulty, rod Effects) string {
	if state.Turn%2 == 0 {
		return "reel"
	}
	return "wait"
}

// Simulate 模擬一次拉扯（假設玩家及時拉竿），回傳 (結果, 操作次數)。fishDifficulty 預設 3、maxTurns 預設 200。
func Simulate(settings Settings, rarity string, rod, line Effects, policy Policy, rng RNG, fishDifficulty, maxTurns int) (string, int) {
	diff := settings.Difficulty[rarity]
	state := NewFight(settings, rarity, fishDifficulty, line, rng)
	for i := 0; i < maxTurns; i++ {
		next, result, err := Step(state, policy(state, diff, rod), diff, rod, rng)
		if err != nil {
			panic(err)
		}
		state = next
		if result != "" {
			return result, state.Turn
		}
	}
	return "timeout", state.Turn
}

// SuccessRate 回傳 (成功率, 平均操作次數)。Python 預設 trials 2000、fishDifficulty 3。
func SuccessRate(settings Settings, rarity string, rod, line Effects, policy Policy, rng RNG, trials, fishDifficulty int) (float64, float64) {
	wins, turns := 0, 0
	for i := 0; i < trials; i++ {
		result, n := Simulate(settings, rarity, rod, line, policy, rng, fishDifficulty, 200)
		if result == "landed" {
			wins++
		}
		turns += n
	}
	return float64(wins) / float64(trials), float64(turns) / float64(trials)
}

// EstimatedSeconds 估計一次釣魚的總時間（等待上鉤 + 反應 + 拉扯）。Python 預設 secondsPerTurn 2.0。
func EstimatedSeconds(settings Settings, turns, secondsPerTurn float64) float64 {
	wait := float64(settings.BiteWaitMinSeconds+settings.BiteWaitMaxSeconds) / 2
	return wait + 1.5 + math.Ceil(turns)*secondsPerTurn
}
