// Package checkin 每日簽到：以台灣日期為準每天一次，開啟農場面板時自動簽到並發放獎勵（對應 Python app/game/checkin.py）。
//
//   - 昨天有簽到時連續天數 +1，中斷一天就從 1 重新計算。
//   - 獎勵預設 7 天一輪（第 7 天最多），之後重新循環；以道具為主、金幣少量，避免通膨。
//     每天的獎勵與循環天數可在管理網站調整（featurecfg 的 "checkin"），一律經 Rewards()／RewardFor() 讀取。
//   - 道具放不進倉庫時改發等值金幣（道具商店價格），自動簽到不會因倉庫滿而失敗。
//
// 日期以 time.Time 表示，只看其 Year/Month/Day（呼叫端應傳入台灣日期，例如 now.In(clock.TaipeiTZ)）。
package checkin

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

const (
	ConfigKey    = "checkin"
	MaxCycleDays = 31
)

// Reward 一天的簽到獎勵。
type Reward struct {
	Gold  int
	Items map[string]int
}

func dayValue(gold int, items map[string]int) map[string]any {
	return map[string]any{"gold": gold, "items": items}
}

func defaults() featurecfg.Value {
	// 第 1–7 天的獎勵：{"gold": 金幣, "items": {道具代碼: 數量}}
	return featurecfg.Value{"rewards": []any{
		dayValue(100, map[string]int{}),
		dayValue(0, map[string]int{balance.ItemFertilizer: 3}),
		dayValue(200, map[string]int{}),
		dayValue(0, map[string]int{balance.ItemPatrolTicket: 2}),
		dayValue(300, map[string]int{}),
		dayValue(0, map[string]int{balance.ItemFertilizer: 5}),
		dayValue(500, map[string]int{balance.ItemEarthworm: 1}),
	}}
}

// ValidateConfig 驗證並正規化簽到設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	days, ok := featurecfg.AsList(raw["rewards"])
	if !ok || len(days) < 1 || len(days) > MaxCycleDays {
		return nil, gameerr.Validationf("簽到獎勵必須有 1–%d 天。", MaxCycleDays)
	}
	out := make([]any, 0, len(days))
	for i, d := range days {
		n := i + 1
		day, ok := featurecfg.AsMap(d)
		if !ok {
			return nil, gameerr.Validationf("第 %d 天的獎勵格式不正確。", n)
		}
		goldRaw, has := day["gold"]
		if !has {
			goldRaw = 0
		}
		gold, err := featurecfg.IntValue(goldRaw, fmt.Sprintf("第 %d 天的金幣", n), 0, 1_000_000)
		if err != nil {
			return nil, err
		}
		items, err := featurecfg.ItemsValue(day["items"], fmt.Sprintf("第 %d 天", n), 3, 100)
		if err != nil {
			return nil, err
		}
		out = append(out, dayValue(gold, items))
	}
	return featurecfg.Value{"rewards": out}, nil
}

func init() {
	featurecfg.Register(ConfigKey, "每日簽到", defaults, ValidateConfig)
}

func toReward(v any) Reward {
	m, _ := featurecfg.AsMap(v)
	gold, _ := featurecfg.AsInt(m["gold"])
	items := map[string]int{}
	if im, ok := featurecfg.AsMap(m["items"]); ok {
		for code, q := range im {
			if n, ok := featurecfg.AsInt(q); ok {
				items[code] = n
			}
		}
	}
	return Reward{Gold: gold, Items: items}
}

// Rewards 目前生效的每日獎勵（第 1 天起）。
func Rewards() []Reward {
	list, _ := featurecfg.AsList(featurecfg.Get(ConfigKey)["rewards"])
	out := make([]Reward, len(list))
	for i, v := range list {
		out[i] = toReward(v)
	}
	return out
}

func CycleDays() int { return len(Rewards()) }

func sameDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// NextStreak 今天簽到後的連續天數：上一次簽到是昨天就接續，否則重新從 1 開始。lastDay 為 nil 表示從未簽到。
func NextStreak(lastDay *time.Time, lastStreak int, today time.Time) int {
	if lastDay != nil && sameDate(*lastDay, today.AddDate(0, 0, -1)) {
		return lastStreak + 1
	}
	return 1
}

// RewardFor 連續第 streak 天的獎勵（CycleDays() 天一輪）。
func RewardFor(streak int) Reward {
	days := Rewards()
	return days[numfmt.Mod(max(1, streak)-1, len(days))]
}

// ItemGoldValue 道具放不進倉庫時改發的金幣（道具商店價格）。
func ItemGoldValue(itemCode string) int { return balance.Items[itemCode].Price }

func ItemName(itemCode string) string { return balance.Items[itemCode].Name }

// SortedItemCodes 依道具商店順序排列道具代碼（Go map 沒有順序；Python 依 JSON 的插入順序）。
func SortedItemCodes(items map[string]int) []string {
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

// RewardText 例如「100 金幣、化肥 ×3」；沒有獎勵時為「無」。
func RewardText(r Reward) string {
	var parts []string
	if r.Gold != 0 {
		parts = append(parts, numfmt.Comma(r.Gold)+" 金幣")
	}
	for _, code := range SortedItemCodes(r.Items) {
		parts = append(parts, fmt.Sprintf("%s ×%d", ItemName(code), r.Items[code]))
	}
	if len(parts) == 0 {
		return "無"
	}
	return strings.Join(parts, "、")
}
