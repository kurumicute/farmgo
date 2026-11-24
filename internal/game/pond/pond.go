// Package pond 魚塘（Go 版新增，2026-11-24，設計見「魚塘_流程圖.md」）。數值可在管理網站「魚塘」調整
// （featurecfg key = ConfigKey）；魚飼料的價格在「設施與道具價格」。
//
//   - 農場 Lv.UnlockLevel 開放，入口在釣魚中心，不佔農田。每座農場 1 個，可以升到 MaxLevel 級。
//   - 從魚簍把普通、稀有、史詩魚放進魚塘（傳說魚、要用指定魚餌的魚不行；史詩魚最多佔容量的 EpicSharePct%）。
//     放進去的魚不在魚簍裡，只能從魚塘賣出。
//   - 每 PeriodHours 小時一個週期：每隻魚吃 1 份飼料（飼料不夠時先餵售價高的魚），吃到的魚變重
//     (最大重量 − 最小重量) × GrowPct% × (1 + 等級加成) × 天氣倍率，最多到這種魚的最大重量。沒吃到的魚不長，不會死。
//   - 長到最大重量、而且在魚塘裡待滿 1 個週期的魚是「池養極品」：售價 = 最高價 × 等級倍率（以賣出時的魚塘等級計算）。
//     還沒長滿的魚售價 = fishing.FishPrice(養殖後的重量)，和釣到同樣重量的魚一樣。
//   - 繁殖：週期跨過台灣時間 00:00 時，同種魚 BreedMin 隻以上、這個週期都有吃到飼料、重量都在區間 BreedWeightPct% 以上，
//     依稀有度的機率生 1 隻魚苗（最小重量）；每天最多 BreedPerDay 隻，魚塘滿了或史詩魚名額滿了就不生。
//   - 結算在打開魚塘頁或操作魚塘時進行，天氣由日期決定（weather.On），不需要背景排程；最多回推 CatchupDays 天。
//   - 魚塘的等級、飼料、上次結算時間、每隻魚放進來的時間存在 farm_tools（ToolCode）的 settings；
//     魚的養殖重量在 farm_fish.pond_weight_g，魚苗是 farm_fish.bred（Migration 0049）。
package pond

import (
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/weather"
	"farmgm/internal/numfmt"
)

const (
	ConfigKey   = "fish_pond"
	ToolCode    = "fish_pond"
	FeedItem    = "fish_feed" // 魚飼料（balance.ItemFishFeed）
	PeriodHours = 12
	LevelLimit  = 10 // 最多幾級（等級存在 settings，沒有 CHECK）
	priceMax    = 1_000_000_000
)

// Period 一個成長週期。
const Period = PeriodHours * time.Hour

// Rarities 可以放進魚塘的稀有度。
var Rarities = []string{"common", "rare", "epic"}

// Config 目前生效的設定（唯讀）。
type Config struct {
	UnlockLevel  int
	Prices       []int // 第 1 項 = 建造費，第 2 項起 = 升到第 2、3…級的價格
	Capacities   []int // 各級容量（隻）
	GrowBonusPct []int // 各級成長速度加成 %
	PremiumPct   []int // 各級池養極品倍率（最高價的 %）
	GrowPct      int   // 每週期成長佔重量區間的 %
	EpicSharePct int   // 史詩魚最多佔容量的 %
	SunnyPct     int
	CloudyPct    int
	RainyPct     int
	StormPct     int
	DroughtPct   int // 乾旱（連續晴天）時取代晴天的倍率
	FeedSlots    int // 飼料槽 = 容量 × FeedSlots
	BreedMin     int // 同種魚幾隻以上才會繁殖
	BreedWeight  int // 重量要在區間的幾 % 以上
	BreedBP      map[string]int
	BreedPerDay  int
	CatchupDays  int
}

func defaults() featurecfg.Value {
	return featurecfg.Value{
		"unlock_level":    4,
		"prices":          []int{20_000, 50_000, 100_000, 200_000, 400_000},
		"capacities":      []int{10, 14, 18, 24, 30},
		"grow_bonus_pct":  []int{0, 20, 40, 60, 80},
		"premium_pct":     []int{170, 190, 220, 260, 300},
		"grow_pct":        30,
		"epic_share_pct":  25,
		"sunny_pct":       100,
		"cloudy_pct":      100,
		"rainy_pct":       150,
		"storm_pct":       0,
		"drought_pct":     50,
		"feed_slots":      4,
		"breed_min":       2,
		"breed_weight":    60,
		"breed_common_bp": 2000,
		"breed_rare_bp":   1000,
		"breed_epic_bp":   400,
		"breed_per_day":   1,
		"catchup_days":    14,
	}
}

