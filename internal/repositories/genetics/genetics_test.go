package genetics_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/repositories/genetics"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestVersions(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		v, s, err := genetics.Current(ctx, tx)
		if err != nil || v != 0 || s.GiantMultiplier != 2 {
			t.Fatal(v, s, err)
		}
		cfg := balance.DefaultGenetics()
		cfg.GiantMultiplier = 3
		if v, err = genetics.AddVersion(ctx, tx, cfg, "admin", now); err != nil || v != 1 {
			t.Fatal(v, err)
		}
		if v, s, _ = genetics.Current(ctx, tx); v != 1 || s.GiantMultiplier != 3 {
			t.Fatal(v, s)
		}
		h, _ := genetics.History(ctx, tx, 20)
		if len(h) != 1 {
			t.Fatal(h)
		}
		st, err := genetics.Stats(ctx, tx)
		if err != nil || st.Sold == nil || len(st.Planted) != 0 {
			t.Fatal(st, err)
		}
		return nil
	})
}

func TestStatsGroups(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 1)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		for _, gd := range []int{10, 20} {
			if _, err := tx.Exec(ctx, `INSERT INTO game_transactions (farm_id, event_type, item_code, quantity, gold_delta,
				description, created_at, quality, mutation) VALUES ($1, 'sell', 'wheat', 2, $2, 'x', $3, 'good', 'none')`,
				f.FarmID, gd, now); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, "INSERT INTO inventory_items (farm_id, item_code, quality, mutation, quantity) VALUES ($1, 'wheat', 'rare', 'none', 3)", f.FarmID)
		return err
	})
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		st, err := genetics.Stats(ctx, tx)
		if err != nil {
			return err
		}
		if len(st.Sold) != 1 || st.Sold[0].Count != 2 || st.Sold[0].Quantity != 4 || *st.Sold[0].Income != 30 {
			t.Fatalf("%+v", st.Sold)
		}
		if len(st.Holdings) != 1 || st.Holdings[0].Quantity != 3 {
			t.Fatalf("%+v", st.Holdings)
		}
		return nil
	})
}
