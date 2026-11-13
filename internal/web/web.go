// Package web 監控／管理網站（對應 Python app/main.py、app/api/*）。前端靜態檔（static/monitor）由 Python 專案原樣複製。
//
// 防護（與 Python 相同）：
//   - 只接受設定的 Host（防 DNS rebinding；Python TrustedHostMiddleware）。
//   - 安全標頭（CSP、X-Frame-Options 等）；/api/ 一律 Cache-Control: no-store；靜態檔 no-cache＋ETag。
//   - 監控 API 只有 GET；管理 API 的防護在 admin.go／auth.go。
//   - 不提供 API 文件頁面（Python docs_url=None）。
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"farmgm/internal/auth"
	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/services"
)

//go:embed static/monitor
var staticFS embed.FS

// SecurityHeaders 所有回應都帶的安全標頭（Python SECURITY_HEADERS）。
var SecurityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Referrer-Policy":        "no-referrer",
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
}

// Options 建立網站的設定（Python create_app 的參數）。
type Options struct {
	DB             *db.DB
	Clock          clock.Clock // nil：系統時鐘
	RefreshSeconds float64     // WebSocket 推送間隔；0 表示 3 秒
	AllowedHosts   []string    // 允許的 Host（不含 Port）；空的時候只允許 127.0.0.1、localhost
	// Auth 管理員登入；nil 時管理功能停用（fail closed）。
	Auth *auth.AdminAuth
}

// App 監控／管理網站。
type App struct {
	monitor  *services.MonitorService
	admin    *services.AdminService
	auth     *auth.AdminAuth
	refresh  time.Duration
	hosts    []string
	snapshot *SnapshotCache
	mux      *http.ServeMux

	wsMu   sync.Mutex
	wsDone []chan struct{}
}

// New 建立網站。
func New(opt Options) *App {
	refresh := time.Duration(opt.RefreshSeconds * float64(time.Second))
	if refresh <= 0 {
		refresh = 3 * time.Second
	}
	hosts := opt.AllowedHosts
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1", "localhost"}
	}
	if opt.Auth == nil {
		opt.Auth = auth.New("", "", auth.Options{})
	}
	a := &App{
		monitor:  services.NewMonitorService(opt.DB, opt.Clock),
		admin:    services.NewAdminService(opt.DB, services.Options{Clock: opt.Clock}),
		auth:     opt.Auth,
		refresh:  refresh,
		hosts:    hosts,
		snapshot: &SnapshotCache{},
		mux:      http.NewServeMux(),
	}
	a.routes()
	return a
}

func (a *App) routes() {
	a.monitorRoutes()
	a.authRoutes()
	a.adminRoutes()
	a.mux.HandleFunc("GET /ws/monitor", a.monitorWS)
	static, _ := fs.Sub(staticFS, "static/monitor")
	a.mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	a.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, static, "index.html")
	})
}

// ServeHTTP 依序套用 Host 檢查、安全標頭、例外保護。
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.hostAllowed(r.Host) {
		http.Error(w, "Invalid host header", http.StatusBadRequest)
		return
	}
	h := w.Header()
	for k, v := range SecurityHeaders {
		h.Set(k, v)
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			slog.Error("網站處理請求時發生未預期的錯誤", "path", r.URL.Path, "panic", p)
			writeDetail(w, http.StatusInternalServerError, "伺服器發生未預期的錯誤。")
		}
	}()
	a.mux.ServeHTTP(w, r)
}

// hostAllowed 對應 Starlette TrustedHostMiddleware：比對不含 Port 的 Host；支援 "*" 與 "*.example.com"。
func (a *App) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	for _, allowed := range a.hosts {
		allowed = strings.ToLower(allowed)
		switch {
		case allowed == "*", allowed == host:
			return true
		case strings.HasPrefix(allowed, "*.") && strings.HasSuffix(host, allowed[1:]):
			return true
		}
	}
	return false
}

// Close 關閉所有 WebSocket 連線（關機時呼叫）。
func (a *App) Close() {
	a.wsMu.Lock()
	defer a.wsMu.Unlock()
	for _, c := range a.wsDone {
		close(c)
	}
	a.wsDone = nil
}

func (a *App) trackWS() (done chan struct{}, untrack func()) {
	done = make(chan struct{})
	a.wsMu.Lock()
	a.wsDone = append(a.wsDone, done)
	a.wsMu.Unlock()
	return done, func() {
		a.wsMu.Lock()
		defer a.wsMu.Unlock()
		for i, c := range a.wsDone {
			if c == done {
				a.wsDone = append(a.wsDone[:i], a.wsDone[i+1:]...)
				return
			}
		}
	}
}

// contentTypes 靜態檔的 Content-Type（不依賴作業系統的 MIME 設定；Windows 登錄檔可能把 .js 設成 text/plain）。
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".png":  "image/png",
}

func staticHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, fsys, r.URL.Path)
	})
}

func serveStatic(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	if name == "" || strings.Contains(name, "..") || strings.HasSuffix(name, "/") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "application/octet-stream"
	if i := strings.LastIndex(name, "."); i >= 0 {
		if v, ok := contentTypes[strings.ToLower(name[i:])]; ok {
			ct = v
		}
	}
	w.Header().Set("Content-Type", ct)
	// 更新後管理者立即拿到新版前端（例如設定欄位的上限）：可快取，但每次先以 ETag 向伺服器確認；
	// 也避免前面的 CDN 依預設把 .js 快取數小時。
	sum := sha256.Sum256(data)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(data)))
}

// ctxOf 請求的 context（之後若要加逾時集中在這裡）。
func ctxOf(r *http.Request) context.Context { return r.Context() }
