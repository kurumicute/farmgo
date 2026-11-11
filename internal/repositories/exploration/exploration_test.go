package exploration

import (
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

func TestCooldownAndLogs(t *testing.T) {
	d := setup(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inTx(t, d, func(tx *db.Tx) error {
		_, fid := mkFarm(t, tx, "甲", nil)
		row, err := SetCooldown(ctx, tx, nil, fid, "forest", now, now.Add(time.Hour))
		if err != nil || row.ExploreCount != 1 {
			t.Fatal(row, err)
		}
		locked, err := GetCooldown(ctx, tx, fid, "forest", true)
		if err != nil || locked == nil {
			t.Fatal(locked, err)
		}
		row, err = SetCooldown(ctx, tx, locked, fid, "forest", now.Add(time.Hour), now.Add(2*time.Hour))
		if err != nil || row.ExploreCount != 2 || !row.NextAvailableAt.Equal(now.Add(2*time.Hour)) {
			t.Fatal(row, err)
		}
		all, err := ListCooldowns(ctx, tx, fid)
		if err != nil || len(all) != 1 || all["forest"].ExploreCount != 2 {
			t.Fatal(all, err)
		}
		log, err := AddLog(ctx, tx, models.ExplorationLog{FarmID: fid, AreaCode: "forest", FarmLevel: 1,
			Rewards: map[string]int{"stone": 3}, Events: []string{"假幣"}, ExploredAt: now, NextAvailableAt: now.Add(time.Hour)})
		if err != nil || log.ID == 0 {
			t.Fatal(log, err)
		}
		active, err := GetActive(ctx, tx, fid, true)
		if err != nil || active == nil || active.Rewards["stone"] != 3 || active.Events[0] != "假幣" {
			t.Fatal(active, err)
		}
		if logs, _ := ListLogs(ctx, tx, fid, 10); len(logs) != 0 {
			t.Fatal("unclaimed log listed")
		}
		if err := MarkClaimed(ctx, tx, log.ID, now.Add(time.Hour)); err != nil {
			return err
		}
		logs, err := ListLogs(ctx, tx, fid, 10)
		if err != nil || len(logs) != 1 {
			t.Fatal(logs, err)
		}
		if a, _ := GetActive(ctx, tx, fid, false); a != nil {
			t.Fatal("claimed log still active")
		}
		return nil
	})
	// 同一區域唯一：重複建立失敗
	err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		_, err := SetCooldown(ctx, tx, nil, 1, "forest", now, now.Add(time.Hour))
		return err
	})
	if !db.IsUniqueViolation(err, "") {
		t.Fatal(err)
	}
}
