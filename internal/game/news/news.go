// Package news 農場日報：把群組前一天（台灣日期）的交易紀錄整理成八卦小報風格的頭條（純規則，不碰資料庫）。
// 對應 Python app/game/news.py。
//
// 只報導所屬群組（farms.home_group_id）是這個群組的農場。日報顯示在 /farm 總覽頁的最下方（只有開面板的人看得到），
// Bot 不會主動發訊息到頻道，也沒有額外的按鈕。
//
// 農場傳聞：每天約一半的機率，日報多一條「某座農場的田底下埋了寶藏」的傳聞（主角是前一天有動靜的成員）。
// 傳聞不一定是真的；當天偷那座農場的菜成功時會順手挖一次（每人每天一次），傳聞是真的就有機率挖到寶藏，
// 寶藏只有第一個挖到的人拿得到。傳聞由（群組, 日期, 前一天有動靜的農場）決定，不必存資料庫，日報與挖寶看到的一定相同。
// 亂數使用與 CPython random.Random 相同的實作（pyrandom.go），因此與 Python 版結果完全一致。
package news

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
	"farmgm/internal/numfmt"
)

const (
	MaxHeadlines     = 4    // 總覽頁只放最重要的幾條，避免面板太長
	FieldLimit       = 1024 // Discord Embed 欄位上限
	RumorChance      = 0.5  // 每天出現傳聞的機率
	RumorTrueChance  = 0.7  // 傳聞是真的機率
	DigChance        = 0.35 // 傳聞是真的時，每次挖寶挖到的機率
	TreasureGoldLow  = 300
	TreasureGoldHigh = 800
)

var RumorTemplates = []string{
	"🗺️ 傳聞：據說 {who} 的田底下埋著前任農場主人的寶藏…（真假未知，偷菜時順手挖挖看？）",
	"🗺️ 傳聞：有人半夜看到 {who} 鬼鬼祟祟地在田裡挖洞，好像埋了什麼東西。",
	"🗺️ 傳聞：酒館老闆說 {who} 的田裡有一張藏寶圖指著的位置，但他喝醉了。",
}

var SalesEvents = []string{"sell", "fish_sell"}

var markdownReplacer = func() *strings.Replacer {
	var pairs []string
	for _, c := range "\\*_~`|>[]()#" {
		pairs = append(pairs, string(c), "\\"+string(c))
	}
	pairs = append(pairs, "@", "@​")
	return strings.NewReplacer(pairs...)
}()

// dateOnly 取 t 的年月日（不轉時區），回傳台灣時間 00:00。日期一律以這種 time.Time 表示。
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
}

// NewsDay 現在顯示的日報是哪一天（台灣時間的昨天）。
func NewsDay(now time.Time) time.Time {
	return dateOnly(now.In(clock.TaipeiTZ)).AddDate(0, 0, -1)
}

// DayRange 某個台灣日期的 [開始, 結束)。
func DayRange(day time.Time) (time.Time, time.Time) {
	start := dateOnly(day)
	return start, start.AddDate(0, 0, 1)
}

// SafeName 玩家取的農場名稱放進 Discord 訊息前跳脫 Markdown，並讓 @ 不會變成提及。
func SafeName(name string) string {
	return markdownReplacer.Replace(name)
}

// Event 當日交易彙總：(farm_id, event_type, 筆數, 數量合計, 金幣合計)。
type Event struct {
	FarmID    int64
	EventType string
	Count     int
	Qty       int
	Gold      int
}

// Special 特殊收成：(farm_id, item_code, quality, mutation, 數量)。
type Special struct {
	FarmID   int64
	ItemCode string
	Quality  string
	Mutation string
	Qty      int
}

// Fish 最大的魚：(farm_id, 魚名, 稀有度名稱, 重量 g)。
type Fish struct {
	FarmID  int64
	Name    string
	Rarity  string
	WeightG int
}

// Ruins 遺跡的當日狀態。
type Ruins struct {
	Donated               int // 昨天捐的素材數
	Percent               int // 修復進度
	CompletedToday        bool
	CollapsedToday        bool
	GuardianDefeatedToday bool
}

// Rumor 傳聞。
type Rumor struct {
	Target   int64 // 傳聞主角的 farm_id
	Real     bool  // 寶藏是不是真的
	Template int
}

// DayData 產生日報需要的當日彙總（由服務層從資料庫查詢後填入）。
type DayData struct {
	Events    []Event
	Special   []Special
	Fish      *Fish
	Names     map[int64]string // farm_id → 農場名稱
	ItemNames map[string]string
	Ruins     *Ruins
	Rumor     *Rumor
}

