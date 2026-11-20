// Package fair 品評會（Go 版新增，2026-11-20，Migration 0048；設計見「新玩法六項_流程圖.md」#5）。
//
//   - 以群組為單位每週一場：週一 00:00 開始報名與投票，週日 00:00 截止（CloseAfter），週日 20:00 起結算（ResultsAfter）。
//   - 組別每週輪換（作物、加工品、實驗室作物）；評審偏好每週由雜湊決定，符合的作品評審分數加成。
//   - 每位玩家每週報名 1 件（作品會被收走）、付 EntryFee；每人 VotesPerWeek 票投給別人的作品。
//   - 最終名次：評審名次分 × JudgeWeight ＋ 人氣名次分 × VoteWeight；獎池 = 報名費 × PoolPct%，前三名依 PrizeShares 分配。
//   - 報名少於 MinEntries 件時流會，退回報名費與作品。
package fair

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/clock"
)

const (
	EntryFee     = 200
	VotesPerWeek = 3
	MinEntries   = 3
	PoolPct      = 150 // 系統補貼 50%
	JudgeWeight  = 7
	VoteWeight   = 3
	CloseAfter   = 6 * 24 * time.Hour            // 週日 00:00 截止報名與投票
	ResultsAfter = 6*24*time.Hour + 20*time.Hour // 週日 20:00 公布結果
)

// PrizeShares 前三名分到獎池的百分比。
var PrizeShares = []int{50, 30, 20}

// 組別。
const (
	CatCrop  = "crop"  // 一般作物（商店有賣的作物）
	CatGoods = "goods" // 加工品、菇類、蜂蜜
	CatLab   = "lab"   // 稀有與雜交作物（商店沒有賣的作物）
)

var CategoryOrder = []string{CatCrop, CatGoods, CatLab}

var CategoryNames = map[string]string{CatCrop: "🌾 作物組", CatGoods: "🍞 加工品組", CatLab: "🧪 實驗室組"}

// Item 參賽作品的特徵（評審偏好用）。CropCode 只有作物類有。
type Item struct {
	Code, CropCode, Quality, Mutation string
}

// Pref 評審偏好：符合的作品評審分數 +Pct%。
type Pref struct {
	Code, Text string
	Pct        int
	Categories []string
	match      func(Item) bool
}

func (p Pref) Matches(it Item) bool { return p.match != nil && p.match(it) }

var crops = []string{CatCrop, CatLab}

// Prefs 評審偏好題庫。
var Prefs = []Pref{
	{"giant", "評審今天偏愛巨型作物", 30, crops, func(it Item) bool { return it.Mutation == "giant" }},
	{"golden", "評審今天偏愛黃金作物", 30, crops, func(it Item) bool { return it.Mutation == "golden" }},
	{"variant", "評審今天偏愛珍稀變種", 30, crops, func(it Item) bool { return it.Mutation == "variant" }},
	{"legendary", "評審今天偏愛傳說品質", 25, crops, func(it Item) bool { return it.Quality == "legendary" }},
	{"rare_up", "評審今天偏愛稀有以上的品質", 15, crops, func(it Item) bool { return it.Quality == "rare" || it.Quality == "legendary" }},
	{"red", "評審今天偏愛紅色系的作物", 20, crops, func(it Item) bool {
		return slices.Contains([]string{"strawberry", "tomato", "watermelon", "ruby_corn", "crimson_moon_flower"}, it.CropCode)
	}},
	{"jam", "評審今天偏愛果醬", 25, []string{CatGoods}, func(it Item) bool { return strings.HasSuffix(it.Code, "_jam") }},
	{"mushroom", "評審今天偏愛菇類與菇類料理", 25, []string{CatGoods}, func(it Item) bool {
		return slices.Contains([]string{"shiitake", "button_mushroom", "enoki", "lingzhi", "truffle", "glowcap",
			"shiitake_bread", "mushroom_soup", "enoki_pancake", "lingzhi_cake", "truffle_bread", "glowcap_pie"}, it.Code)
	}},
	{"dairy", "評審今天偏愛乳製品", 25, []string{CatGoods}, func(it Item) bool {
		return slices.Contains([]string{"cheese", "butter", "mayonnaise", "honey_milk"}, it.Code)
	}},
	{"bakery", "評審今天偏愛烘焙點心", 25, []string{CatGoods}, func(it Item) bool {
		return slices.Contains([]string{"bread", "cake", "strawberry_cake", "honey_cake"}, it.Code)
	}},
}

