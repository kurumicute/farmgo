// Package players 玩家（對應 Python app/repositories/players.py）。
package players

import (
	"context"
	"strconv"
	"strings"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// Create 建立玩家；externalPlatform／externalUserID 為 nil 表示 None。
func Create(ctx context.Context, t *db.Tx, displayName string, externalPlatform, externalUserID *string) (*models.Player, error) {
	return db.QueryStruct[models.Player](ctx, t, `
		INSERT INTO players (display_name, external_platform, external_user_id) VALUES ($1, $2, $3) RETURNING *`,
		displayName, externalPlatform, externalUserID)
}

// Get 依 ID 取得（forUpdate 預設 false）。
func Get(ctx context.Context, t *db.Tx, playerID int64, forUpdate bool) (*models.Player, error) {
	sql := "SELECT * FROM players WHERE id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.Player](ctx, t, sql, playerID)
}

// GetByExternal 依外部平台帳號取得。
func GetByExternal(ctx context.Context, t *db.Tx, platform, userID string) (*models.Player, error) {
	return db.QueryStruct[models.Player](ctx, t,
		"SELECT * FROM players WHERE external_platform = $1 AND external_user_id = $2 LIMIT 1", platform, userID)
}

// ListAll 依 ID 排序（limit 預設 100）。
func ListAll(ctx context.Context, t *db.Tx, limit int) ([]models.Player, error) {
	return db.QueryStructs[models.Player](ctx, t, "SELECT * FROM players ORDER BY id LIMIT $1", limit)
}

// SearchRow (玩家, 是否有農場)。
type SearchRow struct {
	Player  models.Player
	HasFarm bool
}

// Search 依名稱關鍵字或 ID 搜尋玩家（分頁）；回傳 ([(玩家, 是否有農場)], 總數)。keyword 為 "" 表示不篩選。
func Search(ctx context.Context, t *db.Tx, keyword string, limit, offset int) ([]SearchRow, int, error) {
	where := "TRUE"
	args := []any{}
	if keyword != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
		args = append(args, "%"+escaped+"%")
		where = `p.display_name ILIKE $1 ESCAPE '\'`
		if isDigits(keyword) {
			if id, err := strconv.ParseInt(keyword, 10, 64); err == nil {
				args = append(args, id)
				where = "(" + where + " OR p.id = $2)"
			}
		}
	}
	total, _, err := db.QueryValue[int](ctx, t, "SELECT count(*) FROM players p WHERE "+where, args...)
	if err != nil {
		return nil, 0, err
	}
	type row struct {
		models.Player
		HasFarm bool `db:"has_farm"`
	}
	n := len(args)
	rows, err := db.QueryStructs[row](ctx, t, "SELECT p.*, f.id IS NOT NULL AS has_farm FROM players p "+
		"LEFT JOIN farms f ON f.player_id = p.id WHERE "+where+
		" ORDER BY p.id LIMIT $"+strconv.Itoa(n+1)+" OFFSET $"+strconv.Itoa(n+2), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	out := make([]SearchRow, len(rows))
	for i, r := range rows {
		out[i] = SearchRow{r.Player, r.HasFarm}
	}
	return out, total, nil
}

// isDigits 對應 Python str.isdigit()（只接受 ASCII 數字；Python 也接受其他 Unicode 數字，但無法轉成 ID）。
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
