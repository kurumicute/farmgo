package pond

import (
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/weather"
)

// sweetfish 香魚：100–400 g、68–113 金（稀有）。
func sweetfish(id int64, w int) Fish {
	return Fish{ID: id, Code: "sweetfish", Rarity: "rare", MinG: 100, MaxG: 400, MinPrice: 68, MaxPrice: 113, WeightG: w}
}

func fixedWeather(t *testing.T, code string) {
	t.Helper()
	old, oldLucks := weather.Fixed, weather.ForceLucks
	weather.Fixed, weather.ForceLucks = code, []string{}
	t.Cleanup(func() { weather.Fixed, weather.ForceLucks = old, oldLucks })
}

func never() float64  { return 0.99 }
func always() float64 { return 0 }

func TestDefaults(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	if UnlockLevel() != 4 || MaxLevel() != 5 || Price(1) != 20_000 || Price(5) != 400_000 || Price(6) != 0 {
		t.Fatal("價格")
	}
	for _, x := range []struct{ level, cap, epic, premium int }{{1, 10, 2, 170}, {2, 14, 3, 190}, {3, 18, 4, 220}, {4, 24, 6, 260}, {5, 30, 7, 300}, {9, 30, 7, 300}} {
		if Capacity(x.level) != x.cap || EpicLimit(x.level) != x.epic || PremiumPct(x.level) != x.premium {
			t.Errorf("Lv.%d：容量 %d、史詩 %d、極品 %d", x.level, Capacity(x.level), EpicLimit(x.level), PremiumPct(x.level))
		}
	}
	if FeedCapacity(1) != 40 {
		t.Fatal(FeedCapacity(1))
	}
}

func TestPrices(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	f := sweetfish(1, 250) // 一半：68 + 45 × 0.5 = 90.5 → 91（Python round 為 90）
	if p, premium := f.Price(1, now); premium || p != f.NormalPrice() {
		t.Fatal("沒長滿：照重量算", p, premium)
	}
	f.WeightG = 400
	f.PlacedAt = now.Add(-Period + time.Minute)
	if _, premium := f.Price(1, now); premium {
		t.Fatal("剛放進來未滿 1 個週期：不是極品")
	}
	f.PlacedAt = now.Add(-Period)
	if p, premium := f.Price(1, now); !premium || p != 192 { // 113 × 1.7 = 192.1
		t.Fatal("Lv.1 極品", p, premium)
	}
	if p, _ := f.Price(5, now); p != 339 { // 113 × 3
		t.Fatal("Lv.5 極品", p)
	}
	if f.Progress() != 100 || sweetfish(2, 250).Progress() != 50 {
		t.Fatal("進度")
	}
}

func TestPlaceError(t *testing.T) {
	if PlaceError("rare", false) != "" || PlaceError("epic", false) != "" || PlaceError("common", false) != "" {
		t.Fatal("普通、稀有、史詩可以放")
	}
	if PlaceError("legendary", false) == "" || PlaceError("epic", true) == "" {
		t.Fatal("傳說魚、深海魚不能放")
	}
}

func TestSettleGrowthFeedAndWeather(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	fixedWeather(t, weather.Sunny)
	start := time.Date(2026, 11, 24, 1, 0, 0, 0, clock.TaipeiTZ) // 台灣 01:00，兩個週期都不跨午夜
	s := State{Level: 1, Feed: 3, Last: start, Placed: map[int64]time.Time{}}
	// 便宜的魚（比較輕）和貴的魚：飼料只夠 3 份，第 2 個週期先餵貴的
	fish := []Fish{sweetfish(1, 100), sweetfish(2, 300)}
	res := Settle(&s, &fish, start.Add(2*Period+time.Hour), never)
	if res.Periods != 2 || res.FeedUsed != 3 || res.Hungry != 1 || s.Feed != 0 {
		t.Fatalf("%+v，剩 %d", res, s.Feed)
	}
	// 每週期 300 × 30% = 90 g
	if fish[1].WeightG != 400 || fish[0].WeightG != 190 {
		t.Fatalf("重量：%d、%d", fish[0].WeightG, fish[1].WeightG)
	}
	if !s.Last.Equal(start.Add(2 * Period)) {
		t.Fatal("上次結算時間只推到最後一個完整週期", s.Last)
	}
	// 雨天 ×1.5、暴風不長
	fixedWeather(t, weather.Rainy)
	s.Feed = 10
	fish = []Fish{sweetfish(1, 100)}
	Settle(&s, &fish, s.Last.Add(Period), never)
	if fish[0].WeightG != 235 {
		t.Fatal("雨天", fish[0].WeightG)
	}
	fixedWeather(t, weather.Storm)
	Settle(&s, &fish, s.Last.Add(Period), never)
	if fish[0].WeightG != 235 || s.Feed != 8 {
		t.Fatal("暴風天不長，但還是要吃", fish[0].WeightG, s.Feed)
	}
	// Lv.5：+80% → 每週期 162 g
	s.Level = 5
	fixedWeather(t, weather.Sunny)
	Settle(&s, &fish, s.Last.Add(Period), never)
	if fish[0].WeightG != 397 {
		t.Fatal("Lv.5", fish[0].WeightG)
	}
}

func TestSettleCatchupLimit(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	fixedWeather(t, weather.Cloudy)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s := State{Level: 1, Feed: 1_000, Last: now.Add(-100 * 24 * time.Hour), Placed: map[int64]time.Time{}}
	fish := []Fish{}
	res := Settle(&s, &fish, now, never)
	if res.Periods != 28 || !s.Last.Equal(now) {
		t.Fatal("最多回推 14 天（28 個週期）", res.Periods, s.Last)
	}
	// 還沒建造：不結算
	empty := State{}
	if r := Settle(&empty, &fish, now, never); r.Periods != 0 {
		t.Fatal("沒有魚塘")
	}
}