type intField struct {
	key, label string
	low, high  int
	ptr        *int
}

func parse(raw featurecfg.Value) (*Config, error) {
	merged := defaults()
	for k, v := range raw { // 之後新增的設定：之前存的快照沒有時用預設值
		merged[k] = v
	}
	c := &Config{BreedBP: map[string]int{}}
	var err error
	if c.Prices, err = featurecfg.IntList(merged["prices"], "魚塘各級價格", 1, priceMax, 1, LevelLimit); err != nil {
		return nil, err
	}
	n := len(c.Prices)
	for _, f := range []struct {
		key, label string
		low, high  int
		ptr        *[]int
	}{
		{"capacities", "魚塘各級容量", 1, 100, &c.Capacities},
		{"grow_bonus_pct", "魚塘各級成長加成", 0, 1_000, &c.GrowBonusPct},
		{"premium_pct", "魚塘各級池養極品倍率", 100, 500, &c.PremiumPct},
	} {
		if *f.ptr, err = featurecfg.IntList(merged[f.key], f.label, f.low, f.high, n, n); err != nil {
			return nil, gameerr.Validationf("%s要有 %d 個數值（和價格的級數一樣），每個 %d–%d。", f.label, n, f.low, f.high)
		}
	}
	for i := 1; i < n; i++ {
		if c.Capacities[i] < c.Capacities[i-1] {
			return nil, gameerr.Validationf("魚塘第 %d 級的容量不能比第 %d 級小。", i+1, i)
		}
	}
	var common, rare, epic int
	for _, f := range []intField{
		{"unlock_level", "魚塘開放等級", 1, 100, &c.UnlockLevel},
		{"grow_pct", "每週期成長（佔重量區間 %）", 1, 100, &c.GrowPct},
		{"epic_share_pct", "史詩魚佔容量的比例上限（%）", 0, 100, &c.EpicSharePct},
		{"sunny_pct", "晴天成長倍率（%）", 0, 500, &c.SunnyPct},
		{"cloudy_pct", "陰天成長倍率（%）", 0, 500, &c.CloudyPct},
		{"rainy_pct", "雨天成長倍率（%）", 0, 500, &c.RainyPct},
		{"storm_pct", "暴風成長倍率（%）", 0, 500, &c.StormPct},
		{"drought_pct", "乾旱成長倍率（%）", 0, 500, &c.DroughtPct},
		{"feed_slots", "飼料槽（容量的幾倍）", 1, 50, &c.FeedSlots},
		{"breed_min", "繁殖需要的同種魚隻數", 2, 100, &c.BreedMin},
		{"breed_weight", "繁殖的重量門檻（%）", 0, 100, &c.BreedWeight},
		{"breed_common_bp", "普通魚繁殖機率（萬分比）", 0, 10_000, &common},
		{"breed_rare_bp", "稀有魚繁殖機率（萬分比）", 0, 10_000, &rare},
		{"breed_epic_bp", "史詩魚繁殖機率（萬分比）", 0, 10_000, &epic},
		{"breed_per_day", "每天最多繁殖幾隻", 0, 100, &c.BreedPerDay},
		{"catchup_days", "最多回推幾天", 1, 60, &c.CatchupDays},
	} {
		if *f.ptr, err = featurecfg.IntValue(merged[f.key], f.label, f.low, f.high); err != nil {
			return nil, err
		}
	}
	c.BreedBP = map[string]int{"common": common, "rare": rare, "epic": epic}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	return encode(c), nil
}

func encode(c *Config) featurecfg.Value {
	return featurecfg.Value{
		"unlock_level": c.UnlockLevel, "prices": c.Prices, "capacities": c.Capacities, "grow_bonus_pct": c.GrowBonusPct,
		"premium_pct": c.PremiumPct, "grow_pct": c.GrowPct, "epic_share_pct": c.EpicSharePct,
		"sunny_pct": c.SunnyPct, "cloudy_pct": c.CloudyPct, "rainy_pct": c.RainyPct, "storm_pct": c.StormPct,
		"drought_pct": c.DroughtPct, "feed_slots": c.FeedSlots, "breed_min": c.BreedMin, "breed_weight": c.BreedWeight,
		"breed_common_bp": c.BreedBP["common"], "breed_rare_bp": c.BreedBP["rare"], "breed_epic_bp": c.BreedBP["epic"],
		"breed_per_day": c.BreedPerDay, "catchup_days": c.CatchupDays,
	}
}

