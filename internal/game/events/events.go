// Package events 限時全服事件的純規則：設定驗證、是否生效、加成加總與上限（對應 Python app/game/events.py）。
//
// 事件只在「操作當下」判斷是否生效：
//   - 播種時：品質加成、突變加成、收成數量加成（結果固定在土地上，之後事件結束也不變）。
//   - 澆水時：生長時間減少（成熟時間在澆水時決定）；免澆水活動期間種下的作物立即澆水。
//   - 收成時：大風颳走（每格少 N 個，至少保留 1 個）。
//   - 販售作物時：售價倍率（實際成交單價寫入交易紀錄）。
//
// 多個事件同時生效時加成相加，但各自有上限（balance.EventMax*）。
package events

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

// Effect 效果欄位：中文名稱、範圍、無效果時的值、加總上限。
type Effect struct {
	Label   string
	Range   balance.Range
	Neutral int
	Cap     int
}

// EffectOrder 效果欄位順序（Python EFFECTS 的 dict 順序）。
var EffectOrder = []string{
	"sell_price_pct", "grow_reduction_pct", "quality_bonus_bp", "mutation_bonus_bp", "harvest_bonus_pct",
	"free_water", "harvest_loss",
}

var Effects = map[string]Effect{
	"sell_price_pct":     {"售價倍率", balance.EventSellPricePctRange, 100, balance.EventMaxSellBonusPct},
	"grow_reduction_pct": {"生長時間減少", balance.EventGrowReductionPctRange, 0, balance.EventMaxGrowReductionPct},
	"quality_bonus_bp":   {"品質加成", balance.EventQualityBonusBPRange, 0, balance.EventMaxQualityBonusBP},
	"mutation_bonus_bp":  {"突變加成", balance.EventMutationBonusBPRange, 0, balance.EventMaxMutationBonusBP},
	"harvest_bonus_pct":  {"收成加成", balance.EventHarvestBonusPctRange, 0, balance.EventMaxHarvestBonusPct},
	// 0／1：活動期間種下的作物立即澆水並開始生長
	"free_water": {"免澆水", balance.EventFreeWaterRange, 0, balance.EventMaxFreeWater},
	// 收成時每格少 N 個（至少保留 1 個）；屬於負面效果，但同樣以「大於 0 才生效」處理
	"harvest_loss": {"大風颳走", balance.EventHarvestLossRange, 0, balance.EventMaxHarvestLoss},
}

var PlantEffects = []string{"quality_bonus_bp", "mutation_bonus_bp", "harvest_bonus_pct"}

// Event 限時事件（對應資料表 game_events 的欄位）。nil 指標表示資料庫 NULL。
type Event struct {
	ID               int64
	Name             string
	Description      string
	CropTypeID       *int64
	SellPricePct     int
	GrowReductionPct int
	QualityBonusBP   int
	MutationBonusBP  int
	HarvestBonusPct  int
	FreeWater        int
	HarvestLoss      int
	ScheduleType     string
	StartsAt         *time.Time
	EndsAt           *time.Time
	StartMinute      *int
	EndMinute        *int
	Weekdays         *int // 位元遮罩：週一 = 1、週二 = 2 … 週日 = 64
	Enabled          bool
	CreatedBy        string
	UpdatedAt        time.Time
}

// EffectValue 依欄位名稱取效果值（Python getattr(event, effect)）；未知欄位回傳 0。
func (e *Event) EffectValue(key string) int {
	switch key {
	case "sell_price_pct":
		return e.SellPricePct
	case "grow_reduction_pct":
		return e.GrowReductionPct
	case "quality_bonus_bp":
		return e.QualityBonusBP
	case "mutation_bonus_bp":
		return e.MutationBonusBP
	case "harvest_bonus_pct":
		return e.HarvestBonusPct
	case "free_water":
		return e.FreeWater
	case "harvest_loss":
		return e.HarvestLoss
	}
	return 0
}

