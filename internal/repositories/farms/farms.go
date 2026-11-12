// Package farms 農場與土地（對應 Python app/repositories/farms.py）。
//
// Python 的 plant／water／clear 只修改 ORM 物件，由 flush 寫入；Go 版直接 UPDATE 並同步修改傳入的 *models.FarmPlot。
// updated_at（Python onupdate=now()）在這些 UPDATE 中設為 now()。
package farms

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	croprepo "farmgm/internal/repositories/crops"
)

// Create 建立農場。
func Create(ctx context.Context, t *db.Tx, playerID int64, name string, level, gold, warehouseCapacity int, homeGroupID int64) (*models.Farm, error) {
	return db.QueryStruct[models.Farm](ctx, t, `
		INSERT INTO farms (player_id, home_group_id, name, level, gold, warehouse_capacity)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING *`, playerID, homeGroupID, name, level, gold, warehouseCapacity)
}

type farmKey struct{ playerID int64 }

// GetByPlayer 玩家的農場（forUpdate 預設 false）。
// 所有會改變資產的操作都先鎖定農場資料列（forUpdate=true），使同一農場的操作序列化；
// 不加鎖的讀取在讀取範圍中同一筆交易只查一次（db.ReadMemo，有寫入就清空）。
func GetByPlayer(ctx context.Context, t *db.Tx, playerID int64, forUpdate bool) (*models.Farm, error) {
	if forUpdate {
		return db.QueryStruct[models.Farm](ctx, t, "SELECT * FROM farms WHERE player_id = $1 LIMIT 1 FOR UPDATE", playerID)
	}
	return db.ReadMemo(t, farmKey{playerID}, func() (*models.Farm, error) {
		return db.QueryStruct[models.Farm](ctx, t, "SELECT * FROM farms WHERE player_id = $1 LIMIT 1", playerID)
	})
}

// PlotWithCrop 土地與作物種類（Python joinedload(FarmPlot.crop_type)；空地時 Crop 為 nil）。
type PlotWithCrop struct {
	models.FarmPlot
	Crop *models.CropType
}

// ListPlots 依編號排序的土地與作物種類（forUpdate 預設 false；只鎖土地列，作物種類是外部連接不能鎖）。
func ListPlots(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) ([]PlotWithCrop, error) {
	sql := "SELECT * FROM farm_plots WHERE farm_id = $1 ORDER BY plot_number"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	plots, err := db.QueryStructs[models.FarmPlot](ctx, t, sql, farmID)
	if err != nil {
		return nil, err
	}
	need := false
	for _, p := range plots {
		if p.CropTypeID != nil {
			need = true
			break
		}
	}
	var crops map[int64]*models.CropType
	if need && !forUpdate {
		// 不加鎖的讀取只用來顯示（作物代碼與名稱）：用遊戲目錄快取，省一次查詢；快取裡找不到（例如剛新增的作物）才查資料庫
		if crops, err = cropsFromCatalog(ctx, t, plots); err != nil {
			return nil, err
		}
	}
	if need && crops == nil {
		rows, err := db.QueryStructs[models.CropType](ctx, t,
			"SELECT * FROM crop_types WHERE id IN (SELECT crop_type_id FROM farm_plots WHERE farm_id = $1)", farmID)
		if err != nil {
			return nil, err
		}
		crops = make(map[int64]*models.CropType, len(rows))
		for i := range rows {
			crops[rows[i].ID] = &rows[i]
		}
	}
	out := make([]PlotWithCrop, len(plots))
	for i, p := range plots {
		out[i].FarmPlot = p
		if p.CropTypeID != nil {
			out[i].Crop = crops[*p.CropTypeID]
		}
	}
	return out, nil
}

// cropsFromCatalog 從遊戲目錄快取（crops.ListAll）找出土地上的作物；有任何一種找不到時回傳 nil（呼叫端改查資料庫）。
// 回傳的作物與目錄快取共用，唯讀。
func cropsFromCatalog(ctx context.Context, t *db.Tx, plots []models.FarmPlot) (map[int64]*models.CropType, error) {
	all, err := croprepo.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*models.CropType, len(all))
	for i := range all {
		byID[all[i].ID] = &all[i]
	}
	for _, p := range plots {
		if p.CropTypeID != nil && byID[*p.CropTypeID] == nil {
			return nil, nil
		}
	}
	return byID, nil
}

// GetPlot 取得一格土地（forUpdate 預設 false）。
func GetPlot(ctx context.Context, t *db.Tx, farmID int64, plotNumber int, forUpdate bool) (*models.FarmPlot, error) {
	sql := "SELECT * FROM farm_plots WHERE farm_id = $1 AND plot_number = $2"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FarmPlot](ctx, t, sql, farmID, plotNumber)
}

