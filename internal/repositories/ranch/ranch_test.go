package ranch

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
)

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func setup(t *testing.T) *db.DB {
	d := dbtest.Open(t)
	dbtest.Reset(t, d)
	return d
}

func run(t *testing.T, d *db.DB, fn func(ctx context.Context, tx *db.Tx) error) {
	t.Helper()
	if err := d.InTx(context.Background(), db.TxOptions{}, func(tx *db.Tx) error {
		return fn(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func makeFarm(ctx context.Context, t *testing.T, tx *db.Tx, name string) int64 {
	t.Helper()
	pid, err := db.InsertReturningID(ctx, tx, "players", db.C("display_name", name))
	if err != nil {
		t.Fatal(err)
	}
	fid, err := db.InsertReturningID(ctx, tx, "farms", db.C("player_id", pid), db.C("name", name+"農場"),
		db.C("level", 1), db.C("gold", 500), db.C("warehouse_capacity", 50))
	if err != nil {
		t.Fatal(err)
	}
	return fid
}

func TestBuildingsAndReadMemo(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		f := makeFarm(ctx, t, tx, "甲")
		return tx.ReadScope(func() error {
			n, err := CountBuildings(ctx, tx, f, "barn")
			if err != nil || n != 0 {
				t.Fatalf("n=%d err=%v", n, err)
			}
			if _, err := AddBuilding(ctx, tx, f, "mill", 1, now); err != nil {
				return err
			}
			if _, err := AddBuilding(ctx, tx, f, "barn", 2, now); err != nil {
				return err
			}
			b, err := AddBuilding(ctx, tx, f, "barn", 1, now)
			if err != nil || b.ID == 0 || b.BuildingCode != "barn" {
				t.Fatalf("%+v %v", b, err)
			}
			n, _ = CountBuildings(ctx, tx, f, "barn") // 寫入後讀取快取已清空
			if n != 2 {
				t.Fatalf("count after insert = %d", n)
			}
			list, err := ListBuildings(ctx, tx, f)
			if err != nil {
				return err
			}
			if len(list) != 3 || list[0].BuildingCode != "barn" || list[0].Slot != 1 || list[2].BuildingCode != "mill" {
				t.Fatalf("%+v", list)
			}
			return nil
		})
	})
}

func TestAnimalsNumbering(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		f := makeFarm(ctx, t, tx, "甲")
		nums, err := AddAnimals(ctx, tx, f, "chicken", 3, now)
		if err != nil || len(nums) != 3 || nums[0] != 1 || nums[2] != 3 {
			t.Fatalf("%v %v", nums, err)
		}
		nums, _ = AddAnimals(ctx, tx, f, "cow", 2, now)
		if nums[0] != 4 || nums[1] != 5 {
			t.Fatal(nums)
		}
		if n, _ := CountAnimals(ctx, tx, f); n != 5 {
			t.Fatal(n)
		}
		a, err := GetAnimal(ctx, tx, f, 4, true)
		if err != nil || a == nil || a.AnimalCode != "cow" {
			t.Fatalf("%+v %v", a, err)
		}
		if a, _ := GetAnimal(ctx, tx, f, 99, false); a != nil {
			t.Fatal("expected nil")
		}
		list, _ := ListAnimals(ctx, tx, f, true)
		if len(list) != 5 || list[0].AnimalNo != 1 {
			t.Fatal(list)
		}
		ranch, err := FarmsWithRanch(ctx, tx)
		if err != nil || len(ranch) != 1 || ranch[0].ID != f || ranch[0].PlayerName != "甲" {
			t.Fatalf("%+v %v", ranch, err)
		}
		return nil
	})
}

func TestOrders(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		f := makeFarm(ctx, t, tx, "甲")
		other := makeFarm(ctx, t, tx, "乙")
		o, err := AddOrder(ctx, tx, NewOrder{FarmID: f, RecipeCode: "flour", BuildingCode: "mill", Quantity: 2,
			Inputs: map[string]int{"wheat": 6}, OutputCode: "flour", OutputQuantity: 2,
			StartedAt: now, ReadyAt: now.Add(time.Hour)})
		if err != nil || o.Status != "in_progress" || o.Inputs["wheat"] != 6 {
			t.Fatalf("%+v %v", o, err)
		}
		if got, _ := GetOrder(ctx, tx, other, o.ID, false); got != nil {
			t.Fatal("other farm must not see the order")
		}
		if got, _ := GetOrder(ctx, tx, f, o.ID, true); got == nil {
			t.Fatal("own order missing")
		}
		if b, _ := BusyOrder(ctx, tx, f, "mill", now); b == nil || b.ID != o.ID {
			t.Fatal("busy order")
		}
		if b, _ := BusyOrder(ctx, tx, f, "mill", now.Add(time.Hour)); b != nil {
			t.Fatal("finished order is not busy")
		}
		collected := now
		if _, err := AddOrder(ctx, tx, NewOrder{FarmID: other, RecipeCode: "bread", BuildingCode: "bakery", Quantity: 1,
			OutputCode: "bread", OutputQuantity: 1, Status: "collected", StartedAt: now.Add(time.Minute),
			ReadyAt: now.Add(2 * time.Hour), CollectedAt: &collected}); err != nil {
			return err
		}
		all, _ := ListOrders(ctx, tx, nil, true, 50)
		if len(all) != 2 || all[0].RecipeCode != "bread" || all[0].PlayerName != "乙" || all[1].FarmName != "甲農場" {
			t.Fatalf("%+v", all)
		}
		open, _ := ListOrders(ctx, tx, nil, false, 50)
		if len(open) != 1 || open[0].ID != o.ID {
			t.Fatalf("%+v", open)
		}
		mine, _ := ListOrders(ctx, tx, &other, true, 50)
		if len(mine) != 1 || mine[0].FarmID != other {
			t.Fatalf("%+v", mine)
		}
		return nil
	})
}
