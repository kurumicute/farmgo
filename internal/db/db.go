// Package db 資料庫連線、交易與交易內快取（對應 Python app/db/session.py、txcache.py）。
//
// SQLAlchemy 的 Session 在 Go 以 *Tx 表示：每個公開服務方法 = 一筆交易，repository 一律接收 *Tx。
// Go 沒有 ORM 的 autoflush：修改資料一律寫明確的 SQL（見 sqlutil.go 的 UpdateByID／Insert）。
package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Q 可執行 SQL 的對象（*Tx 實作此介面；只讀工具函式可接受 Q）。
type Q interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB 連線池。
type DB struct {
	Pool        *pgxpool.Pool
	poolTimeout time.Duration // 取得連線的逾時（Python pool_timeout）；0 = 只受 ctx 限制
}

// ErrPoolTimeout 在 PoolTimeout 內取不到連線（連線池耗盡；Python sqlalchemy.exc.TimeoutError）。交易沒有開始，可以讓使用者稍後再試。
var ErrPoolTimeout = errors.New("取得資料庫連線逾時")

// Acquire 取得一條連線，最多等 PoolTimeout（逾時回傳 ErrPoolTimeout）；用完要 Release。
func (d *DB) Acquire(ctx context.Context) (*pgxpool.Conn, error) {
	if d.poolTimeout <= 0 {
		return d.Pool.Acquire(ctx)
	}
	actx, cancel := context.WithTimeout(ctx, d.poolTimeout)
	defer cancel()
	conn, err := d.Pool.Acquire(actx)
	if err != nil && ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		return nil, ErrPoolTimeout
	}
	return conn, err
}

// PoolOptions 連線池設定（對應 Settings 的 DB_POOL_*）。
type PoolOptions struct {
	MaxConns    int
	PoolTimeout time.Duration // 取得連線的逾時；0 = 不限制
	// Tracer 選用的 SQL 追蹤（測試計算 SQL 次數用；正式程式不設定）。也實作 pgx.BatchTracer 時會收到 Batch 的事件。
	Tracer pgx.QueryTracer
}

// Open 建立連線池。一律以 UTC 與資料庫溝通；連線逾時 5 秒；pgxpool 會定期檢查並換掉失效連線。
// url 必須是 pgx 可用的 postgres:// 形式（config.Settings.PgxDatabaseURL）。錯誤訊息不含連線字串。
func Open(ctx context.Context, url string, opt PoolOptions) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("DATABASE_URL 格式不正確")
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	if opt.Tracer != nil {
		cfg.ConnConfig.Tracer = opt.Tracer
	}
	// timestamptz 一律讀回 UTC（pgx 預設用本機時區），與 Python 版相同；比較時間仍應使用 Equal。
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{
			Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
		})
		return nil
	}
	if opt.MaxConns > 0 {
		cfg.MaxConns = int32(opt.MaxConns)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("無法建立資料庫連線池（%T）", err)
	}
	return &DB{Pool: pool, poolTimeout: opt.PoolTimeout}, nil
}

func (d *DB) Close() { d.Pool.Close() }

// TxOptions 交易選項。
type TxOptions struct {
	ReadOnly bool // SET TRANSACTION READ ONLY（監控查詢）
}

