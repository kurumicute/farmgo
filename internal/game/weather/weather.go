// Package weather 每日天氣（Go 版新增，2026-11-17）：每天（台灣時間）全服抽一種天氣。
//
//   - 天氣只由日期決定（同日報傳聞的做法），不寫資料庫；同一天每個人、每台機器看到的都一樣。
//   - 天氣的效果做成一個「虛擬限時事件」（ID = EventID），由服務層加進生效中的事件清單，
//     所以生長加速、免澆水、收成減損都直接走 events.BonusOf，和管理員開的活動相加、共用上限。
//   - 釣魚機率不是事件欄位，另外由 FishEffects 加在魚餌效果上（手動釣魚與自動釣魚機都適用）。
//   - 暴風的收成減損可以被防守設施減輕：每種守護這格的設施（稻草人、警戒犬、防盜圍欄）擋下 StormDefenseBlock 個。
package weather

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/events"
	"farmgm/internal/game/fishing"
)

// 天氣代碼。
const (
	Sunny  = "sunny"
	Cloudy = "cloudy"
	Rainy  = "rainy"
	Storm  = "storm"
)

// EventID 天氣虛擬事件的 ID（資料庫的事件 ID 都是正數，不會衝突）。
const EventID int64 = -1

// StormDefenseBlock 暴風時每種守護該格的防守設施擋下幾個（同種設施重疊只算一次）。
const StormDefenseBlock = 1

// Kind 一種天氣與它的效果。
type Kind struct {
	Code   string
	Name   string
	Emoji  string
	Weight int // 抽中的權重（合計 100）
	// 事件效果欄位（對應 events.Effects）
	GrowReductionPct int // 澆水時生長時間減少 %
	FreeWater        int // 1 = 種下立即澆水（下雨省水）
	HarvestLoss      int // 收成時每格少 N 個（至少保留 1 個），防守設施可以減輕
	// 釣魚：加在魚餌效果上的稀有度機率（萬分比，從普通扣除）
	FishBP map[string]int
}

// Order 抽籤順序。
var Order = []string{Sunny, Cloudy, Rainy, Storm}

// Kinds 天氣表。
var Kinds = map[string]Kind{
	Sunny:  {Code: Sunny, Name: "晴天", Emoji: "☀️", Weight: 35, GrowReductionPct: 10},
	Cloudy: {Code: Cloudy, Name: "陰天", Emoji: "☁️", Weight: 30},
	Rainy: {Code: Rainy, Name: "雨天", Emoji: "🌧️", Weight: 25, FreeWater: 1,
		FishBP: map[string]int{"rare": 300, "epic": 100, "legendary": 30}},
	Storm: {Code: Storm, Name: "暴風", Emoji: "⛈️", Weight: 10, HarvestLoss: 1}, // 2026-11-17 使用者要求：吹走 1 個就好（原本 2 個）
}

// Fixed 不是空字串時固定為這種天氣（測試用，避免結果隨測試日期改變）。
var Fixed string

// Today now 所在的台灣日期的天氣。
func Today(now time.Time) Kind {
	if k, ok := Kinds[Fixed]; ok {
		return k
	}
	return ForDate(now)
}

// ForDate 只由台灣日期決定：SHA-256("weather-YYYY-MM-DD") 的前 8 位元組取餘數，依權重落點。
func ForDate(now time.Time) Kind {
	day := now.In(clock.TaipeiTZ).Format("2006-01-02")
	sum := sha256.Sum256([]byte("weather-" + day))
	total := 0
	for _, c := range Order {
		total += Kinds[c].Weight
	}
	roll := int(binary.BigEndian.Uint64(sum[:8]) % uint64(total))
	for _, c := range Order {
		if roll < Kinds[c].Weight {
			return Kinds[c]
		}
		roll -= Kinds[c].Weight
	}
	return Kinds[Order[len(Order)-1]]
}

// Label 例如「☀️ 晴天」。
func (k Kind) Label() string { return k.Emoji + " " + k.Name }

// Event 天氣的虛擬事件；沒有任何事件效果時回傳 nil。
func (k Kind) Event() *events.Event {
	if k.GrowReductionPct == 0 && k.FreeWater == 0 && k.HarvestLoss == 0 {
		return nil
	}
	return &events.Event{
		ID: EventID, Name: "今日天氣・" + k.Name, Description: k.EffectText(),
		SellPricePct: 100, GrowReductionPct: k.GrowReductionPct, FreeWater: k.FreeWater, HarvestLoss: k.HarvestLoss,
		ScheduleType: "once", Enabled: true, CreatedBy: "weather",
	}
}

// FishEffects 魚餌效果加上天氣的稀有度加成（回傳新的 map，不改動傳入的效果）。
func (k Kind) FishEffects(bait fishing.Effects) fishing.Effects {
	if len(k.FishBP) == 0 {
		return bait
	}
	out := make(fishing.Effects, len(bait)+len(k.FishBP))
	for key, v := range bait {
		out[key] = v
	}
	for r, bp := range k.FishBP {
		out[r+"_bp"] += bp
	}
	return out
}

// EffectText 效果說明，例如「澆水後生長時間 -10%」。
func (k Kind) EffectText() string {
	var parts []string
	if k.GrowReductionPct > 0 {
		parts = append(parts, fmt.Sprintf("澆水後生長時間 -%d%%", k.GrowReductionPct))
	}
	if k.FreeWater > 0 {
		parts = append(parts, "種下的作物自動澆水")
	}
	if len(k.FishBP) > 0 {
		var fish []string
		for _, r := range fishing.Rarities {
			if bp := k.FishBP[r]; bp > 0 {
				fish = append(fish, fmt.Sprintf("%s +%s%%", fishing.RarityNames[r], bpText(bp)))
			}
		}
		parts = append(parts, "釣魚稀有度 "+strings.Join(fish, "、"))
	}
	if k.HarvestLoss > 0 {
		parts = append(parts, fmt.Sprintf("收成時每格被吹壞 %d 個（至少留 1 個；每種守護這格的防守設施擋下 %d 個）", k.HarvestLoss, StormDefenseBlock))
	}
	if len(parts) == 0 {
		return "沒有特別影響，平常心種田"
	}
	return strings.Join(parts, "；")
}

func bpText(bp int) string { return fmt.Sprintf("%g", float64(bp)/100) }
