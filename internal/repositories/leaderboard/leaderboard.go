// Package leaderboard 排行榜查詢（對應 Python app/repositories/leaderboard.py）。
//
// 金幣為目前真實餘額（指定期間時為期間內賣出農產品（作物）的收入，不受購買影響）；販售統計取自統計表。
//   - 全部期間：farm_stats（每座農場一列，隨販售在同一筆交易中更新）。
//   - 本日／本週／本月與排名獎勵結算：期間以台灣日期切分，加總 farm_daily_stats。
//   - 期間邊界不是台灣午夜時（非標準期間）才回退為直接加總 game_transactions。
//
// 每位玩家最多一座農場，總排行榜中每位玩家只計算一次。群組總排行：把「主要所屬群組為該群組且仍是成員」的農場加總。
// 同分時依固定的次要條件與 ID 排序，名次穩定。
package leaderboard

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/ranking"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farmstats"
)

// Kinds money：目前金幣（期間內為作物販售收入）；sales：農產品（作物）販售；goods：商品總販售（作物＋動物產品＋加工品）
var Kinds = []string{"money", "sales", "goods"}

func validKind(kind string) error {
	for _, k := range Kinds {
		if k == kind {
			return nil
		}
	}
	return fmt.Errorf("unknown leaderboard kind: %s", kind)
}

// args 累積 SQL 參數，回傳佔位符。
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// txStats 直接由交易紀錄加總（只用於非標準期間）。
func txStats(kind string, since, until *time.Time, a *args) string {
	sold := "t.event_type = 'sell'"
	if kind != "goods" {
		// 原有販售排行只統計作物（含珍稀變種）
		sold += " AND t.item_code IN " + crops.ProduceCodesSelect
	}
	excl := make([]string, len(ranking.NetGoldExcluded))
	for i, e := range ranking.NetGoldExcluded {
		excl[i] = "'" + e + "'"
	}
	var where []string
	if since != nil {
		where = append(where, "t.created_at >= "+a.add(*since))
	}
	if until != nil {
		where = append(where, "t.created_at < "+a.add(*until))
	}
	sql := `(SELECT t.farm_id,
		sum(CASE WHEN ` + sold + ` THEN t.quantity ELSE 0 END) AS qty,
		sum(CASE WHEN ` + sold + ` THEN t.gold_delta ELSE 0 END) AS income,
		sum(CASE WHEN t.event_type NOT IN (` + strings.Join(excl, ", ") + `) THEN t.gold_delta ELSE 0 END) AS net
		FROM game_transactions t`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	return sql + " GROUP BY t.farm_id)"
}

// forceTxStats 測試用：一律直接加總交易紀錄（對應 Python 測試 monkeypatch leaderboard._stats = _tx_stats）。
var forceTxStats bool

// ForceTxStatsForTest 測試用：on 為 true 時排行一律直接加總交易紀錄，用來比對統計表與交易紀錄兩條路徑的結果。
// 回傳還原函式。不可在正式程式中呼叫，也不可與其他查詢同時執行。
func ForceTxStatsForTest(on bool) (restore func()) {
	old := forceTxStats
	forceTxStats = on
	return func() { forceTxStats = old }
}

// stats 各農場的販售數量、販售收入與淨賺金幣（子查詢，欄位 farm_id／qty／income／net）。
func stats(kind string, since, until *time.Time, a *args) string {
	if forceTxStats {
		return txStats(kind, since, until, a)
	}
	prefix := "crop"
	if kind == "goods" {
		prefix = "goods"
	}
	if since == nil && until == nil {
		// 全部期間淨賺金幣 = 目前金幣 - 非經營金幣（交易紀錄 gold_delta 加總 = 金幣）
		return fmt.Sprintf(`(SELECT s.farm_id, s.%s_sold_quantity AS qty, s.%s_sales_income AS income,
			f2.gold - s.non_operating_gold AS net FROM farm_stats s JOIN farms f2 ON f2.id = s.farm_id)`, prefix, prefix)
	}
	if since != nil && until != nil && farmstats.IsDayAligned(since) && farmstats.IsDayAligned(until) {
		first, last := farmstats.PeriodDays(*since, *until)
		d := farmstats.PeriodRowsSQL(a.add(first), a.add(last))
		return fmt.Sprintf("(SELECT d.farm_id, d.%s_qty AS qty, d.%s_income AS income, d.net FROM %s d)", prefix, prefix, d)
	}
	return txStats(kind, since, until, a)
}

// Row 個人排行。
type Row struct {
	Rank         int    `json:"rank"`
	PlayerID     int64  `json:"player_id"`
	PlayerName   string `json:"player_name"`
	FarmName     string `json:"farm_name"`
	Level        int    `json:"level"`
	Gold         int    `json:"gold"`
	SoldQuantity int    `json:"sold_quantity"`
	SalesIncome  int    `json:"sales_income"`
	NetGold      int    `json:"net_gold"`
	Score        int    `json:"score"`
	HomeGroupID  *int64 `json:"home_group_id"`
}

