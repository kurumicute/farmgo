// Package worldboss 世界 Boss（群組後勤活動）：補給類型、可捐獻物品、積分、戰鬥模擬公式、獎勵與設定驗證
// （純規則，不碰資料庫）。對應 Python app/game/world_boss.py。
//
// 玩家不直接攻擊 Boss，而是捐獻農作物、漁獲、素材與加工品給前線；前線依四種補給的達成率自動作戰：
//   - 糧食補給 → 持續作戰能力（出勤率）
//   - 醫療補給 → 恢復能力（把缺勤時間救回來）
//   - 戰鬥補給 → 傷害效率
//   - 裝備補給 → 戰鬥能力（另一個傷害倍率）
//
// 所有數值集中在本檔案的預設值；每場活動建立時把設定與獎勵快照存進 world_boss_events（管理網站可修改），
// Migration 只寫死資料表結構，不引用這裡的數值。設定與獎勵的 JSON 欄位名稱與 Python 版相同。
package worldboss

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/clock"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

// ---------- 補給類型與捐獻類別 ----------

var Supplies = []string{"food", "medical", "combat", "equipment"}

// SupplyInfo emoji、名稱、效果名稱、說明。
type SupplyInfo struct {
	Emoji, Name, Effect, Desc string
}

var SupplyInfos = map[string]SupplyInfo{
	"food":      {"🌾", "糧食補給", "持續作戰", "農作物；讓前線維持出勤，不足時大部分時間在休息"},
	"medical":   {"🩹", "醫療補給", "恢復能力", "藥草、雞蛋、牛奶、果醬等指定食物與素材；把受傷缺勤的時間救回來"},
	"combat":    {"⚔️", "戰鬥補給", "傷害效率", "漁獲與麵粉、麵包等加工物品；提高每次攻擊的傷害"},
	"equipment": {"🛡️", "裝備補給", "戰鬥能力", "洞窟探索與機器製作素材；強化前線裝備"},
}

var Categories = []string{"crop", "fish", "material", "goods", "special"}

// CategoryInfo emoji、名稱。
type CategoryInfo struct {
	Emoji, Name string
}

var CategoryInfos = map[string]CategoryInfo{
	"crop":     {"🌾", "農作物"},
	"fish":     {"🐟", "漁獲"},
	"material": {"⚙️", "農場素材"},
	"goods":    {"🍲", "指定加工物品"},
	"special":  {"🧪", "指定特殊材料"},
}

// SpecialMaterials 山洞素材中屬於「特殊材料」的代碼（其餘普通素材歸在農場素材）；假幣是貨幣，不能捐
var (
	SpecialMaterials = []string{"iron_ore", "gear", "crystal", "ancient_core", "machine_core"}
	NotDonatable     = []string{"fake_coin"}

	Statuses    = []string{"active", "paused", "ended"}
	StateLabels = map[string]string{
		"scheduled": "尚未開始", "active": "進行中", "paused": "已暫停", "ended": "已結束", "defeated": "已擊敗",
	}
	EndReasons = []string{"defeated", "timeout", "admin"}
)

// ---------- 設定與獎勵的資料結構 ----------

// Effects 補給效果。
type Effects struct {
	RatioCapPct        int `json:"ratio_cap_pct"`        // 補給最多算到目標的 150%（超過不再加成，避免單一補給灌爆）
	BaseUptimePct      int `json:"base_uptime_pct"`      // 完全沒有糧食時的前線出勤率
	MedicalRecoveryPct int `json:"medical_recovery_pct"` // 醫療滿額時，缺勤時間被救回的比例
	CombatAttackPct    int `json:"combat_attack_pct"`    // 戰鬥補給滿額時傷害 +100%
	EquipmentAttackPct int `json:"equipment_attack_pct"` // 裝備補給滿額時傷害 +50%
}

// Limits 捐獻限制。
type Limits struct {
	PerItemMax   int `json:"per_item_max"`   // 每位玩家每種物品在一場活動最多捐獻的數量
	PerActionMax int `json:"per_action_max"` // 單次捐獻上限
}

