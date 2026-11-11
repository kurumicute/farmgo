package animals_test

import (
	"context"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestAnimals(t *testing.T) {
	d := setup(t)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		all, err := animals.ListAll(ctx, tx, false)
		if err != nil || len(all) < 3 {
			t.Fatalf("%d %v", len(all), err)
		}
		m, _ := animals.ByCode(ctx, tx)
		if m["chicken"].ProductCode != "egg" {
			t.Fatal("by code")
		}
		if a, _ := animals.Get(ctx, tx, "cow", true); a == nil || a.Code != "cow" {
			t.Fatal("get")
		}
		if a, _ := animals.Find(ctx, tx, "CHICKEN"); a == nil {
			t.Fatal("find")
		}
		if a, _ := animals.FindProduct(ctx, tx, "Egg"); a == nil || a.Code != "chicken" {
			t.Fatal("find product")
		}
		return nil
	})
}
