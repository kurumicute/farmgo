// Package rules 純遊戲規則：不依賴資料庫、CMD 或 Web，方便單元測試與重用（對應 Python app/game/rules.py）。
//
// 慣例：Python 的 None 在 Go 以 nil 指標表示（時間、ID）；字串參數以 "" 表示 None。
package rules

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

// PlotState 土地狀態。
type PlotState string

const (
	PlotEmpty        PlotState = "empty"
	PlotWaitingWater PlotState = "waiting_water"
	PlotGrowing      PlotState = "growing"
	PlotReady        PlotState = "ready"
	PlotWithered     PlotState = "withered"
)

var PlotStateLabels = map[PlotState]string{
	PlotEmpty:        "空地",
	PlotWaitingWater: "等待澆水",
	PlotGrowing:      "生長中",
	PlotReady:        "可收成",
	PlotWithered:     "已乾枯",
}

// EventType 交易紀錄事件類型（與資料庫 CHECK 約束一致）。
type EventType string

const (
	EvCreateFarm        EventType = "create_farm"
	EvBuySeed           EventType = "buy_seed"
	EvPlant             EventType = "plant"
	EvHarvest           EventType = "harvest"
	EvSell              EventType = "sell"
	EvUpgrade           EventType = "upgrade"
	EvBuyItem           EventType = "buy_item"
	EvUseItem           EventType = "use_item"
	EvWater             EventType = "water"
	EvClear             EventType = "clear"
	EvSteal             EventType = "steal"
	EvStolen            EventType = "stolen"
	EvBindGroup         EventType = "bind_group"
	EvStealBlocked      EventType = "steal_blocked"
	EvDefended          EventType = "defended"
	EvDefensePlace      EventType = "defense_place"
	EvBuild             EventType = "build"
	EvBuyAnimal         EventType = "buy_animal"
	EvFeedAnimal        EventType = "feed_animal"
	EvCollectAnimal     EventType = "collect_animal"
	EvCraftStart        EventType = "craft_start"
	EvCraftCollect      EventType = "craft_collect"
	EvQuestReward       EventType = "quest_reward"
	EvCompetitionReward EventType = "competition_reward"
	EvAdminAdjust       EventType = "admin_adjust"
	EvRankingReward     EventType = "ranking_reward"
	EvTransferGroup     EventType = "transfer_group"
	EvGamble            EventType = "gamble"
	EvExplore           EventType = "explore"
	EvBuildMachine      EventType = "build_machine"
	EvExchange          EventType = "exchange"
	EvMakeSeed          EventType = "make_seed"
	EvFishingBuy        EventType = "fishing_buy"
	EvFishSell          EventType = "fish_sell"
	EvReleaseAnimal     EventType = "release_animal"
	EvBossDonate        EventType = "boss_donate"
	EvBossReward        EventType = "boss_reward"
	EvRenameFarm        EventType = "rename_farm"
	EvCheckinReward     EventType = "checkin_reward"
	EvAchievementReward EventType = "achievement_reward"
	EvRuinsDonate       EventType = "ruins_donate"
	EvRuinsGuardian     EventType = "ruins_guardian"
	EvRumorTreasure     EventType = "rumor_treasure"
	EvSeedLab           EventType = "seed_lab"   // 種子實驗室（Go 版新增，Migration 0039）
	EvSeedShare         EventType = "seed_share" // 分享特殊種子（Go 版新增，Migration 0039）
)

// EventTypeOrder 事件類型的定義順序（Python EventType 列舉順序）。
var EventTypeOrder = []EventType{
	EvCreateFarm, EvBuySeed, EvPlant, EvHarvest, EvSell, EvUpgrade, EvBuyItem, EvUseItem, EvWater, EvClear,
	EvSteal, EvStolen, EvBindGroup, EvStealBlocked, EvDefended, EvDefensePlace, EvBuild, EvBuyAnimal, EvFeedAnimal,
	EvCollectAnimal, EvCraftStart, EvCraftCollect, EvQuestReward, EvCompetitionReward, EvAdminAdjust, EvRankingReward,
	EvTransferGroup, EvGamble, EvExplore, EvBuildMachine, EvExchange, EvMakeSeed, EvFishingBuy, EvFishSell,
	EvReleaseAnimal, EvBossDonate, EvBossReward, EvRenameFarm, EvCheckinReward, EvAchievementReward, EvRuinsDonate,
	EvRuinsGuardian, EvRumorTreasure, EvSeedLab, EvSeedShare,
}

