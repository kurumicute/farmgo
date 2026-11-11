// Package crops 作物種類（crop_types）的資料存取（對應 Python app/repositories/crops.py）。
package crops

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

// ListAll 依種子價格、ID 排序的作物（activeOnly 預設 false）。唯讀快照（見 db.CatalogRows）；要修改作物請用 ByCode(forUpdate=true)。
func ListAll(ctx context.Context, t *db.Tx, activeOnly bool) ([]models.CropType, error) {
	sql := "SELECT * FROM crop_types"
	if activeOnly {
		sql += " WHERE active IS TRUE"
	}
	sql += " ORDER BY seed_price, id"
	cacheKey := "crops:all"
	if activeOnly {
		cacheKey = "crops:active"
	}
	return db.ReadMemo(t, listKey{"crops", activeOnly}, func() ([]models.CropType, error) {
		return db.CatalogRows(t, cacheKey, func() ([]models.CropType, error) {
			return db.QueryStructs[models.CropType](ctx, t, sql)
		})
	})
}

// Find 依代碼（不分大小寫）或中文名稱尋找作物（不含珍稀變種）。
func Find(ctx context.Context, t *db.Tx, key string) (*models.CropType, error) {
	key = strings.TrimSpace(key)
	return db.QueryStruct[models.CropType](ctx, t,
		"SELECT * FROM crop_types WHERE lower(code) = $1 OR name = $2 LIMIT 1", strings.ToLower(key), key)
}

// FindVariant 依珍稀變種代碼（不分大小寫）或名稱找到原作物。
func FindVariant(ctx context.Context, t *db.Tx, key string) (*models.CropType, error) {
	key = strings.TrimSpace(key)
	return db.QueryStruct[models.CropType](ctx, t,
		"SELECT * FROM crop_types WHERE lower(variant_code) = $1 OR variant_name = $2 LIMIT 1", strings.ToLower(key), key)
}

// ByCode 依代碼取得作物（forUpdate 預設 false）。
func ByCode(ctx context.Context, t *db.Tx, code string, forUpdate bool) (*models.CropType, error) {
	sql := "SELECT * FROM crop_types WHERE code = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.CropType](ctx, t, sql+" LIMIT 1", code)
}

// Produce 農產品對應的作物與是否為珍稀變種。
type Produce struct {
	Crop    models.CropType
	Variant bool
}

// ProduceMap 農產品代碼 → (作物, 是否為珍稀變種)。
func ProduceMap(ctx context.Context, t *db.Tx) (map[string]Produce, error) {
	crops, err := ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	result := make(map[string]Produce, len(crops)*2)
	for _, c := range crops {
		result[c.Code] = Produce{c, false}
		result[c.VariantCode] = Produce{c, true}
	}
	return result, nil
}

// ProduceCodesSelect 所有屬於「作物」的農產品代碼（含珍稀變種）的子查詢 SQL，供販售排行等判斷（不含參數）。
const ProduceCodesSelect = "(SELECT code FROM crop_types UNION SELECT variant_code FROM crop_types)"

// SetGrowSeconds 修改生長時間（鎖定作物列）；作物不存在時回傳 nil。
func SetGrowSeconds(ctx context.Context, t *db.Tx, code string, seconds int) (*models.CropType, error) {
	crop, err := ByCode(ctx, t, code, true)
	if err != nil || crop == nil {
		return crop, err
	}
	if _, err := db.UpdateByID(ctx, t, models.TableCropType, crop.ID, db.C("grow_seconds", seconds)); err != nil {
		return nil, err
	}
	crop.GrowSeconds = seconds
	return crop, nil
}

// Get 依 ID 取得作物（Python session.get）。
//
// 一鍵操作（Bulk）期間同一個 ID 只查一次，對應 Python session.get 的 identity map：否則逐格查詢會讓延後寫入
// 每格都先送出，無法合併。回傳的作物在 Bulk 期間共用，呼叫端不可修改。
func Get(ctx context.Context, t *db.Tx, cropTypeID int64) (*models.CropType, error) {
	return db.Memo(t, getKey{cropTypeID}, func() (*models.CropType, error) {
		return db.QueryStruct[models.CropType](ctx, t, "SELECT * FROM crop_types WHERE id = $1", cropTypeID)
	})
}

type getKey struct{ id int64 }
