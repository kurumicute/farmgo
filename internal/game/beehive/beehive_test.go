package beehive

import (
	"slices"
	"testing"
	"time"

	"farmgm/internal/game/weather"
)

func TestNeighbors(t *testing.T) {
	cases := map[int][]int{
		1:  {2, 5, 6},                  // 左上角
		6:  {1, 2, 3, 5, 7, 9, 10, 11}, // 中間
		16: {11, 12, 15},               // 右下角
		17: {18, 21, 22},               // 第二塊農田的左上角，不會跨回第一塊
		4:  {3, 7, 8},                  // 右上角
		13: {9, 10, 14},                // 左下角
		20: {19, 23, 24},               // 第二塊的右上角
	}
	for plot, want := range cases {
		got := Neighbors(plot)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%d → %v，應為 %v", plot, got, want)
		}
	}
	if !IsNeighbor(6, 11) || IsNeighbor(6, 6) || IsNeighbor(4, 5) {
		t.Fatal("IsNeighbor")
	}
}

// 產蜜（2026-11-21 改成固定）：每 12 小時 2 罐，最多存 10 罐，天氣與周圍的花都不影響。
func TestAccrue(t *testing.T) {
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, fixed := range []string{weather.Cloudy, weather.Storm, weather.Rainy} { // 天氣不影響產量
		weather.Fixed = fixed
		s := State{Plot: 6, Level: 1, Last: start}
		if s.Accrue(start.Add(11*time.Hour)) != 0 || !s.Last.Equal(start) {
			t.Fatal("不到 12 小時不產")
		}
		if got := s.Accrue(start.Add(25 * time.Hour)); got != 4 || s.Honey != 4 || !s.Last.Equal(start.Add(24*time.Hour)) {
			t.Fatalf("%s：2 次 × 2 = 4，got %d %+v", fixed, got, s)
		}
	}
	weather.Fixed = ""
	// 存滿之後不再累積，Last 推到現在；等級不影響上限
	now := start.Add(100 * time.Hour)
	s := State{Plot: 6, Level: 3, Last: start}
	s.Accrue(now)
	if s.Honey != Capacity || !s.Last.Equal(now) || s.NextAt() != nil {
		t.Fatalf("%+v", s)
	}
	// 舊規則存下超過上限的蜂蜜保留，不會被砍掉
	old := State{Plot: 1, Level: 3, Honey: 25, Last: start}
	if old.Accrue(now) != 0 || old.Honey != 25 {
		t.Fatalf("%+v", old)
	}
	var none State
	if none.Accrue(now) != 0 || none.Placed() {
		t.Fatal("沒有蜂箱")
	}
}

func TestParseEncode(t *testing.T) {
	last := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	s := State{Plot: 7, Level: 2, Honey: 4, Last: last}
	raw := map[string]any{}
	for k, v := range s.Encode() {
		if n, ok := v.(int); ok {
			raw[k] = float64(n) // 存進資料庫再讀出時數字是 float64
		} else {
			raw[k] = v
		}
	}
	if got := Parse(raw); got != s {
		t.Fatalf("%+v", got)
	}
	if Parse(map[string]any{"plot": 3.0, "level": 9.0}).Placed() || Parse(nil).Placed() {
		t.Fatal("壞資料當成沒有蜂箱")
	}
}
