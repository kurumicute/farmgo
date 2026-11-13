// Package achievements 成就與稱號：成就定義、進度指標與獎勵（只放規則，不碰資料庫）。
// 對應 Python app/game/achievements.py。
//
//   - 每個成就看一個指標（metric）是否達到 target；指標由服務層從既有資料計算，因此歷史進度也會算進去。
//   - 達成後在「成就」頁按領取才發獎（金幣、道具、稱號）；每個成就每座農場只能領一次。
//   - 獎勵以道具與稱號為主、金幣少量，避免通膨；道具放不進倉庫時改發等值金幣（同每日簽到）。
//   - 代碼一旦上線就不要改（已領取紀錄以代碼保存）。門檻、金幣、道具與稱號可在管理網站調整
//     （featurecfg 的 "achievements"，只存有修改的成就），一律經 Active()／ByCode() 讀取套用設定後的成就。
//   - 改稱號名稱只影響之後領取的人，已經拿到的稱號維持原本的名稱。
package achievements

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

const (
	ConfigKey   = "achievements"
	TitleMaxLen = 20
)

var PctMetrics = map[string]bool{"codex_crop_pct": true, "codex_fish_pct": true, "codex_pct": true,
	"codex_mushroom_pct": true, "codex_lab_pct": true}

// Metric 指標名稱與單位；值由服務層（ProgressService._achievement_metrics）計算。
type Metric struct {
	Label string
	Unit  string
}

var MetricOrder = []string{
	"crops", "crop_sales", "level", "plots", "gold", "legendary_crop", "variant_crop", "animal_products", "goods",
	"explorations", "fish", "legendary_fish", "steals", "defended", "gambles", "boss_joined", "boss_won",
	"best_streak", "codex_crop_pct", "codex_fish_pct", "codex_pct",
	// 地下菇房與種子實驗室（Go 版新增）
	"mushrooms", "codex_mushroom_pct", "lab_built", "lab_crosses", "lab_mutations", "lab_hybrids", "lab_shares", "codex_lab_pct",
}

var Metrics = map[string]Metric{
	"crops":           {"取得作物", "個"},
	"crop_sales":      {"作物販售收入", "金"},
	"level":           {"農場等級", "級"},
	"plots":           {"農田", "格"},
	"gold":            {"持有金幣", "金"},
	"legendary_crop":  {"傳說品質作物", "次"},
	"variant_crop":    {"珍稀變種作物", "種"},
	"animal_products": {"收集動物產品", "個"},
	"goods":           {"領取加工品", "份"},
	"explorations":    {"完成山洞探險", "次"},
	"fish":            {"釣到的魚", "隻"},
	"legendary_fish":  {"傳說魚", "隻"},
	"steals":          {"偷菜成功", "次"},
	"defended":        {"防守成功", "次"},
	"gambles":         {"賭場遊玩", "次"},
	"boss_joined":     {"領取世界 Boss 獎勵", "場"},
	"boss_won":        {"擊敗世界 Boss", "場"},
	"best_streak":     {"最長連續簽到", "天"},
	"codex_crop_pct":  {"作物圖鑑", "%"},
	"codex_fish_pct":  {"魚類圖鑑", "%"},
	"codex_pct":       {"全部圖鑑", "%"},
	// 地下菇房與種子實驗室（Go 版新增）
	"mushrooms":          {"收成菇類", "朵"},
	"codex_mushroom_pct": {"菇類圖鑑", "%"},
	"lab_built":          {"建造種子實驗室", "座"},
	"lab_crosses":        {"雜交成功", "次"},
	"lab_mutations":      {"突變研究成功", "次"},
	"lab_hybrids":        {"雜交作物種子", "種"},
	"lab_shares":         {"分享特殊種子", "顆"},
	"codex_lab_pct":      {"特殊種子圖鑑", "%"},
}

// Category 成就分類。
type Category struct {
	Code, Emoji, Name string
}

