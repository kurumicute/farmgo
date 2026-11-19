package services

// 加工品浮動售價（2026-11-19；規則在 game/craftprice）：每 3 天抽一次，範圍是總成本 -50%～+100%。

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/craftprice"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/crops"
)

// craftQuotes 所有加工品在 now 這一期的成本、範圍與售價。
func craftQuotes(ctx context.Context, t *db.Tx, now time.Time) (map[string]craftprice.Quote, error) {
	base := map[string]int{}
	cropList, err := crops.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for _, c := range cropList {
		base[c.Code] = c.SellPrice // 基本售價，不含季節加成，讓成本在一期之內固定
	}
	animalList, err := animals.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for _, a := range animalList {
		base[a.ProductCode] = a.ProductPrice
	}
	for code, g := range balance.Goods {
		if !craftprice.IsCrafted(code) {
			base[code] = g.SellPrice // 菇類等非加工品
		}
	}
	return craftprice.Quotes(base, now), nil
}

// goodPrices 所有可販售商品（balance.Goods）目前的單價：加工品是這一期抽到的價格，菇類維持固定售價。
func goodPrices(ctx context.Context, t *db.Tx, now time.Time) (map[string]int, error) {
	quotes, err := craftQuotes(ctx, t, now)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(balance.Goods))
	for code, g := range balance.Goods {
		out[code] = g.SellPrice
		if q, ok := quotes[code]; ok {
			out[code] = q.Price
		}
	}
	return out, nil
}
