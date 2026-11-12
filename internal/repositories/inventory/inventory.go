// Package inventory 背包、素材背包與賭場獎品（對應 Python app/repositories/inventory.py）。
package inventory

import (
	"context"
	"slices"
	"strings"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/exploration"
)

// MaterialCodes 山洞素材與假幣（放在獨立的素材背包，不計入倉庫空間）。
func MaterialCodes() []string { return slices.Clone(exploration.MaterialOrder) }

// SeedPrefix 種子的物品代碼前綴：所有種子（含商店種子，2026-11-12 起）都放素材背包，不佔倉庫。
const SeedPrefix = "seed_"

// seedLikeSQL SQL 條件：種子（LIKE 的 \_ 表示底線本身）。
const seedLikeSQL = `item_code LIKE 'seed\_%'`

// BagCodes 放素材背包的固定物品代碼：山洞素材、假幣與稀有作物種子（其他種子以 SeedPrefix 判斷）。
func BagCodes() []string {
	return append(MaterialCodes(), exploration.RareSeedCodes()...)
}

// IsMaterial 是否放素材背包（不佔倉庫）：山洞素材、假幣與所有種子。
func IsMaterial(itemCode string) bool {
	return strings.HasPrefix(itemCode, SeedPrefix) || slices.Contains(BagCodes(), itemCode)
}

// InWarehouseSQL SQL 條件：佔倉庫空間的庫存（排除素材背包的物品）；參數 $n 需傳入 BagCodes()。
// 例：`... AND ` + InWarehouseSQL("$2")。alias 為空時直接用欄位名稱。
func InWarehouseSQL(param string) string {
	return "NOT (item_code = ANY(" + param + ") OR " + seedLikeSQL + ")"
}

// WarehouseUsedByFarmSQL 子查詢 (farm_id, used)：各農場的倉庫用量（一般庫存＋賭場獎品，不含素材背包）；
// param 為 BagCodes() 的參數位置（例如 "$1"）。
func WarehouseUsedByFarmSQL(param string) string {
	return `(SELECT farm_id, sum(quantity) AS used FROM (
		SELECT farm_id, quantity FROM inventory_items WHERE ` + InWarehouseSQL(param) + `
		UNION ALL SELECT farm_id, quantity FROM gamble_prizes) w GROUP BY farm_id)`
}

// ListItems 數量大於 0 的庫存，依物品、品質、突變排序。
func ListItems(ctx context.Context, t *db.Tx, farmID int64) ([]models.InventoryItem, error) {
	return db.QueryStructs[models.InventoryItem](ctx, t, `
		SELECT * FROM inventory_items WHERE farm_id = $1 AND quantity > 0
		ORDER BY item_code, quality, mutation`, farmID)
}

// GetQuantity 同一物品不同品質／突變是不同的庫存；Python 預設 quality="normal"、mutation="none"。
func GetQuantity(ctx context.Context, t *db.Tx, farmID int64, itemCode, quality, mutation string) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `
		SELECT quantity FROM inventory_items
		WHERE farm_id = $1 AND item_code = $2 AND quality = $3 AND mutation = $4`, farmID, itemCode, quality, mutation)
	return n, err
}

// TotalQuantity 倉庫已使用空間：一般庫存加上賭場獎品（素材背包的物品不計入）。
func TotalQuantity(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `
		SELECT (SELECT coalesce(sum(quantity), 0) FROM inventory_items WHERE farm_id = $1 AND `+InWarehouseSQL("$2")+`)
		     + (SELECT coalesce(sum(quantity), 0) FROM gamble_prizes WHERE farm_id = $1)`, farmID, BagCodes())
	return n, err
}

// MaterialQuantity 素材背包已使用空間（山洞素材、假幣與所有種子的總數）。
func MaterialQuantity(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `
		SELECT coalesce(sum(quantity), 0) FROM inventory_items WHERE farm_id = $1 AND (item_code = ANY($2) OR `+seedLikeSQL+`)`,
		farmID, BagCodes())
	return n, err
}

// ListPrizes 賭場獎品（forUpdate 預設 false）。
func ListPrizes(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) ([]models.GamblePrize, error) {
	sql := `SELECT * FROM gamble_prizes WHERE farm_id = $1 AND quantity > 0
		ORDER BY multiplier DESC, item_code, id`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStructs[models.GamblePrize](ctx, t, sql, farmID)
}

// AddPrize 增加賭場獎品（同一疊合併）。
func AddPrize(ctx context.Context, t *db.Tx, farmID int64, itemCode, quality, mutation string, multiplier, quantity int) error {
	_, err := t.Exec(ctx, `
		INSERT INTO gamble_prizes (farm_id, item_code, quality, mutation, multiplier, quantity)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT ON CONSTRAINT uq_gamble_prizes_farm_stack
		DO UPDATE SET quantity = gamble_prizes.quantity + $6, updated_at = now()`,
		farmID, itemCode, quality, mutation, multiplier, quantity)
	return err
}

type stackKey struct {
	FarmID                      int64
	ItemCode, Quality, Mutation string
}

// pending 一鍵操作中累積的庫存增加量（相同物品合併；送出順序依第一次累積的先後）。
type pending struct {
	order []stackKey
	qty   map[stackKey]int
}

func (p *pending) Flush(ctx context.Context, t *db.Tx) error {
	for _, k := range p.order {
		if err := upsert(ctx, t, k.FarmID, k.ItemCode, p.qty[k], k.Quality, k.Mutation); err != nil {
			return err
		}
	}
	return nil
}

type deferKey struct{}

// Add 增加庫存（Python 預設 quality="normal"、mutation="none"）。
// 一鍵操作中相同的物品合併成一次寫入（db.Deferred）；其他情況立即寫入。
func Add(ctx context.Context, t *db.Tx, farmID int64, itemCode string, quantity int, quality, mutation string) error {
	p, ok := db.Deferred(t, deferKey{}, func() *pending { return &pending{qty: map[stackKey]int{}} })
	if !ok {
		return upsert(ctx, t, farmID, itemCode, quantity, quality, mutation)
	}
	k := stackKey{farmID, itemCode, quality, mutation}
	if _, seen := p.qty[k]; !seen {
		p.order = append(p.order, k)
	}
	p.qty[k] += quantity
	return nil
}

// upsert 以唯一限制 (farm_id, item_code, quality, mutation) 做 upsert，避免重複建立庫存紀錄。
// 用 ExecNoFlush（Python core_execute）：逐格收成時每格一次，也是延後寫入的 Flush 本身。
func upsert(ctx context.Context, t *db.Tx, farmID int64, itemCode string, quantity int, quality, mutation string) error {
	_, err := t.ExecNoFlush(ctx, `
		INSERT INTO inventory_items (farm_id, item_code, quality, mutation, quantity) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT ON CONSTRAINT uq_inventory_items_farm_item
		DO UPDATE SET quantity = inventory_items.quantity + $5, updated_at = now()`,
		farmID, itemCode, quality, mutation, quantity)
	return err
}

// Remove 扣除指定品質／突變的庫存；數量不足時不做任何修改並回傳 false。
func Remove(ctx context.Context, t *db.Tx, farmID int64, itemCode string, quantity int, quality, mutation string) (bool, error) {
	tag, err := t.Exec(ctx, `
		UPDATE inventory_items SET quantity = quantity - $5, updated_at = now()
		WHERE farm_id = $1 AND item_code = $2 AND quality = $3 AND mutation = $4 AND quantity >= $5`,
		farmID, itemCode, quality, mutation, quantity)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
