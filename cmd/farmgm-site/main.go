// 介紹網站的程式進入點（對應 Python app/site/__main__.py）。
//
// 啟動：go run ./cmd/farmgm-site   （預設 http://127.0.0.1:24444；對外公開請設定 SITE_HOST=0.0.0.0 或放在反向代理後面）
//
// 介紹網站只做唯讀的彙總查詢，連線池用小一點即可（Python pool_size=2、max_overflow=2、pool_timeout=5）。
// 與 Python 相同，啟動時資料庫無法連線也照常啟動（統計與手冊回 503，資料庫恢復後自動正常）；
// Go 版另外檢查 Migration 版本，不符時只記錄警告。
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

	"farmgm/internal/config"
	"farmgm/internal/db"
	"farmgm/internal/services"
	"farmgm/internal/site"
)

const shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, ".env", func(addr string) { fmt.Printf("介紹網站：http://%s\n", addr) }); err != nil {
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
	d, err := db.Open(ctx, settings.PgxDatabaseURL(), db.PoolOptions{MaxConns: 4, PoolTimeout: 5 * time.Second})
	if err != nil {
		return err
	}
	defer d.Close()
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := d.CheckDatabase(checkCtx); err != nil {
		slog.Warn("介紹網站啟動檢查未通過，仍繼續啟動（資料暫時無法取得時回 503）", "error", err.Error())
	}
	cancel()
	app := site.New(siteOptions(settings, services.NewSiteService(d, nil)))

	addr := net.JoinHostPort(settings.SiteHost, strconv.Itoa(settings.SitePort))
	srv := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("無法監聽 %s（%T）", addr, err)
	}
	ready(ln.Addr().String())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	slog.Info("收到結束訊號，正在關閉介紹網站。")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// siteOptions 對應 Python main() 傳給 create_site_app 的參數。
func siteOptions(s config.Settings, svc *services.SiteService) site.Options {
	support := s.SiteSupportURL
	if support == "" { // 沒設定或空值時用官方 Discord 群組
		support = config.OfficialGuildURL
	}
	music := site.Music{Enabled: s.MusicEnabled, PrefixCommands: s.MusicPrefixCommands, Prefix: s.MusicPrefix,
		PlaylistLimit: s.MusicPlaylistLimit, IdleTimeout: s.MusicIdleTimeout, Volume: s.MusicVolume}
	if s.MusicBotConfigured { // 音樂改用獨立 Bot 時才有音樂 Bot 的邀請連結
		music.ApplicationID = s.MusicApplicationID
	}
	return site.Options{Service: svc, ApplicationID: s.DiscordApplicationID, SupportURL: support, Music: &music}
}