// MakeRumor day 的日報附帶的傳聞（candidates：day 當天有動靜的農場）；沒有傳聞時回傳 nil。結果只由參數決定。
func MakeRumor(groupID int64, day time.Time, candidates []int64) *Rumor {
	if len(candidates) == 0 {
		return nil
	}
	rng := newPyRandom(fmt.Sprintf("rumor-%d-%s", groupID, dateOnly(day).Format("2006-01-02")))
	if rng.random() >= RumorChance {
		return nil
	}
	sorted := slices.Clone(candidates)
	slices.Sort(sorted)
	target := sorted[rng.randbelow(len(candidates))]
	real := rng.random() < RumorTrueChance
	return &Rumor{Target: target, Real: real, Template: rng.randbelow(len(RumorTemplates))}
}

// TreasureGold u ∈ [0, 1) 換算寶藏金幣。
func TreasureGold(u float64) int {
	low, high := TreasureGoldLow, TreasureGoldHigh
	return low + min(high-low, int(u*float64(high-low+1)))
}

// News 一天的日報。
type News struct {
	Day       time.Time
	Headlines []string
	Summary   string
	Active    bool // 當天有沒有任何交易紀錄
}

// Empty 沒有頭條也沒有任何動靜：總覽不顯示日報。
func (n News) Empty() bool { return len(n.Headlines) == 0 && !n.Active }

func who(data DayData, farmID int64) string {
	name, ok := data.Names[farmID]
	if !ok {
		name = "某座農場"
	}
	return "**" + SafeName(name) + "**"
}

type topEntry struct {
	farmID int64
	value  int
}

// top 數值最大的 (farm_id, 數值)；同分時取 farm_id 小的，結果固定。
func top(perFarm map[int64]int, minimum int) *topEntry {
	var best *topEntry
	for id, v := range perFarm {
		if best == nil || v > best.value || (v == best.value && id < best.farmID) {
			best = &topEntry{id, v}
		}
	}
	if best != nil && best.value >= minimum {
		return best
	}
	return nil
}

