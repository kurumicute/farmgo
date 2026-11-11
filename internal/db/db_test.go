package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
)

const tmpTable = "go_dbcore_tmp"

func setup(t *testing.T) *db.DB {
	d := dbtest.Open(t)
	ctx := context.Background()
	if _, err := d.Pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+tmpTable+" (id bigserial PRIMARY KEY, n int NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(ctx, "DROP TABLE IF EXISTS "+tmpTable) })
	if _, err := d.Pool.Exec(ctx, "TRUNCATE "+tmpTable); err != nil {
		t.Fatal(err)
	}
	return d
}

func count(t *testing.T, d *db.DB) int {
	var n int
	if err := d.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+tmpTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCheckDatabase(t *testing.T) {
	d := dbtest.Open(t)
	if err := d.CheckDatabase(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// 對應 Python test_startup_check_reports_connection_failure_without_secrets（不需要測試資料庫）。
func TestCheckDatabaseConnectionFailureHidesSecrets(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, "postgresql://user:supersecret@0.0.0.0:1/nope", db.PoolOptions{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	err = d.CheckDatabase(ctx)
	var se *db.StartupCheckError
	if !errors.As(err, &se) || strings.Contains(err.Error(), "supersecret") || !strings.Contains(err.Error(), "無法連線") {
		t.Fatal(err)
	}
}

func TestTimestamptzReadsUTC(t *testing.T) {
	d := dbtest.Open(t)
	var ts time.Time
	if err := d.Pool.QueryRow(context.Background(), "SELECT now()").Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts.Location() != time.UTC {
		t.Fatalf("location = %v", ts.Location())
	}
}

func TestInTxCommitAndRollback(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	if err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		return db.Insert(ctx, tx, tmpTable, db.C("n", 1))
	}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		if err := db.Insert(ctx, tx, tmpTable, db.C("n", 2)); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if got := count(t, d); got != 1 {
		t.Fatalf("rows = %d", got)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	err := d.InTx(ctx, db.TxOptions{ReadOnly: true}, func(tx *db.Tx) error {
		return db.Insert(ctx, tx, tmpTable, db.C("n", 1))
	})
	if err == nil {
		t.Fatal("read-only transaction accepted a write")
	}
}

type buf struct{ ns []int }

func (b *buf) Flush(ctx context.Context, tx *db.Tx) error {
	for _, n := range b.ns {
		if _, err := tx.ExecNoFlush(ctx, "INSERT INTO "+tmpTable+" (n) VALUES ($1)", n); err != nil {
			return err
		}
	}
	return nil
}

func TestDeferredWritesVisibleToNextQuery(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		if b, ok := db.Deferred(tx, "x", func() *buf { return &buf{} }); ok {
			t.Fatal("deferred outside bulk", b)
		}
		return tx.Bulk(ctx, func() error {
			b, ok := db.Deferred(tx, "x", func() *buf { return &buf{} })
			if !ok {
				t.Fatal("not deferred in bulk")
			}
			b.ns = append(b.ns, 1, 2)
			n, _, err := db.QueryValue[int](ctx, tx, "SELECT count(*) FROM "+tmpTable)
			if err != nil {
				return err
			}
			if n != 2 {
				t.Fatalf("query did not see deferred writes: %d", n)
			}
			b2, _ := db.Deferred(tx, "x", func() *buf { return &buf{} })
			b2.ns = append(b2.ns, 3)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, d); got != 3 {
		t.Fatalf("rows = %d", got)
	}
}

func TestBulkErrorDiscardsDeferred(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	boom := errors.New("boom")
	_ = d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		err := tx.Bulk(ctx, func() error {
			b, _ := db.Deferred(tx, "x", func() *buf { return &buf{} })
			b.ns = append(b.ns, 1)
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
		return nil // 外層提交：被丟棄的寫入不可出現
	})
	if got := count(t, d); got != 0 {
		t.Fatalf("rows = %d", got)
	}
}

func TestMemoAndAdjust(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	calls := 0
	compute := func() (int, error) { calls++; return 10, nil }
	_ = d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		_, _ = db.Memo(tx, "k", compute)
		_, _ = db.Memo(tx, "k", compute)
		if calls != 2 {
			t.Fatalf("outside bulk should not cache: %d", calls)
		}
		return tx.Bulk(ctx, func() error {
			_, _ = db.Memo(tx, "k", compute)
			tx.Adjust("k", 5)
			v, _ := db.Memo(tx, "k", compute)
			if calls != 3 || v != 15 {
				t.Fatalf("calls=%d v=%d", calls, v)
			}
			return nil
		})
	})
}

func TestReadMemoClearedByWrite(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	_ = d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		return tx.ReadScope(func() error {
			read := func() (int, error) {
				n, _, err := db.QueryValue[int](ctx, tx, "SELECT count(*) FROM "+tmpTable)
				return n, err
			}
			a, _ := db.ReadMemo(tx, "cnt", read)
			if _, err := db.InsertReturningID(ctx, tx, tmpTable, db.C("n", 1)); err != nil {
				return err
			}
			b, _ := db.ReadMemo(tx, "cnt", read)
			if a != 0 || b != 1 {
				t.Fatalf("a=%d b=%d", a, b)
			}
			// SELECT … FOR UPDATE 不算寫入
			if _, err := tx.Exec(ctx, "SELECT 1 FROM "+tmpTable+" FOR UPDATE"); err != nil {
				return err
			}
			return nil
		})
	})
}

func TestSavepointRollsBackOnlyInner(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		if err := db.Insert(ctx, tx, tmpTable, db.C("n", 1)); err != nil {
			return err
		}
		err := tx.Savepoint(ctx, func() error {
			if err := db.Insert(ctx, tx, tmpTable, db.C("n", 2)); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
		_, err = db.UpdateByID(ctx, tx, tmpTable, 1, db.C("n", 9))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, d); got != 1 {
		t.Fatalf("rows = %d", got)
	}
}

func TestUniqueViolationAndRetryable(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		_ = db.Insert(ctx, tx, tmpTable, db.C("id", 1), db.C("n", 1))
		return db.Insert(ctx, tx, tmpTable, db.C("id", 1), db.C("n", 1))
	})
	if !db.IsUniqueViolation(err, "") || db.IsRetryable(err) {
		t.Fatal(err)
	}
}

// TestPoolTimeout 連線池耗盡時，InTx 在 PoolTimeout 後回傳 ErrPoolTimeout（Python pool_timeout），不會無限等待。
func TestPoolTimeout(t *testing.T) {
	if dbtest.URL() == "" {
		t.Skip("略過需要資料庫的測試")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dbtest.URL(), db.PoolOptions{MaxConns: 1, PoolTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	held, err := d.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = d.InTx(ctx, db.TxOptions{}, func(*db.Tx) error { return nil })
	if !errors.Is(err, db.ErrPoolTimeout) || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	held.Release()
	if err := d.InTx(ctx, db.TxOptions{}, func(*db.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// 呼叫端自己的 ctx 取消時回傳 ctx 的錯誤，不是 ErrPoolTimeout
	held, _ = d.Acquire(ctx)
	defer held.Release()
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := d.InTx(cctx, db.TxOptions{}, func(*db.Tx) error { return nil }); errors.Is(err, db.ErrPoolTimeout) || err == nil {
		t.Fatal(err)
	}
}
