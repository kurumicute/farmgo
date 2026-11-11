// Package bot Discord 農場 Bot（對應 Python app/discord_bot）。
//
// 本檔對應 app/discord_bot/runner.py：
//   - Python 把同步的遊戲服務送到有限大小的執行緒池（ServiceRunner），避免卡住 asyncio 事件迴圈；Go 的處理函式本來就在
//     各自的 goroutine，只保留「同時處理中的服務呼叫上限」（= BOT_WORKER_THREADS，應與連線池大小相符）。
//   - 死結（40P01）與序列化失敗（40001）重試由 services.Retrier 負責（Python RetryingService）；連線中斷、連線池耗盡不重試。
//   - PlayerMessage 把錯誤轉成可以給玩家看的訊息：不包含堆疊、SQL 或連線資訊。
package bot

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgconn"

	"farmgm/internal/db"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/services"
)

const (
	MsgBusy       = "伺服器忙碌中，請稍後再試。"
	MsgDBDown     = "資料庫暫時無法連線，請稍後再試；如果剛才是買賣等操作，可用 /farm log 確認是否已完成。"
	MsgUnexpected = "發生未預期的錯誤，已記錄下來，請稍後再試。"
)

// PlayerMessage 錯誤 → 給玩家看的訊息（Python player_message）。
func PlayerMessage(err error) string {
	if ge, ok := gameerr.As(err); ok {
		return ge.Error()
	}
	if errors.Is(err, db.ErrPoolTimeout) || db.IsRetryable(err) {
		return MsgBusy
	}
	if isConnectionError(err) {
		return MsgDBDown
	}
	return MsgUnexpected
}

// isConnectionError 連線失敗或中斷（Python OperationalError／InterfaceError）。
func isConnectionError(err error) bool {
	var ce *pgconn.ConnectError
	var ne net.Error
	if errors.As(err, &ce) || errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// 08：連線異常；57P0x：伺服器關閉中；53300：連線數已滿
		return len(pg.Code) >= 2 && pg.Code[:2] == "08" || pg.Code == "57P01" || pg.Code == "57P02" ||
			pg.Code == "57P03" || pg.Code == "53300"
	}
	return pgconn.Timeout(err)
}

// Runner 限制同時處理中的工作數（Python ServiceRunner 的執行緒數）。死結重試不在這裡：一個工作（例如「全部賣出」）
// 可能依序呼叫多個服務方法、各自提交，整個工作重試會重複已提交的步驟；所以和 Python RetryingService 相同，
// 每個服務呼叫各自用 Call 重試。
type Runner struct {
	Workers int
	Retrier services.Retrier
	sem     chan struct{}
}

// NewRunner workers = 同時處理中的工作上限（BOT_WORKER_THREADS）。
func NewRunner(workers int) *Runner {
	workers = max(1, workers)
	return &Runner{Workers: workers, sem: make(chan struct{}, workers)}
}

// Do 等到有空位後執行 fn（不重試）。等待中 ctx 取消時回傳 ctx 的錯誤。
func (r *Runner) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.sem }()
	return fn(ctx)
}

// Run Do 的回傳值版本。
func Run[T any](ctx context.Context, r *Runner, fn func(ctx context.Context) (T, error)) (T, error) {
	var out T
	err := r.Do(ctx, func(ctx context.Context) error {
		v, err := fn(ctx)
		out = v
		return err
	})
	return out, err
}

// Call 一個服務呼叫（一筆完整交易）；死結或序列化失敗時重試（Python RetryingService）。在 RunBatch 中不重試。
func Call[T any](ctx context.Context, r *Runner, name string, fn func(ctx context.Context) (T, error)) (T, error) {
	return services.Retry(ctx, r.Retrier, name, fn)
}
