package farms_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestFarmAndPlots(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 500, 2)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		farm, err := farms.GetByPlayer(ctx, tx, f.PlayerID, true)
		if err != nil || farm == nil || farm.Gold != 500 {
			t.Fatal(farm, err)
		}
		if g, _ := farms.GetByID(ctx, tx, f.FarmID, true); g == nil || g.PlayerID != f.PlayerID {
			t.Fatal("by id")
		}
		nums, err := farms.AddPlots(ctx, tx, f.FarmID, 2)
		if err != nil || len(nums) != 2 || nums[0] != 3 || nums[1] != 4 {
			t.Fatal(nums, err)
		}
		if n, _ := farms.CountPlots(ctx, tx, f.FarmID); n != 4 {
			t.Fatal(n)
		}
		wheat, _ := crops.ByCode(ctx, tx, "wheat", false)
		plot, _ := farms.GetPlot(ctx, tx, f.FarmID, 2, true)
		if err := farms.Plant(ctx, tx, plot, wheat.ID, 3, now, "good", "none"); err != nil {
			return err
		}
		if err := farms.Water(ctx, tx, plot, now, now.Add(time.Minute)); err != nil {
			return err
		}
		plots, err := farms.ListPlots(ctx, tx, f.FarmID, true)
		if err != nil || len(plots) != 4 || plots[1].Crop == nil || plots[1].Crop.Code != "wheat" || plots[0].Crop != nil {
			t.Fatal(plots, err)
		}
		if plots[1].PlantingNo != 1 || *plots[1].RemainingQuantity != 3 || plots[1].ReadyAt == nil || *plots[1].Quality != "good" {
			t.Fatalf("%+v", plots[1].FarmPlot)
		}
		if err := farms.Clear(ctx, tx, plot); err != nil {
			return err
		}
		p2, _ := farms.GetPlot(ctx, tx, f.FarmID, 2, false)
		if p2.CropTypeID != nil || p2.PlantingNo != 1 || plot.CropTypeID != nil {
			t.Fatalf("%+v", p2)
		}
		return nil
	})
}

func TestCreateFarm(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 1)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		var pid int64
		if err := tx.QueryRow(ctx, "INSERT INTO players (display_name) VALUES ('乙') RETURNING id").Scan(&pid); err != nil {
			return err
		}
		farm, err := farms.Create(ctx, tx, pid, "乙的農場", 1, 500, 50, f.GroupID)
		if err != nil || farm.ID == 0 || *farm.HomeGroupID != f.GroupID || farm.GreenhouseLevel != 0 {
			t.Fatal(farm, err)
		}
		return tx.ReadScope(func() error {
			a, _ := farms.GetByPlayer(ctx, tx, pid, false)
			b, _ := farms.GetByPlayer(ctx, tx, pid, false)
			if a != b { // 讀取範圍中同一個指標
				t.Fatal("read memo")
			}
			return nil
		})
	})
}
