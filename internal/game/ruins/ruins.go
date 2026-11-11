// Package ruins 古代遺跡（群組合作）：群組成員捐山洞素材修復遺跡，三個階段完成後整個群組都能進入「古代遺跡」探險區域。
// 對應 Python app/game/ruins.py。
//
// 純規則，不碰資料庫。遺跡屬於農場的所屬群組（farms.home_group_id），和群組任務計分一樣不受開面板的伺服器影響；
// 所屬群組受 7 天轉移冷卻保護，不能在短時間內借用別的群組已修好的遺跡。
//
// 遺跡守護者：修好後的下一個台灣週一起，每週出現一隻守護者。成員捐素材攻擊（越稀有傷害越高），去遺跡探險回來
// 領獎時也會自動造成傷害；打倒後當週的遺跡探索每次多兩份獎勵。整週都沒打倒，遺跡崩壞回第三階段（要重新捐核心）。
//
// 日期慣例：Python 的 date 在 Go 以「台灣時區 00:00 的 time.Time」表示（見 Date）。
package ruins

import (
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/exploration"
)

// Stage 修復階段。MaterialOrder 保留 Python dict 的素材順序。
type Stage struct {
	Name          string
	Description   string
	Materials     map[string]int // 這個階段需要的素材 {代碼: 數量}
	MaterialOrder []string
}

// Stages 數值以「3–5 位活躍玩家約一到兩週修好」估算：草原一趟約 9 個普通素材；核心只在森林（Lv.7）與礦坑（Lv.10）出現，
// 所以第三階段需要不同等級的成員合作
var Stages = []Stage{
	{"清除碎石", "把堵住入口的碎石與倒下的樹木搬開。", map[string]int{"stone": 150, "wood": 150}, []string{"stone", "wood"}},
	{"修補石柱", "用礦石與樹脂補強快要倒塌的石柱。", map[string]int{"copper_ore": 80, "iron_ore": 50, "resin": 80},
		[]string{"copper_ore", "iron_ore", "resin"}},
	{"喚醒遺跡核心", "把齒輪、水晶與核心裝回遺跡中央的古代裝置。",
		map[string]int{"gear": 20, "crystal": 12, "ancient_core": 3, "machine_core": 3},
		[]string{"gear", "crystal", "ancient_core", "machine_core"}},
}

var Completed = len(Stages) // stage == 3：修復完成

const (
	DonateMax   = 999     // 單次捐獻上限
	Title       = "遺跡修復者" // 修復完成時，所有捐過素材的成員獲得的稱號（farm_titles.title 最長 20 字）
	TitleSource = "ruins"
)

// StageInfo 目前要修的階段；已修復完成時回傳 nil。
func StageInfo(stage int) *Stage {
	if stage >= 0 && stage < Completed {
		return &Stages[stage]
	}
	return nil
}

// Remaining 目前階段每種素材還差幾個（已足夠的不列出）。
func Remaining(stage int, progress map[string]int) map[string]int {
	out := map[string]int{}
	info := StageInfo(stage)
	if info == nil {
		return out
	}
	for code, need := range info.Materials {
		if progress[code] < need {
			out[code] = need - progress[code]
		}
	}
	return out
}

func StageDone(stage int, progress map[string]int) bool {
	return StageInfo(stage) != nil && len(Remaining(stage, progress)) == 0
}

// Percent 整體修復進度（0–100）：每個階段佔三分之一，階段內依已捐數量比例計算。
func Percent(stage int, progress map[string]int) int {
	info := StageInfo(stage)
	if info == nil {
		return 100
	}
	need, have := 0, 0
	for c, n := range info.Materials {
		need += n
		have += min(progress[c], n)
	}
	return min(99, int((float64(stage)+float64(have)/float64(need))*100/float64(Completed)))
}

// ---------- 遺跡守護者 ----------

const (
	GuardianName        = "石像守護者"
	GuardianHPBase      = 3000 // 2026-11-09 使用者要求調為原本的 2 倍
	GuardianHPPerMember = 1000 // 每座所屬群組的農場增加的 HP
	GuardianHPMax       = 40000
	ExpeditionDamage    = 200 // 每次探索古代遺跡時對守護者造成的傷害
	DefeatBonusRolls    = 2   // 打倒後，當週遺跡探索每次多抽的獎勵份數（血量 2 倍、獎勵也加倍）
	AttackMax           = 999
)

// GuardianDamage 每個素材依稀有度造成的傷害（假幣不能用）。
var GuardianDamage = map[string]int{"普通": 3, "稀有": 15, "極稀有": 80}

// Date 以台灣日期 y-m-d 的 00:00（台灣時區）表示 Python 的 date。
func Date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, clock.TaipeiTZ)
}

// WeekStart at 所在的台灣週一（台灣時區 00:00）。
func WeekStart(at time.Time) time.Time {
	local := at.In(clock.TaipeiTZ)
	day := Date(local.Year(), local.Month(), local.Day())
	weekday := (int(day.Weekday()) + 6) % 7 // Python weekday()：週一 = 0
	return day.AddDate(0, 0, -weekday)
}

// WeekEnd week（台灣週一）+ 7 天的 00:00（台灣時間）。
func WeekEnd(week time.Time) time.Time {
	w := week.In(clock.TaipeiTZ)
	return Date(w.Year(), w.Month(), w.Day()).AddDate(0, 0, 7)
}

// FirstGuardianWeek 修好後的下一個週一守護者才出現（修好當週不用打）。
func FirstGuardianWeek(completedAt time.Time) time.Time {
	return WeekStart(completedAt).AddDate(0, 0, 7)
}

func GuardianActive(completedAt *time.Time, now time.Time) bool {
	return completedAt != nil && !WeekStart(now).Before(FirstGuardianWeek(*completedAt))
}

func GuardianHP(members int) int {
	return min(GuardianHPMax, GuardianHPBase+GuardianHPPerMember*max(0, members))
}

// MaterialDamage 這種素材每個造成的傷害；不能用來攻擊時 ok = false。
func MaterialDamage(code string) (damage int, ok bool) {
	m, exists := exploration.Materials[code]
	if code == exploration.FakeCoin || !exists {
		return 0, false
	}
	damage, ok = GuardianDamage[m.Rarity]
	return damage, ok
}

// WeeksToSettle 已經結束、還沒結算的守護者週（由舊到新）；settled 為 nil 表示從未結算。
func WeeksToSettle(completedAt time.Time, settled *time.Time, now time.Time) []time.Time {
	week := FirstGuardianWeek(completedAt)
	if settled != nil {
		s := settled.In(clock.TaipeiTZ)
		s = Date(s.Year(), s.Month(), s.Day())
		if !s.Before(week) {
			week = s.AddDate(0, 0, 7)
		}
	}
	current := WeekStart(now)
	var out []time.Time
	for week.Before(current) {
		out = append(out, week)
		week = week.AddDate(0, 0, 7)
	}
	return out
}
