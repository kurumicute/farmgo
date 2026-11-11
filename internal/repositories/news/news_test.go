package news

import (
	"testing"
	"time"

	"farmgm/internal/db"
)

func TestDailyAggregates(t *testing.T) {
	d := setup(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	inTx(t, d, func(tx *db.Tx) error {
		gid := mkGroup(t, tx, "群組")
		_, a := mkFarm(t, tx, "甲", &gid)
		_, other := mkFarm(t, tx, "乙", nil)
		ins := "INSERT INTO game_transactions (farm_id, event_type, item_code, quantity, gold_delta, description, created_at, " +
			"quality, mutation) VALUES ($1, $2, $3, $4, $5, '', $6, $7, $8)"
		for _, r := range []struct {
			farm      int64
			ev, item  string
			q, g      int
			at        time.Time
			qual, mut *string
		}{
			{a, "harvest", "wheat", 3, 0, start.Add(time.Hour), strp("normal"), strp("giant")},
			{a, "harvest", "wheat", 2, 0, start.Add(2 * time.Hour), strp("legendary"), strp("none")},
			{a, "harvest", "carrot", 9, 0, start.Add(2 * time.Hour), strp("normal"), strp("fast")}, // 快速生長不上頭條
			{a, "sell", "wheat", 5, 50, start.Add(3 * time.Hour), nil, nil},
			{a, "sell", "wheat", 5, 50, end, nil, nil},                                   // 期間外
			{other, "harvest", "wheat", 7, 0, start.Add(time.Hour), nil, strp("golden")}, // 其他群組
		} {
			if _, err := tx.Exec(ctx, ins, r.farm, r.ev, r.item, r.q, r.g, r.at, r.qual, r.mut); err != nil {
				return err
			}
		}
		ev, err := FarmEvents(ctx, tx, gid, start, end)
		if err != nil || len(ev) != 2 {
			t.Fatal(ev, err)
		}
		for _, e := range ev {
			if e.EventType == "harvest" && (e.Count != 3 || e.Quantity != 14) || e.EventType == "sell" && e.Gold != 50 {
				t.Fatal(ev)
			}
		}
		sp, err := SpecialHarvests(ctx, tx, gid, start, end)
		if err != nil || len(sp) != 2 || sp[0].Quantity != 3 || *sp[0].Mutation != "giant" {
			t.Fatal(sp, err)
		}
		if f, _ := BestFishOf(ctx, tx, gid, start, end); f != nil {
			t.Fatal(f)
		}
		names, _ := FarmNames(ctx, tx, []int64{a, other})
		if names[a] != "甲的農場" || len(names) != 2 {
			t.Fatal(names)
		}
		return nil
	})
}

func strp(s string) *string { return &s }
