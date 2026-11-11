package codex

import (
	"testing"
	"time"

	"farmgm/internal/db"
)

func TestRegisterImmediateAndDeferred(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inTx(t, d, func(tx *db.Tx) error {
		_, fid := mkFarm(t, tx, "甲", nil)
		_ = Register(ctx, tx, fid, "wheat", 2, "good", now)
		_ = Register(ctx, tx, fid, "wheat", 0, "legendary", now) // 0 不登記
		err := tx.Bulk(ctx, func() error {
			_ = Register(ctx, tx, fid, "wheat", 3, "", now.Add(time.Hour))
			_ = Register(ctx, tx, fid, "carrot", 1, "rare", now.Add(time.Hour))
			_ = Register(ctx, tx, fid, "carrot", 1, "legendary", now)
			// 查詢會先送出累積的寫入
			e, err := Entries(ctx, tx, fid)
			if err != nil {
				return err
			}
			if e["wheat"].Quantity != 5 || e["carrot"].Quantity != 2 || e["carrot"].BestQuality != 3 {
				t.Fatal(e)
			}
			return nil
		})
		if err != nil {
			return err
		}
		e, _ := Entries(ctx, tx, fid)
		if e["wheat"].BestQuality != 1 || !e["wheat"].FirstAt.Equal(now) || !e["carrot"].FirstAt.Equal(now) {
			t.Fatal(e)
		}
		return nil
	})
}
