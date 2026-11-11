// Package repotest repository 測試的共用夾具（只給 _test.go 使用）：清空遊戲資料表、建立玩家／群組／農場，並在交易中執行。
package repotest

import (
	"context"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
)

// Setup 開啟測試資料庫（沒有時 Skip）並清空遊戲資料表。執行測試時用 go test -p 1，避免不同套件同時清空。
func Setup(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.Open(t)
	dbtest.Reset(t, d)
	return d
}

// Tx 在一筆交易中執行 fn 並提交；錯誤時測試失敗。
func Tx(t *testing.T, d *db.DB, fn func(ctx context.Context, tx *db.Tx) error) {
	t.Helper()
	if err := d.InTx(context.Background(), db.TxOptions{}, func(tx *db.Tx) error {
		return fn(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

// Farm 建立的測試資料。
type Farm struct {
	PlayerID, GroupID, FarmID int64
}

// MakeFarm 建立玩家、群組（含成員）與農場（含指定格數的土地），回傳 ID。
func MakeFarm(t *testing.T, d *db.DB, name string, gold, plots int) Farm {
	t.Helper()
	var f Farm
	Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		if err := tx.QueryRow(ctx, "INSERT INTO players (display_name) VALUES ($1) RETURNING id", name).Scan(&f.PlayerID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "INSERT INTO groups (name) VALUES ($1) RETURNING id", name+"群").Scan(&f.GroupID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO group_members (group_id, player_id) VALUES ($1, $2)", f.GroupID, f.PlayerID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO farms (player_id, home_group_id, name, level, gold, warehouse_capacity)
			VALUES ($1, $2, $3, 1, $4, 50) RETURNING id`, f.PlayerID, f.GroupID, name+"農場", gold).Scan(&f.FarmID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO farm_plots (farm_id, plot_number) SELECT $1, generate_series(1, $2::int)", f.FarmID, plots)
		return err
	})
	return f
}
