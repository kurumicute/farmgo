package services

// 對應 Python tests/test_monitor_api.py 的 test_monitor_session_is_read_only_transaction：監控查詢在唯讀交易中執行，
// 資料庫層級拒絕任何寫入（SQLSTATE 25006 read_only_sql_transaction）。其餘監控 API 測試在 internal/web。

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"farmgm/internal/db"
)

func TestMonitorSessionIsReadOnlyTransaction(t *testing.T) {
	e := setup(t)
	pid := e.player()
	m := NewMonitorService(e.d, e.clock)
	err := m.readOnly(e.ctx, func(tx *db.Tx) error {
		_, err := tx.Exec(e.ctx, "UPDATE farms SET gold = 0")
		return err
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "25006" {
		t.Fatal(err)
	}
	if e.gold(pid) != 500 {
		t.Fatal("唯讀交易不應寫入")
	}
}