var Categories = []Category{
	{"farm", "🌾", "農耕"},
	{"wealth", "💰", "財富"},
	{"ranch", "🐔", "牧場與加工"},
	{"adventure", "⛰️", "冒險與釣魚"},
	{"social", "🥷", "社交與娛樂"},
	{"boss", "🐉", "世界 Boss"},
	{"collect", "📖", "簽到與圖鑑"},
	{"lab", "🧪", "實驗室與菇房"}, // Go 版新增
}

// Achievement 成就定義（值型別；Items 不可修改，需要時用 Reward() 取得副本）。Title 為 "" 表示沒有稱號。
type Achievement struct {
	Code     string
	Category string
	Emoji    string
	Name     string
	Metric   string
	Target   int
	Gold     int
	Items    map[string]int
	Title    string
}

func (a Achievement) Description() string {
	m := Metrics[a.Metric]
	if a.Metric == "lab_built" {
		return "建造種子實驗室"
	}
	if (a.Metric == "legendary_crop" || a.Metric == "variant_crop" || a.Metric == "legendary_fish" || a.Metric == "lab_hybrids") && a.Target == 1 {
		return "取得" + m.Label
	}
	return fmt.Sprintf("%s達到 %s %s", m.Label, numfmt.Comma(a.Target), m.Unit)
}

// AchievementReward 成就獎勵（金幣與道具）。
type AchievementReward struct {
	Gold  int
	Items map[string]int
}

func (a Achievement) Reward() AchievementReward {
	return AchievementReward{Gold: a.Gold, Items: maps.Clone(nonNil(a.Items))}
}

