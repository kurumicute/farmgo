package events_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/events"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestEvents(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		for _, enabled := range []bool{true, false} {
			if _, err := tx.Exec(ctx, `INSERT INTO game_events (name, sell_price_pct, schedule_type, starts_at, ends_at, enabled,
				created_by, created_at, updated_at) VALUES ('活動', 150, 'once', $1, $2, $3, 'admin', $1, $1)`,
				now.Add(-time.Hour), now.Add(time.Hour), enabled); err != nil {
				return err
			}
		}
		all, _ := events.ListAll(ctx, tx)
		if len(all) != 2 || all[0].ID != 2 {
			t.Fatal(all)
		}
		if e, _ := events.Get(ctx, tx, 1, true); e == nil || e.SellPricePct != 150 {
			t.Fatal("get")
		}
		act, err := events.Active(ctx, tx, now)
		if err != nil || len(act) != 1 || act[0].ID != 1 {
			t.Fatal(act, err)
		}
		if act, _ = events.Active(ctx, tx, now.Add(2*time.Hour)); len(act) != 0 {
			t.Fatal("ended")
		}
		return nil
	})
}
