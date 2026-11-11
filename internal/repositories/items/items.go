// Package items 永久工具與限時效果（對應 Python app/repositories/items.py）。
package items

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

type toolsKey struct{ farmID int64 }

// GetTool 取得工具；工具很少：讀一次全部（讀取整頁時共用）。
func GetTool(ctx context.Context, t *db.Tx, farmID int64, toolCode string) (*models.FarmTool, error) {
	tools, err := ListTools(ctx, t, farmID)
	if err != nil {
		return nil, err
	}
	var found *models.FarmTool
	for i := range tools { // 與 Python dict 相同：同代碼取最後一筆
		if tools[i].ToolCode == toolCode {
			tool := tools[i]
			found = &tool
		}
	}
	return found, nil
}

// AddTool 新增工具（預設啟用）。
func AddTool(ctx context.Context, t *db.Tx, farmID int64, toolCode string, now time.Time) (*models.FarmTool, error) {
	return db.QueryStruct[models.FarmTool](ctx, t, `
		INSERT INTO farm_tools (farm_id, tool_code, enabled, acquired_at) VALUES ($1, $2, true, $3) RETURNING *`,
		farmID, toolCode, now)
}

// ListTools 農場的全部工具（依 ID）。讀取範圍中共用（db.ReadMemo），呼叫端不可修改回傳的切片。
func ListTools(ctx context.Context, t *db.Tx, farmID int64) ([]models.FarmTool, error) {
	return db.ReadMemo(t, toolsKey{farmID}, func() ([]models.FarmTool, error) {
		return db.QueryStructs[models.FarmTool](ctx, t, "SELECT * FROM farm_tools WHERE farm_id = $1 ORDER BY id", farmID)
	})
}

// ToolEnabled 是否擁有且啟用。
func ToolEnabled(ctx context.Context, t *db.Tx, farmID int64, toolCode string) (bool, error) {
	tool, err := GetTool(ctx, t, farmID, toolCode)
	return tool != nil && tool.Enabled, err
}

// GetEffect 取得限時效果。
func GetEffect(ctx context.Context, t *db.Tx, farmID int64, effectCode string) (*models.FarmEffect, error) {
	return db.QueryStruct[models.FarmEffect](ctx, t,
		"SELECT * FROM farm_effects WHERE farm_id = $1 AND effect_code = $2 LIMIT 1", farmID, effectCode)
}

// EffectActive started_at <= now < expires_at。
func EffectActive(ctx context.Context, t *db.Tx, farmID int64, effectCode string, now time.Time) (bool, error) {
	e, err := GetEffect(ctx, t, farmID, effectCode)
	if err != nil || e == nil {
		return false, err
	}
	return !now.Before(e.StartedAt) && now.Before(e.ExpiresAt), nil
}

// ListEffects 農場的全部效果（依 ID）。
func ListEffects(ctx context.Context, t *db.Tx, farmID int64) ([]models.FarmEffect, error) {
	return db.QueryStructs[models.FarmEffect](ctx, t, "SELECT * FROM farm_effects WHERE farm_id = $1 ORDER BY id", farmID)
}