// CountPlots 土地數。
func CountPlots(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, "SELECT count(*) FROM farm_plots WHERE farm_id = $1", farmID)
	return n, err
}

// AddPlots 在目前最大編號後新增 count 格土地，回傳新編號。
func AddPlots(ctx context.Context, t *db.Tx, farmID int64, count int) ([]int, error) {
	currentMax, _, err := db.QueryValue[int](ctx, t,
		"SELECT coalesce(max(plot_number), 0) FROM farm_plots WHERE farm_id = $1", farmID)
	if err != nil {
		return nil, err
	}
	numbers := make([]int, count)
	for i := range numbers {
		numbers[i] = currentMax + 1 + i
	}
	if count > 0 {
		if _, err := t.Exec(ctx,
			"INSERT INTO farm_plots (farm_id, plot_number) SELECT $1, n FROM unnest($2::int[]) AS n", farmID, numbers); err != nil {
			return nil, err
		}
	}
	return numbers, nil
}

// plotWrites 一鍵操作中累積的土地更新（種植、澆水、清除）；離開 Bulk 或下一次查詢前以一個 pgx.Batch 依序送出。
type plotWrites struct {
	sqls []string
	args [][]any
}

func (p *plotWrites) Flush(ctx context.Context, t *db.Tx) error {
	if len(p.sqls) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for i, sql := range p.sqls {
		b.Queue(sql, p.args[i]...)
	}
	return t.SendBatchNoFlush(ctx, b)
}

type plotWritesKey struct{}

// plotWrite 更新一格土地。只改這格土地，不依賴尚未送出的庫存、圖鑑、交易紀錄（Python 只改 ORM 屬性，提交前才寫入），
// 所以不先送出其他延後寫入；一鍵操作中再延後到整批一起送出（原本每格的 UPDATE 都會讓前一格的交易紀錄單獨送出，
// 合併寫入無效）。之後的任何查詢都會先送出，讀到的一定是更新後的內容。
func plotWrite(ctx context.Context, t *db.Tx, sql string, args ...any) error {
	if p, ok := db.Deferred(t, plotWritesKey{}, func() *plotWrites { return &plotWrites{} }); ok {
		p.sqls = append(p.sqls, sql)
		p.args = append(p.args, args)
		return nil
	}
	_, err := t.ExecNoFlush(ctx, sql, args...)
	return err
}

// Plant 種下作物（Python 預設 quality="normal"、mutation="none"）；planting_no 遞增。
func Plant(ctx context.Context, t *db.Tx, plot *models.FarmPlot, cropTypeID int64, quantity int, now time.Time, quality, mutation string) error {
	if err := plotWrite(ctx, t, `
		UPDATE farm_plots SET crop_type_id = $2, quality = $3, mutation = $4, planted_at = $5, watered_at = NULL,
		       ready_at = NULL, remaining_quantity = $6, fertilized = false, planting_no = planting_no + 1, updated_at = now()
		WHERE id = $1`, plot.ID, cropTypeID, quality, mutation, now, quantity); err != nil {
		return err
	}
	plot.CropTypeID = &cropTypeID
	plot.Quality, plot.Mutation = &quality, &mutation
	plot.PlantedAt = &now
	plot.WateredAt, plot.ReadyAt = nil, nil
	plot.RemainingQuantity = &quantity
	plot.Fertilized = false
	plot.PlantingNo++
	return nil
}

// Water 澆水並設定成熟時間。
func Water(ctx context.Context, t *db.Tx, plot *models.FarmPlot, now, readyAt time.Time) error {
	if err := plotWrite(ctx, t, "UPDATE farm_plots SET watered_at = $2, ready_at = $3, updated_at = now() WHERE id = $1",
		plot.ID, now, readyAt); err != nil {
		return err
	}
	plot.WateredAt, plot.ReadyAt = &now, &readyAt
	return nil
}

// Clear 清空土地。
func Clear(ctx context.Context, t *db.Tx, plot *models.FarmPlot) error {
	if err := plotWrite(ctx, t, `
		UPDATE farm_plots SET crop_type_id = NULL, quality = NULL, mutation = NULL, planted_at = NULL, watered_at = NULL,
		       ready_at = NULL, remaining_quantity = NULL, fertilized = false, updated_at = now()
		WHERE id = $1`, plot.ID); err != nil {
		return err
	}
	plot.CropTypeID, plot.Quality, plot.Mutation = nil, nil, nil
	plot.PlantedAt, plot.WateredAt, plot.ReadyAt = nil, nil, nil
	plot.RemainingQuantity = nil
	plot.Fertilized = false
	return nil
}

// GetByID 依 ID 取得農場（forUpdate 預設 false）。
func GetByID(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) (*models.Farm, error) {
	sql := "SELECT * FROM farms WHERE id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.Farm](ctx, t, sql, farmID)
}