// ItemEntry 指定物品：補給與每個積分（0 表示不可捐獻）。
type ItemEntry struct {
	Supply string `json:"supply"`
	Points int    `json:"points"`
}

// Settings 活動設定（建立活動時的快照）。
type Settings struct {
	// 前線基礎傷害（每小時）；實際傷害 = 基礎 × 攻擊倍率 × 出勤率
	BaseDamagePerHour int `json:"base_damage_per_hour"`
	// 每 1 積分捐獻時立即造成的傷害（再乘攻擊倍率），讓捐獻馬上看得到效果
	InstantDamagePerPoint int `json:"instant_damage_per_point"`
	// 作物與魚依「單價 ÷ 此值」換算積分（無條件進位，至少 1 分）；指定物品用 items 的固定積分
	GoldPerPoint  int                  `json:"gold_per_point"`
	CropSupply    string               `json:"crop_supply"`
	FishSupply    string               `json:"fish_supply"`
	SupplyTargets map[string]int       `json:"supply_targets"` // 各補給的目標積分
	Effects       Effects              `json:"effects"`
	Limits        Limits               `json:"limits"`
	Items         map[string]ItemEntry `json:"items"` // 作物代碼寫在這裡時改用固定積分
}

// Reward 一份獎勵。
type Reward struct {
	Gold  int            `json:"gold"`
	Items map[string]int `json:"items"`
}

// Rewards 活動獎勵（建立活動時的快照）。
type Rewards struct {
	// 參與門檻：個人積分未達此值的玩家沒有任何獎勵；Migration 0022 以前結束、沒有這個欄位的活動視為 0
	MinPoints     int      `json:"min_points"`
	Victory       Reward   `json:"victory"`       // Boss 被擊敗：所有貢獻者
	Participation Reward   `json:"participation"` // Boss 未被擊敗：所有貢獻者
	BonusPoints   int      `json:"bonus_points"`  // 個人積分達標（>= 此值）另發
	Bonus         Reward   `json:"bonus"`
	Rank          []Reward `json:"rank"`  // 個人排名獎勵（第 1、2、3… 名）
	Title         string   `json:"title"` // 特殊稱號：擊敗 Boss 時前 title_top 名獲得（空字串表示不發）
	TitleTop      int      `json:"title_top"`
}

// ---------- 預設值（建立活動時複製一份到活動設定） ----------

const (
	DefaultName          = "深淵魔龍"
	DefaultLevel         = 50
	DefaultMaxHP         = 100_000
	DefaultDurationHours = 72
)

// DefaultItemOrder 預設設定 items 的代碼順序（Python DEFAULT_SETTINGS["items"] 的 dict 順序；管理網站表單依此顯示）。
var DefaultItemOrder = []string{
	"herb", "egg", "milk", "strawberry_jam", "tomato_jam", "grape_jam", "pumpkin_jam", "watermelon_jam", "flour", "bread", "wool", "wood", "stone", "copper_ore", "resin",
	"iron_ore", "gear", "crystal", "ancient_core", "machine_core",
}

// DefaultSettings 預設設定（每次回傳新的副本）。
func DefaultSettings() Settings {
	return Settings{
		BaseDamagePerHour:     1500,
		InstantDamagePerPoint: 2,
		GoldPerPoint:          10,
		CropSupply:            "food",
		FishSupply:            "combat",
		SupplyTargets:         map[string]int{"food": 2000, "medical": 800, "combat": 1200, "equipment": 800},
		Effects: Effects{
			RatioCapPct: 150, BaseUptimePct: 30, MedicalRecoveryPct: 50, CombatAttackPct: 100, EquipmentAttackPct: 50,
		},
		Limits: Limits{PerItemMax: 200, PerActionMax: 99},
		Items: map[string]ItemEntry{
			"herb":           {"medical", 6},
			"egg":            {"medical", 3},
			"milk":           {"medical", 6},
			"strawberry_jam": {"medical", 30},
			"tomato_jam":     {"medical", 20}, // Go 版新增的果醬：1 積分 ≈ 10 金售價（與草莓果醬相同）
			"grape_jam":      {"medical", 40},
			"pumpkin_jam":    {"medical", 49},
			"watermelon_jam": {"medical", 115},
			"flour":          {"combat", 5},
			"bread":          {"combat", 20},
			"wool":           {"equipment", 12},
			"wood":           {"equipment", 2},
			"stone":          {"equipment", 2},
			"copper_ore":     {"equipment", 3},
			"resin":          {"equipment", 2},
			"iron_ore":       {"equipment", 6},
			"gear":           {"equipment", 12},
			"crystal":        {"equipment", 15},
			"ancient_core":   {"equipment", 40},
			"machine_core":   {"equipment", 40},
		},
	}
}

