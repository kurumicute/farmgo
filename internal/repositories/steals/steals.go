// Package steals 偷菜紀錄的資料存取（對應 Python app/repositories/steals.py）。
package steals

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// NewRecord 新增偷菜紀錄的欄位（Python add(**values)）；Outcome 為 "" 時使用資料庫預設 success。
type NewRecord struct {
	GroupID        int64
	ThiefPlayerID  int64
	VictimPlayerID int64
	PlotID         *int64
	PlotNumber     int
	PlantingNo     int
	CropTypeID     int64
	Quantity       int
	CreatedAt      time.Time
	Outcome        string
	BlockedBy      *string
	BlockChance    *float64
	Roll           *float64
}

func Add(ctx context.Context, t *db.Tx, v NewRecord) (*models.StealRecord, error) {
	cols := []db.Col{
		db.C("group_id", v.GroupID), db.C("thief_player_id", v.ThiefPlayerID), db.C("victim_player_id", v.VictimPlayerID),
		db.C("plot_id", v.PlotID), db.C("plot_number", v.PlotNumber), db.C("planting_no", v.PlantingNo),
		db.C("crop_type_id", v.CropTypeID), db.C("quantity", v.Quantity), db.C("created_at", v.CreatedAt),
		db.C("blocked_by", v.BlockedBy), db.C("block_chance", v.BlockChance), db.C("roll", v.Roll),
	}
	if v.Outcome != "" {
		cols = append(cols, db.C("outcome", v.Outcome))
	}
	sql, args := db.InsertSQL(models.TableStealRecord, cols, "*")
	return db.QueryStruct[models.StealRecord](ctx, t, sql, args...)
}

func AlreadyStolen(ctx context.Context, t *db.Tx, plotID int64, plantingNo int, thiefPlayerID int64) (bool, error) {
	_, ok, err := db.QueryValue[int64](ctx, t, `
		SELECT id FROM steal_records WHERE plot_id = $1 AND planting_no = $2 AND thief_player_id = $3 LIMIT 1`,
		plotID, plantingNo, thiefPlayerID)
	return ok, err
}

// PlotsStolenSince 偷菜者自 since 起從同一位玩家成功偷過的土地數（被阻止的不算）。
func PlotsStolenSince(ctx context.Context, t *db.Tx, thiefPlayerID, victimPlayerID int64, since time.Time) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `
		SELECT count(DISTINCT plot_number) FROM steal_records
		WHERE thief_player_id = $1 AND victim_player_id = $2 AND outcome = 'success' AND created_at >= $3`,
		thiefPlayerID, victimPlayerID, since)
	return n, err
}

// victimsAtLimitSQL 自 since 起偷菜者已達每日土地上限的被偷者 ID（子查詢；參數 thief、since、limit 的編號由 p 指定）。
func victimsAtLimitSQL(thief, since, limit int) string {
	return fmt.Sprintf(`SELECT victim_player_id FROM steal_records
		WHERE thief_player_id = $%d AND outcome = 'success' AND created_at >= $%d
		GROUP BY victim_player_id HAVING count(DISTINCT plot_number) >= $%d`, thief, since, limit)
}

// victimsFullSQL 自 since 起已被 limit 位其他人成功偷過的被偷者 ID（子查詢；這位偷菜者不能再偷他們）。
func victimsFullSQL(thief, since, limit int) string {
	return fmt.Sprintf(`SELECT victim_player_id FROM steal_records
		WHERE thief_player_id <> $%d AND outcome = 'success' AND created_at >= $%d
		GROUP BY victim_player_id HAVING count(DISTINCT thief_player_id) >= $%d`, thief, since, limit)
}

// VictimsAtLimit 自 since 起偷菜者已達每日土地上限的被偷者 ID（Python 回傳子查詢，Go 直接執行並回傳 ID）。
func VictimsAtLimit(ctx context.Context, t *db.Tx, thiefPlayerID int64, since time.Time, limit int) ([]int64, error) {
	return db.QueryValues[int64](ctx, t, victimsAtLimitSQL(1, 2, 3), thiefPlayerID, since, limit)
}

