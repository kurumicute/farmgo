// Package beehive 蜂箱（Go 版新增，2026-11-20，設計見「新玩法六項_流程圖.md」#3）。
//
//   - 農場 Lv.UnlockLevel 開放，放在一格空地上（那一格不能種植），每座農場 1 個，可以升到 MaxLevel 級、付費搬到別格。
//   - 授粉：周圍 8 格（同一塊農田的 3×3）種下的作物，突變機率 +MutationBP。
//   - 產蜜：每 IntervalSeconds 產 (BaseHoney + 周圍花類作物格數) × 天氣倍率，存量上限 Capacity，收成後放倉庫。
//   - 叮小偷：別人偷周圍格子時，防守設施與狗狗都沒擋住的話，再有 StingBP 機率被叮跑。
//   - 狀態存在 farm_tools（ToolCode）的 settings：不需要 Migration。蜂蜜與蜂蜜料理是新的物品代碼，
//     和新果醬一樣，有玩家取得後就不能切回 Python 版 Bot。
package beehive

import (
	"slices"
	"time"

	"farmgm/internal/game/weather"
)

const (
	ToolCode        = "beehive"
	UnlockLevel     = 3
	MaxLevel        = 3
	MoveFee         = 500
	IntervalSeconds = 6 * 3600
	Honey           = "honey"   // 物品代碼（balance.Goods）
	Blocker         = "beehive" // steal_records.blocked_by（被蜜蜂叮跑時）
	FieldSlots      = 16        // 一塊農田的格數（4×4）
	FieldCols       = 4
	maxIntervals    = 4 * 60 // 結算時最多往回算幾次（約 60 天），避免資料異常時迴圈太久
)

// 各等級的數值（索引 = 等級，0 不使用）。Prices[1] 是放置費，Prices[2..] 是升到該級的費用。
var (
	Prices     = [MaxLevel + 1]int{0, 8_000, 20_000, 50_000}
	MutationBP = [MaxLevel + 1]int{0, 100, 150, 200}
	BaseHoney  = [MaxLevel + 1]int{0, 1, 2, 3}
	StingBP    = [MaxLevel + 1]int{0, 1500, 2000, 2500}
	Capacity   = [MaxLevel + 1]int{0, 10, 20, 30}
)

// FlowerCrops 花類作物（周圍每有 1 格，每次多產 1 份蜂蜜）。
var FlowerCrops = []string{"moonflower", "starry_night_flower", "crimson_moon_flower"}

// WeatherPct 天氣對產蜜的倍率（%）：晴天多、雨天少、暴風當天蜂群躲起來。
var WeatherPct = map[string]int{weather.Sunny: 150, weather.Cloudy: 100, weather.Rainy: 50, weather.Storm: 0}

// IsFlower 是不是花類作物。
func IsFlower(cropCode string) bool { return slices.Contains(FlowerCrops, cropCode) }

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

// PerInterval 某種天氣下每次產多少（周圍有 flowers 格花類作物）：四捨五入。
func PerInterval(level, flowers int, weatherCode string) int {
	if level < 1 || level > MaxLevel {
		return 0
	}
	pct, ok := WeatherPct[weatherCode]
	if !ok {
		pct = 100
	}
	return ((BaseHoney[level]+max(0, flowers))*pct + 50) / 100
}

// Accrue 從 Last 結算到 now：每 IntervalSeconds 一次，依當次結束時的天氣產蜜，存量不超過 Capacity。
// 存量滿了時 Last 直接推到 now（滿的期間不累積）。回傳這次增加的數量。
func (s *State) Accrue(now time.Time, flowers int) int {
	if !s.Placed() {
		return 0
	}
	if s.Last.IsZero() || s.Last.After(now) {
		s.Last = now
		return 0
	}
	step := IntervalSeconds * time.Second
	n := int(now.Sub(s.Last) / step)
	if n <= 0 {
		return 0
	}
	if n > maxIntervals {
		s.Last = now.Add(-time.Duration(maxIntervals) * step)
		n = maxIntervals
	}
	gained := 0
	limit := Capacity[min(s.Level, MaxLevel)]
	for i := 1; i <= n; i++ {
		at := s.Last.Add(time.Duration(i) * step)
		add := min(PerInterval(s.Level, flowers, weather.On(at, 0).Code), limit-s.Honey)
		s.Honey += add
		gained += add
		if s.Honey >= limit {
			s.Last = now
			return gained
		}
	}
	s.Last = s.Last.Add(time.Duration(n) * step)
	return gained
}

// NextAt 下一次產蜜的時間（存量滿了或沒有蜂箱時為 nil）。
func (s State) NextAt() *time.Time {
	if !s.Placed() || s.Honey >= Capacity[min(s.Level, MaxLevel)] {
		return nil
	}
	t := s.Last.Add(IntervalSeconds * time.Second)
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
	if s.Level < 1 || s.Level > MaxLevel || s.Plot < 1 {
		return State{}
	}
	return s
}

// Encode 寫回 settings 的格式。
func (s State) Encode() map[string]any {
	return map[string]any{"plot": s.Plot, "level": s.Level, "honey": s.Honey, "last": s.Last.UTC().Format(time.RFC3339Nano)}
}
