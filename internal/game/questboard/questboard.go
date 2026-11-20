// Package questboard 伺服器委託板（Go 版新增，2026-11-20，設計見「新玩法六項_流程圖.md」#2）。
//
// 沿用既有的群組任務（quests 表）：每週一由背景結算替每個有活躍玩家的群組自動建立 3 張委託（簡單、普通、困難），
// created_by 為 CreatorPrefix + 難度。目標與獎池依上週活躍人數調整；Lv.NewbieLevel 以下的玩家貢獻 ×NewbieMult。
// 委託完成後 24 小時內，整個群組有對應的增益（以虛擬活動加進生效中的活動，和天氣相同）。
// 三張都完成時，每位領獎的玩家另外得到 1 片藏寶圖碎片（每週一次）。不需要 Migration。
package questboard

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/events"
)

const (
	CreatorPrefix = "questboard:"
	NewbieLevel   = 3 // 這個等級以下的玩家貢獻加倍
	NewbieMult    = 2
	BuffHours     = 24
	MaxValue      = 1_000_000 // balance.QuestMaxTarget／QuestMaxGoldReward
)

// Tiers 難度順序。
var Tiers = []string{"easy", "normal", "hard"}

var TierEmoji = map[string]string{"easy": "🟢", "normal": "🟡", "hard": "🔴"}
var TierNames = map[string]string{"easy": "簡單", "normal": "普通", "hard": "困難"}

// Template 委託範本：目標 = PerPlayer × 活躍人數，獎池（依貢獻比例分配的金幣）= PoolPerPlayer × 活躍人數。
type Template struct {
	Tier, Name, Description string
	QuestType, Metric       string
	PerPlayer               int
	PoolPerPlayer           int
}

// Templates 各難度的題庫（每週每個難度抽一張）。
var Templates = map[string][]Template{
	"easy": {
		{"easy", "豐收週", "全伺服器一起收成作物。", "harvest", "quantity", 40, 300},
		{"easy", "熱鬧市集", "全伺服器一起賣出作物（依個數計算）。", "sell", "quantity", 40, 300},
	},
	"normal": {
		{"normal", "大豐收", "全伺服器一起收成更多作物。", "harvest", "quantity", 100, 800},
		{"normal", "商隊出發", "全伺服器一起賣出作物，依賣得的金幣計算。", "sell", "amount", 4_000, 800},
	},
	"hard": {
		{"hard", "收成狂潮", "全伺服器拚命收成！", "harvest", "quantity", 250, 2_000},
		{"hard", "黃金市集", "全伺服器一起賺大錢，依賣得的金幣計算。", "sell", "amount", 12_000, 2_000},
	},
}

// Buff 委託完成後群組的 24 小時增益。
type Buff struct {
	EventID          int64 // 虛擬活動 ID（負數，不和資料庫的活動、天氣的 -1 衝突）
	GrowReductionPct int
	SellPricePct     int // 100 = 不變
	MutationBonusBP  int
	Text             string
}

var Buffs = map[string]Buff{
	"easy":   {EventID: -2, GrowReductionPct: 5, SellPricePct: 100, Text: "澆水後生長時間 -5%"},
	"normal": {EventID: -3, SellPricePct: 105, Text: "作物售價 +5%"},
	"hard":   {EventID: -4, SellPricePct: 100, MutationBonusBP: 50, Text: "種下的作物突變機率 +0.5%"},
}

// Event 增益的虛擬活動。
func (b Buff) Event(tier string) *events.Event {
	return &events.Event{ID: b.EventID, Name: "委託增益・" + TierNames[tier], Description: b.Text,
		SellPricePct: b.SellPricePct, GrowReductionPct: b.GrowReductionPct, MutationBonusBP: b.MutationBonusBP,
		ScheduleType: "once", Enabled: true, CreatedBy: "questboard"}
}

// WeekStart now 所在的台灣週一 00:00。
func WeekStart(now time.Time) time.Time {
	local := now.In(clock.TaipeiTZ)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// Pick 這個群組在 at 所在那一週的 3 張範本（每個難度一張）：由 SHA-256(群組, 週一日期) 決定，同一週重算結果相同。
func Pick(groupID int64, at time.Time) []Template {
	week := WeekStart(at)
	out := make([]Template, 0, len(Tiers))
	for _, tier := range Tiers {
		sum := sha256.Sum256(fmt.Appendf(nil, "questboard-%d-%s-%s", groupID, week.Format("2006-01-02"), tier))
		list := Templates[tier]
		out = append(out, list[binary.BigEndian.Uint64(sum[:8])%uint64(len(list))])
	}
	return out
}

func clamp(v int) int { return min(MaxValue, max(1, v)) }

// Target 目標（活躍人數至少算 1 人）。
func (t Template) Target(players int) int { return clamp(t.PerPlayer * max(1, players)) }

// Pool 獎池。
func (t Template) Pool(players int) int { return clamp(t.PoolPerPlayer * max(1, players)) }

// Title 任務名稱，例如「🟢 委託・豐收週」。
func (t Template) Title() string { return TierEmoji[t.Tier] + " 委託・" + t.Name }

// Creator created_by 欄位。
func (t Template) Creator() string { return CreatorPrefix + t.Tier }

// TierOf created_by → 難度；不是委託板建立的任務回傳 false。
func TierOf(createdBy string) (string, bool) {
	tier, ok := strings.CutPrefix(createdBy, CreatorPrefix)
	if _, known := Buffs[tier]; !ok || !known {
		return "", false
	}
	return tier, true
}

// Weighted 委託板任務的貢獻（新手加倍）。
func Weighted(amount, farmLevel int) int {
	if farmLevel <= NewbieLevel {
		return amount * NewbieMult
	}
	return amount
}
