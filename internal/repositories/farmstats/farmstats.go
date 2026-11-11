// Package farmstats 農場累積統計（farm_stats）與每日統計（farm_daily_stats）（對應 Python app/repositories/farm_stats.py）。
//
//   - OnTransaction：由 GameService._record 在寫入交易紀錄的同一筆資料庫交易中呼叫，以原子遞增更新。
//   - Rebuild／Check：由 game_transactions 重算，或比對目前統計與重算結果。
//
// 日期以台灣時間（UTC+8）切分，與排行榜的本日／本週／本月期間一致。
package farmstats

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/ranking"
	"farmgm/internal/repositories/transactions"
)

var StatColumns = []string{"crop_sold_quantity", "crop_sales_income", "goods_sold_quantity", "goods_sales_income"}

// CountColumns 只累計在 farm_stats 的次數（成就進度用）：交易類型 → 欄位
var CountColumns = map[string]string{"steal": "steal_count", "defended": "defended_count", "gamble": "gamble_count"}

// countOrder CountColumns 的順序（Python dict 順序）。
var countOrder = []string{"steal", "defended", "gamble"}

// TaipeiDay 台灣日期（台灣時間 00:00）。
func TaipeiDay(at time.Time) time.Time {
	l := at.In(clock.TaipeiTZ)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
}

// IsCropProduce 是否為作物（含珍稀變種）；itemCode 為 nil 時 false。
func IsCropProduce(ctx context.Context, t *db.Tx, itemCode *string) (bool, error) {
	if itemCode == nil {
		return false, nil
	}
	_, ok, err := db.QueryValue[int64](ctx, t,
		"SELECT id FROM crop_types WHERE code = $1 OR variant_code = $1 LIMIT 1", *itemCode)
	return ok, err
}

// upsert 以 keys 為衝突鍵遞增 values 中非 0 的欄位（player_id 不屬於衝突鍵）。用 ExecNoFlush（Python core_execute）。
func upsert(ctx context.Context, t *db.Tx, table string, keys, values []db.Col) error {
	var nonzero []db.Col
	for _, v := range values {
		if v.Value.(int) != 0 {
			nonzero = append(nonzero, v)
		}
	}
	if len(nonzero) == 0 {
		return nil
	}
	sql, args := db.InsertSQL(table, append(slices.Clone(keys), nonzero...), "")
	var conflict, set []string
	for _, k := range keys {
		if k.Name != "player_id" {
			conflict = append(conflict, k.Name)
		}
	}
	for _, v := range nonzero {
		set = append(set, fmt.Sprintf("%s = %s.%s + excluded.%s", v.Name, table, v.Name, v.Name))
	}
	sql += " ON CONFLICT (" + strings.Join(conflict, ", ") + ") DO UPDATE SET " + strings.Join(set, ", ")
	_, err := t.ExecNoFlush(ctx, sql, args...)
	return err
}

// OnTransaction 依一筆交易紀錄遞增統計；與交易紀錄在同一筆資料庫交易中提交或回滾。
func OnTransaction(ctx context.Context, t *db.Tx, farm *models.Farm, tx transactions.TxRecord) error {
	isSell := tx.EventType == "sell"
	nonOperating := slices.Contains(ranking.NetGoldExcluded, tx.EventType)
	counter := CountColumns[tx.EventType]
	if !isSell && tx.GoldDelta == 0 && counter == "" {
		return nil
	}
	var sales []db.Col
	if isSell {
		sales = []db.Col{db.C("goods_sold_quantity", tx.Quantity), db.C("goods_sales_income", tx.GoldDelta)}
		crop, err := IsCropProduce(ctx, t, tx.ItemCode)
		if err != nil {
			return err
		}
		if crop {
			sales = append(sales, db.C("crop_sold_quantity", tx.Quantity), db.C("crop_sales_income", tx.GoldDelta))
		}
	}
	totals := slices.Clone(sales)
	if counter != "" {
		totals = append(totals, db.C(counter, 1))
	}
	if nonOperating {
		totals = append(totals, db.C("non_operating_gold", tx.GoldDelta))
	}
	if err := upsert(ctx, t, models.TableFarmStats,
		[]db.Col{db.C("farm_id", farm.ID), db.C("player_id", farm.PlayerID)}, totals); err != nil {
		return err
	}
	daily := slices.Clone(sales)
	if !nonOperating {
		daily = append(daily, db.C("net_gold", tx.GoldDelta))
	}
	return upsert(ctx, t, models.TableFarmDailyStats,
		[]db.Col{db.C("farm_id", farm.ID), db.C("day", db.DateParam(TaipeiDay(tx.CreatedAt)))}, daily)
}

