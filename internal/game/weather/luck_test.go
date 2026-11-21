package weather

import (
	"slices"
	"testing"
	"time"

	"farmgm/internal/clock"
)

func TestLucksFrom(t *testing.T) {
	cases := []struct {
		days []string
		want []string
	}{
		{[]string{Cloudy, Storm, Cloudy}, nil}, // 雷擊種子已刪除（2026-11-21）：暴風隔天沒有奇遇
		{[]string{Sunny, Sunny, Sunny}, []string{LuckDrought}},
		{[]string{Cloudy, Sunny, Sunny}, nil},
		{[]string{Cloudy, Rainy, Rainy}, []string{LuckDrizzle}},
		{[]string{Rainy, Cloudy, Rainy}, nil},
		{[]string{Sunny, Storm, Rainy}, nil},
		{[]string{Cloudy, Rainy, Sunny}, []string{LuckRainbow}},
		{[]string{Sunny, Sunny, Sunny}, []string{LuckDrought}}, // 連續晴天不是彩虹日
		{[]string{Rainy, Sunny, Sunny}, nil},
		{[]string{Storm, Rainy, Rainy}, []string{LuckDrizzle}},
	}
	for _, c := range cases {
		if got := LucksFrom(c.days); !slices.Equal(got, c.want) {
			t.Errorf("%v → %v，應為 %v", c.days, got, c.want)
		}
	}
}

func TestOnMatchesForDate(t *testing.T) {
	Fixed = ""
	defer func() { Fixed = "" }()
	now := time.Date(2026, 11, 20, 23, 30, 0, 0, clock.TaipeiTZ)
	if On(now, 0).Code != ForDate(now).Code || On(now, 1).Code != ForDate(now.Add(time.Hour)).Code {
		t.Fatal("On 0／1 天應對應今天／明天")
	}
	if On(now, -1).Code != ForDate(now.AddDate(0, 0, -1)).Code {
		t.Fatal("On -1 應對應昨天")
	}
	f := Forecast(now, 3)
	if len(f) != 3 || f[0].Code != ForDate(now).Code || f[2].Code != ForDate(now.AddDate(0, 0, 2)).Code {
		t.Fatalf("%v", f)
	}
	// LucksOn 由前幾天的天氣決定
	days := []string{ForDate(now.AddDate(0, 0, -2)).Code, ForDate(now.AddDate(0, 0, -1)).Code, ForDate(now).Code}
	if !slices.Equal(LucksOn(now, 0), LucksFrom(days)) {
		t.Fatal("LucksOn 與 LucksFrom 不一致")
	}
}

func TestFixedLucks(t *testing.T) {
	defer func() { Fixed = "" }()
	for fixed, luck := range map[string]string{Sunny: LuckDrought, Rainy: LuckDrizzle} {
		Fixed = fixed
		if !HasLuck(time.Now(), luck) {
			t.Errorf("固定 %s 時應有 %s", fixed, luck)
		}
	}
	for _, fixed := range []string{Cloudy, Storm} {
		Fixed = fixed
		if len(LucksOn(time.Now(), 0)) != 0 {
			t.Errorf("固定 %s 時不應有奇遇", fixed)
		}
	}
}
