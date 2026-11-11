package pets

import (
	"context"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
)

var ctx = context.Background()

func setup(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.Open(t)
	dbtest.Reset(t, d)
	return d
}

// inTx 在交易中執行 fn，失敗時 t.Fatal。
func inTx(t *testing.T, d *db.DB, fn func(tx *db.Tx) error) {
	t.Helper()
	if err := d.InTx(ctx, db.TxOptions{}, fn); err != nil {
		t.Fatal(err)
	}
}

// mkFarm 建立玩家與農場，回傳 (player_id, farm_id)。groupID 不為 nil 時設為所屬群組。
func mkFarm(t *testing.T, tx *db.Tx, name string, groupID *int64) (int64, int64) {
	t.Helper()
	var pid, fid int64
	if err := tx.QueryRow(ctx, "INSERT INTO players (display_name) VALUES ($1) RETURNING id", name).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, "INSERT INTO farms (player_id, name, level, gold, warehouse_capacity, home_group_id) "+
		"VALUES ($1, $2, 1, 500, 50, $3) RETURNING id", pid, name+"的農場", groupID).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	return pid, fid
}

// mkGroup 建立群組。
func mkGroup(t *testing.T, tx *db.Tx, name string) int64 {
	t.Helper()
	var gid int64
	if err := tx.QueryRow(ctx, "INSERT INTO groups (name) VALUES ($1) RETURNING id", name).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	return gid
}
