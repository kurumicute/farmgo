package services

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/pond"
	"farmgm/internal/game/weather"
)

// addFish 直接放一隻魚進魚簍（沒有釣魚 Session），回傳 ID。
func (e *env) addFish(pid int64, code, rarity string, weight, price int) int64 {
	e.t.Helper()
	return int64(e.scalar(`INSERT INTO farm_fish (farm_id, fish_code, rarity, weight_g, price, caught_at)
		SELECT id, $2, $3, $4, $5, now() FROM farms WHERE player_id = $1 RETURNING id`, pid, code, rarity, weight, price))
}

// 魚塘（2026-11-24）：建造、不能放的魚、史詩魚上限、飼料、成長、池養極品、賣出、繁殖、待辦區。
func TestPond(t *testing.T) {
	e := setup(t)
	old := weather.Fixed
	weather.Fixed = weather.Cloudy // 陰天 ×1，不受測試日期影響
	defer func() { weather.Fixed = old }()
	e.s.PondRng = func() float64 { return 0.99 } // 先不繁殖
	pid := e.player()
	e.setGoldRecorded(pid, 1_000_000)
	e.exec("UPDATE farms SET level = 3 WHERE player_id = $1", pid)
	_, err := e.f.BuildPond(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "Lv.4")

	e.exec("UPDATE farms SET level = 4 WHERE player_id = $1", pid)
	v := must[PondView](t)(e.f.Pond(e.ctx, pid))
	if !v.Unlocked || v.Level != 0 || v.NextPrice != pond.Price(1) {
		t.Fatalf("%+v", v)
	}
	if r := e.ok(e.f.BuildPond(e.ctx, pid)); r.GoldDelta != -pond.Price(1) {
		t.Fatal(r.Message)
	}
	_, err = e.f.BuildPond(e.ctx, pid)
	wantKind(t, err, gameerr.AlreadyExists)

	// 香魚：100–400 g、68–113 金（稀有）
	a := e.addFish(pid, "sweetfish", "rare", 250, 91)
	b := e.addFish(pid, "sweetfish", "rare", 250, 91)
	koi := e.addFish(pid, "golden_koi", "legendary", 5000, 350)
	sword := e.addFish(pid, "swordfish", "epic", 50000, 320)
	s1, s2, s3 := e.addFish(pid, "salmon", "epic", 3000, 124), e.addFish(pid, "salmon", "epic", 3000, 124), e.addFish(pid, "salmon", "epic", 3000, 124)

	_, err = e.f.PutFishInPond(e.ctx, pid, []int64{koi})
	wantMsg(t, err, gameerr.Validation, "傳說魚")
	_, err = e.f.PutFishInPond(e.ctx, pid, []int64{sword})
	wantMsg(t, err, gameerr.Validation, "深海魚")
	_, err = e.f.PutFishInPond(e.ctx, pid, []int64{s1, s2, s3}) // Lv.1 史詩魚最多 2 隻
	wantMsg(t, err, gameerr.Validation, "史詩魚")
	r := e.ok(e.f.PutFishInPond(e.ctx, pid, []int64{a, b}))
	if !strings.Contains(r.Message, "飼料") {
		t.Fatal("沒有飼料時提醒", r.Message)
	}
	basket := must[FishBasket](t)(e.f.FishBasket(e.ctx, pid))
	for _, f := range basket.Fish {
		if f.ID == a || f.ID == b {
			t.Fatal("魚塘裡的魚不在魚簍")
		}
	}
	_, err = e.f.SellFish(e.ctx, pid, []int64{a})
	wantMsg(t, err, gameerr.Validation, "魚簍")

	// 補滿飼料：Lv.1 容量 10 × 4 = 40 份，素材背包有 5 份，另外買 35 份
	e.give(pid, balance.ItemFishFeed, 5)
	r = e.ok(e.f.FillPondFeed(e.ctx, pid))
	if r.GoldDelta != -35*balance.ItemPrice(balance.ItemFishFeed) || e.qty(pid, balance.ItemFishFeed) != 0 {
		t.Fatal(r.Message, e.qty(pid, balance.ItemFishFeed))
	}
	_, err = e.f.FillPondFeed(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "已經滿了")

	// 每 12 小時 +300 × 30% = 90 g：250 → 340 → 400（長滿）
	e.clock.Advance(float64(pond.PeriodHours * 3600))
	v = must[PondView](t)(e.f.Pond(e.ctx, pid))
	if len(v.Fish) != 2 || v.Fish[0].WeightG != 340 || v.Feed != 38 || v.Premiums != 0 {
		t.Fatalf("%+v", v)
	}
	if e.scalar("SELECT weight_g FROM farm_fish WHERE id = $1", a) != 250 {
		t.Fatal("捕獲時的重量不變")
	}
	_, err = e.f.SellPondFish(e.ctx, pid, nil)
	wantMsg(t, err, gameerr.Validation, "還沒有長滿")
	e.clock.Advance(float64(pond.PeriodHours * 3600))
	v = must[PondView](t)(e.f.Pond(e.ctx, pid))
	premium := 192 // 113 × 1.7
	if v.Premiums != 2 || v.PremiumSum != 2*premium || !v.Fish[0].Premium || v.Fish[0].Price != premium {
		t.Fatalf("%+v", v)
	}
	td := must[TodoSummary](t)(e.s.TodoSummary(e.ctx, pid))
	if !td.PondBuilt || td.PondPremiums != 2 || td.PondFish != 2 {
		t.Fatalf("待辦區：%+v", td)
	}
	r = e.ok(e.f.SellPondFish(e.ctx, pid, nil))
	if r.GoldDelta != 2*premium {
		t.Fatal(r.Message)
	}
	if e.scalar("SELECT sold_price FROM farm_fish WHERE id = $1", a) != premium || e.scalar("SELECT price FROM farm_fish WHERE id = $1", a) != 91 {
		t.Fatal("售價寫在 sold_price，捕獲時的價格不變")
	}

	// 繁殖：兩隻長滿的香魚，跨過台灣時間 00:00 時生 1 隻魚苗（不算捕獲數）
	e.s.PondRng = func() float64 { return 0 }
	c, d := e.addFish(pid, "sweetfish", "rare", 400, 113), e.addFish(pid, "sweetfish", "rare", 400, 113)
	e.ok(e.f.PutFishInPond(e.ctx, pid, []int64{c, d}))
	e.clock.Advance(float64(2 * pond.PeriodHours * 3600))
	v = must[PondView](t)(e.f.Pond(e.ctx, pid))
	if v.Settled.Born != 1 || len(v.Fish) != 3 {
		t.Fatalf("%+v", v)
	}
	if e.scalar("SELECT count(*) FROM farm_fish WHERE bred AND pond_weight_g IS NOT NULL AND sold_at IS NULL") != 1 {
		t.Fatal("魚苗")
	}
	if e.scalar("SELECT count(*) FROM farm_fish f JOIN farms fa ON fa.id = f.farm_id WHERE fa.player_id = $1 AND NOT f.bred", pid) != 9 {
		t.Fatal("捕獲數不含魚苗")
	}

	// 升級：容量、史詩魚上限增加
	r = e.ok(e.f.UpgradePond(e.ctx, pid))
	if r.GoldDelta != -pond.Price(2) {
		t.Fatal(r.Message)
	}
	e.ok(e.f.PutFishInPond(e.ctx, pid, []int64{s1, s2, s3})) // Lv.2 史詩魚最多 3 隻
}