// WeekStart now 所在的台灣週一 00:00。
func WeekStart(now time.Time) time.Time {
	local := now.In(clock.TaipeiTZ)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// WeekKey 週一的日期字串（寫進 DATE 欄位用，避免時區轉換）。
func WeekKey(week time.Time) string { return week.In(clock.TaipeiTZ).Format("2006-01-02") }

// epochWeek 從 1970-01-05（週一）起算第幾週。
func epochWeek(week time.Time) int64 {
	base := time.Date(1970, 1, 5, 0, 0, 0, 0, clock.TaipeiTZ)
	return int64(WeekStart(week).Sub(base).Hours()) / (24 * 7)
}

// Category 這一週的組別（依週次輪換，全服相同）。
func Category(at time.Time) string { return CategoryOrder[epochWeek(at)%int64(len(CategoryOrder))] }

// WeekPref 這一週的評審偏好：從適用於本週組別的偏好中，由 SHA-256(週一日期) 選一個。
func WeekPref(at time.Time) Pref {
	cat := Category(at)
	var list []Pref
	for _, p := range Prefs {
		if slices.Contains(p.Categories, cat) {
			list = append(list, p)
		}
	}
	sum := sha256.Sum256([]byte("fair-" + WeekKey(WeekStart(at))))
	return list[binary.BigEndian.Uint64(sum[:8])%uint64(len(list))]
}

// Open 報名與投票是否開放。
func Open(now time.Time) bool { return now.Before(WeekStart(now).Add(CloseAfter)) }

// ResultsAt 這一週公布結果的時間。
func ResultsAt(week time.Time) time.Time { return WeekStart(week).Add(ResultsAfter) }

// JudgeScore 評審分數 = 作品價值 × 偏好加成。
func JudgeScore(value int, it Item, pref Pref) int {
	if pref.Matches(it) {
		return value * (100 + pref.Pct) / 100
	}
	return value
}

// Entry 排名用的資料。
type Entry struct {
	ID        int64
	Judge     int
	Votes     int
	CreatedAt time.Time
}

// Rank 依最終分數排序，回傳作品 ID（第 1 名在前）。
// 評審名次分與人氣名次分 = 件數 − 分數比自己高的件數（同分的作品拿到相同的名次分，不會因為報名先後吃虧）；
// 最終分數同分時評審分數高的在前，再相同時先報名的在前。
func Rank(entries []Entry) []int64 {
	n := len(entries)
	points := func(value func(Entry) int, e Entry) int {
		higher := 0
		for _, o := range entries {
			if value(o) > value(e) {
				higher++
			}
		}
		return n - higher
	}
	judge := func(e Entry) int { return e.Judge }
	votes := func(e Entry) int { return e.Votes }
	score := map[int64]int{}
	for _, e := range entries {
		score[e.ID] = points(judge, e)*JudgeWeight + points(votes, e)*VoteWeight
	}
	final := slices.Clone(entries)
	sort.SliceStable(final, func(i, j int) bool {
		a, b := final[i], final[j]
		if score[a.ID] != score[b.ID] {
			return score[a.ID] > score[b.ID]
		}
		if a.Judge != b.Judge {
			return a.Judge > b.Judge
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	out := make([]int64, n)
	for i, e := range final {
		out[i] = e.ID
	}
	return out
}

// Prizes 前三名的獎金（依名次）。
func Prizes(totalFees int) []int {
	pool := totalFees * PoolPct / 100
	out := make([]int, len(PrizeShares))
	for i, s := range PrizeShares {
		out[i] = pool * s / 100
	}
	return out
}
