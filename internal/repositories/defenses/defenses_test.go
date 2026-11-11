package defenses_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/defenses"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestDefenses(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 4)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		one := 1
		if _, err := defenses.Add(ctx, tx, f.FarmID, "scarecrow", 1, &one, now); err != nil {
			return err
		}
		if _, err := defenses.Add(ctx, tx, f.FarmID, "guard_dog", 1, nil, now); err != nil {
			return err
		}
		list, _ := defenses.ListForFarm(ctx, tx, f.FarmID)
		if len(list) != 2 || list[0].DeviceCode != "guard_dog" {
			t.Fatal(list)
		}
		if s, _ := defenses.GetSlot(ctx, tx, f.FarmID, "scarecrow", 1); s == nil || *s.StartPlot != 1 {
			t.Fatal("slot")
		}
		all, err := defenses.AllWithOwners(ctx, tx)
		if err != nil || len(all) != 2 || all[0].PlayerName != "甲" || all[0].Farm.ID != f.FarmID {
			t.Fatal(all, err)
		}
		return nil
	})
}
