// Package genetics 品質與突變設定的版本紀錄（對應 Python app/repositories/genetics.py）。
package genetics

import (
	"context"
	"encoding/json"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/repositories/crops"
)

// Current 回傳 (版本, 設定)；資料庫沒有設定時使用預設值（版本 0）。設定唯讀（見 db.CatalogValue）。
// 資料庫的設定 JSON 以 json.Unmarshal 讀入 balance.GeneticsSettings（不再驗證，與 Python 直接使用 dict 相同）。
func Current(ctx context.Context, t *db.Tx) (int, balance.GeneticsSettings, error) {
	type cur struct {
		Version  int
		Settings balance.GeneticsSettings
	}
	v, err := db.CatalogValue(t, "genetics", func() (cur, error) {
		row, err := db.QueryStruct[models.CropGeneticsSetting](ctx, t,
			"SELECT * FROM crop_genetics_settings ORDER BY version DESC LIMIT 1")
		if err != nil {
			return cur{}, err
		}
		if row == nil {
			return cur{0, balance.DefaultGenetics()}, nil
		}
		var s balance.GeneticsSettings
		if err := json.Unmarshal(row.Settings, &s); err != nil {
			return cur{}, err
		}
		return cur{row.Version, s}, nil
	})
	return v.Version, v.Settings, err
}

// AddVersion 新增一個設定版本。並行更新若取得相同版本號，主鍵會擋下其中一筆（該次更新失敗並回滾）。
// settings 為可序列化成 JSON 的值（通常是 genetics.SettingsMap 的結果或 balance.GeneticsSettings）。
func AddVersion(ctx context.Context, t *db.Tx, settings any, actor string, now time.Time) (int, error) {
	latest, _, err := db.QueryValue[int](ctx, t, "SELECT coalesce(max(version), 0) FROM crop_genetics_settings")
	if err != nil {
		return 0, err
	}
	version := latest + 1
	data, err := json.Marshal(settings)
	if err != nil {
		return 0, err
	}
	err = db.Insert(ctx, t, models.TableCropGeneticsSetting,
		db.C("version", version), db.C("settings", json.RawMessage(data)), db.C("created_by", actor), db.C("created_at", now))
	return version, err
}

// History 最近的設定版本（limit 預設 20）。
func History(ctx context.Context, t *db.Tx, limit int) ([]models.CropGeneticsSetting, error) {
	return db.QueryStructs[models.CropGeneticsSetting](ctx, t,
		"SELECT * FROM crop_genetics_settings ORDER BY version DESC LIMIT $1", limit)
}

// TxGroup 交易紀錄依品質與突變分組的統計。Income 只有販售有值。
type TxGroup struct {
	Quality  string `json:"quality"`
	Mutation string `json:"mutation"`
	Count    int    `json:"count"`
	Quantity int    `json:"quantity"`
	Income   *int   `json:"income,omitempty"`
}

// Planted 種植中的土地依品質與突變分組。
type Planted struct {
	Quality  *string `json:"quality"`
	Mutation *string `json:"mutation"`
	Plots    int     `json:"plots"`
}

// Holding 目前持有的農產品。
type Holding struct {
	ItemCode string `json:"item_code"`
	Quality  string `json:"quality"`
	Mutation string `json:"mutation"`
	Quantity int    `json:"quantity"`
}

// StatsResult 依品質與突變分組的實際紀錄。
type StatsResult struct {
	Planted   []Planted `json:"planted"`
	Harvested []TxGroup `json:"harvested"`
	Stolen    []TxGroup `json:"stolen"`
	Sold      []TxGroup `json:"sold"`
	Holdings  []Holding `json:"holdings"`
}

// Stats 依品質與突變分組的實際紀錄：種植中、收成、偷取、販售與目前持有。沒有紀錄時回傳空清單。
func Stats(ctx context.Context, t *db.Tx) (*StatsResult, error) {
	txGroup := func(event string, withIncome bool) ([]TxGroup, error) {
		rows, err := t.Query(ctx, `
			SELECT quality, coalesce(mutation, ''), count(*), coalesce(sum(quantity), 0), coalesce(sum(gold_delta), 0)
			FROM game_transactions
			WHERE event_type = $1 AND item_code IN `+crops.ProduceCodesSelect+` AND quality IS NOT NULL
			GROUP BY quality, mutation ORDER BY quality, mutation`, event)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []TxGroup{}
		for rows.Next() {
			var g TxGroup
			var income int
			if err := rows.Scan(&g.Quality, &g.Mutation, &g.Count, &g.Quantity, &income); err != nil {
				return nil, err
			}
			if withIncome {
				g.Income = &income
			}
			out = append(out, g)
		}
		return out, rows.Err()
	}
	res := &StatsResult{Planted: []Planted{}, Holdings: []Holding{}}
	rows, err := t.Query(ctx, `
		SELECT quality, mutation, count(*) FROM farm_plots WHERE crop_type_id IS NOT NULL
		GROUP BY quality, mutation ORDER BY quality, mutation`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p Planted
		if err := rows.Scan(&p.Quality, &p.Mutation, &p.Plots); err != nil {
			rows.Close()
			return nil, err
		}
		res.Planted = append(res.Planted, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if res.Harvested, err = txGroup("harvest", false); err != nil {
		return nil, err
	}
	if res.Stolen, err = txGroup("steal", false); err != nil {
		return nil, err
	}
	if res.Sold, err = txGroup("sell", true); err != nil {
		return nil, err
	}
	rows, err = t.Query(ctx, `
		SELECT item_code, quality, mutation, sum(quantity) FROM inventory_items
		WHERE item_code IN `+crops.ProduceCodesSelect+` AND quantity > 0
		GROUP BY item_code, quality, mutation ORDER BY item_code, quality, mutation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h Holding
		if err := rows.Scan(&h.ItemCode, &h.Quality, &h.Mutation, &h.Quantity); err != nil {
			return nil, err
		}
		res.Holdings = append(res.Holdings, h)
	}
	return res, rows.Err()
}
