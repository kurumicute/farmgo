// Package catalog 物品目錄（對應 Python app/repositories/catalog.py）：整合資料庫中的作物、動物產品與程式中固定的道具、加工品，
// 供顯示名稱與管理員驗證使用。
package catalog

import (
	"context"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/crops"
)

// 物品種類
const (
	Seed          = "seed"
	ProduceKind   = "produce"
	Variant       = "variant"
	AnimalProduct = "animal_product"
	Goods         = "goods"
	Item          = "item"
	Material      = "material" // 山洞探索取得的素材與假幣
)

// Entry 物品的種類與顯示名稱。
type Entry struct {
	Kind string
	Name string
}

// Items 可放入背包的物品：Order 保留 Python dict 的插入順序（重複代碼保留第一次出現的位置、值以最後一次為準）。
type Items struct {
	Order []string
	Map   map[string]Entry
}

func (it *Items) set(code string, e Entry) {
	if _, ok := it.Map[code]; !ok {
		it.Order = append(it.Order, code)
	}
	it.Map[code] = e
}

// ListItems 可放入背包的物品代碼 → (種類, 顯示名稱)。永久工具不在背包中，不列入。（Python items()）
func ListItems(ctx context.Context, t *db.Tx) (*Items, error) {
	result := &Items{Map: map[string]Entry{}}
	cropList, err := crops.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for _, c := range cropList {
		result.set(rules.SeedItemCode(c.Code), Entry{Seed, c.Name + "種子"})
		result.set(c.Code, Entry{ProduceKind, c.Name})
		result.set(c.VariantCode, Entry{Variant, c.VariantName})
	}
	animalList, err := animals.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for _, a := range animalList {
		result.set(a.ProductCode, Entry{AnimalProduct, a.ProductName})
	}
	for _, code := range balance.GoodOrder {
		result.set(code, Entry{Goods, balance.Goods[code].Name})
	}
	for _, code := range balance.ItemOrder {
		if it := balance.Items[code]; it.Kind == "consumable" {
			result.set(code, Entry{Item, it.Name})
		}
	}
	for _, code := range exploration.MaterialOrder {
		result.set(code, Entry{Material, exploration.Materials[code].Name})
	}
	return result, nil
}

// ItemNames 物品代碼 → 顯示名稱（含永久工具與機器）。
func ItemNames(ctx context.Context, t *db.Tx) (map[string]string, error) {
	items, err := ListItems(ctx, t)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(items.Map)+16)
	for code, e := range items.Map {
		names[code] = e.Name
	}
	for _, code := range balance.ItemOrder {
		if _, ok := names[code]; !ok {
			names[code] = balance.Items[code].Name
		}
	}
	for _, code := range exploration.MachineOrder {
		if _, ok := names[code]; !ok {
			names[code] = exploration.Machines[code].Name
		}
	}
	return names, nil
}

// ReservedCodes 已被使用的所有代碼（作物、變種、動物、產品、道具、設施、配方等），新代碼不可與其重複。
func ReservedCodes(ctx context.Context, t *db.Tx) (map[string]bool, error) {
	codes := map[string]bool{}
	rows, err := t.Query(ctx, "SELECT code, variant_code FROM crop_types UNION ALL SELECT code, product_code FROM animal_types")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		codes[a], codes[b] = true, true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for code := range balance.Goods {
		codes[code] = true
	}
	for code := range balance.Items {
		codes[code] = true
	}
	for code := range balance.Buildings {
		codes[code] = true
	}
	for code := range balance.Recipes {
		codes[code] = true
	}
	for code := range balance.Defenses {
		codes[code] = true
	}
	for code := range exploration.Materials {
		codes[code] = true
	}
	for code := range exploration.Machines {
		codes[code] = true
	}
	for code := range exploration.IncubatorRecipes {
		codes[code] = true
	}
	return codes, nil
}