func sum(m map[int64]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// Build 產生日報；seed 讓同一天同一群組的措辭固定（重新整理不會變）。
func Build(day time.Time, data DayData, seed string) News {
	rng := newPyRandom(dateOnly(day).Format("2006-01-02") + "-" + seed)
	count := map[string]map[int64]int{}
	qty := map[string]map[int64]int{}
	gold := map[string]map[int64]int{}
	add := func(m map[string]map[int64]int, event string, id int64, v int) {
		if m[event] == nil {
			m[event] = map[int64]int{}
		}
		m[event][id] += v
	}
	for _, e := range data.Events {
		add(count, e.EventType, e.FarmID, e.Count)
		add(qty, e.EventType, e.FarmID, e.Qty)
		add(gold, e.EventType, e.FarmID, e.Gold)
	}
	var headlines []string

	if r := data.Ruins; r != nil {
		if r.CollapsedToday {
			headlines = append(headlines, "🏚️ **噩耗！** 上週沒有人打倒遺跡守護者，古代遺跡崩塌了，要重新捐核心素材才能修好。")
		}
		if r.GuardianDefeatedToday {
			headlines = append(headlines, "⚔️ 群組成員合力打倒了本週的遺跡守護者，本週遺跡探索每次多兩份獎勵！")
		}
		if r.CompletedToday {
			headlines = append(headlines, "🏛️ **號外！古代遺跡修復完成！** 群組成員合力喚醒了遺跡核心，山洞多了一個新的探險區域。")
		}
	}

	sales := map[int64]int{}
	for _, event := range SalesEvents {
		for id, v := range gold[event] {
			sales[id] += v
		}
	}
	if t := top(sales, 1); t != nil {
		lead := choice(rng, []string{"一天狂賣", "昨天豪賺", "靠著一車農產品賺進"})
		headlines = append(headlines, fmt.Sprintf("💰 %s %s **%s** 金，榮登昨日銷售冠軍！", who(data, t.farmID), lead, numfmt.Comma(t.value)))
	}

	if len(data.Special) > 0 {
		s := data.Special[0]
		crop, ok := data.ItemNames[s.ItemCode]
		if !ok {
			crop = s.ItemCode
		}
		var tags []string
		if s.Quality == "legendary" {
			tags = append(tags, "傳說品質")
		}
		if name, ok := balance.Mutations[s.Mutation]; ok && s.Mutation != "none" {
			tags = append(tags, name)
		}
		headlines = append(headlines, fmt.Sprintf("🌟 奇蹟！%s 收成了 **%d** 個%s的%s，鄰居都跑來圍觀。",
			who(data, s.FarmID), s.Qty, strings.Join(tags, "、"), crop))
	}

	victims := top(count["stolen"], 2)
	thefts := sum(count["steal"])
	if victims != nil {
		sob := choice(rng, []string{"田裡只剩稻草人在哭泣", "主人表示要去買一隻狗", "警方（並沒有）介入調查"})
		headlines = append(headlines, fmt.Sprintf("🥷 驚！%s 一天被偷 **%d** 次，%s。", who(data, victims.farmID), victims.value, sob))
	} else if thefts != 0 {
		headlines = append(headlines, fmt.Sprintf("🥷 昨天發生 **%d** 起偷菜事件，請大家看好自己的田。", thefts))
	}
	if thief := top(count["steal"], 3); thief != nil {
		headlines = append(headlines, fmt.Sprintf("🧤 神偷 %s 昨天出手 **%d** 次，手套都磨破了。", who(data, thief.farmID), thief.value))
	}

	if defended := sum(count["defended"]); defended != 0 {
		if guard := top(count["defended"], 1); guard != nil {
			headlines = append(headlines, fmt.Sprintf("🛡️ 防守設施昨天擋下 **%d** 次偷菜，最堅固的是 %s 的農場。", defended, who(data, guard.farmID)))
		}
	}

	if f := data.Fish; f != nil {
		headlines = append(headlines, fmt.Sprintf("🎣 %s 釣到一條 **%.1f** 公斤的%s魚「%s」！",
			who(data, f.FarmID), float64(f.WeightG)/1000, f.Rarity, f.Name))
	}

	if r := data.Ruins; r != nil && r.Donated != 0 && !r.CompletedToday {
		headlines = append(headlines, fmt.Sprintf("🏛️ 古代遺跡修復進度 **%d%%**，昨天大家捐了 **%d** 個素材。", r.Percent, r.Donated))
	}

	if finder := top(gold["rumor_treasure"], 1); finder != nil {
		headlines = append([]string{fmt.Sprintf("💎 %s 昨天在傳聞說的田裡挖到寶藏，進帳 **%s** 金！",
			who(data, finder.farmID), numfmt.Comma(finder.value))}, headlines...)
	}

	if gambles := sum(count["gamble"]); gambles != 0 {
		mood := choice(rng, []string{"有人笑著離開", "莊家依然微笑", "骰子都快滾壞了"})
		headlines = append(headlines, fmt.Sprintf("🎰 賭場昨天開了 **%d** 局，%s。", gambles, mood))
	}

	farms := map[int64]bool{}
	for _, e := range data.Events {
		farms[e.FarmID] = true
	}
	summary := fmt.Sprintf("🌾 收成 %s 個作物　💰 銷售 %s 金　👨‍🌾 %d 座農場有動靜",
		numfmt.Comma(sum(qty["harvest"])), numfmt.Comma(sum(sales)), len(farms))
	limit := MaxHeadlines
	if data.Rumor != nil {
		limit = MaxHeadlines - 1
	}
	if len(headlines) > limit {
		headlines = headlines[:limit]
	}
	if data.Rumor != nil { // 傳聞一定放在最後一條
		headlines = append(headlines, strings.ReplaceAll(RumorTemplates[data.Rumor.Template], "{who}", who(data, data.Rumor.Target)))
	}
	return News{Day: dateOnly(day), Headlines: headlines, Summary: summary, Active: len(farms) > 0}
}

// Render 總覽頁「📰 昨日農場日報」欄位的內容（以整行截斷，不超過 Discord 欄位上限；長度以字元計算）。
func Render(n News) string {
	quiet := "昨天農場一片寧靜，大家都在睡覺。"
	if n.Active {
		quiet = "昨天大家都在默默耕耘，沒有什麼大新聞。"
	}
	lines := n.Headlines
	if len(lines) == 0 {
		lines = []string{quiet}
	}
	lines = append(slices.Clone(lines), "-# "+n.Summary)
	var out []string
	for _, line := range lines {
		// 放不下的那一行跳過，後面較短的行（含摘要）照放
		if utf8.RuneCountInString(strings.Join(append(slices.Clone(out), line), "\n")) <= FieldLimit {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
