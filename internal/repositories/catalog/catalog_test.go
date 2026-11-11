package catalog_test

import (
	"context"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestCatalog(t *testing.T) {
	d := setup(t)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		items, err := catalog.ListItems(ctx, tx)
		if err != nil {
			return err
		}
		if items.Map["seed_wheat"].Kind != catalog.Seed || items.Map["egg"].Kind != catalog.AnimalProduct ||
			items.Map["fertilizer"].Kind != catalog.Item || items.Map["wood"].Kind != catalog.Material {
			t.Fatalf("%+v", items.Map)
		}
		if _, ok := items.Map["auto_waterer"]; ok {
			t.Fatal("tools are not inventory items")
		}
		if items.Order[0] != "seed_"+items.Order[1] {
			t.Fatal("order", items.Order[:3])
		}
		names, _ := catalog.ItemNames(ctx, tx)
		if names["auto_waterer"] == "" || names["seed_maker"] == "" {
			t.Fatal("names")
		}
		codes, _ := catalog.ReservedCodes(ctx, tx)
		for _, c := range []string{"wheat", "amber_wheat", "chicken", "egg", "barn", "scarecrow", "wood", "seed_maker"} {
			if !codes[c] {
				t.Fatal("missing", c)
			}
		}
		return nil
	})
}