// DefaultRewards 預設獎勵（每次回傳新的副本）。
func DefaultRewards() Rewards {
	return Rewards{
		MinPoints:     30,
		Victory:       Reward{800, map[string]int{"crystal": 1}},
		Participation: Reward{200, map[string]int{}},
		BonusPoints:   300,
		Bonus:         Reward{300, map[string]int{"seed_amber_melon": 1}},
		Rank: []Reward{
			{1500, map[string]int{"seed_moonflower": 1}},
			{1000, map[string]int{"ancient_core": 1}},
			{600, map[string]int{"gear": 2}},
		},
		Title:    "屠龍後勤英雄",
		TitleTop: 3,
	}
}

// 驗證範圍
var (
	MaxHPRange                   = [2]int{1, 1_000_000_000}
	LevelRange                   = [2]int{1, 999}
	DurationRangeSeconds         = [2]int{60, 60 * 86400}
	TargetRange                  = [2]int{1, 100_000_000}
	PointsRange                  = [2]int{0, 100_000}
	GoldRewardRange              = [2]int{0, 1_000_000}
	ItemRewardRange              = [2]int{1, 1000}
	ScheduleIntervalRange        = [2]int{1, 30}           // 每幾天出現一次
	ScheduleDurationMinutesRange = [2]int{10, 7 * 24 * 60} // 每次持續多久（分鐘）
)

const (
	NameMaxLen      = 40
	MaxRewardItems  = 5
	MaxRankRewards  = 10
	MaxItems        = 60
	TitleMaxLen     = 20
	LeaderboardSize = 20
	SchedulePreview = 5 // 管理網站顯示接下來幾場的時間
)

// ---------- 固定週期出現的世界 Boss（world_boss_schedules） ----------

// ScheduleTimes 排程的時間欄位與 HP。AnchorDate 以台灣時間 00:00 的 time.Time 表示日期。
type ScheduleTimes struct {
	AnchorDate      time.Time
	IntervalDays    int
	SpawnHour       int
	SpawnMinute     int
	DurationMinutes int
	MaxHP           int
}

// Schedule 固定出現的 Boss（含設定與獎勵）。
type Schedule struct {
	Name string
	ScheduleTimes
	Settings Settings
	Rewards  Rewards
}

// AbyssDragon 預設排程「深淵魔龍」：全服（所有伺服器的玩家一起打），每 2 天晚上 9 點（台灣時間）出現、持續 3 小時。
// 數值以 10 位玩家同時支援可以擊敗為目標：每人一次捐約 310 積分（作物 100、醫療 60、戰鬥 70、裝備 80），
// 10 人剛好補滿四種補給；捐獻的立即傷害約 1 萬，補滿後每小時 3,750，約 1 小時內擊敗 15,000 HP。
// 只有 1 人時 3 小時約 2,700 傷害、5 人約 1 萬，都打不倒。Migration 0020 寫入相同數值。每次回傳新的副本。
func AbyssDragon() Schedule {
	settings := DefaultSettings()
	settings.SupplyTargets = map[string]int{"food": 1000, "medical": 600, "combat": 700, "equipment": 800}
	crystal1 := func() Reward { return Reward{400, map[string]int{"crystal": 1}} }
	gear2 := func() Reward { return Reward{800, map[string]int{"gear": 2}} }
	return Schedule{
		Name: "深淵魔龍",
		ScheduleTimes: ScheduleTimes{
			AnchorDate:      time.Date(2026, 11, 4, 0, 0, 0, 0, clock.TaipeiTZ),
			IntervalDays:    2,
			SpawnHour:       21,
			SpawnMinute:     0,
			DurationMinutes: 180,
			MaxHP:           15_000,
		},
		Settings: settings,
		Rewards: Rewards{
			MinPoints:     30,
			Victory:       Reward{1200, map[string]int{"crystal": 2}},
			Participation: Reward{300, map[string]int{}},
			BonusPoints:   200,
			Bonus:         Reward{500, map[string]int{"seed_amber_melon": 2}},
			Rank: []Reward{
				{3000, map[string]int{"seed_moonflower": 2, "ancient_core": 1}},
				{2000, map[string]int{"seed_moonflower": 1, "machine_core": 1}},
				{1500, map[string]int{"ancient_core": 1}},
				gear2(), gear2(),
				crystal1(), crystal1(), crystal1(), crystal1(), crystal1(),
			},
			Title:    "降龍勇士",
			TitleTop: 3,
		},
	}
}

