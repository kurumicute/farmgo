package services

// 對應 Python tests/test_database.py 的服務層部分（啟動檢查在 internal/db/db_test.go）。

import (
	"context"
	"slices"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
	"farmgm/internal/game/gameerr"
)

func TestInsertAndQuery(t *testing.T) {
	e := setup(t)
	p := must[PlayerInfo](t)(e.s.CreatePlayer(e.ctx, "資料庫測試", nil, nil))
	if n := e.text("SELECT display_name FROM players WHERE id = $1", p.ID); n != "資料庫測試" {
		t.Fatal(n)
	}
	var codes, notBuyable []string
	repotestQuery(t, e, "SELECT code FROM crop_types ORDER BY code", &codes)
	repotestQuery(t, e, "SELECT code FROM crop_types WHERE NOT shop_buyable ORDER BY code", &notBuyable)
	want := []string{"amber_melon", "carrot", "corn", "grape", "moonflower", "pumpkin", "strawberry", "tomato", "watermelon", "wheat"}
	if !slices.Equal(codes, want) || !slices.Equal(notBuyable, []string{"amber_melon", "moonflower"}) {
		t.Fatal(codes, notBuyable)
	}
	if e.scalar("SELECT seed_price FROM crop_types WHERE code = 'strawberry'") != 60 ||
		e.scalar("SELECT grow_seconds FROM crop_types WHERE code = 'strawberry'") != 600 {
		t.Fatal("草莓的種子價格或生長時間不符")
	}
}

func TestConstraintsEnforced(t *testing.T) {
	cases := map[string]string{
		"負金幣":     "UPDATE farms SET gold = -1",
		"負庫存":     "UPDATE inventory_items SET quantity = -1",
		"重複土地編號":  "INSERT INTO farm_plots (farm_id, plot_number) SELECT id, 1 FROM farms",
		"重複庫存":    "INSERT INTO inventory_items (farm_id, item_code, quantity) SELECT farm_id, item_code, 1 FROM inventory_items",
		"有作物沒有時間": "UPDATE farm_plots SET crop_type_id = 1 WHERE plot_number = 1",
		"不存在的玩家":  "INSERT INTO farms (player_id, name, level, gold, warehouse_capacity) VALUES (999999, 'x', 1, 0, 1)",
		"未知交易類型":  "INSERT INTO game_transactions (farm_id, event_type, description, created_at) SELECT id, 'hack', 'x', now() FROM farms",
		"外部帳號不完整": "INSERT INTO players (display_name, external_platform) VALUES ('x', 'discord')",
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			pid := e.player()
			e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
			err := e.d.InTx(e.ctx, db.TxOptions{}, func(tx *db.Tx) error {
				_, err := tx.Exec(e.ctx, sql)
				return err
			})
			if !isIntegrity(err) {
				t.Fatal(err)
			}
		})
	}
}

func TestOneFarmPerPlayerAndUniqueExternalIdentity(t *testing.T) {
	e := setup(t)
	platform, uid := "discord", "1234567890"
	p := must[PlayerInfo](t)(e.s.CreatePlayer(e.ctx, "discord 玩家", &platform, &uid))
	_, err := e.s.CreatePlayer(e.ctx, "重複綁定", &platform, &uid)
	wantKind(t, err, gameerr.AlreadyExists)
	if found := must[*PlayerInfo](t)(e.s.GetPlayerByExternal(e.ctx, platform, uid)); found == nil || found.ID != p.ID {
		t.Fatal(found)
	}
	g := e.group("群")
	must[FarmStatus](t)(e.s.CreateFarm(e.ctx, p.ID, "", &g))
	err = e.d.InTx(e.ctx, db.TxOptions{}, func(tx *db.Tx) error {
		_, err := tx.Exec(e.ctx, "INSERT INTO farms (player_id, name, level, gold, warehouse_capacity) VALUES ($1, '第二座', 1, 0, 1)", p.ID)
		return err
	})
	if !isIntegrity(err) {
		t.Fatal(err)
	}
}

func TestDataPersistsAcrossPools(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 3))
	d, err := db.Open(e.ctx, dbtest.URL(), db.PoolOptions{MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var gold, qty, plots int
	if err := d.InTx(e.ctx, db.TxOptions{ReadOnly: true}, func(tx *db.Tx) error {
		ctx := context.Background()
		return tx.QueryRow(ctx, `SELECT f.gold,
			(SELECT quantity FROM inventory_items WHERE farm_id = f.id),
			(SELECT count(*) FROM farm_plots WHERE farm_id = f.id)
			FROM farms f WHERE f.player_id = $1`, pid).Scan(&gold, &qty, &plots)
	}); err != nil {
		t.Fatal(err)
	}
	if gold != 470 || qty != 3 || plots != 4 {
		t.Fatal(gold, qty, plots)
	}
}
