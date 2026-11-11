// Package rumors 農場傳聞的挖寶紀錄（rumor_digs）：每座農場每天挖一次，寶藏每個群組每天只會被挖到一次
// （對應 Python app/repositories/rumors.py）。day 參數是台灣日期。
package rumors

import (
	"context"
	"time"

	"farmgm/internal/db"
)

func Dug(ctx context.Context, t *db.Tx, farmID int64, day time.Time) (bool, error) {
	_, ok, err := db.QueryValue[int](ctx, t, "SELECT 1 FROM rumor_digs WHERE farm_id = $1 AND day = $2",
		farmID, db.DateParam(day))
	return ok, err
}

func Found(ctx context.Context, t *db.Tx, groupID int64, day time.Time) (bool, error) {
	v, _, err := db.QueryValue[bool](ctx, t,
		"SELECT EXISTS (SELECT 1 FROM rumor_digs WHERE group_id = $1 AND day = $2 AND found)", groupID, db.DateParam(day))
	return v, err
}

// Add 新增挖寶紀錄。主鍵與 (group_id, day) WHERE found 唯一是重複挖寶的最後防線（違反時回傳錯誤）。
func Add(ctx context.Context, t *db.Tx, farmID int64, day time.Time, groupID, targetFarmID int64, found bool,
	gold int, now time.Time) error {
	_, err := t.Exec(ctx, "INSERT INTO rumor_digs (farm_id, day, group_id, target_farm_id, found, gold, created_at) "+
		"VALUES ($1, $2, $3, $4, $5, $6, $7)", farmID, db.DateParam(day), groupID, targetFarmID, found, gold, now)
	return err
}