// parseISODate 等同 date.fromisoformat 的常用格式（YYYY-MM-DD 與 YYYYMMDD）。
func parseISODate(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.ParseInLocation(layout, s, clock.TaipeiTZ); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ValidateSchedule 驗證排程的時間欄位（設定與獎勵另外以 ValidateSettings／ValidateRewards 驗證）。
// anchor_date 可為 "YYYY-MM-DD" 字串或 time.Time（取其年月日）。
func ValidateSchedule(raw map[string]any) (ScheduleTimes, error) {
	var anchor time.Time
	switch v := raw["anchor_date"].(type) {
	case string:
		t, ok := parseISODate(v)
		if !ok {
			return ScheduleTimes{}, gameerr.Validationf("起始日期格式不正確。")
		}
		anchor = t
	case time.Time:
		anchor = time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
	default:
		return ScheduleTimes{}, gameerr.Validationf("請設定起始日期。")
	}
	var out ScheduleTimes
	out.AnchorDate = anchor
	fields := []struct {
		dst       *int
		key       string
		label     string
		low, high int
	}{
		{&out.IntervalDays, "interval_days", "出現間隔（天）", ScheduleIntervalRange[0], ScheduleIntervalRange[1]},
		{&out.SpawnHour, "spawn_hour", "出現時間（時）", 0, 23},
		{&out.SpawnMinute, "spawn_minute", "出現時間（分）", 0, 59},
		{&out.DurationMinutes, "duration_minutes", "持續時間（分鐘）", ScheduleDurationMinutesRange[0], ScheduleDurationMinutesRange[1]},
		{&out.MaxHP, "max_hp", "Boss HP", MaxHPRange[0], MaxHPRange[1]},
	}
	for _, f := range fields {
		n, err := IntValue(raw[f.key], f.label, f.low, f.high)
		if err != nil {
			return ScheduleTimes{}, err
		}
		*f.dst = n
	}
	return out, nil
}

// pyTimedelta 等同 Python timedelta(seconds=x)：四捨五入（成雙）到微秒。
func pyTimedelta(seconds float64) time.Duration {
	return time.Duration(math.RoundToEven(seconds*1e6)) * time.Microsecond
}

// SlotAfter 下一個出現時間（台灣時間對齊）：結束時間晚於 now，且晚於 after（上一場的排定時間；nil 表示沒有）。
func SlotAfter(anchorDate time.Time, hour, minute, intervalDays, durationMinutes int, now time.Time, after *time.Time) time.Time {
	base := time.Date(anchorDate.Year(), anchorDate.Month(), anchorDate.Day(), hour, minute, 0, 0, clock.TaipeiTZ)
	step := intervalDays * 86400
	earliest := now.Add(-time.Duration(durationMinutes) * time.Minute) // 出現時間要 > earliest，才會在 now 之後結束
	k := max(0, int(math.Floor(earliest.Sub(base).Seconds()/float64(step)))+1)
	slot := base.Add(time.Duration(k*step) * time.Second)
	for after != nil && !slot.After(*after) {
		slot = slot.Add(time.Duration(step) * time.Second)
	}
	return slot
}

// UpcomingSlots 接下來 count 場的出現時間。
func UpcomingSlots(s ScheduleTimes, now time.Time, after *time.Time, count int) []time.Time {
	slots := make([]time.Time, 0, count)
	last := after
	for i := 0; i < count; i++ {
		slot := SlotAfter(s.AnchorDate, s.SpawnHour, s.SpawnMinute, s.IntervalDays, s.DurationMinutes, now, last)
		slots = append(slots, slot)
		last = &slot
	}
	return slots
}

// ---------- 設定驗證 ----------

// IntValue 整數驗證（bool 與浮點數不算整數）。
func IntValue(value any, label string, low, high int) (int, error) {
	n, ok := featurecfg.AsInt(value)
	if !ok {
		return 0, gameerr.Validationf("%s必須是整數。", label)
	}
	if n < low || n > high {
		return 0, gameerr.Validationf("%s必須介於 %d 到 %d。", label, low, high)
	}
	return n, nil
}

// toRaw 把 Go 值轉成 JSON 形狀（map[string]any、[]any、json.Number），讓預設值與原始輸入走同一套驗證。
func toRaw(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		panic(err)
	}
	return out
}