var (
	cfgMu      sync.Mutex
	cfgVersion = -1
	cfgCache   *Config
)

// Current 目前生效的設定。
func Current() *Config {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if v := featurecfg.Version(); cfgCache == nil || cfgVersion != v {
		c, err := parse(featurecfg.Get(ConfigKey))
		if err != nil {
			c, _ = parse(defaults())
		}
		cfgCache, cfgVersion = c, v
	}
	return cfgCache
}

func init() {
	featurecfg.Register(ConfigKey, "魚塘", defaults, ValidateConfig)
}

// ---------- 等級與數值 ----------

// UnlockLevel 開放的農場等級。
func UnlockLevel() int { return Current().UnlockLevel }

// MaxLevel 目前的最高等級。
func MaxLevel() int { return len(Current().Prices) }

// Price 建造（level = 1）或升到 level 的價格；超出範圍時是 0。
func Price(level int) int {
	p := Current().Prices
	if level < 1 || level > len(p) {
		return 0
	}
	return p[level-1]
}

// levelValue 各級的數值（超過目前最高等級時以最高等級計算）。
func levelValue(list []int, level int) int {
	level = min(level, len(list))
	if level < 1 {
		return 0
	}
	return list[level-1]
}

// Capacity 容量（隻）。
func Capacity(level int) int { return levelValue(Current().Capacities, level) }

// EpicLimit 史詩魚最多幾隻（無條件捨去）。
func EpicLimit(level int) int { return Capacity(level) * Current().EpicSharePct / 100 }

// GrowBonusPct 成長速度加成 %。
func GrowBonusPct(level int) int { return levelValue(Current().GrowBonusPct, level) }

// PremiumPct 池養極品倍率（最高價的 %）。
func PremiumPct(level int) int { return levelValue(Current().PremiumPct, level) }

// FeedCapacity 飼料槽最多放幾份。
func FeedCapacity(level int) int { return Capacity(level) * Current().FeedSlots }

// GrowPctOf 這個等級每週期成長佔重量區間的 %（晴天，含等級加成）。
func GrowPctOf(level int) float64 {
	return float64(Current().GrowPct) * float64(100+GrowBonusPct(level)) / 100
}

// WeatherPct 某個時間的天氣成長倍率 %。
func WeatherPct(at time.Time) int {
	c := Current()
	k := weather.On(at, 0)
	switch k.Code {
	case weather.Rainy:
		return c.RainyPct
	case weather.Storm:
		return c.StormPct
	case weather.Cloudy:
		return c.CloudyPct
	}
	if slices.Contains(weather.LucksOn(at, 0), weather.LuckDrought) {
		return c.DroughtPct
	}
	return c.SunnyPct
}

// ---------- 魚 ----------

// Fish 魚塘裡的一隻魚（ID 0 表示這次結算剛生的魚苗，還沒寫進資料庫）。
type Fish struct {
	ID       int64
	Code     string
	Rarity   string
	MinG     int
	MaxG     int
	MinPrice int
	MaxPrice int
	WeightG  int       // 養殖後的重量
	PlacedAt time.Time // 放進魚塘（或出生）的時間；零值表示很久以前
}

// Full 已經長到最大重量。
func (f Fish) Full() bool { return f.WeightG >= f.MaxG }

// Premium 是池養極品：長到最大，而且在魚塘裡待滿 1 個週期。
func (f Fish) Premium(now time.Time) bool {
	return f.Full() && (f.PlacedAt.IsZero() || !now.Before(f.PlacedAt.Add(Period)))
}

// NormalPrice 依養殖後的重量計算的售價（和釣到同樣重量的魚一樣）。
func (f Fish) NormalPrice() int {
	return fishing.FishPrice(f.MinPrice, f.MaxPrice, f.MinG, f.MaxG, f.WeightG)
}

// PremiumPrice 長滿時的售價（池養極品，以 level 的倍率計算）。
func (f Fish) PremiumPrice(level int) int {
	return numfmt.Round(float64(f.MaxPrice) * float64(PremiumPct(level)) / 100)
}

