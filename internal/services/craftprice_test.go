package services

import (
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/craftprice"
)

// goodPrice 商品在目前這一期的單價（加工品浮動，菇類固定）。
func (e *env) goodPrice(code string) int {
	e.t.Helper()
	prices := must[map[string]int](e.t)(inTx(e.ctx, e.s, func(t *db.Tx) (map[string]int, error) {
		return goodPrices(e.ctx, t, e.s.now())
	}))
	return prices[code]
}

// 加工品售價（2026-11-19）：在總成本 -50%～+100% 之間，同一期固定、換期重抽；販售、背包與售價表用同一個價格。
func TestCraftedGoodsFloatingPrice(t *testing.T) {
	e := setup(t)
	f := e.player()
	quotes := must[map[string]craftprice.Quote](t)(inTx(e.ctx, e.s, func(tx *db.Tx) (map[string]craftprice.Quote, error) {
		return craftQuotes(e.ctx, tx, e.s.now())
	}))
	q := quotes["flour"]
	if q.Cost <= 0 || q.Low != q.Cost/2 || q.High != q.Cost*2 || q.Price < q.Low || q.Price > q.High {
		t.Fatalf("%+v", q)
	}
	if e.goodPrice("flour") != q.Price || e.goodPrice("shiitake") != balance.Goods["shiitake"].SellPrice {
		t.Fatal("加工品用浮動價格，菇類維持固定")
	}
	recipes := must[[]RecipeView](t)(e.r.Recipes(e.ctx, &f))
	for _, r := range recipes {
		if r.Code == "flour" && (*r.SellPrice != q.Price || r.PriceLow != q.Low || r.PriceHigh != q.High || r.PriceUntil.IsZero()) {
			t.Fatalf("%+v", r)
		}
	}
	e.give(f, "flour", 1)
	if r := e.ok(e.r.SellGoods(e.ctx, f, "麵粉", 1)); r.GoldDelta != q.Price {
		t.Fatal(r.GoldDelta, q.Price)
	}
	// 同一期內價格不變；換期後重新抽（仍在範圍內）
	e.clock.Advance(3600)
	if e.goodPrice("flour") != q.Price {
		t.Fatal("同一期價格應固定")
	}
	e.clock.Set(craftprice.PeriodEnd(e.clock.Now()))
	if p := e.goodPrice("flour"); p < q.Low || p > q.High || p != craftprice.Price("flour", q.Cost, craftprice.Period(e.clock.Now())) {
		t.Fatal(p)
	}
}
