package groups_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestGroups(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 100, 3)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		guild := "123"
		g, err := groups.Create(ctx, tx, "新群", &guild)
		if err != nil || g.ID == 0 {
			t.Fatal(g, err)
		}
		if gg, _ := groups.GetByGuild(ctx, tx, "123"); gg == nil || gg.ID != g.ID {
			t.Fatal("by guild")
		}
		if ok, _ := groups.AddMember(ctx, tx, g.ID, f.PlayerID); !ok {
			t.Fatal("add")
		}
		if ok, _ := groups.AddMember(ctx, tx, g.ID, f.PlayerID); ok {
			t.Fatal("dup add")
		}
		if ok, _ := groups.IsMember(ctx, tx, g.ID, f.PlayerID); !ok {
			t.Fatal("is member")
		}
		list, err := groups.ListWithCounts(ctx, tx, &f.PlayerID)
		if err != nil || len(list) != 2 || list[1].MemberCount != 1 || !list[1].IsMember {
			t.Fatalf("%+v %v", list, err)
		}
		if list, _ = groups.ListWithCounts(ctx, tx, nil); list[0].IsMember {
			t.Fatal("nil player")
		}
		if ok, _ := groups.RemoveMember(ctx, tx, g.ID, f.PlayerID); !ok {
			t.Fatal("remove")
		}
		// 一格成熟、一格乾枯、一格空地
		ready, withered := now.Add(-time.Hour), now.Add(-20*time.Hour)
		if _, err := tx.Exec(ctx, `UPDATE farm_plots SET crop_type_id = (SELECT id FROM crop_types WHERE code = 'wheat'),
			planted_at = $2, watered_at = $2, ready_at = CASE plot_number WHEN 1 THEN $3::timestamptz ELSE $4::timestamptz END,
			remaining_quantity = 1, quality = 'normal', mutation = 'none'
			WHERE farm_id = $1 AND plot_number <= 2`, f.FarmID, withered.Add(-time.Hour), ready, withered); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory_items (farm_id, item_code, quantity)
			VALUES ($1, 'wheat', 4), ($1, 'wood', 9)`, f.FarmID); err != nil {
			return err
		}
		members, err := groups.MembersWithFarms(ctx, tx, f.GroupID, now, now.Add(-16*time.Hour))
		if err != nil || len(members) != 1 {
			t.Fatal(members, err)
		}
		m := members[0].Farm
		if m.Plots != 3 || m.Empty != 1 || m.Ready != 1 || m.Withered != 1 || m.WarehouseUsed != 4 || !m.RankedInGroup {
			t.Fatalf("%+v", m)
		}
		return nil
	})
}
