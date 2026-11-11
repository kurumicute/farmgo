package feedback

import (
	"testing"
	"time"

	"farmgm/internal/db"
)

func TestTouchAndRecords(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inTx(t, d, func(tx *db.Tx) error {
		_, fid := mkFarm(t, tx, "甲", nil)
		prev, err := TouchSeen(ctx, tx, fid, now)
		if err != nil || prev != nil {
			t.Fatal(prev, err)
		}
		prev, _ = TouchSeen(ctx, tx, fid, now.Add(time.Hour))
		if prev == nil || !prev.Equal(now) {
			t.Fatal(prev)
		}
		if old, _ := BumpRecord(ctx, tx, fid, "sale", 100); old != nil { // 已有資料列、第一次記錄
			t.Fatal(old)
		}
		if old, _ := BumpRecord(ctx, tx, fid, "sale", 50); old != nil { // 沒破紀錄
			t.Fatal(old)
		}
		old, _ := BumpRecord(ctx, tx, fid, "sale", 150)
		if old == nil || *old != 100 {
			t.Fatal(old)
		}
		_, fid2 := mkFarm(t, tx, "乙", nil)
		if old, err := BumpRecord(ctx, tx, fid2, "sale", 10); old != nil || err != nil { // 沒有資料列時建立
			t.Fatal(old, err)
		}
		if old, _ := BumpRecord(ctx, tx, fid2, "sale", 20); old == nil || *old != 10 {
			t.Fatal(old)
		}
		return nil
	})
}

func TestStealsAgainst(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inTx(t, d, func(tx *db.Tx) error {
		gid := mkGroup(t, tx, "群組")
		victim, _ := mkFarm(t, tx, "甲", &gid)
		thief, _ := mkFarm(t, tx, "乙", &gid)
		var crop int64
		if err := tx.QueryRow(ctx, "SELECT id FROM crop_types WHERE code = 'wheat'").Scan(&crop); err != nil {
			return err
		}
		ins := "INSERT INTO steal_records (thief_player_id, victim_player_id, created_at, plot_number, planting_no, " +
			"crop_type_id, quantity, group_id, outcome, blocked_by) VALUES ($1, $2, $3, 1, $4, $5, $6, $7, $8, $9)"
		dog := "pet_dog"
		rows := []struct {
			q       int
			outcome string
			by      *string
		}{{2, "success", nil}, {1, "blocked", &dog}, {1, "success", nil}}
		for i, r := range rows {
			if _, err := tx.Exec(ctx, ins, thief, victim, now.Add(time.Duration(i+1)*time.Minute), i+1, crop, r.q, gid,
				r.outcome, r.by); err != nil {
				return err
			}
		}
		s, err := StealsAgainst(ctx, tx, victim, now)
		if err != nil || s.StolenTimes != 2 || s.StolenQuantity != 3 || s.Blocked["pet_dog"] != 1 ||
			len(s.Thieves) != 1 || s.Thieves[0] != "乙" {
			t.Fatal(s, err)
		}
		return nil
	})
}
