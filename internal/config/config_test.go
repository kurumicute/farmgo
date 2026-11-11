package config

import (
	"fmt"
	"testing"
)

func TestParseDotEnvAndDefaults(t *testing.T) {
	env := ParseDotEnv(bom + "# 註解\nDATABASE_URL=postgresql+psycopg://farmgm:pw@127.0.0.1:5434/farmgm\nDISCORD_DEV_GUILD_ID=\nAPP_PORT=\"8010\"\nMUSIC_VOLUME=0.5\n")
	s, err := FromLookup(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if s.AppPort != 8010 || s.DiscordDevGuildID != 0 || s.BotWorkerThreads != 16 || s.BackgroundTasksRunner != "web" {
		t.Fatalf("%+v", s)
	}
	if got := s.PgxDatabaseURL(); got != "postgresql://farmgm:pw@127.0.0.1:5434/farmgm" {
		t.Fatal(got)
	}
	if s.String() == "" || contains(s.String(), "pw@") {
		t.Fatal("database url must be masked")
	}
	// 介紹網站顯示的音樂設定（Python 預設值）；MUSIC_BOT_TOKEN 只記錄有沒有設定
	if !s.MusicEnabled || s.MusicBotConfigured || s.MusicPrefix != "!" || s.MusicPlaylistLimit != 20 ||
		s.MusicIdleTimeout != 300 || s.MusicVolume != 0.5 || s.MusicPrefixCommands {
		t.Fatalf("%+v", s)
	}
}

func TestMusicSettings(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgresql://x", "MUSIC_BOT_TOKEN": "secret-token", "MUSIC_PREFIX": "？？",
		"MUSIC_ENABLED": "false", "MUSIC_APPLICATION_ID": "42"}
	s, err := FromLookup(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !s.MusicBotConfigured || s.MusicEnabled || s.MusicPrefix != "？？" || s.MusicApplicationID != 42 {
		t.Fatalf("%+v", s)
	}
	if contains(fmt.Sprintf("%+v %s", s, s), "secret-token") {
		t.Fatal("Token 不可保存在設定中")
	}
	for k, v := range map[string]string{"MUSIC_PREFIX": "toolong", "MUSIC_VOLUME": "3", "MUSIC_IDLE_TIMEOUT": "10",
		"MUSIC_PLAYLIST_LIMIT": "0"} {
		bad := map[string]string{"DATABASE_URL": "postgresql://x", k: v}
		if _, err := FromLookup(func(k string) string { return bad[k] }); err == nil {
			t.Errorf("%s=%s 應該被拒絕", k, v)
		}
	}
}

func TestInvalidValues(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgresql://x", "APP_PORT": "0", "APP_ENV": "prod"}
	if _, err := FromLookup(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected error")
	}
	if _, err := FromLookup(func(string) string { return "" }); err == nil {
		t.Fatal("DATABASE_URL required")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
