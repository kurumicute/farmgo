package main

// 啟動檢查（Python app/discord_bot/bot.py 的 main 沒有對應測試）：沒有 Token 時不連資料庫就結束；
// 資料庫連不上時回傳啟動失敗且不洩漏密碼。都不會連到 Discord。

import (
	"context"
	"strings"
	"testing"
)

func TestRunRequiresToken(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://farmgm:secret-pw@127.0.0.1:1/farmgm")
	t.Setenv("DISCORD_BOT_TOKEN", "   ")
	err := run(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "未設定 DISCORD_BOT_TOKEN") {
		t.Fatal(err)
	}
}

func TestRunDatabaseFailureHidesSecrets(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://farmgm:secret-pw@127.0.0.1:1/farmgm")
	t.Setenv("DISCORD_BOT_TOKEN", "token-should-not-leak")
	t.Setenv("DB_POOL_TIMEOUT", "1")
	err := run(context.Background(), "")
	if err == nil {
		t.Fatal("資料庫連不上時應啟動失敗")
	}
	if msg := err.Error(); strings.Contains(msg, "secret-pw") || strings.Contains(msg, "token-should-not-leak") {
		t.Fatal(msg)
	}
}
