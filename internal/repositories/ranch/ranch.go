// Package ranch 建築、動物與加工訂單的資料存取（對應 Python app/repositories/ranch.py）。
package ranch

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// ---------- 建築 ----------

type buildingsKey struct{ farmID int64 }

type buildingCountKey struct {
	farmID int64
	code   string
}

// ListBuildings 農場的建築（依代碼、欄位排序）；讀取範圍中只查一次（回傳的切片不可修改）。
func ListBuildings(ctx context.Context, t *db.Tx, farmID int64) ([]models.FarmBuilding, error) {
	return db.ReadMemo(t, buildingsKey{farmID}, func() ([]models.FarmBuilding, error) {
		return db.QueryStructs[models.FarmBuilding](ctx, t,
			`SELECT * FROM farm_buildings WHERE farm_id = $1 ORDER BY building_code, slot`, farmID)
	})
}

func CountBuildings(ctx context.Context, t *db.Tx, farmID int64, buildingCode string) (int, error) {
	return db.ReadMemo(t, buildingCountKey{farmID, buildingCode}, func() (int, error) {
		n, _, err := db.QueryValue[int](ctx, t,
			`SELECT count(*) FROM farm_buildings WHERE farm_id = $1 AND building_code = $2`, farmID, buildingCode)
		return n, err
	})
}

func AddBuilding(ctx context.Context, t *db.Tx, farmID int64, buildingCode string, slot int, now time.Time) (*models.FarmBuilding, error) {
	return db.QueryStruct[models.FarmBuilding](ctx, t,
		`INSERT INTO farm_buildings (farm_id, building_code, slot, built_at) VALUES ($1, $2, $3, $4) RETURNING *`,
		farmID, buildingCode, slot, now)
}

// ---------- 動物 ----------

// ListAnimals forUpdate 預設 false。
func ListAnimals(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) ([]models.FarmAnimal, error) {
	sql := `SELECT * FROM farm_animals WHERE farm_id = $1 ORDER BY animal_no`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStructs[models.FarmAnimal](ctx, t, sql, farmID)
}

// GetAnimal forUpdate 預設 false。
func GetAnimal(ctx context.Context, t *db.Tx, farmID int64, animalNo int, forUpdate bool) (*models.FarmAnimal, error) {
	sql := `SELECT * FROM farm_animals WHERE farm_id = $1 AND animal_no = $2 LIMIT 1`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FarmAnimal](ctx, t, sql, farmID, animalNo)
}

func CountAnimals(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `SELECT count(*) FROM farm_animals WHERE farm_id = $1`, farmID)
	return n, err
}

// AddAnimals 新增 quantity 隻動物，編號接在目前最大編號之後；回傳新動物的編號。
func AddAnimals(ctx context.Context, t *db.Tx, farmID int64, animalCode string, quantity int, now time.Time) ([]int, error) {
	maxNo, _, err := db.QueryValue[int](ctx, t,
		`SELECT coalesce(max(animal_no), 0) FROM farm_animals WHERE farm_id = $1`, farmID)
	if err != nil {
		return nil, err
	}
	numbers := make([]int, 0, quantity)
	for n := maxNo + 1; n < maxNo+1+quantity; n++ {
		numbers = append(numbers, n)
	}
	if len(numbers) == 0 {
		return numbers, nil
	}
	_, err = t.Exec(ctx, `
		INSERT INTO farm_animals (farm_id, animal_no, animal_code, acquired_at)
		SELECT $1, n, $2, $3 FROM unnest($4::int[]) AS n`, farmID, animalCode, now, numbers)
	if err != nil {
		return nil, err
	}
	return numbers, nil
}

// ---------- 加工訂單 ----------

// NewOrder 新增加工訂單的欄位（Python add_order(**values)）；Status 為 "" 時使用資料庫預設 in_progress。
type NewOrder struct {
	FarmID         int64
	RecipeCode     string
	BuildingCode   string
	Quantity       int
	Inputs         map[string]int
	OutputCode     string
	OutputQuantity int
	Status         string
	StartedAt      time.Time
	ReadyAt        time.Time
	CollectedAt    *time.Time
}

