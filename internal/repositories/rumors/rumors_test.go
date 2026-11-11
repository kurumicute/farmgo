package rumors

import (
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
)

func TestDigs(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	day := time.Date(2026, 1, 4, 0, 0, 0, 0, clock.TaipeiTZ)
	var gid, a, b int64
	inTx(t, d, func(tx *db.Tx) error {
		gid = mkGroup(t, tx, "群組")
		_, a = mkFarm(t, tx, "甲", &gid)
		_, b = mkFarm(t, tx, "乙", &gid)
		if ok, _ := Dug(ctx, tx, a, day); ok {
			t.Fatal("dug")
		}
		if ok, _ := Found(ctx, tx, gid, day); ok {
			t.Fatal("found")
		}
		if err := Add(ctx, tx, a, day, gid, b, true, 100, now); err != nil {
			return err
		}
		if ok, _ := Dug(ctx, tx, a, day); !ok {
			t.Fatal("not dug")
		}
		if ok, _ := Found(ctx, tx, gid, day); !ok {
			t.Fatal("not found")
		}
		return nil
	})
	// 同一群組同一天只能挖到一次
	err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error { return Add(ctx, tx, b, day, gid, a, true, 50, now) })
	if !db.IsUniqueViolation(err, "") {
		t.Fatal(err)
	}
}