// VictimsFull 自 since 起已被 limit 位其他人成功偷過的被偷者 ID（Python 回傳子查詢，Go 直接執行並回傳 ID）。
func VictimsFull(ctx context.Context, t *db.Tx, thiefPlayerID int64, since time.Time, limit int) ([]int64, error) {
	return db.QueryValues[int64](ctx, t, victimsFullSQL(1, 2, 3), thiefPlayerID, since, limit)
}

// ThievesSince 自 since 起成功偷過這位玩家的不同偷菜者人數（被阻止的不算；excludeThief 不為 nil 時排除該偷菜者）。
func ThievesSince(ctx context.Context, t *db.Tx, victimPlayerID int64, since time.Time, excludeThief *int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `
		SELECT count(DISTINCT thief_player_id) FROM steal_records
		WHERE victim_player_id = $1 AND outcome = 'success' AND created_at >= $2
		AND ($3::bigint IS NULL OR thief_player_id <> $3)`,
		victimPlayerID, since, excludeThief)
	return n, err
}

// StolenQuantity 同一株作物（土地＋種植次數）被成功偷走的總數。
func StolenQuantity(ctx context.Context, t *db.Tx, plotID int64, plantingNo int) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `
		SELECT coalesce(sum(quantity), 0) FROM steal_records
		WHERE plot_id = $1 AND planting_no = $2 AND outcome = 'success'`, plotID, plantingNo)
	return n, err
}

// StolenByCurrentPlantings 整座農場每格「目前這株作物」被成功偷走的總數：{plot_id: 數量}（沒被偷的不列出）。
func StolenByCurrentPlantings(ctx context.Context, t *db.Tx, farmID int64) (map[int64]int, error) {
	rows, err := t.Query(ctx, `
		SELECT s.plot_id, sum(s.quantity) FROM steal_records s
		JOIN farm_plots p ON p.id = s.plot_id AND p.planting_no = s.planting_no
		WHERE p.farm_id = $1 AND s.outcome = 'success'
		GROUP BY s.plot_id`, farmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var qty int
		if err := rows.Scan(&id, &qty); err != nil {
			return nil, err
		}
		out[id] = qty
	}
	return out, rows.Err()
}

// CandidatePlot 可偷的土地：(玩家 ID, 玩家名稱, 農場名稱, 土地編號, 作物名稱, 剩餘數量, 作物代碼)。
type CandidatePlot struct {
	PlayerID          int64  `db:"player_id"`
	PlayerName        string `db:"player_name"`
	FarmName          string `db:"farm_name"`
	PlotNumber        int    `db:"plot_number"`
	CropName          string `db:"crop_name"`
	RemainingQuantity int    `db:"remaining_quantity"`
	CropCode          string `db:"crop_code"`
}

// CandidatePlots 目前群組中其他成員、已成熟未乾枯、仍有剩餘、偷菜者尚未偷過、今天還沒偷滿該玩家，
// 且該玩家今天被其他人偷的人數還沒達上限的土地（上限為 nil 時不限制，例如傳說扒手手套）。
func CandidatePlots(
	ctx context.Context, t *db.Tx, groupID, thiefPlayerID int64, now, witherCutoff, dayStart time.Time,
	plotsPerVictim, thievesPerVictim *int,
) ([]CandidatePlot, error) {
	args := []any{groupID, thiefPlayerID, now, witherCutoff}
	var limits []string
	if plotsPerVictim != nil || thievesPerVictim != nil {
		args = append(args, dayStart) // $5
	}
	if plotsPerVictim != nil {
		args = append(args, *plotsPerVictim)
		limits = append(limits, "gm.player_id NOT IN ("+victimsAtLimitSQL(2, 5, len(args))+")")
	}
	if thievesPerVictim != nil {
		args = append(args, *thievesPerVictim)
		limits = append(limits, "gm.player_id NOT IN ("+victimsFullSQL(2, 5, len(args))+")")
	}
	extra := ""
	if len(limits) > 0 {
		extra = " AND " + strings.Join(limits, " AND ")
	}
	sql := `
		SELECT p.id AS player_id, p.display_name AS player_name, f.name AS farm_name, fp.plot_number,
			c.name AS crop_name, fp.remaining_quantity, c.code AS crop_code
		FROM group_members gm
		JOIN players p ON p.id = gm.player_id
		JOIN farms f ON f.player_id = p.id
		JOIN farm_plots fp ON fp.farm_id = f.id
		JOIN crop_types c ON c.id = fp.crop_type_id
		WHERE gm.group_id = $1 AND gm.player_id <> $2` + extra + `
			AND fp.watered_at IS NOT NULL AND fp.ready_at <= $3 AND fp.ready_at > $4 AND fp.remaining_quantity > 0
			AND NOT EXISTS (SELECT 1 FROM steal_records s
				WHERE s.plot_id = fp.id AND s.planting_no = fp.planting_no AND s.thief_player_id = $2)
		ORDER BY p.id, fp.plot_number`
	return db.QueryStructs[CandidatePlot](ctx, t, sql, args...)
}

