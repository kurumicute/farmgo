// Discord Bot（農場面板）的程式進入點（對應 Python app/discord_bot/bot.py 的 main）。
//
// 啟動：go run ./cmd/farmgm-bot   （讀取工作目錄的 .env 與系統環境變數）
//
// 流程與 Python 相同：
//   - 讀設定；沒有 DISCORD_BOT_TOKEN 時結束（Token 只能放在 .env 或環境變數）；
//   - 連線池至少能讓每個工作執行緒各拿一條連線，另加背景結算與 advisory lock 各一條；檢查 Migration 版本（只檢查，不升級）；
//   - 清理逾時的釣魚、登入、同步 /farm 與 /help、處理互動；BACKGROUND_TASKS_RUNNER=bot 時背景結算；
//   - 收到 SIGINT／SIGTERM 時關閉連線。
//
// 音樂（internal/music）與 Python 相同：沒有設定 MUSIC_BOT_TOKEN 時在農場 Bot 程序內執行，連線 LAVALINK_URI 的 Lavalink；
// 只有斜線指令（前綴指令需要 Message Content 特權 Intent，正式環境沒有開啟，不移植）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"farmgm/internal/bot"
	"farmgm/internal/bot/discordio"
	"farmgm/internal/bot/ui"
	"farmgm/internal/cachebus"
	"farmgm/internal/config"
	"farmgm/internal/db"
	"farmgm/internal/services"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, ".env"); err != nil {
		stop()
		fmt.Fprintf(os.Stderr, "[啟動失敗] %s\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, envFile string) error {
	settings, err := config.Load(envFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(settings.DiscordBotToken) == "" {
		return fmt.Errorf("未設定 DISCORD_BOT_TOKEN（請寫在 .env 或系統環境變數，不要寫進程式碼）。")
	}
	workers := settings.BotWorkerThreads
	d, err := db.Open(ctx, settings.PgxDatabaseURL(), db.PoolOptions{
		MaxConns:    settings.PoolSize(workers + 2),
		PoolTimeout: time.Duration(settings.DBPoolTimeout * float64(time.Second)),
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

	svc := services.NewAdminService(d, services.Options{})
	cmds := bot.NewCommands(svc, bot.NewRunner(workers), ui.NewRegistry())
	b, err := discordio.New(settings, cmds, svc)
	if err != nil {
		return err
	}
	slog.Info("啟動 Discord Bot", "app_env", settings.AppEnv, "workers", workers,
		"pool", settings.PoolSize(workers+2), "background_tasks", settings.BackgroundTasksRunner)
	return b.Run(ctx)
}
