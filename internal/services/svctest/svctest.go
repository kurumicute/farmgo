// Package svctest 服務層與網站測試共用的資料庫夾具（對應 Python tests/conftest.py 的 clean_db、
// 基準品質設定與測試用作物生長時間）。不引用 services 套件（避免 services 的內部測試循環引用）。
package svctest

import (
	"context"
	"encoding/json"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/fishing"
	"farmgm/internal/repositories/featuresettings"
	"farmgm/internal/repositories/repotest"
)

// TestGrowSeconds 既有測試以短生長時間撰寫（正式資料由 Migration 0007 設為 2 小時）。
var TestGrowSeconds = map[string]int{"wheat": 60, "carrot": 180, "tomato": 300, "strawberry": 600, "corn": 120,
	"pumpkin": 240, "grape": 360, "watermelon": 480}

// BaselineGenetics 100% 普通、不突變、無等級加成，讓與品質無關的測試結果固定。
func BaselineGenetics() balance.GeneticsSettings {
	g := balance.DefaultGenetics()
	g.QualityBP = map[string]int{"normal": 10000, "good": 0, "rare": 0, "legendary": 0}
	g.MutationBP = map[string]int{"giant": 0, "golden": 0, "fast": 0, "variant": 0}
	g.LevelBonusBP, g.LevelBonusMaxBP = 0, 0
	return g
}

// Prepare 清空資料庫、寫入基準品質設定、重設作物、動物與釣魚目錄，並重設程序內的快取與全域設定
// （沒有測試資料庫時 Skip）。clearCaches 是呼叫端額外要清的快取（例如 services.ClearNewsCache），開始與結束時各呼叫一次。
func Prepare(t *testing.T, clearCaches func()) *db.DB {
	t.Helper()
	d := repotest.Setup(t)
	exploration.ResetRareCrops()
	featurecfg.Apply(nil)
	featuresettings.Invalidate()
	oldTTL := db.CatalogTTL
	db.CatalogTTL = 0 // 測試用原生 SQL 改目錄
	db.CatalogClear()
	if clearCaches == nil {
		clearCaches = func() {}
	}
	clearCaches()
	t.Cleanup(func() { db.CatalogTTL = oldTTL; db.CatalogClear(); clearCaches() })
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		data, err := json.Marshal(BaselineGenetics())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crop_genetics_settings (version, settings, created_by, created_at)
			VALUES (1, $1, 'test', now())`, string(data)); err != nil {
			return err
		}
		var codes []string
		for _, c := range balance.InitialCrops {
			codes = append(codes, c.Code)
		}
		for _, c := range exploration.RareCrops {
			codes = append(codes, c.Code)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM crop_types WHERE NOT (code = ANY($1))", codes); err != nil {
			return err
		}
		const upd = `UPDATE crop_types SET name = $2, grow_seconds = $3, seed_price = $4, harvest_quantity = $5,
			sell_price = $6, variant_code = $7, variant_name = $8, active = true, shop_buyable = $9,
			seasons = NULL, hour_start = NULL, hour_end = NULL WHERE code = $1`
		for _, c := range balance.InitialCrops {
			v := balance.InitialVariants[c.Code]
			if _, err := tx.Exec(ctx, upd, c.Code, c.Name, TestGrowSeconds[c.Code], c.SeedPrice, c.HarvestQty, c.SellPrice,
				v.Code, v.Name, true); err != nil {
				return err
			}
		}
		for _, c := range exploration.RareCrops {
			if _, err := tx.Exec(ctx, upd, c.Code, c.Name, c.GrowSeconds, c.SeedPrice, c.HarvestQty, c.SellPrice,
				c.VariantCode, c.VariantName, false); err != nil {
				return err
			}
		}
		var animals []string
		for _, a := range balance.InitialAnimals {
			animals = append(animals, a.Code)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM animal_types WHERE NOT (code = ANY($1))", animals); err != nil {
			return err
		}
		for _, a := range balance.InitialAnimals {
			if _, err := tx.Exec(ctx, `UPDATE animal_types SET name = $2, price = $3, feed_cost = $4, cycle_seconds = $5,
				product_name = $6, product_quantity = $7, product_price = $8, active = true WHERE code = $1`,
				a.Code, a.Name, a.BuyPrice, a.FeedCost, a.CycleSeconds, a.ProductName, a.ProductQty, a.ProductPrice); err != nil {
				return err
			}
		}
		// 釣魚目錄：刪除測試新增的魚種／釣具，初始資料重設為 Migration 的值（farm_fish 等已先清空）
		var fishCodes, gearCodes []string
		for _, f := range fishing.InitialFish {
			fishCodes = append(fishCodes, f.Code)
		}
		for _, g := range fishing.InitialGear {
			gearCodes = append(gearCodes, g.Code)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM fish_types WHERE NOT (code = ANY($1))", fishCodes); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM fishing_gear WHERE NOT (code = ANY($1))", gearCodes); err != nil {
			return err
		}
		for i, f := range fishing.InitialFish {
			var baits any
			if b, ok := fishing.FishRequiredBaits[f.Code]; ok {
				data, _ := json.Marshal(b)
				baits = string(data)
			}
			if _, err := tx.Exec(ctx, `UPDATE fish_types SET name = $2, emoji = $3, rarity = $4, min_price = $5, max_price = $6,
				min_weight_g = $7, max_weight_g = $8, difficulty = $9, description = $10, sort_order = $11, active = true,
				required_baits = $12::json, seasons = NULL, hour_start = NULL, hour_end = NULL WHERE code = $1`,
				f.Code, f.Name, f.Emoji, f.Rarity, f.MinPrice, f.MaxPrice, f.MinWeightG, f.MaxWeightG, f.Difficulty, f.Desc,
				(i+1)*10, baits); err != nil {
				return err
			}
		}
		for i, g := range fishing.InitialGear {
			data, _ := json.Marshal(g.Effects)
			if _, err := tx.Exec(ctx, `UPDATE fishing_gear SET kind = $2, name = $3, emoji = $4, price = $5, required_level = $6,
				description = $7, effects = $8::json, stock = NULL, sort_order = $9, active = true WHERE code = $1`,
				g.Code, g.Kind, g.Name, g.Emoji, g.Price, g.Level, g.Desc, string(data), (i+1)*10); err != nil {
				return err
			}
		}
		return nil
	})
	// 釣魚防腳本預設關閉（ManualClock 下所有操作間隔都是 0）；防腳本測試自行開啟
	oldGuard := fishing.GuardEnabled
	fishing.GuardEnabled = false
	t.Cleanup(func() { fishing.GuardEnabled = oldGuard })
	return d
}
