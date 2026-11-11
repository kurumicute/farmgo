// Package titles 稱號：玩家擁有的稱號（farm_titles）與目前選用的稱號（farms.title），
// 對應 Python app/repositories/titles.py。
package titles

import (
	"context"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// Grant 取得稱號；已擁有時不重複建立（保留第一次取得的時間與來源）。空白稱號不做任何事。
func Grant(ctx context.Context, t *db.Tx, farmID int64, title, source string, now time.Time) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	_, err := t.Exec(ctx, "INSERT INTO farm_titles (farm_id, title, source, obtained_at) VALUES ($1, $2, $3, $4) "+
		"ON CONFLICT (farm_id, title) DO NOTHING", farmID, title, source, now)
	return err
}

func Owned(ctx context.Context, t *db.Tx, farmID int64) ([]models.FarmTitle, error) {
	return db.QueryStructs[models.FarmTitle](ctx, t,
		"SELECT farm_id, title, source, obtained_at FROM farm_titles WHERE farm_id = $1 ORDER BY obtained_at, title", farmID)
}

// SelectedByPlayer 玩家 → 目前選用的稱號（沒有選用的不列出）；排行榜顯示用。
func SelectedByPlayer(ctx context.Context, t *db.Tx, playerIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(playerIDs) == 0 {
		return out, nil
	}
	rows, err := t.Query(ctx, "SELECT player_id, title FROM farms WHERE player_id = ANY($1) AND title IS NOT NULL", playerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[id] = title
	}
	return out, rows.Err()
}
