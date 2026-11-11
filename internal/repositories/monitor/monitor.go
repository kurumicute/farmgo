// Package monitor 監控儀表板使用的唯讀彙總查詢（對應 Python app/repositories/monitor.py）。
// 回傳給網站的資料用帶 json tag 的 struct，欄位名稱與 Python dict 的 key 相同。
package monitor

import (
	"context"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/repositories/inventory"
)

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// plotStateCounts 依時間即時結算土地狀態的計數欄位（與 rules.PlotStateAt 相同的規則）；now、witherCutoff 的參數位置由呼叫端指定。
func plotStateCounts(now, cutoff string) string {
	return `count(*) FILTER (WHERE crop_type_id IS NULL) AS empty,
		count(*) FILTER (WHERE crop_type_id IS NOT NULL AND watered_at IS NULL) AS waiting_water,
		count(*) FILTER (WHERE watered_at IS NOT NULL AND ready_at > ` + now + `) AS growing,
		count(*) FILTER (WHERE watered_at IS NOT NULL AND ready_at <= ` + now + ` AND ready_at > ` + cutoff + `) AS ready,
		count(*) FILTER (WHERE watered_at IS NOT NULL AND ready_at <= ` + cutoff + `) AS withered`
}

// Summary 儀表板總覽。
type Summary struct {
	Players           int `json:"players"`
	Farms             int `json:"farms"`
	Groups            int `json:"groups"`
	TotalGold         int `json:"total_gold"`
	Plots             int `json:"plots"`
	WaitingWater      int `json:"waiting_water"`
	Growing           int `json:"growing"`
	Ready             int `json:"ready"`
	Withered          int `json:"withered"`
	StealsToday       int `json:"steals_today"`
	BlocksToday       int `json:"blocks_today"`
	InventoryItems    int `json:"inventory_items"`
	TransactionsToday int `json:"transactions_today"`
}

