// Package ranking 排名獎勵的純規則：統計期間（台灣時間）與獎勵規則驗證（對應 Python app/game/ranking.py）。
//
//   - 每日：00:00 起算；每週：週一 00:00 起算；每月：1 日 00:00 起算。期間為 [開始, 結束)。
//   - 群組排名的分數是「主要所屬群組為該群組的成員」加總；個人排名為所有有農場的玩家。
//   - 期間內的項目：
//   - money（金幣）：期間內賣出農產品（作物，含珍稀變種）的收入；買東西等花費不影響排名，
//     動物產品、加工品、魚與各種獎勵也不算。不指定期間時為目前金幣餘額。
//   - sales（農產品販售）：期間內作物（含珍稀變種）販售數量。
//   - goods（商品總販售）：期間內所有商品（作物＋動物產品＋加工品）販售收入。
//   - 只有分數大於 0 的群組／玩家可以得獎；同分時依次要條件與 ID 排序，名次不重複。
package ranking

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

var (
	PeriodOrder = []string{"daily", "weekly", "monthly"}
	Periods     = map[string]string{"daily": "每日", "weekly": "每週", "monthly": "每月"}

	ViewPeriodOrder = []string{"all", "day", "week", "month"}
	ViewPeriods     = map[string]string{"all": "全部期間", "day": "本日", "week": "本週", "month": "本月"}
	ViewToPeriod    = map[string]string{"day": "daily", "week": "weekly", "month": "monthly"}

	ScopeOrder = []string{"group", "individual"}
	Scopes     = map[string]string{"group": "群組排名（獎勵發給群組全體成員）", "individual": "個人排名"}

	MetricOrder = []string{"money", "sales", "goods"}
	Metrics     = map[string]string{"money": "金幣", "sales": "農產品販售", "goods": "商品總販售"}

	// NetGoldExcluded 計算「淨賺金幣」時排除的交易類型（非遊戲經營所得）
	NetGoldExcluded = []string{
		"create_farm", "admin_adjust", "quest_reward", "competition_reward", "ranking_reward", "boss_reward",
		"checkin_reward", "achievement_reward", "rumor_treasure",
	}
)

// PeriodBounds 回傳包含 at 的期間 [開始, 結束)，皆為台灣時間；period 不正確時回傳錯誤（Python 為 ValueError）。
func PeriodBounds(period string, at time.Time) (time.Time, time.Time, error) {
	local := at.In(clock.TaipeiTZ)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
	switch period {
	case "daily":
		return day, day.AddDate(0, 0, 1), nil
	case "weekly":
		weekday := (int(day.Weekday()) + 6) % 7 // 週一 = 0（Python weekday()）
		start := day.AddDate(0, 0, -weekday)
		return start, start.AddDate(0, 0, 7), nil
	case "monthly":
		start := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, clock.TaipeiTZ)
		return start, start.AddDate(0, 1, 0), nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("unknown period: %s", period)
}

// PeriodLabel 期間的顯示文字。
func PeriodLabel(period string, start time.Time) string {
	local := start.In(clock.TaipeiTZ)
	switch period {
	case "daily":
		return local.Format("2006-01-02")
	case "weekly":
		end := local.AddDate(0, 0, 6)
		return fmt.Sprintf("%s～%s（週）", local.Format("2006-01-02"), end.Format("01-02"))
	}
	return local.Format("2006-01") + "（月）"
}

// Reward 單一名次的獎勵。
type Reward struct {
	RewardType string `json:"reward_type"`
	Amount     int    `json:"amount"`
}

// Rule 驗證後的排名獎勵規則。
type Rule struct {
	Name    string   `json:"name"`
	Scope   string   `json:"scope"`
	Metric  string   `json:"metric"`
	Period  string   `json:"period"`
	Rewards []Reward `json:"rewards"`
	Enabled bool     `json:"enabled"`
}

func intField(value any, label string, low, high int, errs *[]string) int {
	n, ok := featurecfg.AsInt(value)
	if !ok || n < low || n > high {
		*errs = append(*errs, fmt.Sprintf("%s必須是 %d–%d 之間的整數。", label, low, high))
		return low
	}
	return n
}

// truthy 等同 Python bool(value)（JSON 形狀的值）。
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func strIn(value any, m map[string]string) (string, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	_, ok = m[s]
	return s, ok
}

// ValidateRule 驗證排名獎勵規則；rewards 依名次排列，每項為 {"reward_type", "amount"}。錯誤一次列出。
func ValidateRule(raw map[string]any) (Rule, error) {
	var errs []string
	name := ""
	if s, ok := raw["name"].(string); ok {
		name = strings.TrimSpace(s)
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > balance.RankingNameMaxLen {
		errs = append(errs, fmt.Sprintf("規則名稱長度必須為 1–%d 字。", balance.RankingNameMaxLen))
	}
	scope, okScope := strIn(raw["scope"], Scopes)
	metric, okMetric := strIn(raw["metric"], Metrics)
	period, okPeriod := strIn(raw["period"], Periods)
	if !okScope {
		errs = append(errs, "排名對象只能是群組或個人。")
	}
	if !okMetric {
		errs = append(errs, "排名項目只能是金幣、農產品販售或商品總販售。")
	}
	if !okPeriod {
		errs = append(errs, "結算週期只能是每日、每週或每月。")
	}
	rewards := []Reward{}
	rewardsRaw, isList := featurecfg.AsList(raw["rewards"])
	if !isList || len(rewardsRaw) < 1 || len(rewardsRaw) > balance.RankingMaxRanks {
		errs = append(errs, fmt.Sprintf("獎勵名次必須設定 1–%d 名。", balance.RankingMaxRanks))
	} else {
		for i, r := range rewardsRaw {
			m, ok := featurecfg.AsMap(r)
			var kind string
			if ok {
				kind, _ = m["reward_type"].(string)
			}
			if !ok || !slices.Contains(balance.QuestRewardTypes, kind) {
				errs = append(errs, fmt.Sprintf("第 %d 名的獎勵類型只能是金幣、化肥、蚯蚓或巡查券。", i+1))
				continue
			}
			high := balance.RankingMaxItems
			if kind == "gold" {
				high = balance.RankingMaxGold
			}
			amount := intField(m["amount"], fmt.Sprintf("第 %d 名的獎勵數量", i+1), 1, high, &errs)
			rewards = append(rewards, Reward{RewardType: kind, Amount: amount})
		}
	}
	if len(errs) > 0 {
		return Rule{}, gameerr.Validationf("%s", "排名獎勵規則無效："+strings.Join(errs, "；"))
	}
	enabled := true
	if v, ok := raw["enabled"]; ok {
		enabled = truthy(v)
	}
	return Rule{Name: name, Scope: scope, Metric: metric, Period: period, Rewards: rewards, Enabled: enabled}, nil
}

// RewardText 獎勵的顯示文字。
func RewardText(r Reward) string {
	if r.RewardType == "gold" {
		return fmt.Sprintf("%d 金幣", r.Amount)
	}
	return fmt.Sprintf("%d 個%s", r.Amount, balance.Items[r.RewardType].Name)
}
