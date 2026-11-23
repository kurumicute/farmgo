// Package beehive 蜂箱（Go 版新增，2026-11-20，設計見「新玩法六項_流程圖.md」#3）。
//
//   - 農場 Lv.UnlockLevel 開放，放在一格空地上（那一格不能種植），每座農場 1 個，可以升到 MaxLevel 級、付費搬到別格。
//   - 授粉：周圍 8 格（同一塊農田的 3×3）種下的作物，突變機率 +MutationBP。
//   - 產蜜：每 IntervalSeconds 產 HoneyPerInterval 罐，存量上限 Capacity，收成後放倉庫（2026-11-21 使用者要求改成固定：
//     每 12 小時 2 罐、存 10 罐，不分等級，天氣與周圍的花不再影響；原本是每 6 小時 (等級 + 周圍花類作物格數) × 天氣倍率）。
//   - 叮小偷：別人偷周圍格子時，防守設施都沒擋住的話，再有 StingBP 機率被叮跑。
//   - 2026-11-21 起各級價格、突變加成、叮小偷機率、搬家費、產蜜間隔與數量、存量上限都可在管理網站「設施與道具價格」調整
//     （game/upgrades），一律經這裡的函式讀取。降低等級數時，已超過的蜂箱以新的最高等級計算。
//   - 狀態存在 farm_tools（ToolCode）的 settings：不需要 Migration。蜂蜜與蜂蜜料理是新的物品代碼，
//     和新果醬一樣，有玩家取得後就不能切回 Python 版 Bot。
package beehive

import (
	"slices"
	"time"

	"farmgm/internal/game/upgrades"
)

const (
	ToolCode     = "beehive"
	UnlockLevel  = 3
	Honey        = "honey"   // 物品代碼（balance.Goods）
	Blocker      = "beehive" // steal_records.blocked_by（被蜜蜂叮跑時）
	FieldSlots   = 16        // 一塊農田的格數（4×4）
	FieldCols    = 4
	maxIntervals = 4 * 60 // 結算時最多往回算幾次，避免資料異常時數字溢位
)

// MaxLevel 目前的最高等級（預設 5 級）。
func MaxLevel() int { return len(upgrades.Current().BeehivePrices) }

// Price 放置（level = 1）或升到 level 的價格；超出範圍時是 0。
func Price(level int) int {
	p := upgrades.Current().BeehivePrices
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

// MutationBP 周圍作物的突變加成（萬分比）。
func MutationBP(level int) int { return levelValue(upgrades.Current().BeehiveMutationBP, level) }

// StingBP 叮跑小偷的機率（萬分比）。
func StingBP(level int) int { return levelValue(upgrades.Current().BeehiveStingBP, level) }

// MoveFee 搬家費。
func MoveFee() int { return upgrades.Current().BeehiveMoveFee }

// IntervalSeconds 每幾秒產一次蜂蜜。
func IntervalSeconds() int { return upgrades.Current().BeehiveHours * 3600 }

// HoneyPerInterval 每次產幾罐。
func HoneyPerInterval() int { return upgrades.Current().BeehiveHoney }

// Capacity 存量上限（每個蜂箱，不分等級）。
func Capacity() int { return upgrades.Current().BeehiveCapacity }

// EffectiveLevel 計算效果用的等級（不超過目前的最高等級）。
func EffectiveLevel(level int) int { return min(level, MaxLevel()) }

// Neighbors 周圍 8 格的格子編號（1 起算；同一塊 4×4 農田內，不跨到另一塊）。不檢查格子是否存在。
func Neighbors(plot int) []int {
	if plot < 1 {
		return nil
	}
	k := plot - 1
	block, idx := k/FieldSlots, k%FieldSlots
	row, col := idx/FieldCols, idx%FieldCols
	var out []int
	for dr := -1; dr <= 1; dr++ {
		for dc := -1; dc <= 1; dc++ {
			r, c := row+dr, col+dc
			if (dr == 0 && dc == 0) || r < 0 || c < 0 || r >= FieldSlots/FieldCols || c >= FieldCols {
				continue
			}
			out = append(out, block*FieldSlots+r*FieldCols+c+1)
		}
	}
	return out
}

// IsNeighbor plot 是否在蜂箱 hive 的周圍 8 格。
func IsNeighbor(hive, plot int) bool { return slices.Contains(Neighbors(hive), plot) }

// State 一座農場的蜂箱（Plot 0 表示還沒放）。
type State struct {
	Plot  int
	Level int
	Honey int       // 存量
	Last  time.Time // 上次結算到的時間
}

// Placed 已經放了蜂箱。
func (s State) Placed() bool { return s.Plot > 0 && s.Level >= 1 }

// Accrue 從 Last 結算到 now：每 IntervalSeconds 產 HoneyPerInterval 罐，存量不超過 Capacity。
// 存量滿了時 Last 直接推到 now（滿的期間不累積）。回傳這次增加的數量。
func (s *State) Accrue(now time.Time) int {
	if !s.Placed() {
		return 0
	}
	if s.Last.IsZero() || s.Last.After(now) {
		s.Last = now
		return 0
	}
	step := time.Duration(IntervalSeconds()) * time.Second
	capacity, per := Capacity(), HoneyPerInterval()
	n := int(now.Sub(s.Last) / step)
	if n <= 0 {
		return 0
	}
	if n > maxIntervals {
		s.Last = now.Add(-time.Duration(maxIntervals) * step)
		n = maxIntervals
	}
	before := s.Honey
	// 舊規則（Lv.2、Lv.3 上限 20、30 罐）存下的超過 Capacity 的蜂蜜保留，收走之前不再增加
	s.Honey = max(s.Honey, min(capacity, s.Honey+n*per))
	if s.Honey >= capacity { // 存滿了：滿的期間不累積
		s.Last = now
	} else {
		s.Last = s.Last.Add(time.Duration(n) * step)
	}
	return max(0, s.Honey-before)
}

// NextAt 下一次產蜜的時間（存量滿了或沒有蜂箱時為 nil）。
func (s State) NextAt() *time.Time {
	if !s.Placed() || s.Honey >= Capacity() {
		return nil
	}
	t := s.Last.Add(time.Duration(IntervalSeconds()) * time.Second)
	return &t
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

// Parse 讀出狀態；格式錯誤時當成沒有蜂箱。
func Parse(settings map[string]any) State {
	s := State{Plot: num(settings["plot"]), Level: num(settings["level"]), Honey: max(0, num(settings["honey"]))}
	if raw, _ := settings["last"].(string); raw != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			s.Last = t
		}
	}
	if s.Level < 1 || s.Level > upgrades.BeehiveLevelLimit || s.Plot < 1 { // 超過目前最高等級的保留（降低等級數時不會消失）
		return State{}
	}
	return s
}

// Encode 寫回 settings 的格式。
func (s State) Encode() map[string]any {
	return map[string]any{"plot": s.Plot, "level": s.Level, "honey": s.Honey, "last": s.Last.UTC().Format(time.RFC3339Nano)}
}