// Applied 實際貢獻加成的事件。
type Applied struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Bonus 加總後（已套用上限）的加成值與實際貢獻的事件。
type Bonus struct {
	Value   int
	Applied []Applied
}

// pyG 等同 Python f"{x:g}"。
func pyG(x float64) string { return fmt.Sprintf("%.6g", x) }

func checkInt(value any, label string, low, high int, errs *[]string) int {
	n, ok := featurecfg.AsInt(value)
	if !ok {
		*errs = append(*errs, fmt.Sprintf("%s必須是整數。", label))
		return low
	}
	if n < low || n > high {
		*errs = append(*errs, fmt.Sprintf("%s必須在 %d–%d 之間（目前 %d）。", label, low, high, n))
	}
	return n
}

// Settings 驗證後正規化的事件設定。CropCode 為 "" 表示 None（全部作物）。
type Settings struct {
	Name         string
	Description  string
	CropCode     string
	Effects      map[string]int
	ScheduleType string
	StartsAt     *time.Time
	EndsAt       *time.Time
	StartMinute  *int
	EndMinute    *int
	Weekdays     *int
	Enabled      bool
}

// asTime 取出時間；nil 回傳 (nil, true)；不是時間型別回傳 ok = false。
func asTime(v any) (*time.Time, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case time.Time:
		return &t, true
	case *time.Time:
		return t, true
	}
	return nil, false
}

func strValue(v any) string {
	s, _ := v.(string)
	return s
}

// truthy 等同 Python bool(value)。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	}
	if n, ok := featurecfg.AsInt(v); ok {
		return n != 0
	}
	return true
}