// 消耗品當天逐次加價（2026-11-24）：沙漏前 5 個原價，之後每個 +10%，台灣時間 00:00 歸零。
func TestDailyPriceEscalates(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 100_000)
	base := balance.ItemPrice(balance.ItemHourglass)
	if r := e.ok(e.s.BuyItem(e.ctx, pid, balance.ItemHourglass, 5)); r.GoldDelta != -5*base || strings.Contains(r.Message, "加價") {
		t.Fatal(r.Message)
	}
	r := e.ok(e.s.BuyItem(e.ctx, pid, balance.ItemHourglass, 2))
	if want := base*110/100 + base*120/100; r.GoldDelta != -want || !strings.Contains(r.Message, "加價") {
		t.Fatal(r.GoldDelta, want, r.Message)
	}
	prices := must[map[string]DailyPrice](t)(e.s.DailyPrices(e.ctx, pid))
	if p := prices[balance.ItemHourglass]; p.Bought != 7 || p.Next != base*130/100 || !p.Raised() {
		t.Fatalf("%+v", p)
	}
	if _, ok := prices[balance.ItemFishFeed]; ok {
		t.Fatal("魚飼料不加價")
	}
	e.clock.Advance(24 * time.Hour.Seconds())
	prices = must[map[string]DailyPrice](t)(e.s.DailyPrices(e.ctx, pid))
	if p := prices[balance.ItemHourglass]; p.Bought != 0 || p.Next != base {
		t.Fatalf("隔天恢復原價：%+v", p)
	}
}
