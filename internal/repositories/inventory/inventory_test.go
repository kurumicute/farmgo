package inventory_test

import (
	"context"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestAddRemoveAndTotals(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 1)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		adds := []struct {
			code, q, m string
			n          int
		}{{"wheat", "normal", "none", 3}, {"wheat", "normal", "none", 2}, {"wheat", "rare", "golden", 1}, {"wood", "normal", "none", 7}}
		for _, a := range adds {
			if err := inventory.Add(ctx, tx, f.FarmID, a.code, a.n, a.q, a.m); err != nil {
				return err
			}
		}
		if err := inventory.AddPrize(ctx, tx, f.FarmID, "wheat", "rare", "none", 2, 4); err != nil {
			return err
		}
		if err := inventory.AddPrize(ctx, tx, f.FarmID, "wheat", "rare", "none", 2, 1); err != nil {
			return err
		}
		if q, _ := inventory.GetQuantity(ctx, tx, f.FarmID, "wheat", "normal", "none"); q != 5 {
			t.Fatal("qty", q)
		}
		if q, _ := inventory.GetQuantity(ctx, tx, f.FarmID, "carrot", "normal", "none"); q != 0 {
			t.Fatal("missing qty", q)
		}
		if n, err := inventory.TotalQuantity(ctx, tx, f.FarmID); n != 5+1+5 || err != nil {
			t.Fatal("total", n, err)
		}
		if n, _ := inventory.MaterialQuantity(ctx, tx, f.FarmID); n != 7 {
			t.Fatal("material", n)
		}
		if ok, _ := inventory.Remove(ctx, tx, f.FarmID, "wheat", 6, "normal", "none"); ok {
			t.Fatal("removed too many")
		}
		if ok, _ := inventory.Remove(ctx, tx, f.FarmID, "wheat", 5, "normal", "none"); !ok {
			t.Fatal("remove")
		}
		items, _ := inventory.ListItems(ctx, tx, f.FarmID)
		if len(items) != 2 { // 數量 0 的不列出
			t.Fatalf("%+v", items)
		}
		prizes, err := inventory.ListPrizes(ctx, tx, f.FarmID, true)
		if err != nil || len(prizes) != 1 || prizes[0].Quantity != 5 {
			t.Fatal(prizes, err)
		}
		if !inventory.IsMaterial("fake_coin") || inventory.IsMaterial("wheat") {
			t.Fatal("is material")
		}
		return nil
	})
}

func TestDeferredMergesInBulk(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 1)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		return tx.Bulk(ctx, func() error {
			for i := 0; i < 3; i++ {
				if err := inventory.Add(ctx, tx, f.FarmID, "carrot", 2, "normal", "none"); err != nil {
					return err
				}
			}
			// 查詢前會先送出累積的寫入
			if q, _ := inventory.GetQuantity(ctx, tx, f.FarmID, "carrot", "normal", "none"); q != 6 {
				t.Fatal("deferred not flushed before query", q)
			}
			return inventory.Add(ctx, tx, f.FarmID, "carrot", 1, "normal", "none")
		})
	})
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		if q, _ := inventory.GetQuantity(ctx, tx, f.FarmID, "carrot", "normal", "none"); q != 7 {
			t.Fatal("after bulk", q)
		}
		return nil
	})
}
