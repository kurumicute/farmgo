// 監控／管理網站的程式進入點（對應 Python app/main.py 的 run() 與 create_app 的 lifespan）。
//
// 啟動：go run ./cmd/farmgm-web   （讀取工作目錄的 .env 與系統環境變數）
//
// 流程與 Python 相同：
//   - 讀設定；連線並檢查 Migration 版本（只檢查，不升級），失敗時印出「[啟動失敗] …」並結束；
//   - 未設定 ADMIN_USERNAME／ADMIN_PASSWORD_HASH 時管理功能停用（fail closed）；
//   - BACKGROUND_TASKS_RUNNER=web 時定期執行背景結算（排名獎勵、競賽、世界 Boss）；
//   - 收到 SIGINT／SIGTERM 時停止背景結算、關閉 WebSocket、等待進行中的請求結束後關閉連線池。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"farmgm/internal/auth"
	"farmgm/internal/cachebus"
	"farmgm/internal/config"
	"farmgm/internal/db"
	"farmgm/internal/services"
	"farmgm/internal/web"
)

// shutdownTimeout 關機時等待進行中請求的上限。
const shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, ".env", func(addr string) { fmt.Printf("監控網頁：http://%s\n", addr) }); err != nil {
		stop()
		fmt.Fprintf(os.Stderr, "[啟動失敗] %s\n", err)
		os.Exit(1)
	}
}

// run 啟動網站直到 ctx 取消（收到結束訊號）後優雅關機。ready 在開始接受連線時以實際監聽位址呼叫。
func run(ctx context.Context, envFile string, ready func(addr string)) error {
	settings, err := config.Load(envFile)
	if err != nil {
		return err
	}

	d, err := db.Open(ctx, settings.PgxDatabaseURL(), db.PoolOptions{
		MaxConns:    settings.PoolSize(0),
		PoolTimeout: seconds(settings.DBPoolTimeout),
	})
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.CheckDatabase(ctx); err != nil {
		return err
	}
	bus := cachebus.StartFromSettings(ctx, settings.RedisURL, slog.Default()) // 選用：沒有設定或連不上時照常啟動
	defer bus.Close()

	adminAuth := adminAuthFromSettings(settings)
	if !adminAuth.Enabled() {
		slog.Warn("未設定 ADMIN_USERNAME / ADMIN_PASSWORD_HASH，網頁管理功能停用。")
	}
	app := web.New(web.Options{
		DB:             d,
		RefreshSeconds: settings.MonitorRefreshSeconds,
		AllowedHosts:   settings.AllowedHostList(),
		Auth:           adminAuth,
	})

	settleDone := make(chan struct{})
	settleCtx, cancelSettle := context.WithCancel(ctx)
	defer cancelSettle()
	if settings.BackgroundTasksRunner == "web" {
		svc := services.NewAdminService(d, services.Options{})
		go func() {
			defer close(settleDone)
			autoSettle(settleCtx, slog.Default(), svc, seconds(settings.RankingSettleIntervalSeconds))
		}()
	} else {
		close(settleDone)
	}

	addr := net.JoinHostPort(settings.AppHost, strconv.Itoa(settings.AppPort))
	srv := newServer(addr, app)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("無法監聽 %s（%T）", addr, err)
	}
	ready(ln.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		cancelSettle()
		<-settleDone
		app.Close()
		return err
	case <-ctx.Done():
	}
	slog.Info("收到結束訊號，正在關閉網站。")
	cancelSettle()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err = srv.Shutdown(shutdownCtx) // 會先呼叫 RegisterOnShutdown 的 app.Close 關閉 WebSocket
	<-settleDone
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// newServer 建立 HTTP 伺服器。不設定 ReadTimeout／WriteTimeout：WebSocket 是長時間連線，
// 這兩個逾時會套用到升級後的連線；以 ReadHeaderTimeout 防止慢速標頭攻擊，請求本文另有 1 MiB 上限（web/body.go）。
func newServer(addr string, app *web.App) *http.Server {
	srv := &http.Server{
		Addr:              addr,
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	// Shutdown 不會關閉已升級（hijack）的 WebSocket 連線，要另外通知
	srv.RegisterOnShutdown(app.Close)
	return srv
}

// adminAuthFromSettings 對應 Python _admin_auth_from_settings；帳號或雜湊為空時管理功能停用。
func adminAuthFromSettings(s config.Settings) *auth.AdminAuth {
	return auth.New(s.AdminUsername, s.AdminPasswordHash, auth.Options{
		SessionDuration: time.Duration(s.AdminSessionHours * float64(time.Hour)),
	})
}

// settler 背景結算（AdminService 經由 WorldBossService 提供）。
type settler interface {
	RunBackgroundSettlement(ctx context.Context, actor string) (*services.WorldBossSettlement, error)
}

// autoSettle 對應 Python _auto_settle：定期結算到期的排名獎勵、競賽與世界 Boss，直到 ctx 取消。
// 結算可重複執行：advisory lock 讓多個程序不會同時結算，唯一限制保證不會重複發獎。
// 錯誤（例如資料庫暫時無法連線）只記錄類型，下次再試，不中斷服務。
func autoSettle(ctx context.Context, log *slog.Logger, svc settler, interval time.Duration) {
	for {
		settled, err := svc.RunBackgroundSettlement(ctx, "system")
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			log.Warn("自動結算排名獎勵失敗", "type", fmt.Sprintf("%T", err))
		case settled != nil && (len(settled.Rankings) > 0 || len(settled.Competitions) > 0 || len(settled.WorldBoss) > 0):
			log.Info("已自動結算", "rankings", settled.Rankings, "competitions", settled.Competitions,
				"world_boss", settled.WorldBoss, "world_boss_spawned", settled.WorldBossSpawned)
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
