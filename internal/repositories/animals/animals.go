// Package animals 動物種類與產品（animal_types）的資料存取（對應 Python app/repositories/animals.py）。
package animals

import (
	"context"
	"strings"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

type listKey struct {
	name       string
	activeOnly bool
}

// ListAll 依價格、ID 排序（activeOnly 預設 false）。唯讀快照；要修改動物種類請用 Get(forUpdate=true)。
func ListAll(ctx context.Context, t *db.Tx, activeOnly bool) ([]models.AnimalType, error) {
	sql := "SELECT * FROM animal_types"
	cacheKey := "animals:all"
	if activeOnly {
		sql += " WHERE active IS TRUE"
		cacheKey = "animals:active"
	}
	sql += " ORDER BY price, id"
	return db.ReadMemo(t, listKey{"animals", activeOnly}, func() ([]models.AnimalType, error) {
		return db.CatalogRows(t, cacheKey, func() ([]models.AnimalType, error) {
			return db.QueryStructs[models.AnimalType](ctx, t, sql)
		})
	})
}

// ByCode 代碼 → 動物種類（含停用的）。
func ByCode(ctx context.Context, t *db.Tx) (map[string]models.AnimalType, error) {
	list, err := ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.AnimalType, len(list))
	for _, a := range list {
		out[a.Code] = a
	}
	return out, nil
}

// Get 依代碼取得（forUpdate 預設 false）。
func Get(ctx context.Context, t *db.Tx, code string, forUpdate bool) (*models.AnimalType, error) {
	sql := "SELECT * FROM animal_types WHERE code = $1 LIMIT 1"
	if forUpdate {
		sql = "SELECT * FROM animal_types WHERE code = $1 LIMIT 1 FOR UPDATE"
	}
	return db.QueryStruct[models.AnimalType](ctx, t, sql, code)
}

// Find 依代碼（不分大小寫）或名稱尋找動物。
func Find(ctx context.Context, t *db.Tx, key string) (*models.AnimalType, error) {
	key = strings.TrimSpace(key)
	return db.QueryStruct[models.AnimalType](ctx, t,
		"SELECT * FROM animal_types WHERE lower(code) = $1 OR name = $2 LIMIT 1", strings.ToLower(key), key)
}

// FindProduct 依產品代碼（不分大小寫）或產品名稱找到動物種類。
func FindProduct(ctx context.Context, t *db.Tx, key string) (*models.AnimalType, error) {
	key = strings.TrimSpace(key)
	return db.QueryStruct[models.AnimalType](ctx, t,
		"SELECT * FROM animal_types WHERE lower(product_code) = $1 OR product_name = $2 LIMIT 1", strings.ToLower(key), key)
}
