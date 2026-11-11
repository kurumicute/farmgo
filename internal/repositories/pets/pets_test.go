package pets

import (
	"testing"
	"time"

	"farmgm/internal/db"
)

func TestPets(t *testing.T) {
	d := setup(t)
	inTx(t, d, func(tx *db.Tx) error {
		_, fid := mkFarm(t, tx, "甲", nil)
		if lv, _ := Level(ctx, tx, fid, "cat"); lv != nil {
			t.Fatal(lv)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO farm_pets (farm_id, pet_code, level, bought_at) VALUES ($1, 'cat', 2, $2)",
			fid, time.Now()); err != nil {
			return err
		}
		lv, _ := Level(ctx, tx, fid, "cat")
		all, _ := Levels(ctx, tx, fid)
		p, _ := Get(ctx, tx, fid, "cat")
		if lv == nil || *lv != 2 || all["cat"] != 2 || len(all) != 1 || p == nil || p.Level != 2 {
			t.Fatal(lv, all, p)
		}
		return nil
	})
}