func nonNil(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

const (
	f = balance.ItemFertilizer
	e = balance.ItemEarthworm
	p = balance.ItemPatrolTicket
)

type it = map[string]int

func ach(code, cat, emoji, name, metric string, target, gold int, items it, title string) Achievement {
	return Achievement{code, cat, emoji, name, metric, target, gold, nonNil(items), title}
}

// BaseAchievements 預設的成就（程式內建）；實際生效的是 Active()（套用管理網站的設定）。
var BaseAchievements = []Achievement{
	// 農耕
	ach("crops_100", "farm", "🌱", "初出茅廬", "crops", 100, 200, nil, ""),
	ach("crops_1000", "farm", "🌾", "勤勞農夫", "crops", 1_000, 0, it{f: 10}, "勤勞農夫"),
	ach("crops_10000", "farm", "🚜", "農業大亨", "crops", 10_000, 2_000, it{e: 3}, "農業大亨"),
	ach("level_5", "farm", "⭐", "小有規模", "level", 5, 0, it{f: 5}, ""),
	ach("level_10", "farm", "🌟", "資深農夫", "level", 10, 0, it{e: 2}, "資深農夫"),
	ach("plots_16", "farm", "🗺️", "大地主", "plots", balance.MaxPlots, 1_000, nil, "大地主"),
	ach("legendary_crop", "farm", "👑", "傳說農夫", "legendary_crop", 1, 0, it{p: 3}, "傳說農夫"),
	ach("variant_crop", "farm", "💎", "變種獵人", "variant_crop", 1, 0, it{e: 1}, "變種獵人"),
	// 財富
	ach("sales_10k", "wealth", "🪙", "小本生意", "crop_sales", 10_000, 300, nil, ""),
	ach("sales_100k", "wealth", "💵", "生意興隆", "crop_sales", 100_000, 0, it{e: 2}, "生意興隆"),
	ach("sales_1m", "wealth", "💰", "富甲一方", "crop_sales", 1_000_000, 5_000, nil, "富甲一方"),
	ach("gold_500k", "wealth", "🏦", "大富翁", "gold", 500_000, 0, nil, "大富翁"),
	// 牧場與加工
	ach("animal_50", "ranch", "🥚", "小小牧場", "animal_products", 50, 0, it{f: 5}, ""),
	ach("animal_500", "ranch", "🐄", "牧場主人", "animal_products", 500, 0, it{e: 2}, "牧場主人"),
	ach("goods_20", "ranch", "🍞", "學徒工匠", "goods", 20, 300, nil, ""),
	ach("goods_200", "ranch", "🏭", "工匠", "goods", 200, 0, it{e: 2}, "工匠"),
	// 冒險與釣魚
	ach("explore_10", "adventure", "🔦", "初探山洞", "explorations", 10, 0, it{f: 5}, ""),
	ach("explore_100", "adventure", "⛏️", "洞穴探險家", "explorations", 100, 0, it{e: 2}, "洞穴探險家"),
	ach("fish_10", "adventure", "🎣", "第一次釣魚", "fish", 10, 0, it{p: 2}, ""),
	ach("fish_100", "adventure", "🐟", "釣魚高手", "fish", 100, 1_000, nil, "釣魚高手"),
	ach("fish_legendary", "adventure", "🐋", "傳說釣手", "legendary_fish", 1, 0, it{e: 2}, "傳說釣手"),
	// 社交與娛樂
	ach("steal_20", "social", "🥷", "神偷", "steals", 20, 0, it{p: 3}, "神偷"),
	ach("defend_10", "social", "🛡️", "守護者", "defended", 10, 0, it{p: 3}, "守護者"),
	ach("gamble_50", "social", "🎰", "幸運兒", "gambles", 50, 200, nil, "幸運兒"),
	// 世界 Boss
	ach("boss_join", "boss", "📦", "後勤新兵", "boss_joined", 1, 300, nil, ""),
	ach("boss_win_1", "boss", "⚔️", "屠龍者", "boss_won", 1, 0, it{e: 1}, "屠龍者"),
	ach("boss_win_10", "boss", "🐉", "龍之剋星", "boss_won", 10, 3_000, it{e: 3}, "龍之剋星"),
	// 簽到與圖鑑
	ach("streak_7", "collect", "📅", "一週全勤", "best_streak", 7, 0, it{f: 5}, ""),
	ach("streak_30", "collect", "🗓️", "全勤農夫", "best_streak", 30, 0, it{e: 3}, "全勤農夫"),
	ach("codex_crop_all", "collect", "🌽", "作物學者", "codex_crop_pct", 100, 2_000, nil, "作物學者"),
	ach("codex_fish_all", "collect", "🐠", "垂釣大師", "codex_fish_pct", 100, 2_000, nil, "垂釣大師"),
	ach("codex_half", "collect", "📗", "收藏家", "codex_pct", 50, 0, it{e: 2}, ""),
	ach("codex_all", "collect", "📚", "博物學家", "codex_pct", 100, 5_000, nil, "博物學家"),
	// 實驗室與菇房（Go 版新增，2026-11-13）
	ach("lab_build", "lab", "🧪", "實驗室落成", "lab_built", 1, 0, it{f: 5}, ""),
	ach("lab_cross_10", "lab", "🧬", "育種學徒", "lab_crosses", 10, 0, it{e: 2}, ""),
	ach("lab_cross_100", "lab", "🔬", "育種專家", "lab_crosses", 100, 3_000, nil, "育種專家"),
	ach("lab_hybrid_1", "lab", "🌱", "新品種誕生", "lab_hybrids", 1, 0, it{e: 1}, ""),
	ach("lab_hybrid_all", "lab", "🌈", "雜交大師", "lab_hybrids", 12, 5_000, nil, "雜交大師"),
	ach("lab_mutation_1", "lab", "☢️", "突變研究員", "lab_mutations", 1, 0, it{p: 2}, "突變研究員"),
	ach("lab_share_10", "lab", "🎁", "慷慨育種家", "lab_shares", 10, 500, nil, "慷慨育種家"),
	ach("codex_lab_all", "lab", "🌰", "種子收藏家", "codex_lab_pct", 100, 5_000, nil, "種子收藏家"),
	ach("mushroom_100", "lab", "🍄", "採菇人", "mushrooms", 100, 0, it{f: 5}, ""),
	ach("codex_mushroom_all", "lab", "✨", "菇類達人", "codex_mushroom_pct", 100, 2_000, nil, "菇類達人"),
}

var BaseByCode = func() map[string]Achievement {
	m := make(map[string]Achievement, len(BaseAchievements))
	for _, a := range BaseAchievements {
		m[a.Code] = a
	}
	return m
}()

func validateBase() {
	if len(BaseByCode) != len(BaseAchievements) {
		panic("成就代碼重複")
	}
	cats := map[string]bool{}
	for _, c := range Categories {
		cats[c.Code] = true
	}
	titles := map[string]bool{}
	for _, a := range BaseAchievements {
		if a.Title != "" {
			if titles[a.Title] {
				panic("成就稱號重複")
			}
			titles[a.Title] = true
		}
		if _, ok := Metrics[a.Metric]; !cats[a.Category] || !ok || a.Target <= 0 {
			panic(a.Code)
		}
		if utf8.RuneCountInString(a.Title) > TitleMaxLen {
			panic(a.Code)
		}
		for code := range a.Items {
			if _, ok := balance.Items[code]; !ok {
				panic(a.Code)
			}
		}
	}
}

// ---------- 可調整的設定：{"items": {成就代碼: {"target", "gold", "items", "title"}}}（只列有修改的成就） ----------

// Editable 可調整的欄位（title 為 nil 表示沒有稱號）。
func Editable(a Achievement) map[string]any {
	var title any
	if a.Title != "" {
		title = a.Title
	}
	return map[string]any{"target": a.Target, "gold": a.Gold, "items": maps.Clone(nonNil(a.Items)), "title": title}
}

func defaults() featurecfg.Value { return featurecfg.Value{"items": map[string]any{}} }

func getOr(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func sameAsBase(target, gold int, items map[string]int, title string, base Achievement) bool {
	return target == base.Target && gold == base.Gold && maps.Equal(items, nonNil(base.Items)) && title == base.Title
}

// ValidateConfig 驗證並正規化成就設定（和預設一樣的成就不存）。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	items, ok := featurecfg.AsMap(raw["items"])
	if !ok {
		return nil, gameerr.Validationf("成就設定格式不正確。")
	}
	codes := make([]string, 0, len(items))
	for c := range items {
		codes = append(codes, c)
	}
	sort.Strings(codes) // Go map 沒有順序：依代碼排序，錯誤訊息才固定
	out := map[string]any{}
	for _, code := range codes {
		base, ok := BaseByCode[code]
		if !ok {
			return nil, gameerr.Validationf("沒有「%s」這個成就。", code)
		}
		values, ok := featurecfg.AsMap(items[code])
		if !ok {
			return nil, gameerr.Validationf("成就「%s」的設定格式不正確。", base.Name)
		}
		high := 1_000_000_000_000
		if PctMetrics[base.Metric] {
			high = 100
		}
		target, err := featurecfg.IntValue(getOr(values, "target", base.Target), fmt.Sprintf("「%s」的門檻", base.Name), 1, high)
		if err != nil {
			return nil, err
		}
		gold, err := featurecfg.IntValue(getOr(values, "gold", base.Gold), fmt.Sprintf("「%s」的金幣", base.Name), 0, 10_000_000)
		if err != nil {
			return nil, err
		}
		its, err := featurecfg.ItemsValue(getOr(values, "items", nonNil(base.Items)), fmt.Sprintf("「%s」的道具", base.Name), 3, 999)
		if err != nil {
			return nil, err
		}
		var baseTitle any
		if base.Title != "" {
			baseTitle = base.Title
		}
		title, hasTitle, err := featurecfg.TitleValue(getOr(values, "title", baseTitle), fmt.Sprintf("「%s」的稱號", base.Name), TitleMaxLen, false)
		if err != nil {
			return nil, err
		}
		if !hasTitle {
			title = ""
		}
		if sameAsBase(target, gold, its, title, base) { // 和預設一樣的不必存
			continue
		}
		var tv any
		if hasTitle {
			tv = title
		}
		out[code] = map[string]any{"target": target, "gold": gold, "items": its, "title": tv}
	}
	cfg := featurecfg.Value{"items": out}
	count := map[string]int{}
	for _, a := range ApplyConfig(cfg) {
		if a.Title != "" {
			count[a.Title]++
		}
	}
	var dup []string
	for t, n := range count {
		if n > 1 {
			dup = append(dup, t)
		}
	}
	if len(dup) > 0 {
		sort.Strings(dup)
		return nil, gameerr.Validationf("成就稱號不可重複：%s。", strings.Join(dup, "、"))
	}
	return cfg, nil
}

// ApplyConfig 把設定套用到預設成就上（不改變目前生效的設定；存檔前檢查用）。
func ApplyConfig(cfg featurecfg.Value) []Achievement {
	over, _ := featurecfg.AsMap(cfg["items"])
	out := make([]Achievement, len(BaseAchievements))
	for i, a := range BaseAchievements {
		if raw, ok := over[a.Code]; ok {
			m, _ := featurecfg.AsMap(raw)
			if n, ok := featurecfg.AsInt(m["target"]); ok {
				a.Target = n
			}
			if n, ok := featurecfg.AsInt(m["gold"]); ok {
				a.Gold = n
			}
			items := map[string]int{}
			if im, ok := featurecfg.AsMap(m["items"]); ok {
				for c, q := range im {
					if n, ok := featurecfg.AsInt(q); ok {
						items[c] = n
					}
				}
			}
			a.Items = items
			a.Title, _ = m["title"].(string)
		}
		out[i] = a
	}
	return out
}

func init() {
	validateBase()
	featurecfg.Register(ConfigKey, "成就", defaults, ValidateConfig)
}

var (
	cacheMu      sync.Mutex
	cacheValid   bool
	cacheVersion int
	cacheRows    []Achievement
	cacheByCode  map[string]Achievement
)

func current() ([]Achievement, map[string]Achievement) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	version := featurecfg.Version()
	if !cacheValid || cacheVersion != version {
		rows := ApplyConfig(featurecfg.Get(ConfigKey))
		byCode := make(map[string]Achievement, len(rows))
		for _, a := range rows {
			byCode[a.Code] = a
		}
		cacheValid, cacheVersion, cacheRows, cacheByCode = true, version, rows, byCode
	}
	return cacheRows, cacheByCode
}