// get 等同 dict.get(key, default)。
func get(raw map[string]any, key string, def any) any {
	if v, ok := raw[key]; ok {
		return v
	}
	return def
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidateSettings 驗證並補齊活動設定。itemCategory(code) 回傳物品的捐獻類別，不能捐的物品回傳 ok = false。
// raw 為 nil 表示格式不正確（Python 非 dict）。
func ValidateSettings(raw map[string]any, itemCategory func(code string) (string, bool)) (Settings, error) {
	if raw == nil {
		return Settings{}, gameerr.Validationf("補給設定格式不正確。")
	}
	base := toRaw(DefaultSettings()).(map[string]any)
	var out Settings
	var err error
	if out.BaseDamagePerHour, err = IntValue(get(raw, "base_damage_per_hour", base["base_damage_per_hour"]), "每小時基礎傷害", 0, 100_000_000); err != nil {
		return Settings{}, err
	}
	if out.InstantDamagePerPoint, err = IntValue(get(raw, "instant_damage_per_point", base["instant_damage_per_point"]), "每積分立即傷害", 0, 100_000); err != nil {
		return Settings{}, err
	}
	if out.GoldPerPoint, err = IntValue(get(raw, "gold_per_point", base["gold_per_point"]), "每積分金幣價值", 1, 100_000); err != nil {
		return Settings{}, err
	}
	for _, f := range []struct {
		key, label string
		dst        *string
	}{{"crop_supply", "作物補給類型", &out.CropSupply}, {"fish_supply", "漁獲補給類型", &out.FishSupply}} {
		s, ok := get(raw, f.key, base[f.key]).(string)
		if !ok || !slices.Contains(Supplies, s) {
			return Settings{}, gameerr.Validationf("%s不正確。", f.label)
		}
		*f.dst = s
	}
	baseTargets := base["supply_targets"].(map[string]any)
	targets, ok := featurecfg.AsMap(get(raw, "supply_targets", baseTargets))
	if !ok {
		return Settings{}, gameerr.Validationf("補給目標格式不正確。")
	}
	out.SupplyTargets = map[string]int{}
	for _, s := range Supplies {
		n, err := IntValue(get(targets, s, baseTargets[s]), SupplyInfos[s].Name+"目標", TargetRange[0], TargetRange[1])
		if err != nil {
			return Settings{}, err
		}
		out.SupplyTargets[s] = n
	}
	baseEffects := base["effects"].(map[string]any)
	effects, ok := featurecfg.AsMap(get(raw, "effects", baseEffects))
	if !ok {
		return Settings{}, gameerr.Validationf("補給效果格式不正確。")
	}
	eff := func(key string) any { return get(effects, key, baseEffects[key]) }
	for _, f := range []struct {
		dst       *int
		key       string
		label     string
		low, high int
	}{
		{&out.Effects.RatioCapPct, "ratio_cap_pct", "補給加成上限", 100, 500},
		{&out.Effects.BaseUptimePct, "base_uptime_pct", "基礎出勤率", 0, 100},
		{&out.Effects.MedicalRecoveryPct, "medical_recovery_pct", "醫療恢復比例", 0, 100},
		{&out.Effects.CombatAttackPct, "combat_attack_pct", "戰鬥補給傷害加成", 0, 1000},
		{&out.Effects.EquipmentAttackPct, "equipment_attack_pct", "裝備補給傷害加成", 0, 1000},
	} {
		if *f.dst, err = IntValue(eff(f.key), f.label, f.low, f.high); err != nil {
			return Settings{}, err
		}
	}
	baseLimits := base["limits"].(map[string]any)
	limits, ok := featurecfg.AsMap(get(raw, "limits", baseLimits))
	if !ok {
		return Settings{}, gameerr.Validationf("捐獻限制格式不正確。")
	}
	if out.Limits.PerItemMax, err = IntValue(get(limits, "per_item_max", baseLimits["per_item_max"]), "每人每種物品上限", 1, 100_000); err != nil {
		return Settings{}, err
	}
	if out.Limits.PerActionMax, err = IntValue(get(limits, "per_action_max", baseLimits["per_action_max"]), "單次捐獻上限", 1, 999); err != nil {
		return Settings{}, err
	}
	items, ok := featurecfg.AsMap(get(raw, "items", base["items"]))
	if !ok || len(items) > MaxItems {
		return Settings{}, gameerr.Validationf("指定物品格式不正確（最多 %d 項）。", MaxItems)
	}
	out.Items = map[string]ItemEntry{}
	for _, code := range sortedKeys(items) { // Go map 沒有順序：依代碼排序，錯誤訊息固定
		if _, ok := itemCategory(code); !ok {
			return Settings{}, gameerr.Validationf("「%s」不是可以捐獻的物品（只能是作物、加工品、動物產品或山洞素材）。", code)
		}
		entry, ok := featurecfg.AsMap(items[code])
		supply, _ := entry["supply"].(string)
		if !ok || !slices.Contains(Supplies, supply) {
			return Settings{}, gameerr.Validationf("「%s」的補給類型不正確。", code)
		}
		points, err := IntValue(entry["points"], fmt.Sprintf("「%s」的積分", code), PointsRange[0], PointsRange[1])
		if err != nil {
			return Settings{}, err
		}
		out.Items[code] = ItemEntry{Supply: supply, Points: points}
	}
	return out, nil
}

func validateReward(raw any, label string, validItem func(string) bool) (Reward, error) {
	m, ok := featurecfg.AsMap(raw)
	if !ok {
		return Reward{}, gameerr.Validationf("%s格式不正確。", label)
	}
	var items map[string]any
	if v := m["items"]; v != nil {
		items, ok = featurecfg.AsMap(v)
		if !ok {
			if list, isList := v.([]any); isList && len(list) == 0 { // Python 的 `or {}`：空值視為沒有物品
				items, ok = map[string]any{}, true
			}
		}
		if !ok || len(items) > MaxRewardItems {
			return Reward{}, gameerr.Validationf("%s的物品最多 %d 種。", label, MaxRewardItems)
		}
	}
	codes := sortedKeys(items)
	for _, code := range codes {
		if !validItem(code) {
			return Reward{}, gameerr.Validationf("%s的「%s」不是可以發放的物品。", label, code)
		}
	}
	gold, err := IntValue(get(m, "gold", 0), label+"金幣", GoldRewardRange[0], GoldRewardRange[1])
	if err != nil {
		return Reward{}, err
	}
	out := Reward{Gold: gold, Items: map[string]int{}}
	for _, code := range codes {
		q, err := IntValue(items[code], fmt.Sprintf("%s「%s」數量", label, code), ItemRewardRange[0], ItemRewardRange[1])
		if err != nil {
			return Reward{}, err
		}
		out.Items[code] = q
	}
	return out, nil
}

// ValidateRewards 驗證獎勵設定；validItem(code) 判斷物品代碼是否可以放進背包。raw 為 nil 表示格式不正確。
func ValidateRewards(raw map[string]any, validItem func(string) bool) (Rewards, error) {
	if raw == nil {
		return Rewards{}, gameerr.Validationf("獎勵設定格式不正確。")
	}
	base := toRaw(DefaultRewards()).(map[string]any)
	ranks, ok := get(raw, "rank", base["rank"]).([]any)
	if !ok || len(ranks) > MaxRankRewards {
		return Rewards{}, gameerr.Validationf("排名獎勵最多 %d 名。", MaxRankRewards)
	}
	title, ok := get(raw, "title", base["title"]).(string)
	if !ok || utf8.RuneCountInString(strings.TrimSpace(title)) > TitleMaxLen {
		return Rewards{}, gameerr.Validationf("稱號最多 %d 字。", TitleMaxLen)
	}
	var out Rewards
	var err error
	if out.MinPoints, err = IntValue(get(raw, "min_points", base["min_points"]), "參與門檻", 0, 100_000_000); err != nil {
		return Rewards{}, err
	}
	if out.Victory, err = validateReward(get(raw, "victory", base["victory"]), "勝利獎勵", validItem); err != nil {
		return Rewards{}, err
	}
	if out.Participation, err = validateReward(get(raw, "participation", base["participation"]), "參與獎勵", validItem); err != nil {
		return Rewards{}, err
	}
	if out.BonusPoints, err = IntValue(get(raw, "bonus_points", base["bonus_points"]), "額外獎勵門檻", 0, 100_000_000); err != nil {
		return Rewards{}, err
	}
	if out.Bonus, err = validateReward(get(raw, "bonus", base["bonus"]), "貢獻達標獎勵", validItem); err != nil {
		return Rewards{}, err
	}
	out.Rank = []Reward{}
	for i, r := range ranks {
		rw, err := validateReward(r, fmt.Sprintf("第 %d 名獎勵", i+1), validItem)
		if err != nil {
			return Rewards{}, err
		}
		out.Rank = append(out.Rank, rw)
	}
	out.Title = strings.TrimSpace(title)
	if out.TitleTop, err = IntValue(get(raw, "title_top", base["title_top"]), "稱號名額", 0, 1000); err != nil {
		return Rewards{}, err
	}
	return out, nil
}

// ---------- 積分 ----------

// PricePoints 作物與魚：依單價換算每個的積分（無條件進位，至少 1 分）。
func PricePoints(unitPrice, goldPerPoint int) int {
	return max(1, int(math.Ceil(float64(max(0, unitPrice))/float64(goldPerPoint))))
}

// ---------- 戰鬥模擬 ----------

// CombatStats 前線狀態。
type CombatStats struct {
	Ratios        map[string]float64
	AttackMult    float64
	Uptime        float64
	DamagePerHour float64
}

// Combat 依補給進度計算前線狀態：各補給達成率、攻擊倍率、出勤率與每小時傷害。
func Combat(progress map[string]int, s Settings) CombatStats {
	eff, targets := s.Effects, s.SupplyTargets
	limit := float64(eff.RatioCapPct) / 100
	ratios := map[string]float64{}
	for _, sup := range Supplies {
		ratios[sup] = min(limit, float64(progress[sup])/float64(targets[sup]))
	}
	attack := 1 + float64(eff.CombatAttackPct)/100*ratios["combat"] + float64(eff.EquipmentAttackPct)/100*ratios["equipment"]
	baseUp := float64(eff.BaseUptimePct) / 100
	stamina := baseUp + (1-baseUp)*min(1.0, ratios["food"])
	uptime := stamina + (1-stamina)*float64(eff.MedicalRecoveryPct)/100*min(1.0, ratios["medical"])
	return CombatStats{
		Ratios:        ratios,
		AttackMult:    attack,
		Uptime:        uptime,
		DamagePerHour: float64(s.BaseDamagePerHour) * attack * uptime,
	}
}

// InstantDamage 捐獻時立即造成的傷害（無條件捨去）。
func InstantDamage(points int, s Settings, attackMult float64) int {
	return int(float64(points*s.InstantDamagePerPoint) * attackMult)
}

// SimResult 推進結果。
type SimResult struct {
	HP         int
	LastTickAt time.Time
	DefeatedAt *time.Time
	Expired    bool
}

// Simulate 從 lastTickAt 推進到 now（不超過活動結束時間）：回傳新的 HP、新的 lastTickAt、擊敗時間與是否到期。
//
// 傷害取整數；不足 1 點的部分不推進 lastTickAt，留到下次累積，頻繁讀取不會吃掉傷害。
func Simulate(hp int, lastTickAt, startsAt, endsAt time.Time, damagePerHour float64, now time.Time) SimResult {
	start := lastTickAt
	if startsAt.After(start) {
		start = startsAt
	}
	end := now
	if endsAt.Before(end) {
		end = endsAt
	}
	result := SimResult{HP: hp, LastTickAt: lastTickAt, Expired: !now.Before(endsAt)}
	if !end.After(start) || hp <= 0 {
		return result
	}
	if damagePerHour <= 0 {
		result.LastTickAt = end
		return result
	}
	seconds := end.Sub(start).Seconds()
	damage := int(damagePerHour * seconds / 3600)
	if damage >= hp {
		defeated := start.Add(pyTimedelta(float64(hp) * 3600 / damagePerHour))
		return SimResult{HP: 0, LastTickAt: defeated, DefeatedAt: &defeated, Expired: false}
	}
	if damage > 0 {
		result.HP = hp - damage
		result.LastTickAt = start.Add(pyTimedelta(float64(damage) * 3600 / damagePerHour))
	} else if lastTickAt.Before(startsAt) {
		result.LastTickAt = startsAt
	}
	return result
}

// DisplayState 顯示狀態：scheduled／active／paused／ended／defeated。
func DisplayState(status string, defeated bool, startsAt, now time.Time) string {
	if status == "ended" {
		if defeated {
			return "defeated"
		}
		return "ended"
	}
	if status == "paused" {
		return "paused"
	}
	if now.Before(startsAt) {
		return "scheduled"
	}
	return "active"
}

// ---------- 獎勵 ----------

// MinPoints 參與門檻（積分）。Migration 0022 以前結束、沒有這個欄位的活動解碼後為 0（沒有門檻）。
func MinPoints(r Rewards) int { return r.MinPoints }

// Composed 組合後的獎勵；Title 為 "" 表示沒有稱號。
type Composed struct {
	Gold  int
	Items map[string]int
	Title string
	Parts []string
}

// ComposeRewards 依活動結果、個人積分與名次組合獎勵。積分未達參與門檻時什麼都沒有。
func ComposeRewards(r Rewards, defeated bool, points, rank int) Composed {
	out := Composed{Items: map[string]int{}, Parts: []string{}}
	if points < MinPoints(r) {
		return out
	}
	add := func(part Reward, label string) {
		if part.Gold == 0 && len(part.Items) == 0 {
			return
		}
		out.Gold += part.Gold
		for code, qty := range part.Items {
			out.Items[code] += qty
		}
		out.Parts = append(out.Parts, label)
	}
	if defeated {
		add(r.Victory, "勝利獎勵")
	} else {
		add(r.Participation, "參與獎勵")
	}
	if r.BonusPoints != 0 && points >= r.BonusPoints {
		add(r.Bonus, "貢獻達標獎勵")
	}
	if rank >= 1 && rank <= len(r.Rank) {
		add(r.Rank[rank-1], fmt.Sprintf("第 %d 名獎勵", rank))
	}
	if defeated && r.Title != "" && rank <= r.TitleTop {
		out.Title = r.Title
	}
	return out
}

// Bar 進度條（width 格）。
func Bar(value, total float64, width int) string {
	filled := 0
	if total > 0 {
		filled = min(width, max(0, numfmt.Round(float64(width)*value/total)))
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
