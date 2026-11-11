package crops_test

import (
	"context"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestCropsQueries(t *testing.T) {
	d := setup(t)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		all, err := crops.ListAll(ctx, tx, false)
		if err != nil || len(all) < 8 {
			t.Fatalf("list %d %v", len(all), err)
		}
		for i := 1; i < len(all); i++ {
			if all[i-1].SeedPrice > all[i].SeedPrice {
				t.Fatal("not sorted by seed_price")
			}
		}
		w, err := crops.Find(ctx, tx, " WHEAT ")
		if err != nil || w == nil || w.Code != "wheat" {
			t.Fatalf("find %v %v", w, err)
		}
		if c, _ := crops.Find(ctx, tx, w.Name); c == nil || c.ID != w.ID {
			t.Fatal("find by name")
		}
		if v, _ := crops.FindVariant(ctx, tx, "amber_wheat"); v == nil || v.Code != "wheat" {
			t.Fatal("find variant")
		}
		if c, _ := crops.ByCode(ctx, tx, "no_such", false); c != nil {
			t.Fatal("expected nil")
		}
		pm, err := crops.ProduceMap(ctx, tx)
		if err != nil || !pm["amber_wheat"].Variant || pm["wheat"].Variant {
			t.Fatal("produce map", err)
		}
		old := w.GrowSeconds
		c, err := crops.SetGrowSeconds(ctx, tx, "wheat", old+1)
		if err != nil || c.GrowSeconds != old+1 {
			t.Fatal(err)
		}
		g, _ := crops.Get(ctx, tx, w.ID)
		if g.GrowSeconds != old+1 {
			t.Fatal("not updated")
		}
		_, err = crops.SetGrowSeconds(ctx, tx, "wheat", old) // 還原目錄
		return err
	})
}

func TestReadMemoInScope(t *testing.T) {
	d := setup(t)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		return tx.ReadScope(func() error {
			a, _ := crops.ListAll(ctx, tx, true)
			b, _ := crops.ListAll(ctx, tx, true)
			if len(a) == 0 || len(a) != len(b) {
				t.Fatal("memo")
			}
			return nil
		})
	})
}
