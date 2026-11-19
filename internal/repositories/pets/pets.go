// Package pets 農場寵物（farm_pets），對應 Python app/repositories/pets.py。
package pets

import (
	"context"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// Levels 寵物代碼 → 等級（沒有的寵物不列出）。
// Skin 寵物造型（Migration 0046；"" = 沒有）。
type Skin struct {
	Hat, Color string
}

// LevelsAndSkins 寵物代碼 → 等級與造型（農場狀態一次讀取）。
func LevelsAndSkins(ctx context.Context, t *db.Tx, farmID int64) (map[string]int, map[string]Skin, error) {
	rows, err := t.Query(ctx, "SELECT pet_code, level, hat, color FROM farm_pets WHERE farm_id = $1", farmID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	levels, skins := map[string]int{}, map[string]Skin{}
	for rows.Next() {
		var code string
		var lv int
		var hat, color *string
		if err := rows.Scan(&code, &lv, &hat, &color); err != nil {
			return nil, nil, err
		}
		levels[code] = lv
		if hat != nil || color != nil {
			s := Skin{}
			if hat != nil {
				s.Hat = *hat
			}
			if color != nil {
				s.Color = *color
			}
			skins[code] = s
		}
	}
	return levels, skins, rows.Err()
}

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
		"SELECT farm_id, pet_code, level, bought_at, hat, color FROM farm_pets WHERE farm_id = $1 AND pet_code = $2", farmID, petCode)
}