var EventTypeLabels = map[EventType]string{
	EvCreateFarm:        "建立農場",
	EvBuySeed:           "購買種子",
	EvPlant:             "種植",
	EvHarvest:           "收成",
	EvSell:              "販售",
	EvUpgrade:           "擴建土地",
	EvBuyItem:           "購買道具",
	EvUseItem:           "使用道具",
	EvWater:             "澆水",
	EvClear:             "清除乾枯",
	EvSteal:             "偷菜",
	EvStolen:            "被偷",
	EvBindGroup:         "設定所屬群組",
	EvStealBlocked:      "偷菜被阻止",
	EvDefended:          "防守成功",
	EvDefensePlace:      "調整防守位置",
	EvBuild:             "建造設施",
	EvBuyAnimal:         "購買動物",
	EvFeedAnimal:        "餵食動物",
	EvCollectAnimal:     "收集動物產品",
	EvCraftStart:        "開始加工",
	EvCraftCollect:      "領取加工品",
	EvQuestReward:       "任務獎勵",
	EvCompetitionReward: "競賽獎勵",
	EvAdminAdjust:       "管理員調整",
	EvRankingReward:     "排名獎勵",
	EvTransferGroup:     "轉移所屬群組",
	EvGamble:            "賭場",
	EvExplore:           "山洞探索",
	EvBuildMachine:      "製作機器",
	EvExchange:          "假幣兌換",
	EvMakeSeed:          "製造種子",
	EvFishingBuy:        "購買釣具",
	EvFishSell:          "販售魚類",
	EvReleaseAnimal:     "放生動物",
	EvBossDonate:        "世界 Boss 捐獻",
	EvBossReward:        "世界 Boss 獎勵",
	EvRenameFarm:        "農場改名",
	EvCheckinReward:     "每日簽到",
	EvAchievementReward: "成就獎勵",
	EvRuinsDonate:       "修復遺跡",
	EvRuinsGuardian:     "攻擊遺跡守護者",
	EvRumorTreasure:     "傳聞寶藏",
	EvSeedLab:           "種子實驗室",
	EvSeedShare:         "分享種子",
}

func SeedItemCode(cropCode string) string    { return "seed_" + cropCode }
func ProduceItemCode(cropCode string) string { return cropCode }

// PlotStateAt 依時間即時結算土地狀態，不依賴任何儲存的狀態欄位。
func PlotStateAt(cropTypeID *int64, wateredAt, readyAt *time.Time, now time.Time) PlotState {
	if cropTypeID == nil {
		return PlotEmpty
	}
	if wateredAt == nil || readyAt == nil {
		return PlotWaitingWater
	}
	if now.Before(*readyAt) {
		return PlotGrowing
	}
	if !now.Before(WitherTime(*readyAt)) {
		return PlotWithered
	}
	return PlotReady
}

func WitherTime(readyAt time.Time) time.Time {
	return readyAt.Add(balance.WitherSeconds * time.Second)
}

func GrowSecondsWithEffects(baseSeconds int, earthwormActive bool) int {
	seconds := float64(baseSeconds)
	if earthwormActive {
		seconds = float64(baseSeconds) * balance.EarthwormGrowMultiplier
	}
	return max(1, numfmt.Round(seconds))
}

// RemainingSeconds 無條件進位、不為負數；readyAt 為 nil 時為 0。
func RemainingSeconds(readyAt *time.Time, now time.Time) int {
	if readyAt == nil {
		return 0
	}
	return max(0, int(readyAt.Sub(now).Seconds()+0.999))
}

func ReadyTime(plantedAt time.Time, growSeconds int) time.Time {
	return plantedAt.Add(time.Duration(growSeconds) * time.Second)
}

func ValidateQuantity(quantity int) (int, error) {
	if quantity <= 0 {
		return 0, gameerr.Validationf("數量必須大於 0。")
	}
	if quantity > balance.MaxTradeQuantity {
		return 0, gameerr.Validationf("單次數量不可超過 %d。", balance.MaxTradeQuantity)
	}
	return quantity, nil
}

// ValidateName 去除前後空白後檢查長度（以字元計算，與 Python len 相同）與控制字元。
func ValidateName(value, label string, maxLen int) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", gameerr.Validationf("%s不可為空白。", label)
	}
	if utf8.RuneCountInString(name) > maxLen {
		return "", gameerr.Validationf("%s最多 %d 個字元。", label, maxLen)
	}
	for _, ch := range name {
		if ch < 32 {
			return "", gameerr.Validationf("%s不可包含控制字元。", label)
		}
	}
	return name, nil
}

func FormatDuration(seconds int) string {
	seconds = max(0, seconds)
	minutes, sec := seconds/60, seconds%60
	hours, minutes := minutes/60, minutes%60
	if hours > 0 {
		return fmt.Sprintf("%d 小時 %d 分 %d 秒", hours, minutes, sec)
	}
	if minutes > 0 {
		return fmt.Sprintf("%d 分 %d 秒", minutes, sec)
	}
	return fmt.Sprintf("%d 秒", sec)
}

