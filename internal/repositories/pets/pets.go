// Package pets 農場寵物（farm_pets），對應 Python app/repositories/pets.py。
package pets

import (
	"context"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// Levels 寵物代碼 → 等級（沒有的寵物不列出）。
func Levels(ctx context.Context, t *db.Tx, farmID int64) (map[string]int, error) {
	rows, err := t.Query(ctx, "SELECT pet_code, level FROM farm_pets WHERE farm_id = $1", farmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var code string
		var lv int
		if err := rows.Scan(&code, &lv); err != nil {
			return nil, err
		}
		out[code] = lv
	}
	return out, rows.Err()
}

// Level 這隻寵物的等級；沒有時回傳 nil。
func Level(ctx context.Context, t *db.Tx, farmID int64, petCode string) (*int, error) {
	v, ok, err := db.QueryValue[int](ctx, t, "SELECT level FROM farm_pets WHERE farm_id = $1 AND pet_code = $2",
		farmID, petCode)
	if err != nil || !ok {
		return nil, err
	}
	return &v, nil
}

func Get(ctx context.Context, t *db.Tx, farmID int64, petCode string) (*models.FarmPet, error) {
	return db.QueryStruct[models.FarmPet](ctx, t,
		"SELECT farm_id, pet_code, level, bought_at FROM farm_pets WHERE farm_id = $1 AND pet_code = $2", farmID, petCode)
}
