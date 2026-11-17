// Package aid 送禮與互助紀錄（Go 版新增，Migration 0044）。
package aid

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// DayCounts 某座農場某天的互助次數。
type DayCounts struct {
	GiftSent, GiftReceived, WaterSent, WaterReceived int
	// 今天已經送過禮／幫過忙的對象（receiver_farm_id）
	GiftedTo, WateredFor map[int64]bool
	// 今天收過哪些農場的禮物／幫忙（sender_farm_id）
	GiftedBy, WateredBy map[int64]bool
}

// Counts 某座農場在 day（台灣日期）的送出與收到次數。
func Counts(ctx context.Context, t *db.Tx, farmID int64, day time.Time) (DayCounts, error) {
	rows, err := db.QueryStructs[models.AidLog](ctx, t,
		"SELECT * FROM aid_logs WHERE aid_day = $2 AND (sender_farm_id = $1 OR receiver_farm_id = $1) ORDER BY id",
		farmID, db.DateParam(day))
	out := DayCounts{GiftedTo: map[int64]bool{}, WateredFor: map[int64]bool{}, GiftedBy: map[int64]bool{}, WateredBy: map[int64]bool{}}
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		sent := r.SenderFarmID == farmID
		switch {
		case r.Kind == "gift" && sent:
			out.GiftSent++
			out.GiftedTo[r.ReceiverFarmID] = true
		case r.Kind == "gift":
			out.GiftReceived++
			out.GiftedBy[r.SenderFarmID] = true
		case sent:
			out.WaterSent++
			out.WateredFor[r.ReceiverFarmID] = true
		default:
			out.WaterReceived++
			out.WateredBy[r.SenderFarmID] = true
		}
	}
	return out, nil
}

// Add 新增一筆紀錄。itemCode 為 "" 時寫入 NULL。
func Add(ctx context.Context, t *db.Tx, kind string, senderFarmID, receiverFarmID int64, groupID *int64,
	itemCode string, quantity int, day, now time.Time) error {
	var item *string
	if itemCode != "" {
		item = &itemCode
	}
	return db.Insert(ctx, t, models.TableAidLog,
		db.C("kind", kind), db.C("sender_farm_id", senderFarmID), db.C("receiver_farm_id", receiverFarmID),
		db.C("group_id", groupID), db.C("item_code", item), db.C("quantity", quantity),
		db.C("aid_day", db.DateParam(day)), db.C("created_at", now))
}

// Target 同群組、有農場的其他玩家。
type Target struct {
	PlayerID   int64     `db:"player_id"`
	PlayerName string    `db:"display_name"`
	FarmID     int64     `db:"farm_id"`
	FarmName   string    `db:"farm_name"`
	Level      int       `db:"level"`
	CreatedAt  time.Time `db:"created_at"`
	Waiting    int       `db:"waiting"` // 等待澆水的土地數
}

// Targets 群組中有農場的其他玩家與他們等待澆水的土地數（依玩家名稱）。
func Targets(ctx context.Context, t *db.Tx, groupID, exceptPlayerID int64) ([]Target, error) {
	return db.QueryStructs[Target](ctx, t, `
		SELECT p.id AS player_id, p.display_name, f.id AS farm_id, f.name AS farm_name, f.level, f.created_at,
		       (SELECT count(*)::int FROM farm_plots fp
		        WHERE fp.farm_id = f.id AND fp.crop_type_id IS NOT NULL AND (fp.watered_at IS NULL OR fp.ready_at IS NULL)) AS waiting
		FROM group_members m
		JOIN players p ON p.id = m.player_id
		JOIN farms f ON f.player_id = p.id
		WHERE m.group_id = $1 AND m.player_id <> $2
		ORDER BY p.display_name, p.id`, groupID, exceptPlayerID)
}
