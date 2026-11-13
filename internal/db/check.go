package db

// 啟動檢查（對應 Python app/db/check.py）：確認資料庫可連線且 Migration 已套用到最新版本（只檢查，不修改資料）。
// Migration 仍由 Python 專案的 Alembic 管理；Python 新增 Migration 並更新 Go 對應程式後，要同步修改 AlembicHead。

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// AlembicHead Go 版程式對應的 Alembic 版本。
const AlembicHead = "0039"

// StartupCheckError 啟動檢查失敗。
type StartupCheckError struct{ Msg string }

func (e *StartupCheckError) Error() string { return e.Msg }

// CheckDatabase 確認可連線且 alembic_version 等於 AlembicHead。
func (d *DB) CheckDatabase(ctx context.Context) error {
	var current []string
	err := func() error {
		if _, err := d.Pool.Exec(ctx, "SELECT 1"); err != nil {
			return err
		}
		rows, err := d.Pool.Query(ctx, "SELECT version_num FROM alembic_version")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			current = append(current, v)
		}
		return rows.Err()
	}()
	if err != nil {
		// 只回報例外類型，避免把含帳密的連線字串印出
		return &StartupCheckError{fmt.Sprintf(
			"無法連線到 PostgreSQL 或讀取 Migration 版本，請確認資料庫已啟動且 .env 的 DATABASE_URL 正確（%T）。", err)}
	}
	sort.Strings(current)
	if len(current) != 1 || current[0] != AlembicHead {
		now := strings.Join(current, ", ")
		if now == "" {
			now = "無"
		}
		return &StartupCheckError{fmt.Sprintf(
			"資料庫 Migration 版本與程式不符（目前：%s；程式需要：%s）。請在 Python 專案執行：alembic upgrade head", now, AlembicHead)}
	}
	return nil
}
