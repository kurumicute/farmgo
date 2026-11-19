package craftprice

import (
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
)

func tpe(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, clock.TaipeiTZ)
}

func TestPeriodChangesEveryThreeTaipeiDays(t *testing.T) {
	start := PeriodEnd(tpe(2026, 11, 19, 12)) // 某一期的開始（台灣 00:00）
	if l := start.In(clock.TaipeiTZ); l.Hour() != 0 || l.Minute() != 0 {
		t.Fatal(l)
	}
	p := Period(start)
	for _, d := range []time.Duration{0, 24 * time.Hour, 71*time.Hour + 59*time.Minute} {
		if Period(start.Add(d)) != p {
			t.Fatalf("+%v 不應換期", d)
		}
	}
	if Period(start.Add(72*time.Hour)) != p+1 || Period(start.Add(-time.Minute)) != p-1 {
		t.Fatal("72 小時後應換期，前一分鐘是上一期")
	}
	if !PeriodEnd(start).Equal(start.Add(72 * time.Hour)) {
		t.Fatal(PeriodEnd(start))
	}
	// 換期看台灣日期：UTC 15:59 是台灣 23:59，UTC 16:00 是台灣隔天 00:00
	end := PeriodEnd(start).UTC()
	if Period(end.Add(-time.Minute)) != p || Period(end) != p+1 {
		t.Fatal("應依台灣時間換期")
	}
}

func TestCostsRecursive(t *testing.T) {
	base := map[string]int{"wheat": 10, "strawberry": 34, "egg": 25, "milk": 60, "wool": 100}
	c := Costs(base)
	want := map[string]int{
		"flour":           30,           // 小麥 ×3
		"bread":           60,           // 麵粉 ×2
		"strawberry_jam":  102,          // 草莓 ×3
		"cake":            60 + 50 + 60, // 麵粉 ×2＋雞蛋 ×2＋牛奶
		"strawberry_cake": 170 + 102,    // 蛋糕＋草莓果醬
		"lucky_sachet":    100,          // 藥草不能賣（0）＋羊毛
		"shiitake_bread":  0*2 + 30,     // base 沒給香菇價格 → 0
		"pet_treat":       30 + 25 + 60,
	}
	for code, w := range want {
		if c[code] != w {
			t.Errorf("%s 成本 %d，預期 %d", code, c[code], w)
		}
	}
	if len(c) != len(balance.Recipes) {
		t.Fatal(len(c), len(balance.Recipes))
	}
}

func TestPriceWithinRangeAndStable(t *testing.T) {
	if lo, hi := Range(102); lo != 51 || hi != 204 { // 草莓果醬：成本 102 → 51～204
		t.Fatal(lo, hi)
	}
	if lo, hi := Range(1); lo != 1 || hi != 2 { // 下限至少 1 金
		t.Fatal(lo, hi)
	}
	seen := map[int]bool{}
	for period := int64(0); period < 200; period++ {
		p := Price("bread", 200, period)
		if p < 100 || p > 400 {
			t.Fatal(period, p)
		}
		if Price("bread", 200, period) != p {
			t.Fatal("同一期價格應固定")
		}
		seen[p] = true
	}
	if len(seen) < 50 {
		t.Fatal("價格應該會變動", len(seen))
	}
}

func TestQuotes(t *testing.T) {
	base := map[string]int{"wheat": 10}
	q := Quotes(base, tpe(2026, 11, 19, 12))
	if f := q["flour"]; f.Cost != 30 || f.Low != 15 || f.High != 60 || f.Price < 15 || f.Price > 60 {
		t.Fatalf("%+v", f)
	}
	// 原料都沒有價格（成本 0）時沿用內建售價
	if j := q["grape_jam"]; j.Cost != 0 || j.Price != balance.Goods["grape_jam"].SellPrice || j.Low != j.High {
		t.Fatalf("%+v", j)
	}
	if _, ok := q["shiitake"]; ok || IsCrafted("shiitake") || !IsCrafted("bread") {
		t.Fatal("菇類不是加工品")
	}
}