// InTx 執行一筆交易：fn 回傳錯誤時回滾，否則提交。交易中寫過遊戲目錄時，結束後（不論成敗）清空目錄快取。
func (d *DB) InTx(ctx context.Context, opt TxOptions, fn func(t *Tx) error) (err error) {
	txo := pgx.TxOptions{}
	if opt.ReadOnly {
		txo.AccessMode = pgx.ReadOnly
	}
	conn, err := d.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	ptx, err := conn.BeginTx(ctx, txo)
	if err != nil {
		return err
	}
	t := newTx(ptx)
	defer func() {
		if t.catalogDirty {
			CatalogClear()
			if err == nil && CatalogChanged != nil {
				CatalogChanged() // 已提交：通知其他程序（Redis）
			}
		}
	}()
	defer func() {
		if p := recover(); p != nil {
			_ = ptx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()
	if err = fn(t); err != nil {
		_ = ptx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err = t.FlushWrites(ctx); err != nil {
		_ = ptx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return ptx.Commit(ctx)
}

// IsRetryable 死結（40P01）或序列化失敗（40001）：整筆交易可以安全重試。
func IsRetryable(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "40P01" || pg.Code == "40001"
	}
	return false
}

// IsUniqueViolation 違反唯一限制（23505）；constraint 不為空時也比對限制名稱。
func IsUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return constraint == "" || pg.ConstraintName == constraint
	}
	return false
}

// ---------- Tx ----------

// Tx 一筆交易（對應 SQLAlchemy Session），附帶交易內快取。不可跨 goroutine 同時使用。
type Tx struct {
	tx pgx.Tx

	bulk  map[any]any // Bulk() 期間的逐格快取；nil = 不在 bulk 中
	read  map[any]any // ReadScope() 期間的讀取快取；nil = 不在讀取範圍中
	order []any       // 延後寫入第一次累積的順序
	bufs  map[any]Flusher

	flushing     bool
	catalogDirty bool

	// Info 服務層自用的交易資訊（對應 Session.info，例如面板共用交易的標記）。
	Info map[string]any
}

func newTx(ptx pgx.Tx) *Tx {
	return &Tx{tx: ptx, Info: map[string]any{}}
}

// Raw 底層 pgx.Tx（只供 COPY 等特殊用途）。
func (t *Tx) Raw() pgx.Tx { return t.tx }

var (
	writeRe   = regexp.MustCompile(`(?is)\b(insert\s+into|update\s+[a-z_"]+\s+set|delete\s+from|merge\s+into)\b`)
	catalogRe = regexp.MustCompile(`(?is)\b(?:insert\s+into|update|delete\s+from)\s+"?(crop_types|animal_types|fish_types|crop_genetics_settings)\b`)
)

func (t *Tx) noteSQL(sql string) {
	if writeRe.MatchString(sql) {
		t.clearReads()
		if catalogRe.MatchString(sql) {
			t.catalogDirty = true
		}
	}
}

// Exec 執行語句。先送出累積的延後寫入（查詢要看得到它們）；寫入語句會清空讀取快取。
func (t *Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := t.FlushWrites(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	return t.ExecNoFlush(ctx, sql, args...)
}

// Query 查詢。先送出累積的延後寫入。
func (t *Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := t.FlushWrites(ctx); err != nil {
		return nil, err
	}
	t.noteSQL(sql)
	return t.tx.Query(ctx, sql, args...)
}

// QueryRow 查詢單列。先送出累積的延後寫入。
func (t *Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := t.FlushWrites(ctx); err != nil {
		return errRow{err}
	}
	t.noteSQL(sql)
	return t.tx.QueryRow(ctx, sql, args...)
}

// ExecNoFlush 不先送出延後寫入（對應 Python txcache.core_execute）：只用於不依賴尚未送出內容的寫入，
// 以及延後寫入的 Flusher 本身。仍會清空讀取快取。
func (t *Tx) ExecNoFlush(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.noteSQL(sql)
	return t.tx.Exec(ctx, sql, args...)
}

// QueryRowNoFlush 同 ExecNoFlush，用於 INSERT … RETURNING。
func (t *Tx) QueryRowNoFlush(ctx context.Context, sql string, args ...any) pgx.Row {
	t.noteSQL(sql)
	return t.tx.QueryRow(ctx, sql, args...)
}

// SendBatchNoFlush 送出 pgx.Batch（例如一次 executemany 的交易紀錄）。
func (t *Tx) SendBatchNoFlush(ctx context.Context, b *pgx.Batch) error {
	t.clearReads()
	return t.tx.SendBatch(ctx, b).Close()
}

// Savepoint 在 SAVEPOINT 中執行 fn；fn 失敗只回滾 SAVEPOINT 之後的部分（錯誤照樣回傳）。
func (t *Tx) Savepoint(ctx context.Context, fn func() error) error {
	if err := t.FlushWrites(ctx); err != nil {
		return err
	}
	sp, err := t.tx.Begin(ctx)
	if err != nil {
		return err
	}
	outer := t.tx
	t.tx = sp
	err = fn()
	if err == nil {
		err = t.FlushWrites(ctx)
	}
	t.tx = outer
	if err != nil {
		t.discardWrites()
		t.clearReads()
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return sp.Commit(ctx)
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }
