// Package news 農場日報：產生日報用的當日彙總查詢（只讀取交易紀錄與魚獲，不寫入），對應 Python app/repositories/news.py。
package news

import (
	"context"
	"time"

	"farmgm/internal/db"
)

// 日報挑選的突變／品質（快速生長不算稀奇，不上頭條）
var (
	NewsMutations    = []string{"giant", "golden", "variant"}
	NewsQualities    = []string{"legendary"}
	NewsFishRarities = []string{"epic", "legendary"}
)

// FarmEvent (farm_id, event_type, 筆數, 數量合計, 金幣合計)。
type FarmEvent struct {
	FarmID    int64  `db:"farm_id"`
	EventType string `db:"event_type"`
	Count     int    `db:"count"`
	Quantity  int    `db:"quantity"`
	Gold      int    `db:"gold"`
}

// FarmEvents 所屬群組為 groupID 的農場在期間內的交易紀錄彙總。
func FarmEvents(ctx context.Context, t *db.Tx, groupID int64, start, end time.Time) ([]FarmEvent, error) {
	return db.QueryStructs[FarmEvent](ctx, t,
		"SELECT g.farm_id, g.event_type, count(*) AS count, coalesce(sum(g.quantity), 0)::bigint AS quantity, "+
			"coalesce(sum(g.gold_delta), 0)::bigint AS gold FROM game_transactions g JOIN farms f ON f.id = g.farm_id "+
			"WHERE f.home_group_id = $1 AND g.created_at >= $2 AND g.created_at < $3 GROUP BY g.farm_id, g.event_type",
		groupID, start, end)
}

// SpecialHarvest (farm_id, item_code, quality, mutation, 數量)。
type SpecialHarvest struct {
	FarmID   int64   `db:"farm_id"`
	ItemCode *string `db:"item_code"`
	Quality  *string `db:"quality"`
	Mutation *string `db:"mutation"`
	Quantity int     `db:"quantity"`
}

// SpecialHarvests 期間內收成的巨型／黃金／珍稀變種或傳說品質作物，依數量由多到少。
func SpecialHarvests(ctx context.Context, t *db.Tx, groupID int64, start, end time.Time) ([]SpecialHarvest, error) {
	return db.QueryStructs[SpecialHarvest](ctx, t,
		"SELECT g.farm_id, g.item_code, g.quality, g.mutation, sum(g.quantity)::bigint AS quantity "+
			"FROM game_transactions g JOIN farms f ON f.id = g.farm_id "+
			"WHERE f.home_group_id = $1 AND g.created_at >= $2 AND g.created_at < $3 AND g.event_type = 'harvest' "+
			"AND (g.mutation = ANY($4) OR g.quality = ANY($5)) "+
			"GROUP BY g.farm_id, g.item_code, g.quality, g.mutation ORDER BY sum(g.quantity) DESC",
		groupID, start, end, NewsMutations, NewsQualities)
}

// BestFish (farm_id, 魚名, 稀有度, 重量 g)。
type BestFish struct {
	FarmID  int64  `db:"farm_id"`
	Name    string `db:"name"`
	Rarity  string `db:"rarity"`
	WeightG int    `db:"weight_g"`
}

// BestFishOf 期間內釣到最有價值的史詩／傳說魚；沒有時回傳 nil（Python 名稱 best_fish）。
func BestFishOf(ctx context.Context, t *db.Tx, groupID int64, start, end time.Time) (*BestFish, error) {
	return db.QueryStruct[BestFish](ctx, t,
		"SELECT ff.farm_id, ft.name, ff.rarity, ff.weight_g FROM farm_fish ff JOIN farms f ON f.id = ff.farm_id "+
			"JOIN fish_types ft ON ft.code = ff.fish_code WHERE f.home_group_id = $1 AND ff.caught_at >= $2 "+
			"AND ff.caught_at < $3 AND ff.rarity = ANY($4) ORDER BY ff.price DESC, ff.weight_g DESC, ff.id LIMIT 1",
		groupID, start, end, NewsFishRarities)
}

// FarmNames 農場 ID → 名稱。
func FarmNames(ctx context.Context, t *db.Tx, farmIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(farmIDs) == 0 {
		return out, nil
	}
	rows, err := t.Query(ctx, "SELECT id, name FROM farms WHERE id = ANY($1)", farmIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