// Ranking 個人排行；groupID 為 nil 時是總排行；since／until 為 nil 表示不限。
func Ranking(ctx context.Context, t *db.Tx, kind string, groupID *int64, limit int, since, until *time.Time) ([]Row, error) {
	if err := validKind(kind); err != nil {
		return nil, err
	}
	var a args
	st := stats(kind, since, until, &a)
	const qty, income, net = "coalesce(st.qty, 0)", "coalesce(st.income, 0)", "coalesce(st.net, 0)"
	var score, order string
	switch {
	case kind == "money" && since != nil:
		// 期間金幣排名只看賣出農產品（作物）賺的錢，買種子、道具、動物等花費不影響排名
		score, order = income, income+" DESC, "+qty+" DESC, f.gold DESC, p.id ASC"
	case kind == "money":
		score, order = "f.gold", "f.gold DESC, "+income+" DESC, p.id ASC"
	case kind == "goods":
		// 商品單價差異大（雞蛋 vs 麵包），總販售依收入排名，再比數量
		score, order = income, income+" DESC, "+qty+" DESC, p.id ASC"
	default:
		score, order = qty, qty+" DESC, "+income+" DESC, p.id ASC"
	}
	sql := "SELECT p.id, p.display_name, f.name, f.level, f.gold, " + qty + ", " + income + ", " + net + ", " + score +
		", f.home_group_id FROM players p JOIN farms f ON f.player_id = p.id LEFT JOIN " + st + " st ON st.farm_id = f.id"
	if groupID != nil {
		// 群組排行：必須是目前成員，且農場的主要所屬群組就是此群組。
		g := a.add(*groupID)
		sql += " JOIN group_members gm ON gm.player_id = p.id WHERE gm.group_id = " + g + " AND f.home_group_id = " + g
	}
	sql += " ORDER BY " + order + " LIMIT " + a.add(limit)
	rows, err := t.Query(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r := Row{Rank: len(out) + 1}
		if err := rows.Scan(&r.PlayerID, &r.PlayerName, &r.FarmName, &r.Level, &r.Gold, &r.SoldQuantity,
			&r.SalesIncome, &r.NetGold, &r.Score, &r.HomeGroupID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GroupRow 群組總排行。
type GroupRow struct {
	Rank         int    `json:"rank"`
	GroupID      int64  `json:"group_id"`
	GroupName    string `json:"group_name"`
	MemberCount  int    `json:"member_count"`
	Gold         int    `json:"gold"`
	SoldQuantity int    `json:"sold_quantity"`
	SalesIncome  int    `json:"sales_income"`
	NetGold      int    `json:"net_gold"`
	Score        int    `json:"score"`
}

// GroupRanking 群組總排行：各群組成員（主要所屬群組為此群組且仍是成員）的金幣、販售加總。沒有成員的群組不列入。
func GroupRanking(ctx context.Context, t *db.Tx, kind string, limit int, since, until *time.Time) ([]GroupRow, error) {
	if err := validKind(kind); err != nil {
		return nil, err
	}
	var a args
	st := stats(kind, since, until, &a)
	const gold, qty, income, net = "coalesce(sum(f.gold), 0)", "coalesce(sum(st.qty), 0)",
		"coalesce(sum(st.income), 0)", "coalesce(sum(st.net), 0)"
	var score, order string
	switch {
	case kind == "money" && since != nil:
		score, order = income, income+" DESC, "+qty+" DESC, "+gold+" DESC, g.id ASC"
	case kind == "money":
		score, order = gold, gold+" DESC, g.id ASC"
	case kind == "goods":
		score, order = income, income+" DESC, "+qty+" DESC, g.id ASC"
	default:
		score, order = qty, qty+" DESC, "+income+" DESC, g.id ASC"
	}
	sql := "SELECT g.id, g.name, count(f.id), " + gold + ", " + qty + ", " + income + ", " + net + ", " + score +
		" FROM groups g JOIN farms f ON f.home_group_id = g.id" +
		" JOIN group_members gm ON gm.player_id = f.player_id AND gm.group_id = g.id" +
		" LEFT JOIN " + st + " st ON st.farm_id = f.id GROUP BY g.id, g.name ORDER BY " + order + " LIMIT " + a.add(limit)
	rows, err := t.Query(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupRow
	for rows.Next() {
		r := GroupRow{Rank: len(out) + 1}
		if err := rows.Scan(&r.GroupID, &r.GroupName, &r.MemberCount, &r.Gold, &r.SoldQuantity, &r.SalesIncome,
			&r.NetGold, &r.Score); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