// EnsureRow 建立農場時先建立累積統計列，讓排行查詢可直接以內部連結取得。
func EnsureRow(ctx context.Context, t *db.Tx, farm *models.Farm) error {
	_, err := t.Exec(ctx, "INSERT INTO farm_stats (farm_id, player_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		farm.ID, farm.PlayerID)
	return err
}

// ---------- 由交易紀錄重算 ----------

const cropCodes = "(SELECT code FROM crop_types UNION SELECT variant_code FROM crop_types)"

func excluded() string {
	parts := make([]string, len(ranking.NetGoldExcluded))
	for i, e := range ranking.NetGoldExcluded {
		parts[i] = "'" + e + "'" // 程式內常數（不是使用者輸入）
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

var cropSell = "t.event_type = 'sell' AND t.item_code IN " + cropCodes

// ExpectedTotalsSQL 由交易紀錄重算 farm_stats。
var ExpectedTotalsSQL = `
SELECT f.id AS farm_id, f.player_id,
       coalesce(sum(t.quantity) FILTER (WHERE ` + cropSell + `), 0) AS crop_sold_quantity,
       coalesce(sum(t.gold_delta) FILTER (WHERE ` + cropSell + `), 0) AS crop_sales_income,
       coalesce(sum(t.quantity) FILTER (WHERE t.event_type = 'sell'), 0) AS goods_sold_quantity,
       coalesce(sum(t.gold_delta) FILTER (WHERE t.event_type = 'sell'), 0) AS goods_sales_income,
       coalesce(sum(t.gold_delta) FILTER (WHERE t.event_type IN ` + excluded() + `), 0) AS non_operating_gold,
       count(t.id) FILTER (WHERE t.event_type = 'steal') AS steal_count,
       count(t.id) FILTER (WHERE t.event_type = 'defended') AS defended_count,
       count(t.id) FILTER (WHERE t.event_type = 'gamble') AS gamble_count
FROM farms f
LEFT JOIN game_transactions t ON t.farm_id = f.id
GROUP BY f.id, f.player_id
`

// ExpectedDailySQL 由交易紀錄重算 farm_daily_stats。
var ExpectedDailySQL = `
SELECT t.farm_id, ((t.created_at AT TIME ZONE 'UTC') + interval '8 hours')::date AS day,
       coalesce(sum(t.quantity) FILTER (WHERE ` + cropSell + `), 0) AS crop_sold_quantity,
       coalesce(sum(t.gold_delta) FILTER (WHERE ` + cropSell + `), 0) AS crop_sales_income,
       coalesce(sum(t.quantity) FILTER (WHERE t.event_type = 'sell'), 0) AS goods_sold_quantity,
       coalesce(sum(t.gold_delta) FILTER (WHERE t.event_type = 'sell'), 0) AS goods_sales_income,
       coalesce(sum(t.gold_delta) FILTER (WHERE t.event_type NOT IN ` + excluded() + `), 0) AS net_gold
FROM game_transactions t
WHERE t.event_type = 'sell' OR (t.gold_delta <> 0 AND t.event_type NOT IN ` + excluded() + `)
GROUP BY 1, 2
`

var (
	TotalColumns = append(append(slices.Clone(StatColumns), "non_operating_gold"), countValues()...)
	DailyColumns = append(slices.Clone(StatColumns), "net_gold")
)

func countValues() []string {
	out := make([]string, len(countOrder))
	for i, k := range countOrder {
		out[i] = CountColumns[k]
	}
	return out
}

// RebuildResult 重算寫入的列數。
type RebuildResult struct {
	FarmStats      int64 `json:"farm_stats"`
	FarmDailyStats int64 `json:"farm_daily_stats"`
}

// Rebuild 以 game_transactions 重算全部統計（呼叫端負責交易；會鎖定兩張統計表避免同時更新）。
func Rebuild(ctx context.Context, t *db.Tx) (RebuildResult, error) {
	var r RebuildResult
	for _, sql := range []string{
		"LOCK TABLE farm_stats, farm_daily_stats IN EXCLUSIVE MODE", "DELETE FROM farm_daily_stats", "DELETE FROM farm_stats",
	} {
		if _, err := t.Exec(ctx, sql); err != nil {
			return r, err
		}
	}
	cols := strings.Join(append([]string{"farm_id", "player_id"}, TotalColumns...), ", ")
	tag, err := t.Exec(ctx, "INSERT INTO farm_stats ("+cols+") "+ExpectedTotalsSQL)
	if err != nil {
		return r, err
	}
	r.FarmStats = tag.RowsAffected()
	cols = strings.Join(append([]string{"farm_id", "day"}, DailyColumns...), ", ")
	tag, err = t.Exec(ctx, "INSERT INTO farm_daily_stats ("+cols+") "+ExpectedDailySQL)
	if err != nil {
		return r, err
	}
	r.FarmDailyStats = tag.RowsAffected()
	return r, nil
}

// CheckResult 不一致的筆數與前幾筆差異（每筆是查詢結果的原始欄位值）。
type CheckResult struct {
	FarmStatsMismatches      int     `json:"farm_stats_mismatches"`
	FarmDailyStatsMismatches int     `json:"farm_daily_stats_mismatches"`
	Examples                 [][]any `json:"examples"`
}

func allValues(ctx context.Context, t *db.Tx, sql string) ([][]any, error) {
	rows, err := t.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Check 比對統計表與交易紀錄重算結果（limit 預設 20）。
func Check(ctx context.Context, t *db.Tx, limit int) (CheckResult, error) {
	var diffs, pairs []string
	for _, c := range TotalColumns {
		diffs = append(diffs, fmt.Sprintf("e.%s IS DISTINCT FROM s.%s", c, c))
		pairs = append(pairs, fmt.Sprintf("e.%s, s.%s", c, c))
	}
	totals, err := allValues(ctx, t, "SELECT e.farm_id, "+strings.Join(pairs, ", ")+
		" FROM ("+ExpectedTotalsSQL+") e LEFT JOIN farm_stats s ON s.farm_id = e.farm_id"+
		" WHERE s.farm_id IS NULL OR "+strings.Join(diffs, " OR "))
	if err != nil {
		return CheckResult{}, err
	}
	diffs = diffs[:0]
	for _, c := range DailyColumns {
		diffs = append(diffs, fmt.Sprintf("coalesce(e.%s, 0) <> coalesce(s.%s, 0)", c, c))
	}
	daily, err := allValues(ctx, t, "SELECT coalesce(e.farm_id, s.farm_id), coalesce(e.day, s.day)"+
		" FROM ("+ExpectedDailySQL+") e FULL JOIN farm_daily_stats s ON s.farm_id = e.farm_id AND s.day = e.day"+
		" WHERE "+strings.Join(diffs, " OR "))
	if err != nil {
		return CheckResult{}, err
	}
	res := CheckResult{FarmStatsMismatches: len(totals), FarmDailyStatsMismatches: len(daily), Examples: [][]any{}}
	res.Examples = append(res.Examples, totals[:min(limit, len(totals))]...)
	res.Examples = append(res.Examples, daily[:min(limit, len(daily))]...)
	return res, nil
}

// PeriodRowsSQL [since, until) 的台灣日期範圍內，各農場的統計加總（子查詢 SQL，欄位 farm_id／crop_qty／crop_income／
// goods_qty／goods_income／net）。firstParam／lastParam 為 DATE 參數位置，值用 PeriodDays 計算。
func PeriodRowsSQL(firstParam, lastParam string) string {
	return `(SELECT farm_id, sum(crop_sold_quantity) AS crop_qty, sum(crop_sales_income) AS crop_income,
		sum(goods_sold_quantity) AS goods_qty, sum(goods_sales_income) AS goods_income, sum(net_gold) AS net
		FROM farm_daily_stats WHERE day >= ` + firstParam + ` AND day < ` + lastParam + ` GROUP BY farm_id)`
}

// PeriodDays PeriodRowsSQL 的 DATE 參數值（台灣日期）。
func PeriodDays(since, until time.Time) (first, last time.Time) {
	return db.DateParam(TaipeiDay(since)), db.DateParam(TaipeiDay(until))
}

// IsDayAligned 是否為台灣午夜（nil 視為對齊）。
func IsDayAligned(at *time.Time) bool {
	if at == nil {
		return true
	}
	l := at.In(clock.TaipeiTZ)
	return l.Hour() == 0 && l.Minute() == 0 && l.Second() == 0 && l.Nanosecond() == 0
}
