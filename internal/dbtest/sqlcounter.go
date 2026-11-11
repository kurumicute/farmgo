package dbtest

// SQL 計數器（對應 Python 測試的 SqlCounter：監聽 engine 的 before_cursor_execute）。
// 安裝在共用的測試連線池上；Start 之後才開始記錄，Stop 停止並回傳期間執行的 SQL。
//
// pgx.Batch 整批只記一次（第一條 SQL），對應 SQLAlchemy executemany 只觸發一次 before_cursor_execute。

import (
	"context"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// SQLCounter 記錄執行的 SQL（空白已正規化成單一空格）。可同時被多條連線使用。
type SQLCounter struct {
	mu    sync.Mutex
	on    bool
	stmts []string
}

// SQL 共用測試連線池的計數器。
var SQL = &SQLCounter{}

func (c *SQLCounter) add(sql string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.on {
		c.stmts = append(c.stmts, strings.Join(strings.Fields(sql), " "))
	}
}

// Start 清空並開始記錄；測試結束時自動停止。同一時間只能有一個測試使用（測試本來就以 -p 1 依序執行）。
func (c *SQLCounter) Start(t interface{ Cleanup(func()) }) {
	c.mu.Lock()
	c.on, c.stmts = true, nil
	c.mu.Unlock()
	t.Cleanup(func() { c.Stop() })
}

// Stop 停止記錄，回傳 Start 之後執行的 SQL。
func (c *SQLCounter) Stop() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.on = false
	return append([]string(nil), c.stmts...)
}

// Statements 目前為止記錄的 SQL（不停止）。
func (c *SQLCounter) Statements() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.stmts...)
}

// Count 以 prefix 開頭的 SQL 數量（Python SqlCounter.count）。
func (c *SQLCounter) Count(prefix string) int {
	n := 0
	for _, s := range c.Statements() {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

// CountContaining 含有 sub 的 SQL 數量（Go 的查詢寫法與 SQLAlchemy 產生的不同時使用，例如 "FROM crop_types"）。
func (c *SQLCounter) CountContaining(sub string) int {
	n := 0
	for _, s := range c.Statements() {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func (c *SQLCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.add(data.SQL)
	return ctx
}

func (c *SQLCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *SQLCounter) TraceBatchStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	if data.Batch != nil && len(data.Batch.QueuedQueries) > 0 {
		c.add(data.Batch.QueuedQueries[0].SQL)
	}
	return ctx
}

func (c *SQLCounter) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

func (c *SQLCounter) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