// DefenseCovers rangeSize 為 nil 表示守護全部土地（例如警戒犬）。
func DefenseCovers(rangeSize, startPlot *int, plotNumber int) bool {
	if rangeSize == nil {
		return true
	}
	return startPlot != nil && *startPlot <= plotNumber && plotNumber < *startPlot+*rangeSize
}

// ActiveDefense 參與判定的防守設施。
type ActiveDefense struct {
	Code   string
	Chance float64
}

// ResolveDefense 一輪防守判定：只擲一次亂數 roll ∈ [0, 1)。
//
// active 依判定順序排列（同種設施已去重）。依序累加「前面都沒擋下時本設施擋下」的機率，
// 整體上限為 limit；roll 落在哪個區間就由該設施阻止。回傳 (阻止的設施代碼或 "", 整體阻止機率)。
func ResolveDefense(active []ActiveDefense, roll float64, limit float64) (string, float64) {
	cumulative := 0.0
	notBlocked := 1.0
	blockedBy := ""
	for _, d := range active {
		share := min(notBlocked*d.Chance, limit-cumulative)
		if share <= 0 {
			break
		}
		if blockedBy == "" && roll < cumulative+share {
			blockedBy = d.Code
		}
		cumulative += share
		notBlocked *= 1 - d.Chance
	}
	return blockedBy, cumulative
}

// AnimalState 動物狀態。
type AnimalState string

const (
	AnimalHungry    AnimalState = "hungry"
	AnimalProducing AnimalState = "producing"
	AnimalReady     AnimalState = "ready"
)

var AnimalStateLabels = map[AnimalState]string{
	AnimalHungry:    "待餵食",
	AnimalProducing: "生產中",
	AnimalReady:     "可收集",
}

// AnimalStateAt 未餵食時停止生產；餵食後到 readyAt 才能收集，收集後回到待餵食。
func AnimalStateAt(fedAt, readyAt *time.Time, now time.Time) AnimalState {
	if fedAt == nil || readyAt == nil {
		return AnimalHungry
	}
	if now.Before(*readyAt) {
		return AnimalProducing
	}
	return AnimalReady
}

// CraftState 加工訂單狀態。
type CraftState string

const (
	CraftInProgress CraftState = "in_progress"
	CraftReady      CraftState = "ready"
	CraftCollected  CraftState = "collected"
)

var CraftStateLabels = map[CraftState]string{
	CraftInProgress: "加工中",
	CraftReady:      "待領取",
	CraftCollected:  "已領取",
}

func CraftStateAt(status string, readyAt, now time.Time) CraftState {
	if status == "collected" {
		return CraftCollected
	}
	if now.Before(readyAt) {
		return CraftInProgress
	}
	return CraftReady
}

// QuestAmount 一筆成功的遊戲操作對某類型任務貢獻的進度（0 表示不相關）。itemFilter／itemCode 的 "" 表示 None。
func QuestAmount(questType, metric, itemFilter, eventType, itemCode string, quantity, goldDelta int) int {
	switch {
	case questType == "harvest" && eventType == "harvest":
		return quantity
	case questType == "sell" && eventType == "sell":
		if metric == "amount" {
			return goldDelta
		}
		return quantity
	case questType == "build" && (eventType == "upgrade" || eventType == "build"):
		return 1
	case questType == "animal" && eventType == "collect_animal":
		if itemFilter == "" || itemFilter == itemCode {
			return quantity
		}
		return 0
	case questType == "defense" && eventType == "defended":
		return 1
	}
	return 0
}

// CompetitionPoints 一筆成功的遊戲操作在某類型競賽中的得分（0 表示不相關）。
func CompetitionPoints(compType, eventType string, quantity, goldDelta int) int {
	switch {
	case compType == "harvest" && eventType == "harvest":
		return quantity
	case compType == "sales" && eventType == "sell":
		return goldDelta
	case compType == "development":
		return balance.DevelopmentPoints[eventType]
	case compType == "defense" && eventType == "defended":
		return 1
	}
	return 0
}

// ProportionalShare 依貢獻比例分配獎池（無條件捨去）。
func ProportionalShare(pool, mine, total int) int {
	if total <= 0 || mine <= 0 {
		return 0
	}
	return numfmt.FloorDiv(pool*mine, total)
}

// EventState 任務／競賽的顯示狀態：scheduled／active／expired，或資料庫中的 completed／settled／cancelled。
func EventState(status string, startsAt, endsAt, now time.Time) string {
	if status != "active" {
		return status
	}
	if now.Before(startsAt) {
		return "scheduled"
	}
	if now.Before(endsAt) {
		return "active"
	}
	return "expired"
}

var EventStateLabels = map[string]string{
	"scheduled": "尚未開始",
	"active":    "進行中",
	"expired":   "已截止",
	"completed": "已完成",
	"settled":   "已結算",
	"cancelled": "已取消",
}
