// Package events 限時全服事件的資料存取（對應 Python app/repositories/events.py）。
package events

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	eventrules "farmgm/internal/game/events"
)

// ListAll 全部事件（ID 由新到舊）。
func ListAll(ctx context.Context, t *db.Tx) ([]models.GameEvent, error) {
	return db.QueryStructs[models.GameEvent](ctx, t, "SELECT * FROM game_events ORDER BY id DESC")
}

// Get 依 ID 取得（forUpdate 預設 false）。
func Get(ctx context.Context, t *db.Tx, eventID int64, forUpdate bool) (*models.GameEvent, error) {
	sql := "SELECT * FROM game_events WHERE id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.GameEvent](ctx, t, sql, eventID)
}

// Active 目前生效中的事件（事件數量少，先取出啟用的事件再判斷時段）。回傳規則套件用的 *events.Event。
func Active(ctx context.Context, t *db.Tx, now time.Time) ([]*eventrules.Event, error) {
	rows, err := db.QueryStructs[models.GameEvent](ctx, t, "SELECT * FROM game_events WHERE enabled IS TRUE ORDER BY id")
	if err != nil {
		return nil, err
	}
	var out []*eventrules.Event
	for i := range rows {
		e := ToRule(&rows[i])
		if eventrules.IsActive(e, now) {
			out = append(out, e)
		}
	}
	return out, nil
}

// ToRule 把資料列轉成規則套件的事件。
func ToRule(g *models.GameEvent) *eventrules.Event {
	return &eventrules.Event{
		ID: g.ID, Name: g.Name, Description: g.Description, CropTypeID: g.CropTypeID,
		SellPricePct: g.SellPricePct, GrowReductionPct: g.GrowReductionPct, QualityBonusBP: g.QualityBonusBP,
		MutationBonusBP: g.MutationBonusBP, HarvestBonusPct: g.HarvestBonusPct, FreeWater: g.FreeWater,
		HarvestLoss: g.HarvestLoss, ScheduleType: g.ScheduleType, StartsAt: g.StartsAt, EndsAt: g.EndsAt,
		StartMinute: g.StartMinute, EndMinute: g.EndMinute, Weekdays: g.Weekdays, Enabled: g.Enabled,
		CreatedBy: g.CreatedBy, UpdatedAt: g.UpdatedAt,
	}
}
