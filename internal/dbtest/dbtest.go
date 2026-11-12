// Package dbtest 資料庫測試輔助：從環境變數 FARMGM_TEST_DATABASE_URL 取得測試資料庫（名稱必須以 _test 結尾，
// 測試會清空遊戲資料表）。沒有設定時跳過需要資料庫的測試。
//
// 測試資料庫的 schema 由 Python 專案的 Alembic 建立：`alembic upgrade head`（目前 0038）。
package dbtest

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"farmgm/internal/db"
)

// EnvVar 測試資料庫連線字串的環境變數名稱（可用 SQLAlchemy 的 postgresql+psycopg:// 形式）。
const EnvVar = "FARMGM_TEST_DATABASE_URL"

var (
	once   sync.Once
	shared *db.DB
	errMsg string
)

// URL 回傳 pgx 形式的測試資料庫 URL；未設定或名稱不以 _test 結尾時回傳 ""。
func URL() string {
	raw := os.Getenv(EnvVar)
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme := raw[:i]
		if p := strings.Index(scheme, "+"); p >= 0 {
			raw = scheme[:p] + raw[i:]
		}
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.HasSuffix(strings.TrimPrefix(u.Path, "/"), "_test") {
		return ""
	}
	return raw
}

// Open 取得共用的測試連線池；沒有測試資料庫時 t.Skip。
func Open(t testing.TB) *db.DB {
	t.Helper()
	once.Do(func() {
		u := URL()
		if u == "" {
			errMsg = EnvVar + " 未設定或資料庫名稱不是以 _test 結尾"
			return
		}
		maxConns := 20 // 壓力測試可用 FARMGM_TEST_DB_MAX_CONNS 調整
		if n, err := strconv.Atoi(os.Getenv("FARMGM_TEST_DB_MAX_CONNS")); err == nil && n > 0 {
			maxConns = n
		}
		d, err := db.Open(context.Background(), u, db.PoolOptions{MaxConns: maxConns, Tracer: SQL})
		if err != nil {
			errMsg = err.Error()
			return
		}
		if err := d.CheckDatabase(context.Background()); err != nil {
			errMsg = err.Error()
			return
		}
		shared = d
	})
	if shared == nil {
		t.Skip("略過需要資料庫的測試：" + errMsg)
	}
	return shared
}
