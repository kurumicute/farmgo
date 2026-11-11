package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/auth"
	"farmgm/internal/config"
	"farmgm/internal/dbtest"
	"farmgm/internal/services"
	"farmgm/internal/web"

	"github.com/gorilla/websocket"
)

// fakeSettler 依序回傳結果；用完後取消 ctx。
type fakeSettler struct {
	mu      sync.Mutex
	results []func() (*services.WorldBossSettlement, error)
	calls   int
	cancel  context.CancelFunc
}

func (f *fakeSettler) RunBackgroundSettlement(ctx context.Context, actor string) (*services.WorldBossSettlement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if actor != "system" {
		panic("actor 應為 system")
	}
	f.calls++
	if len(f.results) == 0 {
		f.cancel()
		return nil, ctx.Err()
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r()
}

type secretErr struct{}

func (secretErr) Error() string { return "postgresql://farmgm:secret@db/farmgm 連線失敗" }

func TestAutoSettleRetriesAndLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settled := &services.WorldBossSettlement{WorldBoss: []int64{7}}
	settled.Rankings = []int64{3}
	f := &fakeSettler{cancel: cancel, results: []func() (*services.WorldBossSettlement, error){
		func() (*services.WorldBossSettlement, error) { return nil, secretErr{} },                     // 資料庫錯誤：下次再試
		func() (*services.WorldBossSettlement, error) { return nil, nil },                             // 另一個程序正在結算
		func() (*services.WorldBossSettlement, error) { return &services.WorldBossSettlement{}, nil }, // 沒有到期的項目
		func() (*services.WorldBossSettlement, error) { return settled, nil },
	}}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	done := make(chan struct{})
	go func() {
		autoSettle(ctx, log, f, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("autoSettle 沒有在 ctx 取消後結束")
	}
	if f.calls != 5 {
		t.Fatalf("calls=%d", f.calls)
	}
	out := buf.String()
	if strings.Count(out, "自動結算排名獎勵失敗") != 1 || !strings.Contains(out, "type=main.secretErr") {
		t.Fatalf("錯誤紀錄不正確：%s", out)
	}
	if strings.Contains(out, "secret@") {
		t.Fatalf("日誌不可包含錯誤內容（可能含連線字串）：%s", out)
	}
	if strings.Count(out, "已自動結算") != 1 || !strings.Contains(out, "rankings=[3]") || !strings.Contains(out, "world_boss=[7]") {
		t.Fatalf("結算紀錄不正確：%s", out)
	}
}

func TestAutoSettleStopsDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeSettler{cancel: func() {}, results: []func() (*services.WorldBossSettlement, error){
		func() (*services.WorldBossSettlement, error) { cancel(); return nil, nil },
	}}
	done := make(chan struct{})
	go func() {
		autoSettle(ctx, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), f, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("等待下一次結算時應能立即停止")
	}
	if f.calls != 1 {
		t.Fatalf("calls=%d", f.calls)
	}
}

func TestAdminAuthFromSettings(t *testing.T) {
	if adminAuthFromSettings(config.Settings{AdminUsername: "admin"}).Enabled() {
		t.Fatal("沒有雜湊時管理功能應停用")
	}
	if adminAuthFromSettings(config.Settings{AdminPasswordHash: "scrypt:x"}).Enabled() {
		t.Fatal("沒有帳號時管理功能應停用")
	}
	hash, err := auth.HashPassword("password1")
	if err != nil {
		t.Fatal(err)
	}
	a := adminAuthFromSettings(config.Settings{AdminUsername: "admin", AdminPasswordHash: hash, AdminSessionHours: 1.5})
	if !a.Enabled() || a.Username() != "admin" || a.SessionDuration() != 90*time.Minute {
		t.Fatalf("enabled=%v duration=%v", a.Enabled(), a.SessionDuration())
	}
	if tok, err := a.Login("admin", "password1", "127.0.0.1"); err != nil || tok == "" {
		t.Fatalf("登入失敗：%v", err)
	}
}

func TestNewServer(t *testing.T) {
	app := web.New(web.Options{})
	srv := newServer("127.0.0.1:0", app)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout != 0 || srv.WriteTimeout != 0 || srv.Handler != app {
		t.Fatalf("伺服器設定不正確：%+v", srv)
	}
	if err := srv.Shutdown(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSeconds(t *testing.T) {
	if seconds(0.5) != 500*time.Millisecond || seconds(60) != time.Minute {
		t.Fatal("秒數轉換錯誤")
	}
}

// TestRunServesAndShutsDown 以測試資料庫實際啟動網站：可取得監控資料、WebSocket 可連線，
// ctx 取消（收到結束訊號）後關閉 WebSocket 並正常結束。
func TestRunServesAndShutsDown(t *testing.T) {
	url := dbtest.URL()
	if url == "" {
		t.Skip("略過需要資料庫的測試：" + dbtest.EnvVar + " 未設定")
	}
	dbtest.Open(t) // 確認 schema 與清空資料
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	for k, v := range map[string]string{
		"DATABASE_URL": url, "APP_HOST": "127.0.0.1", "APP_PORT": strconv.Itoa(port),
		"ADMIN_USERNAME": "", "ADMIN_PASSWORD_HASH": "", "ALLOWED_HOSTS": "",
		"BACKGROUND_TASKS_RUNNER": "web", "RANKING_SETTLE_INTERVAL_SECONDS": "10",
	} {
		t.Setenv(k, v)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, "", func(addr string) { addrCh <- addr }) }()
	var addr string
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		t.Fatalf("啟動失敗：%v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("啟動逾時")
	}
	base := "http://" + addr

	resp, err := http.Get(base + "/api/monitor/summary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("summary status=%d", resp.StatusCode)
	}
	// 管理功能未設定：fail closed
	resp, err = http.Get(base + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	var me map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if me["enabled"] != false {
		t.Fatalf("me=%v", me)
	}

	ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/monitor", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if _, _, err := ws.ReadMessage(); err != nil { // 第一份快照
		t.Fatal(err)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		t.Logf("shutdown took %v", time.Since(start))
		if err != nil {
			t.Fatalf("關機回傳錯誤：%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("收到結束訊號後沒有結束")
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatal("關機後 WebSocket 沒有被關閉")
			}
			break
		}
	}
	if _, err := http.Get(base + "/api/monitor/summary"); err == nil {
		t.Fatal("關機後不應再接受連線")
	}
}