func GetSummary(ctx context.Context, t *db.Tx, now, todayStart, witherCutoff time.Time) (*Summary, error) {
	s := &Summary{}
	var empty int
	err := t.QueryRow(ctx, `SELECT count(*), `+plotStateCounts("$1", "$2")+` FROM farm_plots`, now, witherCutoff).
		Scan(&s.Plots, &empty, &s.WaitingWater, &s.Growing, &s.Ready, &s.Withered)
	if err != nil {
		return nil, err
	}
	err = t.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM players),
		(SELECT count(*) FROM farms),
		(SELECT count(*) FROM groups),
		(SELECT coalesce(sum(gold), 0) FROM farms),
		(SELECT count(*) FROM steal_records WHERE created_at >= $1 AND outcome = 'success'),
		(SELECT count(*) FROM steal_records WHERE created_at >= $1 AND outcome = 'blocked'),
		(SELECT coalesce(sum(quantity), 0) FROM inventory_items),
		(SELECT count(*) FROM game_transactions WHERE created_at >= $1)`, todayStart).
		Scan(&s.Players, &s.Farms, &s.Groups, &s.TotalGold, &s.StealsToday, &s.BlocksToday, &s.InventoryItems,
			&s.TransactionsToday)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// FarmRow 農場列表的一列。
type FarmRow struct {
	FarmID            int64     `db:"farm_id" json:"farm_id"`
	PlayerID          int64     `db:"player_id" json:"player_id"`
	PlayerName        string    `db:"player_name" json:"player_name"`
	HomeGroupID       *int64    `db:"home_group_id" json:"home_group_id"`
	HomeGroupName     *string   `db:"home_group_name" json:"home_group_name"`
	FarmName          string    `db:"farm_name" json:"farm_name"`
	Level             int       `db:"level" json:"level"`
	Gold              int       `db:"gold" json:"gold"`
	Plots             int       `db:"plots" json:"plots"`
	Empty             int       `db:"empty" json:"empty"`
	Growing           int       `db:"growing" json:"growing"`
	Ready             int       `db:"ready" json:"ready"`
	WaitingWater      int       `db:"waiting_water" json:"waiting_water"`
	Withered          int       `db:"withered" json:"withered"`
	WarehouseUsed     int       `db:"warehouse_used" json:"warehouse_used"`
	WarehouseCapacity int       `db:"warehouse_capacity" json:"warehouse_capacity"`
	UpdatedAt         time.Time `db:"updated_at" json:"updated_at"`
}

// Farms 農場列表（玩家名稱或農場名稱模糊搜尋 q，"" 表示不搜尋），回傳 (列, 總數)。
// 倉庫用量與遊戲內相同：含賭場獎品、不含素材背包。
func Farms(ctx context.Context, t *db.Tx, now, witherCutoff time.Time, q string, limit, offset int) ([]FarmRow, int, error) {
	// $1 = 搜尋字串（"" 不篩選）
	filter := `($1 = '' OR p.display_name ILIKE $1 ESCAPE '\' OR f.name ILIKE $1 ESCAPE '\')`
	pattern := ""
	if q != "" {
		pattern = "%" + escapeLike(q) + "%"
	}
	total, _, err := db.QueryValue[int](ctx, t,
		`SELECT count(*) FROM farms f JOIN players p ON p.id = f.player_id WHERE `+filter, pattern)
	if err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryStructs[FarmRow](ctx, t, `
		SELECT f.id AS farm_id, f.player_id, p.display_name AS player_name, f.home_group_id, g.name AS home_group_name,
			f.name AS farm_name, f.level, f.gold,
			coalesce(ps.plots, 0) AS plots, coalesce(ps.empty, 0) AS empty, coalesce(ps.growing, 0) AS growing,
			coalesce(ps.ready, 0) AS ready, coalesce(ps.waiting_water, 0) AS waiting_water,
			coalesce(ps.withered, 0) AS withered, coalesce(inv.used, 0)::bigint AS warehouse_used,
			f.warehouse_capacity, f.updated_at
		FROM farms f JOIN players p ON p.id = f.player_id
		LEFT JOIN (SELECT farm_id, count(*) AS plots, `+plotStateCounts("$2", "$3")+`
			FROM farm_plots GROUP BY farm_id) ps ON ps.farm_id = f.id
		LEFT JOIN `+inventory.WarehouseUsedByFarmSQL("$4")+` inv ON inv.farm_id = f.id
		LEFT JOIN groups g ON g.id = f.home_group_id
		WHERE `+filter+`
		ORDER BY f.updated_at DESC, f.id DESC LIMIT $5 OFFSET $6`,
		pattern, now, witherCutoff, inventory.BagCodes(), limit, offset)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FarmWithPlayer 農場與玩家名稱。
type FarmWithPlayer struct {
	models.Farm
	PlayerName string `db:"player_name"`
}

// FarmDetail 農場與玩家名稱；不存在時 nil。
func FarmDetail(ctx context.Context, t *db.Tx, farmID int64) (*FarmWithPlayer, error) {
	return db.QueryStruct[FarmWithPlayer](ctx, t, `
		SELECT f.*, p.display_name AS player_name FROM farms f JOIN players p ON p.id = f.player_id WHERE f.id = $1`,
		farmID)
}

// PlotWithCrop 土地與作物種類（空地時 Crop 為 nil）。
type PlotWithCrop struct {
	Plot models.FarmPlot
	Crop *models.CropType
}

// Plots 農場的土地（依編號）與作物種類。
func Plots(ctx context.Context, t *db.Tx, farmID int64) ([]PlotWithCrop, error) {
	plots, err := db.QueryStructs[models.FarmPlot](ctx, t,
		`SELECT * FROM farm_plots WHERE farm_id = $1 ORDER BY plot_number`, farmID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, p := range plots {
		if p.CropTypeID != nil {
			ids = append(ids, *p.CropTypeID)
		}
	}
	crops := map[int64]*models.CropType{}
	if len(ids) > 0 {
		list, err := db.QueryStructs[models.CropType](ctx, t, `SELECT * FROM crop_types WHERE id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		for i := range list {
			crops[list[i].ID] = &list[i]
		}
	}
	out := make([]PlotWithCrop, len(plots))
	for i, p := range plots {
		out[i].Plot = p
		if p.CropTypeID != nil {
			out[i].Crop = crops[*p.CropTypeID]
		}
	}
	return out, nil
}

// Inventory 數量大於 0 的庫存（依物品代碼）。
func Inventory(ctx context.Context, t *db.Tx, farmID int64) ([]models.InventoryItem, error) {
	return db.QueryStructs[models.InventoryItem](ctx, t,
		`SELECT * FROM inventory_items WHERE farm_id = $1 AND quantity > 0 ORDER BY item_code`, farmID)
}

// TransactionRow 交易紀錄列表的一列。
type TransactionRow struct {
	ID          int64     `db:"id" json:"id"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	FarmID      int64     `db:"farm_id" json:"farm_id"`
	FarmName    string    `db:"farm_name" json:"farm_name"`
	PlayerName  string    `db:"player_name" json:"player_name"`
	EventType   string    `db:"event_type" json:"event_type"`
	ItemCode    *string   `db:"item_code" json:"item_code"`
	Quantity    int       `db:"quantity" json:"quantity"`
	GoldDelta   int       `db:"gold_delta" json:"gold_delta"`
	Description string    `db:"description" json:"description"`
}

// Transactions farmID 為 nil 時列出所有農場。
func Transactions(ctx context.Context, t *db.Tx, limit int, farmID *int64) ([]TransactionRow, error) {
	return db.QueryStructs[TransactionRow](ctx, t, `
		SELECT tx.id, tx.created_at, tx.farm_id, f.name AS farm_name, p.display_name AS player_name, tx.event_type,
			tx.item_code, tx.quantity, tx.gold_delta, tx.description
		FROM game_transactions tx JOIN farms f ON f.id = tx.farm_id JOIN players p ON p.id = f.player_id
		WHERE ($2::bigint IS NULL OR tx.farm_id = $2)
		ORDER BY tx.created_at DESC, tx.id DESC LIMIT $1`, limit, farmID)
}

// CropDist 作物分布。
type CropDist struct {
	Code    string `db:"code" json:"code"`
	Name    string `db:"name" json:"name"`
	Growing int    `db:"growing" json:"growing"`
	Ready   int    `db:"ready" json:"ready"`
}

func CropDistribution(ctx context.Context, t *db.Tx, now, witherCutoff time.Time) ([]CropDist, error) {
	return db.QueryStructs[CropDist](ctx, t, `
		SELECT c.code, c.name,
			count(p.id) FILTER (WHERE p.ready_at > $1) AS growing,
			count(p.id) FILTER (WHERE p.ready_at <= $1 AND p.ready_at > $2) AS ready
		FROM crop_types c LEFT JOIN farm_plots p ON p.crop_type_id = c.id
		GROUP BY c.id, c.code, c.name, c.seed_price
		ORDER BY c.seed_price, c.id`, now, witherCutoff)
}

// HourActivity (小時, 操作數, 金幣變化總和)。
type HourActivity struct {
	Hour  time.Time `db:"hour"`
	Count int       `db:"count"`
	Gold  int       `db:"gold"`
}

// HourlyActivity 只包含有紀錄的小時（以連線時區 UTC 截斷到整點）。
func HourlyActivity(ctx context.Context, t *db.Tx, since time.Time) ([]HourActivity, error) {
	return db.QueryStructs[HourActivity](ctx, t, `
		SELECT date_trunc('hour', created_at) AS hour, count(*) AS count, coalesce(sum(gold_delta), 0) AS gold
		FROM game_transactions WHERE created_at >= $1
		GROUP BY date_trunc('hour', created_at) ORDER BY date_trunc('hour', created_at)`, since)
}

func TotalGold(ctx context.Context, t *db.Tx) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `SELECT coalesce(sum(gold), 0) FROM farms`)
	return n, err
}

func HasTransactions(ctx context.Context, t *db.Tx) (bool, error) {
	v, _, err := db.QueryValue[bool](ctx, t, `SELECT EXISTS (SELECT 1 FROM game_transactions)`)
	return v, err
}

// PlayerRow 玩家清單的一列（沒有農場時 farm_id 為 null）。
type PlayerRow struct {
	PlayerID   int64  `db:"player_id" json:"player_id"`
	PlayerName string `db:"player_name" json:"player_name"`
	FarmID     *int64 `db:"farm_id" json:"farm_id"`
}

// Players limit 預設 500。
func Players(ctx context.Context, t *db.Tx, limit int) ([]PlayerRow, error) {
	return db.QueryStructs[PlayerRow](ctx, t, `
		SELECT p.id AS player_id, p.display_name AS player_name, f.id AS farm_id
		FROM players p LEFT JOIN farms f ON f.player_id = p.id ORDER BY p.id LIMIT $1`, limit)
}
