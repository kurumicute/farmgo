package services

// 消耗品當天逐次加價（Go 版新增，2026-11-24，規則見 game/dailyprice）。「今天買了幾個」從交易紀錄（buy_item）算，
// 台灣時間 00:00 歸零，不需要 Migration。查詢走 ix_game_transactions_farm_created（farm_id, created_at）。

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/dailyprice"
	"farmgm/internal/game/rules"
)

// boughtToday 今天在道具商店買了幾個這種道具。
func boughtToday(ctx context.Context, t *db.Tx, farmID int64, code string, now time.Time) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `SELECT coalesce(sum(quantity), 0)::int FROM game_transactions
		WHERE farm_id = $1 AND created_at >= $2 AND event_type = $3 AND item_code = $4`,
		farmID, taipeiDayStart(now), string(rules.EvBuyItem), code)
	return n, err
}

// DailyPrice 一種會逐次加價的道具今天的價格。
type DailyPrice struct {
	Code   string
	Base   int // 原價
	Bought int // 今天已經買了幾個
	Free   int // 每天用原價的數量
	Next   int // 下一個的單價
}

// Raised 下一個已經開始加價。
func (d DailyPrice) Raised() bool { return d.Next > d.Base }

// Total 再買 qty 個的總價。
func (d DailyPrice) Total(qty int) int { return dailyprice.Total(d.Code, d.Base, d.Bought, qty) }

// Affordable 持有 gold 金幣時最多還能買幾個（不超過 limit）。
func (d DailyPrice) Affordable(gold, limit int) int {
	return dailyprice.Affordable(d.Code, d.Base, d.Bought, gold, limit)
}

// DailyPrices 會逐次加價的道具今天的價格（沒有加價的道具不在結果裡）。只讀取。
func (s *GameService) DailyPrices(ctx context.Context, playerID int64) (map[string]DailyPrice, error) {
	now := s.now()
	return inTx(ctx, s, func(t *db.Tx) (map[string]DailyPrice, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		var codes []string
		for _, code := range dailyprice.Codes() {
			if dailyprice.Applies(code) {
				codes = append(codes, code)
			}
		}
		out := map[string]DailyPrice{}
		if len(codes) == 0 {
			return out, nil
		}
		rows, err := t.Query(ctx, `SELECT item_code, coalesce(sum(quantity), 0)::int FROM game_transactions
			WHERE farm_id = $1 AND created_at >= $2 AND event_type = $3 AND item_code = ANY($4) GROUP BY item_code`,
			farm.ID, taipeiDayStart(now), string(rules.EvBuyItem), codes)
		if err != nil {
			return nil, err
		}
		bought := map[string]int{}
		for rows.Next() {
			var code string
			var n int
			if err := rows.Scan(&code, &n); err != nil {
				rows.Close()
				return nil, err
			}
			bought[code] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, code := range codes {
			base := balance.ItemPrice(code)
			out[code] = DailyPrice{Code: code, Base: base, Bought: bought[code], Free: dailyprice.FreeOf(code),
				Next: dailyprice.Next(code, base, bought[code])}
		}
		return out, nil
	})
}