func TestBreed(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	fixedWeather(t, weather.Cloudy)
	// 台灣 22:00 起算，第 1 個週期（22:00 → 10:00）跨過午夜
	start := time.Date(2026, 11, 24, 22, 0, 0, 0, clock.TaipeiTZ)
	fresh := func() (State, []Fish) {
		return State{Level: 1, Feed: 100, Last: start, Placed: map[int64]time.Time{}},
			[]Fish{sweetfish(1, 300), sweetfish(2, 300)}
	}
	s, fish := fresh()
	res := Settle(&s, &fish, start.Add(Period), always)
	if res.Born != 1 || len(fish) != 3 || fish[2].ID != 0 || fish[2].WeightG != 100 || s.Born != 1 || s.BredDay != "2026-11-25" {
		t.Fatalf("應該生 1 隻魚苗：%+v %+v", res, fish)
	}
	if !fish[2].PlacedAt.Equal(start.Add(Period)) {
		t.Fatal("魚苗的出生時間", fish[2].PlacedAt)
	}
	s, fish = fresh()
	if res := Settle(&s, &fish, start.Add(Period), never); res.Born != 0 {
		t.Fatal("擲骰沒中")
	}
	// 只有 1 隻、太輕、沒吃到飼料：都不生
	s, fish = fresh()
	fish = fish[:1]
	if Settle(&s, &fish, start.Add(Period), always).Born != 0 {
		t.Fatal("只有 1 隻")
	}
	s, fish = fresh()
	fish[0].WeightG, fish[1].WeightG = 100, 100 // 吃完這一週期 190 g，低於 60%（280 g）
	if Settle(&s, &fish, start.Add(Period), always).Born != 0 {
		t.Fatal("太輕")
	}
	s, fish = fresh()
	s.Feed = 1
	if Settle(&s, &fish, start.Add(Period), always).Born != 0 {
		t.Fatal("有 1 隻沒吃到飼料")
	}
	// 魚塘滿了不生
	s, fish = fresh()
	for i := int64(3); i <= 10; i++ {
		fish = append(fish, Fish{ID: i, Code: "loach", Rarity: "common", MinG: 30, MaxG: 200, MinPrice: 5, MaxPrice: 23, WeightG: 30})
	}
	if Settle(&s, &fish, start.Add(Period), always).Born != 0 {
		t.Fatal("滿了")
	}
	// 史詩魚名額滿了（Lv.1 2 隻）不生
	epic := func(id int64) Fish {
		return Fish{ID: id, Code: "salmon", Rarity: "epic", MinG: 2000, MaxG: 10000, MinPrice: 116, MaxPrice: 180, WeightG: 9000}
	}
	s, _ = fresh()
	fish = []Fish{epic(1), epic(2)}
	if Settle(&s, &fish, start.Add(Period), always).Born != 0 {
		t.Fatal("史詩魚名額滿了")
	}
	// 同一天已經生過：不再生
	s, fish = fresh()
	s.BredDay, s.Born = "2026-11-25", 1
	if Settle(&s, &fish, start.Add(Period), always).Born != 0 {
		t.Fatal("每天最多 1 隻")
	}
}

func TestStateRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 24, 3, 4, 5, 0, time.UTC)
	s := State{Level: 3, Feed: 17, Last: at, Placed: map[int64]time.Time{42: at}, BredDay: "2026-11-24", Born: 1}
	// 模擬 JSON 來回（數字變成 float64）
	raw := map[string]any{}
	for k, v := range s.Encode() {
		switch x := v.(type) {
		case int:
			raw[k] = float64(x)
		default:
			raw[k] = x
		}
	}
	got := Parse(raw)
	if got.Level != 3 || got.Feed != 17 || !got.Last.Equal(at) || !got.Placed[42].Equal(at) || got.BredDay != "2026-11-24" || got.Born != 1 {
		t.Fatalf("%+v", got)
	}
	if Parse(map[string]any{}).Built() || Parse(map[string]any{"level": float64(99)}).Built() {
		t.Fatal("格式錯誤時當成還沒建造")
	}
	fish := []Fish{{ID: 42}, {ID: 7}}
	got.Attach(fish)
	if !fish[0].PlacedAt.Equal(at) || !fish[1].PlacedAt.IsZero() {
		t.Fatal("Attach")
	}
}

func TestValidate(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	raw, _ := featurecfg.DeepCopy(defaults())
	raw["prices"] = []any{1000, 2000}
	if _, err := featurecfg.Validate(ConfigKey, raw); err == nil {
		t.Fatal("各級數值的數量要和價格一樣")
	}
	raw["capacities"], raw["grow_bonus_pct"], raw["premium_pct"] = []any{5, 8}, []any{0, 10}, []any{150, 200}
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if MaxLevel() != 2 || Capacity(2) != 8 || Capacity(5) != 8 {
		t.Fatal("降低級數：超過的以最高等級計算")
	}
	raw["capacities"] = []any{8, 5}
	if _, err := featurecfg.Validate(ConfigKey, raw); err == nil {
		t.Fatal("容量不能變小")
	}
	raw["capacities"], raw["premium_pct"] = []any{5, 8}, []any{50, 200}
	if _, err := featurecfg.Validate(ConfigKey, raw); err == nil {
		t.Fatal("極品倍率不能低於 100%")
	}
}
