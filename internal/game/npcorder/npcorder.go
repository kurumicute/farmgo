// Package npcorder NPC 限時訂單（Go 版新增，2026-11-20，設計見「新玩法六項_流程圖.md」#8）。
//
//   - 同時最多 Slots 張訂單；每 RefillSeconds 補到 Slots 張。逾時的訂單直接消失（連續完成次數歸零），沒有其他懲罰。
//   - 訂單只要求玩家「做得出來」的東西（呼叫端傳入 eligible：當季作物、已蓋設施的加工品、有養的動物產品、已開放的菇類）。
//   - 報酬 = 物品基準售價 × MultMin～MultMax%；VIP（美食評論家）要高價加工品，報酬 VIPMultPct%。
//   - 每天可以免費換單 FreeRerolls 次，之後每次 RerollFee 金幣。連續完成 StreakTarget 張時那一張多 StreakBonusPct%。
//   - 狀態存在 farm_tools（ToolCode）的 settings JSON，不需要新表與 Migration。
package npcorder

import (
	"slices"
	"time"
)

const (
	ToolCode       = "npc_orders"
	Slots          = 3
	RefillSeconds  = 4 * 3600
	MinHours       = 2
	MaxHours       = 6
	MultMin        = 130 // 報酬倍率（%）
	MultMax        = 180
	VIPChanceBP    = 500 // 每張新訂單是 VIP 的機率（萬分比）
	VIPMultPct     = 250
	VIPMinPrice    = 500 // VIP 只要售價至少這麼多的物品
	MaxQty         = 20
	TwoItemLevel   = 5 // 農場 Lv.5 起有機會一張訂單要 2 種物品
	RerollFee      = 100
	FreeRerolls    = 1
	StreakTarget   = 5
	StreakBonusPct = 50
	FragmentSource = "order" // treasure.SourceOrder（不 import treasure，避免循環）
)

// NPC 客人。Likes 依顯示順序；VIP 不看 Likes，改要任何售價 ≥ VIPMinPrice 的物品。
type NPC struct {
	Code, Emoji, Name, Line string
	Likes                   []string
	VIP                     bool
}

var NPCOrder = []string{"baker", "fisher", "girl", "herbalist", "rancher", "critic"}

var NPCs = map[string]NPC{
	"baker":     {"baker", "👩‍🍳", "麵包師 蜜拉", "今天的可頌缺料，拜託了！", []string{"wheat", "flour", "bread", "cake", "strawberry_cake", "butter"}, false},
	"fisher":    {"fisher", "🧓", "老漁夫 阿海", "年輕人，幫我準備做魚餌的材料。", []string{"wheat", "corn", "carrot", "tomato"}, false},
	"girl":      {"girl", "👧", "小女孩 小葵", "我想送媽媽一份禮物！", []string{"strawberry", "grape", "strawberry_jam", "grape_jam", "mixed_jam", "strawberry_cake"}, false},
	"herbalist": {"herbalist", "🧙", "藥草師 莫爾", "這些菇……正是我要的。", []string{"shiitake", "button_mushroom", "enoki", "lingzhi", "truffle", "glowcap", "mushroom_soup", "lingzhi_cake"}, false},
	"rancher":   {"rancher", "🤠", "牧場主 老傑", "牧場忙不過來，幫個忙！", []string{"egg", "milk", "wool", "cheese", "butter", "mayonnaise", "corn"}, false},
	"critic":    {"critic", "🎩", "美食評論家", "讓我看看你的真本事。", nil, true},
}

// Line 訂單需要的一種物品。
type Line struct {
	Code string
	Qty  int
}

// Order 一張訂單。
type Order struct {
	ID      int
	NPC     string
	Items   []Line
	Reward  int
	Expires time.Time
}

// VIP 是不是 VIP 訂單。
func (o Order) VIP() bool { return NPCs[o.NPC].VIP }

// Value 依目前售價計算訂單物品的總值（顯示用）。
func Value(items []Line, prices map[string]int) int {
	v := 0
	for _, l := range items {
		v += prices[l.Code] * l.Qty
	}
	return v
}

// targetValue 一張訂單的目標物品總值（隨農場等級增加）。
func targetValue(level int) int { return 200 + 60*max(1, level) }

// candidates NPC 想要、而且玩家做得出來的物品（依 Likes 順序；VIP 依代碼排序）。
func candidates(npc NPC, eligible map[string]int) []string {
	var out []string
	if npc.VIP {
		for code, price := range eligible {
			if price >= VIPMinPrice {
				out = append(out, code)
			}
		}
		slices.Sort(out)
		return out
	}
	for _, code := range npc.Likes {
		if eligible[code] > 0 {
			out = append(out, code)
		}
	}
	return out
}

func pick(n int, u float64) int { return min(n-1, max(0, int(u*float64(n)))) }

