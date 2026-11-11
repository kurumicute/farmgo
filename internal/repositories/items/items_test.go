package items_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestToolsAndEffects(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 1)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		return tx.ReadScope(func() error {
			if ok, _ := items.ToolEnabled(ctx, tx, f.FarmID, "auto_waterer"); ok {
				t.Fatal("no tool yet")
			}
			if _, err := items.AddTool(ctx, tx, f.FarmID, "auto_waterer", now); err != nil {
				return err
			}
			// 寫入後讀取快取已清空
			if ok, _ := items.ToolEnabled(ctx, tx, f.FarmID, "auto_waterer"); !ok {
				t.Fatal("tool not visible after insert")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO farm_effects (farm_id, effect_code, multiplier, started_at, expires_at)
				VALUES ($1, 'earthworm', 0.85, $2, $3)`, f.FarmID, now, now.Add(time.Hour)); err != nil {
				return err
			}
			if ok, _ := items.EffectActive(ctx, tx, f.FarmID, "earthworm", now); !ok {
				t.Fatal("active")
			}
			if ok, _ := items.EffectActive(ctx, tx, f.FarmID, "earthworm", now.Add(time.Hour)); ok {
				t.Fatal("expired")
			}
			if e, _ := items.ListEffects(ctx, tx, f.FarmID); len(e) != 1 {
				t.Fatal(e)
			}
			return nil
		})
	})
}