// SearchRow 偷菜嘗試紀錄（JSON 欄位名稱與 Python dict 相同）。
type SearchRow struct {
	ID             int64     `db:"id" json:"id"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	GroupID        int64     `db:"group_id" json:"group_id"`
	GroupName      string    `db:"group_name" json:"group_name"`
	ThiefPlayerID  int64     `db:"thief_player_id" json:"thief_player_id"`
	ThiefName      string    `db:"thief_name" json:"thief_name"`
	VictimPlayerID int64     `db:"victim_player_id" json:"victim_player_id"`
	VictimName     string    `db:"victim_name" json:"victim_name"`
	CropName       string    `db:"crop_name" json:"crop_name"`
	CropCode       string    `db:"crop_code" json:"crop_code"`
	Quantity       int       `db:"quantity" json:"quantity"`
	PlotNumber     int       `db:"plot_number" json:"plot_number"`
	Outcome        string    `db:"outcome" json:"outcome"`
	BlockedBy      *string   `db:"blocked_by" json:"blocked_by"`
	BlockChance    *float64  `db:"block_chance" json:"block_chance"`
}

// SearchFilter Python search 的 keyword 參數；nil／"" 表示不篩選。
// PlayerID 比對偷菜者或被偷者；VictimPlayerID 只看自己的農場。
type SearchFilter struct {
	Limit          int
	GroupID        *int64
	PlayerID       *int64
	VictimPlayerID *int64
	Outcome        string
	Since          *time.Time
	Until          *time.Time
}

// Search 偷菜嘗試紀錄（成功與被阻止），新到舊。
func Search(ctx context.Context, t *db.Tx, f SearchFilter) ([]SearchRow, error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.GroupID != nil {
		add("s.group_id = ?", *f.GroupID)
	}
	if f.PlayerID != nil {
		add("(s.thief_player_id = ? OR s.victim_player_id = ?)", *f.PlayerID)
	}
	if f.VictimPlayerID != nil {
		add("s.victim_player_id = ?", *f.VictimPlayerID)
	}
	if f.Outcome != "" {
		add("s.outcome = ?", f.Outcome)
	}
	if f.Since != nil {
		add("s.created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		add("s.created_at < ?", *f.Until)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit)
	sql := fmt.Sprintf(`
		SELECT s.id, s.created_at, s.group_id, g.name AS group_name, s.thief_player_id, th.display_name AS thief_name,
			s.victim_player_id, v.display_name AS victim_name, c.name AS crop_name, c.code AS crop_code,
			s.quantity, s.plot_number, s.outcome, s.blocked_by, s.block_chance
		FROM steal_records s
		JOIN groups g ON g.id = s.group_id
		JOIN players th ON th.id = s.thief_player_id
		JOIN players v ON v.id = s.victim_player_id
		JOIN crop_types c ON c.id = s.crop_type_id
		%s
		ORDER BY s.created_at DESC, s.id DESC LIMIT $%d`, where, len(args))
	return db.QueryStructs[SearchRow](ctx, t, sql, args...)
}