// Generate 產生一張訂單；沒有任何 NPC 想要玩家做得出來的東西時回傳 false。
func Generate(id int, eligible map[string]int, level int, now time.Time, rng func() float64) (Order, bool) {
	var regular []string
	for _, code := range NPCOrder {
		if npc := NPCs[code]; !npc.VIP && len(candidates(npc, eligible)) > 0 {
			regular = append(regular, code)
		}
	}
	npcCode := ""
	if vip := NPCs["critic"]; int(rng()*10_000) < VIPChanceBP && len(candidates(vip, eligible)) > 0 {
		npcCode = vip.Code
	} else if len(regular) > 0 {
		npcCode = regular[pick(len(regular), rng())]
	} else {
		return Order{}, false
	}
	npc := NPCs[npcCode]
	options := candidates(npc, eligible)
	count := 1
	if level >= TwoItemLevel && len(options) >= 2 && rng() < 0.4 {
		count = 2
	}
	target := targetValue(level) / count
	var items []Line
	for range count {
		i := pick(len(options), rng())
		code := options[i]
		options = slices.Delete(options, i, i+1)
		qty := min(MaxQty, max(1, (target+eligible[code]/2)/eligible[code]))
		items = append(items, Line{code, qty})
	}
	value := Value(items, eligible)
	mult := MultMin + pick(MultMax-MultMin+1, rng())
	if npc.VIP {
		mult = VIPMultPct
	}
	hours := MinHours + pick(MaxHours-MinHours+1, rng())
	return Order{ID: id, NPC: npcCode, Items: items, Reward: value * mult / 100,
		Expires: now.Add(time.Duration(hours) * time.Hour)}, true
}

// State 一座農場的訂單狀態（farm_tools.settings）。
type State struct {
	Orders    []Order
	NextID    int
	RefillAt  time.Time // 下次補單時間（零值 = 立刻補）
	RerollDay string    // 換單次數所屬的台灣日期
	Rerolls   int
	Streak    int // 連續完成（沒有逾時）的張數
}

// Refresh 移除逾時的訂單（連續次數歸零），到了補單時間就補到 Slots 張。回傳狀態是否有變動。
func (s *State) Refresh(now time.Time, eligible map[string]int, level int, rng func() float64) bool {
	changed := false
	kept := s.Orders[:0:0]
	for _, o := range s.Orders {
		if now.Before(o.Expires) {
			kept = append(kept, o)
		} else {
			changed = true
			s.Streak = 0
		}
	}
	s.Orders = kept
	if now.Before(s.RefillAt) {
		return changed
	}
	for len(s.Orders) < Slots {
		o, ok := Generate(s.NextID+1, eligible, level, now, rng)
		if !ok {
			break
		}
		s.NextID++
		s.Orders = append(s.Orders, o)
	}
	s.RefillAt = now.Add(RefillSeconds * time.Second)
	return true
}

// Find 依 ID 找訂單的位置（-1 = 沒有）。
func (s State) Find(id int) int {
	return slices.IndexFunc(s.Orders, func(o Order) bool { return o.ID == id })
}

// RerollsToday 今天已經換過幾次。
func (s State) RerollsToday(day string) int {
	if s.RerollDay != day {
		return 0
	}
	return s.Rerolls
}

// RerollFeeToday 今天下一次換單的費用。
func (s State) RerollFeeToday(day string) int {
	if s.RerollsToday(day) < FreeRerolls {
		return 0
	}
	return RerollFee
}

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func parseTime(v any) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, str(v))
	return t, err == nil
}

// Parse 讀出狀態；格式錯誤的訂單略過（不讓壞資料卡住玩家）。
func Parse(settings map[string]any) State {
	s := State{NextID: num(settings["next_id"]), RerollDay: str(settings["reroll_day"]),
		Rerolls: num(settings["rerolls"]), Streak: num(settings["streak"])}
	if t, ok := parseTime(settings["refill_at"]); ok {
		s.RefillAt = t
	}
	raw, _ := settings["orders"].([]any)
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		o := Order{ID: num(m["id"]), NPC: str(m["npc"]), Reward: num(m["reward"])}
		exp, ok := parseTime(m["expires"])
		if _, known := NPCs[o.NPC]; !ok || !known || o.ID <= 0 || o.Reward <= 0 {
			continue
		}
		o.Expires = exp
		lines, _ := m["items"].([]any)
		for _, l := range lines {
			if lm, ok := l.(map[string]any); ok && str(lm["code"]) != "" && num(lm["qty"]) > 0 {
				o.Items = append(o.Items, Line{str(lm["code"]), num(lm["qty"])})
			}
		}
		if len(o.Items) > 0 {
			s.Orders = append(s.Orders, o)
			s.NextID = max(s.NextID, o.ID)
		}
	}
	return s
}

// Encode 寫回 settings 的格式（時間為 UTC RFC3339）。
func (s State) Encode() map[string]any {
	orders := make([]any, len(s.Orders))
	for i, o := range s.Orders {
		items := make([]any, len(o.Items))
		for j, l := range o.Items {
			items[j] = map[string]any{"code": l.Code, "qty": l.Qty}
		}
		orders[i] = map[string]any{"id": o.ID, "npc": o.NPC, "items": items, "reward": o.Reward,
			"expires": o.Expires.UTC().Format(time.RFC3339Nano)}
	}
	out := map[string]any{"orders": orders, "next_id": s.NextID, "reroll_day": s.RerollDay, "rerolls": s.Rerolls,
		"streak": s.Streak}
	if !s.RefillAt.IsZero() {
		out["refill_at"] = s.RefillAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