// Price 現在賣出的價格，以及是不是池養極品。
func (f Fish) Price(level int, now time.Time) (int, bool) {
	if f.Premium(now) {
		return f.PremiumPrice(level), true
	}
	return f.NormalPrice(), false
}

// Progress 成長進度（0–100）。
func (f Fish) Progress() int {
	if f.MaxG <= f.MinG || f.Full() {
		return 100
	}
	return max(0, min(100, (f.WeightG-f.MinG)*100/(f.MaxG-f.MinG)))
}

// Increment 一個週期吃到飼料時增加的重量（level 的成長加成、weatherPct 的天氣倍率）。有成長時至少 1 克。
func Increment(f Fish, level, weatherPct int) int {
	if weatherPct <= 0 || f.MaxG <= f.MinG {
		return 0
	}
	g := float64(f.MaxG-f.MinG) * GrowPctOf(level) / 100 * float64(weatherPct) / 100
	return max(1, numfmt.Round(g))
}

// PeriodsToFull 晴天、每週期都吃到飼料時，還要幾個週期長滿（已經長滿為 0）。
func PeriodsToFull(f Fish, level int) int {
	if f.Full() {
		return 0
	}
	inc := Increment(f, level, 100)
	if inc <= 0 {
		return 0
	}
	return int(math.Ceil(float64(f.MaxG-f.WeightG) / float64(inc)))
}

// PlaceError 這隻魚不能放進魚塘的原因（可以放時回傳 ""）。
func PlaceError(rarity string, needsBait bool) string {
	switch {
	case rarity == "legendary":
		return "傳說魚太珍貴了，不能放進魚塘。"
	case needsBait:
		return "🌊 深海魚（要用指定魚餌才釣得到的魚）沒辦法養在魚塘裡。"
	case !slices.Contains(Rarities, rarity):
		return "這隻魚不能放進魚塘。"
	}
	return ""
}

// ---------- 狀態 ----------

// State 一座農場的魚塘（Level 0 表示還沒建造）。
type State struct {
	Level   int
	Feed    int                 // 飼料槽裡的飼料
	Last    time.Time           // 上次結算到的時間
	Placed  map[int64]time.Time // 魚的 ID → 放進魚塘（或出生）的時間
	BredDay string              // 最後一次繁殖判斷的台灣日期（同一天不會判斷兩次）
	Born    int                 // BredDay 那天已經生了幾隻
}

// Built 已經建造。
func (s State) Built() bool { return s.Level >= 1 }

// Effective 計算效果用的等級（不超過目前的最高等級）。
func (s State) Effective() int { return min(s.Level, MaxLevel()) }

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	if n, ok := featurecfg.AsInt(v); ok {
		return n
	}
	return 0
}

