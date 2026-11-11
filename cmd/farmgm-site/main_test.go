package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"farmgm/internal/config"
	"farmgm/internal/dbtest"
)

func TestSiteOptions(t *testing.T) {
	s := config.Settings{DiscordApplicationID: 7, MusicEnabled: true, MusicApplicationID: 9, MusicPrefix: "!",
		MusicPlaylistLimit: 20, MusicIdleTimeout: 300, MusicVolume: 0.1}
	o := siteOptions(s, nil)
	if o.SupportURL != config.OfficialGuildURL || o.ApplicationID != 7 || o.Music.ApplicationID != 0 {
		t.Fatalf("%+v %+v", o, o.Music) // 沒有 MUSIC_BOT_TOKEN：沒有音樂 Bot 邀請連結；支援連結用官方群組
	}
	s.MusicBotConfigured, s.SiteSupportURL = true, "https://discord.gg/x"
	if o := siteOptions(s, nil); o.SupportURL != "https://discord.gg/x" || o.Music.ApplicationID != 9 {
		t.Fatalf("%+v %+v", o, o.Music)
	}
}

// TestRunServesAndShutsDown 以測試資料庫實際啟動介紹網站，ctx 取消後正常結束。
func TestRunServesAndShutsDown(t *testing.T) {
	url := dbtest.URL()
	if url == "" {
		t.Skip("略過需要資料庫的測試：" + dbtest.EnvVar + " 未設定")
	}
	dbtest.Open(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	for k, v := range map[string]string{"DATABASE_URL": url, "SITE_HOST": "127.0.0.1", "SITE_PORT": strconv.Itoa(port),
		"DISCORD_APPLICATION_ID": "1558428174814154822", "SITE_SUPPORT_URL": "", "MUSIC_BOT_TOKEN": ""} {
		t.Setenv(k, v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh, errCh := make(chan string, 1), make(chan error, 1)
	go func() { errCh <- run(ctx, "", func(addr string) { addrCh <- addr }) }()
	var addr string
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		t.Fatalf("啟動失敗：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("啟動逾時")
	}
	for _, p := range []string{"/", "/api/stats", "/api/guide"} {
		resp, err := http.Get("http://" + addr + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s：%d", p, resp.StatusCode)
		}
	}
	resp, err := http.Get("http://" + addr + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info["support_url"] != config.OfficialGuildURL || info["invite_url"] == nil || info["music_invite_url"] != nil {
		t.Fatal(info)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("收到結束訊號後沒有結束")
	}
}
