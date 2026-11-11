package ruins

import (
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
)

func TestRuinsAndGuardian(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	week := time.Date(2026, 1, 5, 0, 0, 0, 0, clock.TaipeiTZ)
	inTx(t, d, func(tx *db.Tx) error {
		gid := mkGroup(t, tx, "群組")
		_, a := mkFarm(t, tx, "甲", &gid)
		_, b := mkFarm(t, tx, "乙", &gid)
		if ok, _ := IsCompleted(ctx, tx, &gid); ok {
			t.Fatal("completed before creation")
		}
		if ok, _ := IsCompleted(ctx, tx, nil); ok {
			t.Fatal("nil group completed")
		}
		r, err := GetOrCreate(ctx, tx, gid, now)
		if err != nil || r.Stage != 0 || r.Progress == nil {
			t.Fatal(r, err)
		}
		r.Stage, r.Progress["stone"] = 3, 5
		r.CompletedAt = &now
		settled := week
		r.GuardianSettled = &settled
		if err := Save(ctx, tx, r); err != nil {
			return err
		}
		r2, _ := GetOrCreate(ctx, tx, gid, now)
		if r2.Stage != 3 || r2.Progress["stone"] != 5 || !r2.GuardianSettled.Equal(week) {
			t.Fatal(r2)
		}
		if ok, _ := IsCompleted(ctx, tx, &gid); !ok {
			t.Fatal("not completed")
		}
		_ = AddContribution(ctx, tx, gid, a, 3, now)
		_ = AddContribution(ctx, tx, gid, a, 4, now)
		_ = AddContribution(ctx, tx, gid, b, 2, now)
		if n, _ := Contribution(ctx, tx, gid, a); n != 7 {
			t.Fatal(n)
		}
		if n, _ := Contribution(ctx, tx, gid, 999); n != 0 {
			t.Fatal(n)
		}
		top, err := TopContributors(ctx, tx, gid, 10)
		if err != nil || len(top) != 2 || top[0].Quantity != 7 || top[0].Name != "甲的農場" {
			t.Fatal(top, err)
		}
		if ids, _ := ContributorFarms(ctx, tx, gid); len(ids) != 2 {
			t.Fatal(ids)
		}
		if n, _ := MemberFarms(ctx, tx, gid); n != 2 {
			t.Fatal(n)
		}
		g, err := EnsureGuardian(ctx, tx, gid, week, 5000)
		if err != nil || g.HPMax != 5000 || !g.WeekStart.Equal(week) {
			t.Fatal(g, err)
		}
		g2, _ := EnsureGuardian(ctx, tx, gid, week, 9999) // 已存在：不改 HP
		if g2.HPMax != 5000 {
			t.Fatal(g2)
		}
		g2.Damage, g2.DefeatedAt = 5000, &now
		if err := SaveGuardian(ctx, tx, g2); err != nil {
			return err
		}
		_ = AddHit(ctx, tx, gid, week, a, 100)
		_ = AddHit(ctx, tx, gid, week, a, 50)
		_ = AddHit(ctx, tx, gid, week, b, 30)
		if n, _ := MyHits(ctx, tx, gid, week, a); n != 150 {
			t.Fatal(n)
		}
		hits, _ := TopHits(ctx, tx, gid, week, 5)
		if len(hits) != 2 || hits[0].Damage != 150 {
			t.Fatal(hits)
		}
		g3, _ := GetGuardian(ctx, tx, gid, week, false)
		if g3.Damage != 5000 || g3.DefeatedAt == nil {
			t.Fatal(g3)
		}
		return nil
	})
}