func parseTime(v any) time.Time {
	if raw, _ := v.(string); raw != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Parse 讀出狀態；格式錯誤時當成還沒建造。
func Parse(settings map[string]any) State {
	s := State{Level: num(settings["level"]), Feed: max(0, num(settings["feed"])), Last: parseTime(settings["last"]),
		Placed: map[int64]time.Time{}, Born: max(0, num(settings["born"]))}
	s.BredDay, _ = settings["bred_day"].(string)
	if m, ok := settings["placed"].(map[string]any); ok {
		for k, v := range m {
			if id, err := strconv.ParseInt(k, 10, 64); err == nil {
				s.Placed[id] = parseTime(v)
			}
		}
	}
	if s.Level < 1 || s.Level > LevelLimit {
		return State{Placed: map[int64]time.Time{}}
	}
	return s
}

// Encode 寫回 settings 的格式。
func (s State) Encode() map[string]any {
	placed := map[string]any{}
	for id, at := range s.Placed {
		placed[strconv.FormatInt(id, 10)] = at.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{"level": s.Level, "feed": s.Feed, "last": s.Last.UTC().Format(time.RFC3339Nano),
		"placed": placed, "bred_day": s.BredDay, "born": s.Born}
}

// Attach 把放進魚塘的時間填進魚的資料（沒有紀錄時維持零值）。
func (s State) Attach(fish []Fish) {
	for i := range fish {
		if at, ok := s.Placed[fish[i].ID]; ok {
			fish[i].PlacedAt = at
		}
	}
}

// ---------- 結算 ----------

// Result 一次結算的結果。
type Result struct {
	Periods  int // 結算了幾個週期
	FeedUsed int // 用掉的飼料
	Hungry   int // 沒吃到飼料的「魚 × 週期」
	Born     int // 這次生了幾隻魚苗（在 fish 裡 ID 為 0，WeightG 是結算到 now 的重量）
}

func dayKey(t time.Time) string { return t.In(clock.TaipeiTZ).Format("2006-01-02") }

// Settle 從 s.Last 結算到 now（fish 的 WeightG 會被更新；新生的魚苗加在 fish 後面，ID 為 0）。
// rng 回傳 [0, 1)，用來判斷繁殖。fish 必須是魚塘裡全部的魚（容量與史詩魚名額要算全部）。
func Settle(s *State, fish *[]Fish, now time.Time, rng func() float64) Result {
	var res Result
	if !s.Built() {
		return res
	}
	if s.Last.IsZero() || s.Last.After(now) {
		s.Last = now
		return res
	}
	n := int(now.Sub(s.Last) / Period)
	if n <= 0 {
		return res
	}
	if limit := Current().CatchupDays * 24 / PeriodHours; n > limit {
		s.Last = now.Add(-time.Duration(limit) * Period)
		n = limit
	}
	level := s.Effective()
	for i := 0; i < n; i++ {
		start := s.Last
		end := start.Add(Period)
		pct := WeatherPct(start)
		list := *fish
		order := make([]int, len(list))
		for j := range order {
			order[j] = j
		}
		slices.SortStableFunc(order, func(a, b int) int { // 先餵售價高的魚
			pa, pb := list[a].NormalPrice(), list[b].NormalPrice()
			if pa != pb {
				return pb - pa
			}
			return int(list[a].ID - list[b].ID)
		})
		fed := make([]bool, len(list))
		for _, j := range order {
			if s.Feed <= 0 {
				res.Hungry++
				continue
			}
			s.Feed--
			res.FeedUsed++
			fed[j] = true
			if f := &list[j]; !f.Full() {
				f.WeightG = min(f.MaxG, f.WeightG+Increment(*f, level, pct))
			}
		}
		if dayKey(start) != dayKey(end) { // 跨過台灣時間 00:00：繁殖判斷
			res.Born += breed(s, fish, fed, end, level, rng)
		}
		s.Last = end
		res.Periods++
	}
	return res
}

// breed 一次繁殖判斷（每天最多 BreedPerDay 隻）。新生的魚苗加在 fish 後面。
func breed(s *State, fish *[]Fish, fed []bool, at time.Time, level int, rng func() float64) int {
	c := Current()
	day := dayKey(at)
	if s.BredDay != day {
		s.BredDay, s.Born = day, 0
	}
	list := *fish
	var codes []string
	groups := map[string][]int{}
	for j, f := range list {
		if j >= len(fed) || !fed[j] || f.MaxG <= f.MinG {
			continue
		}
		if f.WeightG < f.MinG+(f.MaxG-f.MinG)*c.BreedWeight/100 {
			continue
		}
		if _, ok := groups[f.Code]; !ok {
			codes = append(codes, f.Code)
		}
		groups[f.Code] = append(groups[f.Code], j)
	}
	slices.Sort(codes)
	born := 0
	for _, code := range codes {
		if s.Born >= c.BreedPerDay || len(*fish) >= Capacity(level) {
			break
		}
		members := groups[code]
		if len(members) < c.BreedMin {
			continue
		}
		parent := list[members[0]]
		if parent.Rarity == "epic" && countRarity(*fish, "epic") >= EpicLimit(level) {
			continue
		}
		if rng() >= float64(c.BreedBP[parent.Rarity])/10_000 {
			continue
		}
		fry := parent
		fry.ID, fry.WeightG, fry.PlacedAt = 0, max(1, parent.MinG), at
		*fish = append(*fish, fry)
		born++
		s.Born++
	}
	return born
}

func countRarity(fish []Fish, rarity string) int {
	n := 0
	for _, f := range fish {
		if f.Rarity == rarity {
			n++
		}
	}
	return n
}

// CountRarity 某個稀有度的魚有幾隻。
func CountRarity(fish []Fish, rarity string) int { return countRarity(fish, rarity) }

// FeedHours 目前的飼料還能吃幾小時（沒有魚時為 -1）。
func FeedHours(feed, fishCount int) int {
	if fishCount <= 0 {
		return -1
	}
	return feed / fishCount * PeriodHours
}
