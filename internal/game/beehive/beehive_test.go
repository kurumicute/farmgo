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

func TestPerInterval(t *testing.T) {
	if PerInterval(1, 0, weather.Cloudy) != 1 || PerInterval(1, 0, weather.Rainy) != 1 || PerInterval(1, 0, weather.Storm) != 0 {
		t.Fatal("Lv.1 陰天 1、雨天 0.5 四捨五入為 1、暴風 0")
	}
	if PerInterval(3, 2, weather.Sunny) != 8 || PerInterval(2, 1, weather.Rainy) != 2 {
		t.Fatal("(3+2)×1.5 = 7.5 → 8；(2+1)×0.5 = 1.5 → 2")
	}
}

func TestAccrue(t *testing.T) {
	weather.Fixed = weather.Cloudy
	defer func() { weather.Fixed = "" }()
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	s := State{Plot: 6, Level: 1, Last: start}
	if s.Accrue(start.Add(5*time.Hour), 0) != 0 || !s.Last.Equal(start) {
		t.Fatal("不到 6 小時不產")
	}
	if got := s.Accrue(start.Add(13*time.Hour), 2); got != 6 || s.Honey != 6 || !s.Last.Equal(start.Add(12*time.Hour)) {
		t.Fatalf("2 次 × (1+2) = 6，got %d %+v", got, s)
	}
	// 存滿之後不再累積，Last 推到現在
	now := start.Add(100 * time.Hour)
	s.Accrue(now, 2)
	if s.Honey != Capacity[1] || !s.Last.Equal(now) || s.NextAt() != nil {
		t.Fatalf("%+v", s)
	}
	weather.Fixed = weather.Storm
	s = State{Plot: 1, Level: 3, Last: start}
	if s.Accrue(start.Add(24*time.Hour), 5) != 0 {
		t.Fatal("暴風不產蜜")
	}
	var none State
	if none.Accrue(now, 3) != 0 || none.Placed() {
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