func AddOrder(ctx context.Context, t *db.Tx, v NewOrder) (*models.CraftOrder, error) {
	inputs := v.Inputs
	if inputs == nil {
		inputs = map[string]int{}
	}
	cols := []db.Col{
		db.C("farm_id", v.FarmID), db.C("recipe_code", v.RecipeCode), db.C("building_code", v.BuildingCode),
		db.C("quantity", v.Quantity), db.C("inputs", inputs), db.C("output_code", v.OutputCode),
		db.C("output_quantity", v.OutputQuantity), db.C("started_at", v.StartedAt), db.C("ready_at", v.ReadyAt),
	}
	if v.Status != "" {
		cols = append(cols, db.C("status", v.Status))
	}
	if v.CollectedAt != nil {
		cols = append(cols, db.C("collected_at", *v.CollectedAt))
	}
	sql, args := db.InsertSQL(models.TableCraftOrder, cols, "*")
	return db.QueryStruct[models.CraftOrder](ctx, t, sql, args...)
}

// GetOrder 只回傳屬於該農場的訂單，避免以訂單 ID 存取他人的訂單。forUpdate 預設 false。
func GetOrder(ctx context.Context, t *db.Tx, farmID, orderID int64, forUpdate bool) (*models.CraftOrder, error) {
	sql := `SELECT * FROM craft_orders WHERE id = $1 AND farm_id = $2 LIMIT 1`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.CraftOrder](ctx, t, sql, orderID, farmID)
}

// ActiveOrders 該設施尚未完成的訂單（加工中與排隊中），依完成時間排序。
func ActiveOrders(ctx context.Context, t *db.Tx, farmID int64, buildingCode string, now time.Time) ([]models.CraftOrder, error) {
	return db.QueryStructs[models.CraftOrder](ctx, t, `
		SELECT * FROM craft_orders
		WHERE farm_id = $1 AND building_code = $2 AND status = 'in_progress' AND ready_at > $3
		ORDER BY ready_at, id`,
		farmID, buildingCode, now)
}

// BusyOrder 該設施目前加工中（尚未完成、最早完成）的訂單。
func BusyOrder(ctx context.Context, t *db.Tx, farmID int64, buildingCode string, now time.Time) (*models.CraftOrder, error) {
	orders, err := ActiveOrders(ctx, t, farmID, buildingCode, now)
	if err != nil || len(orders) == 0 {
		return nil, err
	}
	return &orders[0], nil
}

// OrderRow 加工訂單與農場、玩家名稱（Python 的 (CraftOrder, farm_name, player_id, player_name)）。
type OrderRow struct {
	models.CraftOrder
	FarmName   string `db:"farm_name"`
	PlayerID   int64  `db:"player_id"`
	PlayerName string `db:"player_name"`
}

type ordersKey struct {
	farmID           int64
	hasFarm          bool
	includeCollected bool
	limit            int
}

// ListOrders farmID 為 nil 時列出所有農場；includeCollected 預設 true；limit 預設 50。
func ListOrders(ctx context.Context, t *db.Tx, farmID *int64, includeCollected bool, limit int) ([]OrderRow, error) {
	key := ordersKey{includeCollected: includeCollected, limit: limit}
	if farmID != nil {
		key.farmID, key.hasFarm = *farmID, true
	}
	return db.ReadMemo(t, key, func() ([]OrderRow, error) {
		sql := `
			SELECT o.*, f.name AS farm_name, p.id AS player_id, p.display_name AS player_name
			FROM craft_orders o JOIN farms f ON f.id = o.farm_id JOIN players p ON p.id = f.player_id
			WHERE ($1::bigint IS NULL OR o.farm_id = $1) AND ($2 OR o.status = 'in_progress')
			ORDER BY o.started_at DESC, o.id DESC LIMIT $3`
		return db.QueryStructs[OrderRow](ctx, t, sql, farmID, includeCollected, limit)
	})
}

// ---------- 監控 ----------

// FarmWithPlayer 農場與玩家名稱。
type FarmWithPlayer struct {
	models.Farm
	PlayerName string `db:"player_name"`
}

// FarmsWithRanch 有建築或動物的農場。
func FarmsWithRanch(ctx context.Context, t *db.Tx) ([]FarmWithPlayer, error) {
	return db.QueryStructs[FarmWithPlayer](ctx, t, `
		SELECT f.*, p.display_name AS player_name
		FROM farms f JOIN players p ON p.id = f.player_id
		WHERE f.id IN (SELECT farm_id FROM farm_buildings) OR f.id IN (SELECT farm_id FROM farm_animals)
		ORDER BY f.id`)
}
