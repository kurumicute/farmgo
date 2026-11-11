package titles

import (
	"testing"
	"time"

	"farmgm/internal/db"
)

func TestTitles(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inTx(t, d, func(tx *db.Tx) error {
		pid, fid := mkFarm(t, tx, "甲", nil)
		pid2, _ := mkFarm(t, tx, "乙", nil)
		_ = Grant(ctx, tx, fid, " 遺跡修復者 ", "ruins", now)
		_ = Grant(ctx, tx, fid, "遺跡修復者", "shop", now.Add(time.Hour)) // 已擁有：不覆蓋
		_ = Grant(ctx, tx, fid, "  ", "shop", now)
		_ = Grant(ctx, tx, fid, "釣魚大師", "achievement", now.Add(time.Minute))
		owned, err := Owned(ctx, tx, fid)
		if err != nil || len(owned) != 2 || owned[0].Title != "遺跡修復者" || owned[0].Source != "ruins" {
			t.Fatal(owned, err)
		}
		if _, err := tx.Exec(ctx, "UPDATE farms SET title = '遺跡修復者' WHERE id = $1", fid); err != nil {
			return err
		}
		sel, _ := SelectedByPlayer(ctx, tx, []int64{pid, pid2})
		if len(sel) != 1 || sel[pid] != "遺跡修復者" {
			t.Fatal(sel)
		}
		if sel, _ := SelectedByPlayer(ctx, tx, nil); len(sel) != 0 {
			t.Fatal(sel)
		}
		return nil
	})
}
