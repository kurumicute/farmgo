package monitor

import (
	"context"
	"encoding/json"
	"strings"
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

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func run(t *testing.T, d *db.DB, fn func(ctx context.Context, tx *db.Tx) error) {
	t.Helper()
	if err := d.InTx(context.Background(), db.TxOptions{ReadOnly: false}, func(tx *db.Tx) error {
		return fn(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	farmA, farmB, wheat int64
	group               int64
}

func seed(ctx context.Context, tx *db.Tx) fixture {
	var f fixture
	f.wheat, _, _ = db.QueryValue[int64](ctx, tx, "SELECT id FROM crop_types WHERE code = 'wheat'")
	f.group = must(db.InsertReturningID(ctx, tx, "groups", db.C("name", "家")))
	pa := must(db.InsertReturningID(ctx, tx, "players", db.C("display_name", "甲_100%")))
	pb := must(db.InsertReturningID(ctx, tx, "players", db.C("display_name", "乙")))
	must(db.InsertReturningID(ctx, tx, "players", db.C("display_name", "沒農場")))
	f.farmA = must(db.InsertReturningID(ctx, tx, "farms", db.C("player_id", pa), db.C("name", "A農場"), db.C("level", 2),
		db.C("gold", 300), db.C("warehouse_capacity", 50), db.C("home_group_id", f.group), db.C("updated_at", now)))
	f.farmB = must(db.InsertReturningID(ctx, tx, "farms", db.C("player_id", pb), db.C("name", "B農場"), db.C("level", 1),
		db.C("gold", 200), db.C("warehouse_capacity", 50), db.C("updated_at", now.Add(time.Minute))))
	plot := func(farm int64, no int, cols ...db.Col) {
		base := []db.Col{db.C("farm_id", farm), db.C("plot_number", no)}
		must(db.InsertReturningID(ctx, tx, "farm_plots", append(base, cols...)...))
	}
	grown := func(planted, ready time.Time) []db.Col {
		return []db.Col{db.C("crop_type_id", f.wheat), db.C("planted_at", planted), db.C("watered_at", planted),
			db.C("ready_at", ready), db.C("remaining_quantity", 2), db.C("quality", "normal"), db.C("mutation", "none")}
	}
	plot(f.farmA, 1)                                                           // 空地
	plot(f.farmA, 2, grown(now.Add(-time.Hour), now.Add(time.Hour))...)        // 生長中
	plot(f.farmA, 3, grown(now.Add(-2*time.Hour), now.Add(-time.Hour))...)     // 可收成
	plot(f.farmA, 4, grown(now.Add(-40*time.Hour), now.Add(-20*time.Hour))...) // 已乾枯
	plot(f.farmB, 1, db.C("crop_type_id", f.wheat), db.C("planted_at", now), db.C("remaining_quantity", 2),
		db.C("quality", "normal"), db.C("mutation", "none")) // 等待澆水
	must(db.InsertReturningID(ctx, tx, "inventory_items", db.C("farm_id", f.farmA), db.C("item_code", "wheat"), db.C("quantity", 5)))
	must(db.InsertReturningID(ctx, tx, "inventory_items", db.C("farm_id", f.farmA), db.C("item_code", "iron_ore"), db.C("quantity", 7)))
	must(db.InsertReturningID(ctx, tx, "gamble_prizes", db.C("farm_id", f.farmA), db.C("item_code", "wheat"),
		db.C("quality", "rare"), db.C("mutation", "none"), db.C("multiplier", 2), db.C("quantity", 3)))
	for i, gd := range []int{500, -20, 40} {
		must(db.InsertReturningID(ctx, tx, "game_transactions", db.C("farm_id", f.farmA), db.C("event_type", "sell"),
			db.C("gold_delta", gd), db.C("description", "t"), db.C("created_at", now.Add(time.Duration(i*40)*time.Minute))))
	}
	return f
}

func TestSummaryAndFarms(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		f := seed(ctx, tx)
		cutoff := now.Add(-16 * time.Hour)
		s := must(GetSummary(ctx, tx, now, now.Add(-time.Hour), cutoff))
		if s.Players != 3 || s.Farms != 2 || s.Groups != 1 || s.TotalGold != 500 || s.Plots != 5 || s.WaitingWater != 1 ||
			s.Growing != 1 || s.Ready != 1 || s.Withered != 1 || s.InventoryItems != 12 || s.TransactionsToday != 3 {
			t.Fatalf("%+v", s)
		}
		rows, total := func() ([]FarmRow, int) {
			r, n, err := Farms(ctx, tx, now, cutoff, "", 50, 0)
			if err != nil {
				t.Fatal(err)
			}
			return r, n
		}()
		if total != 2 || len(rows) != 2 || rows[0].FarmID != f.farmB {
			t.Fatalf("%+v", rows)
		}
		a := rows[1]
		if a.Plots != 4 || a.Empty != 1 || a.Growing != 1 || a.Ready != 1 || a.Withered != 1 || a.WarehouseUsed != 8 ||
			a.HomeGroupName == nil || *a.HomeGroupName != "家" || a.PlayerName != "甲_100%" {
			t.Fatalf("%+v", a) // 倉庫：小麥 5 + 賭場獎品 3，不含素材背包的鐵礦
		}
		if rows[0].HomeGroupName != nil || rows[0].WaitingWater != 1 {
			t.Fatalf("%+v", rows[0])
		}
		// LIKE 特殊字元要跳脫：「_100%」只比對字面
		hit, n, _ := Farms(ctx, tx, now, cutoff, "_100%", 50, 0)
		if n != 1 || len(hit) != 1 || hit[0].FarmID != f.farmA {
			t.Fatalf("%+v", hit)
		}
		miss, n, _ := Farms(ctx, tx, now, cutoff, "1_0", 50, 0)
		if n != 0 || len(miss) != 0 {
			t.Fatalf("%+v", miss)
		}
		paged, n, _ := Farms(ctx, tx, now, cutoff, "農場", 1, 1)
		if n != 2 || len(paged) != 1 || paged[0].FarmID != f.farmA {
			t.Fatalf("%+v", paged)
		}
		js := string(must(json.Marshal(a)))
		if !strings.Contains(js, `"home_group_name":"家"`) || !strings.Contains(js, `"warehouse_used":8`) {
			t.Fatal(js)
		}
		return nil
	})
}

func TestDetailPlotsInventoryTransactions(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		f := seed(ctx, tx)
		det := must(FarmDetail(ctx, tx, f.farmA))
		if det == nil || det.Name != "A農場" || det.PlayerName != "甲_100%" {
			t.Fatalf("%+v", det)
		}
		if none := must(FarmDetail(ctx, tx, 999)); none != nil {
			t.Fatal("expected nil")
		}
		plots := must(Plots(ctx, tx, f.farmA))
		if len(plots) != 4 || plots[0].Crop != nil || plots[1].Crop == nil || plots[1].Crop.Code != "wheat" {
			t.Fatalf("%+v", plots)
		}
		inv := must(Inventory(ctx, tx, f.farmA))
		if len(inv) != 2 || inv[0].ItemCode != "iron_ore" {
			t.Fatalf("%+v", inv)
		}
		txs := must(Transactions(ctx, tx, 2, nil))
		if len(txs) != 2 || txs[0].GoldDelta != 40 || txs[0].PlayerName != "甲_100%" {
			t.Fatalf("%+v", txs)
		}
		if none := must(Transactions(ctx, tx, 10, &f.farmB)); len(none) != 0 {
			t.Fatal(none)
		}
		hours := must(HourlyActivity(ctx, tx, now.Add(-time.Hour)))
		if len(hours) != 2 || hours[0].Count != 2 || hours[0].Gold != 480 || !hours[1].Hour.Equal(now.Add(time.Hour)) {
			t.Fatalf("%+v", hours)
		}
		if g := must(TotalGold(ctx, tx)); g != 500 {
			t.Fatal(g)
		}
		if !must(HasTransactions(ctx, tx)) {
			t.Fatal("has transactions")
		}
		dist := must(CropDistribution(ctx, tx, now, now.Add(-16*time.Hour)))
		var wheat *CropDist
		for i := range dist {
			if dist[i].Code == "wheat" {
				wheat = &dist[i]
			}
		}
		if wheat == nil || wheat.Growing != 1 || wheat.Ready != 1 {
			t.Fatalf("%+v", dist)
		}
		ps := must(Players(ctx, tx, 500))
		if len(ps) != 3 || ps[2].FarmID != nil || ps[0].FarmID == nil {
			t.Fatalf("%+v", ps)
		}
		return nil
	})
}

func TestEmptyDatabase(t *testing.T) {
	d := setup(t)
	run(t, d, func(ctx context.Context, tx *db.Tx) error {
		if must(HasTransactions(ctx, tx)) {
			t.Fatal("no transactions")
		}
		s := must(GetSummary(ctx, tx, now, now, now))
		if s.Plots != 0 || s.TotalGold != 0 {
			t.Fatalf("%+v", s)
		}
		return nil
	})
}