// Active 目前生效的成就（套用管理網站的設定；呼叫端不可修改）。
func Active() []Achievement {
	rows, _ := current()
	return rows
}

// ByCode 代碼 → 目前生效的成就（呼叫端不可修改）。
func ByCode() map[string]Achievement {
	_, m := current()
	return m
}

// Titles 目前所有成就稱號。
func Titles() map[string]bool {
	out := map[string]bool{}
	for _, a := range Active() {
		if a.Title != "" {
			out[a.Title] = true
		}
	}
	return out
}

func Reached(a Achievement, metrics map[string]int) bool {
	return metrics[a.Metric] >= a.Target
}

// sortedItemCodes 依道具商店順序排列（Go map 沒有順序；Python 依插入順序）。
func sortedItemCodes(items map[string]int) []string {
	rank := map[string]int{}
	for i, c := range balance.ItemOrder {
		rank[c] = i
	}
	codes := make([]string, 0, len(items))
	for c := range items {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		ri, iok := rank[codes[i]]
		rj, jok := rank[codes[j]]
		if iok != jok {
			return iok
		}
		if ri != rj {
			return ri < rj
		}
		return codes[i] < codes[j]
	})
	return codes
}

func RewardText(a Achievement) string {
	var parts []string
	if a.Gold != 0 {
		parts = append(parts, numfmt.Comma(a.Gold)+" 金幣")
	}
	for _, code := range sortedItemCodes(a.Items) {
		parts = append(parts, fmt.Sprintf("%s ×%d", balance.Items[code].Name, a.Items[code]))
	}
	if a.Title != "" {
		parts = append(parts, fmt.Sprintf("稱號「%s」", a.Title))
	}
	if len(parts) == 0 {
		return "無"
	}
	return strings.Join(parts, "、")
}
