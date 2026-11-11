package services

// 對應 Python app/discord_bot/runner.py 的 RetryingService：死結（40P01）與序列化失敗（40001）代表整筆交易已回滾，
// 因此只對這兩種錯誤重試同一個服務呼叫，最多 RetryAttempts 次。連線中斷、連線池耗盡等錯誤不重試：
// 交易可能未開始、也可能已提交。
//
// 與 Python 不同：Python 以 __getattr__ 自動包裝服務的每個公開方法；Go 沒有動態代理，由呼叫端（Discord 面板、CMD）
// 把每個服務呼叫包在 Retrier.Do／Retry 中。面板讀取整頁時包住整個 RunBatch，失敗時整批重試。

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"farmgm/internal/db"
)

// RetryAttempts 預設的最多嘗試次數（Python RETRY_ATTEMPTS）。
const RetryAttempts = 3

// Retrier 遇到可重試的資料庫錯誤時重試服務呼叫。零值即可使用。
type Retrier struct {
	Attempts int                 // 最多嘗試次數；0 表示 RetryAttempts
	Sleep    func(time.Duration) // 重試前等待；nil 表示等待且可被 ctx 取消（測試可換成不等待）
}

// Do 執行 fn（一個完整的服務呼叫，例如 svc.BuySeeds 或 svc.RunBatch）；name 只用於日誌。
//
// 已在 RunBatch 的共用交易中時不重試：失敗代表整筆共用交易已回滾，只能由外層包住 RunBatch 的 Do 整批重試。
func (r Retrier) Do(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	if inAnyBatch(ctx) {
		return fn(ctx)
	}
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = RetryAttempts
	}
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil || !db.IsRetryable(err) || attempt >= attempts {
			return err
		}
		slog.Warn("服務遇到可重試的資料庫錯誤，重試", "method", name, "sqlstate", sqlState(err), "attempt", attempt)
		if err := r.wait(ctx, time.Duration(attempt)*50*time.Millisecond); err != nil {
			return err
		}
	}
}

func (r Retrier) wait(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		r.Sleep(d)
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Retry Do 的回傳值版本。
func Retry[T any](ctx context.Context, r Retrier, name string, fn func(ctx context.Context) (T, error)) (T, error) {
	var out T
	err := r.Do(ctx, name, func(ctx context.Context) error {
		v, err := fn(ctx)
		out = v
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func sqlState(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

// inAnyBatch ctx 是否在任何服務的 RunBatch 共用交易中（Python 檢查被包裝服務的 in_batch()）。
func inAnyBatch(ctx context.Context) bool {
	b, _ := ctx.Value(batchKey{}).(*batch)
	return b != nil
}