// Validate 驗證事件設定並回傳正規化的值；錯誤一次列出。
// raw 的鍵與 Python 相同；starts_at／ends_at 需為 time.Time 或 *time.Time（Go 的時間一定帶時區）。
func Validate(raw map[string]any) (Settings, error) {
	var errs []string
	name := strings.TrimSpace(strValue(raw["name"]))
	if n := utf8.RuneCountInString(name); n < 1 || n > balance.EventNameMaxLen {
		errs = append(errs, fmt.Sprintf("事件名稱長度必須為 1–%d 字。", balance.EventNameMaxLen))
	}
	description := strings.TrimSpace(strValue(raw["description"]))
	if utf8.RuneCountInString(description) > balance.EventDescMaxLen {
		errs = append(errs, fmt.Sprintf("事件說明最多 %d 字。", balance.EventDescMaxLen))
	}
	values := map[string]int{}
	for _, key := range EffectOrder {
		eff := Effects[key]
		v, present := raw[key]
		if !present {
			v = eff.Neutral
		}
		values[key] = checkInt(v, eff.Label, eff.Range.Low, eff.Range.High, &errs)
	}
	if len(errs) == 0 {
		allNeutral := true
		for _, key := range EffectOrder {
			if values[key] != Effects[key].Neutral {
				allNeutral = false
				break
			}
		}
		if allNeutral {
			errs = append(errs, "至少要設定一項加成效果。")
		}
	}

	schedule := strValue(raw["schedule_type"])
	startMinuteRaw, endMinuteRaw, weekdaysRaw := raw["start_minute"], raw["end_minute"], raw["weekdays"]
	if _, ok := balance.EventScheduleTypes[schedule]; !ok {
		errs = append(errs, "排程類型只能是單次、每日或每週。")
	}
	startsAt, startsOK := asTime(raw["starts_at"])
	endsAt, endsOK := asTime(raw["ends_at"])
	if !startsOK {
		errs = append(errs, "開始時間格式不正確。")
	}
	if !endsOK {
		errs = append(errs, "結束時間格式不正確。")
	}
	if schedule == "once" && ((startsOK && startsAt == nil) || (endsOK && endsAt == nil)) {
		errs = append(errs, "單次事件必須設定開始與結束時間。")
	}
	if startsAt != nil && endsAt != nil && !endsAt.After(*startsAt) {
		errs = append(errs, "結束時間必須晚於開始時間。")
	}
	var startMinute, endMinute, weekdays *int
	if schedule == "daily" || schedule == "weekly" {
		if startMinuteRaw == nil || endMinuteRaw == nil {
			errs = append(errs, "每日／每週事件必須設定每天的開始與結束時刻。")
		} else {
			s := checkInt(startMinuteRaw, "開始時刻", 0, 1439, &errs)
			e := checkInt(endMinuteRaw, "結束時刻", 1, 1440, &errs)
			startMinute, endMinute = &s, &e
			if e <= s {
				errs = append(errs, "結束時刻必須晚於開始時刻（不可跨越午夜，可設定到 24:00）。")
			}
		}
	}
	if schedule == "weekly" {
		if weekdaysRaw != nil {
			w := checkInt(weekdaysRaw, "星期", 1, 127, &errs)
			weekdays = &w
		}
		if weekdays == nil {
			errs = append(errs, "每週事件至少要選擇一天。")
		}
	}
	cropCode := strValue(raw["crop_code"])
	if len(errs) > 0 {
		return Settings{}, gameerr.Validationf("%s", "事件設定無效："+strings.Join(errs, "；"))
	}
	enabled := true
	if v, ok := raw["enabled"]; ok {
		enabled = truthy(v)
	}
	return Settings{
		Name:         name,
		Description:  description,
		CropCode:     cropCode,
		Effects:      values,
		ScheduleType: schedule,
		StartsAt:     startsAt,
		EndsAt:       endsAt,
		StartMinute:  startMinute,
		EndMinute:    endMinute,
		Weekdays:     weekdays,
		Enabled:      enabled,
	}, nil
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// mondayWeekday 週一 = 0 … 週日 = 6（Python datetime.weekday()）。
func mondayWeekday(t time.Time) int { return (int(t.Weekday()) + 6) % 7 }

// IsActive 事件在 now（任何時區）是否生效；每日／每週時段以台灣時間判斷，區間為 [開始, 結束)。
// 每日／每週事件缺少時刻時視為不生效（Python 會拋出 TypeError）。
func IsActive(e *Event, now time.Time) bool {
	if !e.Enabled {
		return false
	}
	if e.StartsAt != nil && now.Before(*e.StartsAt) {
		return false
	}
	if e.EndsAt != nil && !now.Before(*e.EndsAt) {
		return false
	}
	if e.ScheduleType == "once" {
		return true
	}
	if e.StartMinute == nil || e.EndMinute == nil {
		return false
	}
	local := now.In(clock.TaipeiTZ)
	minute := local.Hour()*60 + local.Minute()
	if !(*e.StartMinute <= minute && minute < *e.EndMinute) {
		return false
	}
	if e.ScheduleType == "weekly" {
		return deref(e.Weekdays)&(1<<mondayWeekday(local)) != 0
	}
	return true
}

// AppliesToCrop cropTypeID 為 nil 表示 None。
func AppliesToCrop(e *Event, cropTypeID *int64) bool {
	if e.CropTypeID == nil {
		return true
	}
	return cropTypeID != nil && *e.CropTypeID == *cropTypeID
}

// BonusOf 加總已生效事件中某一項效果（已過濾作物），並套用上限。events 應只包含生效中的事件。
func BonusOf(evs []*Event, effect string, cropTypeID *int64) Bonus {
	eff := Effects[effect]
	var result Bonus
	total := 0
	for _, e := range evs {
		amount := e.EffectValue(effect) - eff.Neutral
		if amount > 0 && AppliesToCrop(e, cropTypeID) {
			total += amount
			result.Applied = append(result.Applied, Applied{ID: e.ID, Name: e.Name})
		}
	}
	result.Value = min(total, eff.Cap)
	return result
}

// MergeApplied 合併多項加成的事件清單（依 ID 去重，保留第一次出現的順序）；沒有任何事件時回傳 nil。
func MergeApplied(bonuses ...Bonus) []Applied {
	seen := map[int64]bool{}
	var out []Applied
	for _, b := range bonuses {
		for _, item := range b.Applied {
			if !seen[item.ID] {
				seen[item.ID] = true
				out = append(out, item)
			}
		}
	}
	return out
}

// ApplySellBonus 售價加成：單價 × (100 + 加成%) / 100，無條件捨去。
func ApplySellBonus(unitPrice, bonusPct int) int {
	return numfmt.FloorDiv(unitPrice*(100+bonusPct), 100)
}

func hhmm(minute int) string {
	return fmt.Sprintf("%02d:%02d", numfmt.FloorDiv(minute, 60), numfmt.Mod(minute, 60))
}

func fmtTime(t time.Time) string { return t.In(clock.TaipeiTZ).Format("2006-01-02 15:04") }

// ScheduleText 排程說明文字。
func ScheduleText(e *Event) string {
	period := ""
	if e.StartsAt != nil || e.EndsAt != nil {
		start := "即日起"
		if e.StartsAt != nil {
			start = fmtTime(*e.StartsAt)
		}
		end := "不限"
		if e.EndsAt != nil {
			end = fmtTime(*e.EndsAt)
		}
		period = fmt.Sprintf("%s ～ %s", start, end)
	}
	if e.ScheduleType == "once" {
		return period
	}
	window := fmt.Sprintf("%s–%s", hhmm(deref(e.StartMinute)), hhmm(deref(e.EndMinute)))
	var text string
	if e.ScheduleType == "daily" {
		text = "每日 " + window
	} else {
		var days []string
		for i, n := range balance.WeekdayNames {
			if deref(e.Weekdays)&(1<<i) != 0 {
				days = append(days, n)
			}
		}
		text = fmt.Sprintf("每週%s %s", strings.Join(days, "、"), window)
	}
	if period != "" {
		return fmt.Sprintf("%s（%s）", text, period)
	}
	return text
}

// EffectsText 效果說明文字。
func EffectsText(e *Event) string {
	var parts []string
	if e.SellPricePct != 100 {
		parts = append(parts, "售價 ×"+pyG(float64(e.SellPricePct)/100))
	}
	if e.GrowReductionPct != 0 {
		parts = append(parts, fmt.Sprintf("生長時間 -%d%%", e.GrowReductionPct))
	}
	if e.QualityBonusBP != 0 {
		parts = append(parts, "品質加成 +"+pyG(float64(e.QualityBonusBP)/100)+"%")
	}
	if e.MutationBonusBP != 0 {
		parts = append(parts, "突變機率 +"+pyG(float64(e.MutationBonusBP)/100)+"%")
	}
	if e.HarvestBonusPct != 0 {
		parts = append(parts, fmt.Sprintf("收成 +%d%%", e.HarvestBonusPct))
	}
	if e.FreeWater != 0 {
		parts = append(parts, "免澆水（種下立即生長）")
	}
	if e.HarvestLoss != 0 {
		parts = append(parts, fmt.Sprintf("收成時每格被吹走 %d 個", e.HarvestLoss))
	}
	return strings.Join(parts, "、")
}

// View 管理網站顯示用（鍵與 Python view() 相同）。
func View(e *Event, now time.Time) map[string]any {
	v := map[string]any{
		"id":             e.ID,
		"name":           e.Name,
		"description":    e.Description,
		"crop_type_id":   e.CropTypeID,
		"schedule_type":  e.ScheduleType,
		"schedule_label": balance.EventScheduleTypes[e.ScheduleType],
		"starts_at":      e.StartsAt,
		"ends_at":        e.EndsAt,
		"start_minute":   e.StartMinute,
		"end_minute":     e.EndMinute,
		"weekdays":       e.Weekdays,
		"enabled":        e.Enabled,
		"active_now":     IsActive(e, now),
		"schedule_text":  ScheduleText(e),
		"effects_text":   EffectsText(e),
		"created_by":     e.CreatedBy,
		"updated_at":     e.UpdatedAt,
	}
	for _, k := range EffectOrder {
		v[k] = e.EffectValue(k)
	}
	return v
}
