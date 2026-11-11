// Package defenses 防守設施（對應 Python app/repositories/defenses.py）。
package defenses

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// ListForFarm 依設施代碼、欄位排序。
func ListForFarm(ctx context.Context, t *db.Tx, farmID int64) ([]models.FarmDefense, error) {
	return db.QueryStructs[models.FarmDefense](ctx, t,
		"SELECT * FROM farm_defenses WHERE farm_id = $1 ORDER BY device_code, slot", farmID)
}

// GetSlot 取得指定欄位的設施。
func GetSlot(ctx context.Context, t *db.Tx, farmID int64, deviceCode string, slot int) (*models.FarmDefense, error) {
	return db.QueryStruct[models.FarmDefense](ctx, t,
		"SELECT * FROM farm_defenses WHERE farm_id = $1 AND device_code = $2 AND slot = $3 LIMIT 1", farmID, deviceCode, slot)
}

// Add 新增設施；startPlot 為 nil 表示守護全部土地。
func Add(ctx context.Context, t *db.Tx, farmID int64, deviceCode string, slot int, startPlot *int, now time.Time) (*models.FarmDefense, error) {
	return db.QueryStruct[models.FarmDefense](ctx, t, `
		INSERT INTO farm_defenses (farm_id, device_code, slot, start_plot, acquired_at) VALUES ($1, $2, $3, $4, $5)
		RETURNING *`, farmID, deviceCode, slot, startPlot, now)
}

// WithOwner (設施, 農場, 玩家名稱)。
type WithOwner struct {
	Defense    models.FarmDefense
	Farm       models.Farm
	PlayerName string
}

// AllWithOwners 全部設施與擁有者（依農場、設施代碼、欄位）。
func AllWithOwners(ctx context.Context, t *db.Tx) ([]WithOwner, error) {
	devices, err := db.QueryStructs[models.FarmDefense](ctx, t, `
		SELECT d.* FROM farm_defenses d JOIN farms f ON f.id = d.farm_id
		ORDER BY f.id, d.device_code, d.slot`)
	if err != nil || len(devices) == 0 {
		return nil, err
	}
	type farmRow struct {
		models.Farm
		PlayerName string `db:"player_name"`
	}
	farms, err := db.QueryStructs[farmRow](ctx, t, `
		SELECT f.*, p.display_name AS player_name FROM farms f JOIN players p ON p.id = f.player_id
		WHERE f.id IN (SELECT farm_id FROM farm_defenses)`)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]farmRow, len(farms))
	for _, f := range farms {
		byID[f.ID] = f
	}
	out := make([]WithOwner, 0, len(devices))
	for _, d := range devices {
		f := byID[d.FarmID]
		out = append(out, WithOwner{Defense: d, Farm: f.Farm, PlayerName: f.PlayerName})
	}
	return out, nil
}
