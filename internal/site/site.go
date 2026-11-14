// Package site 介紹網站（公開；對應 Python app/site/server.py）：Bot 功能介紹、邀請連結與彙總統計。預設 Port 24444。
//
//   - 只提供彙總數字（玩家數、農場數、伺服器數、今日操作數等），不輸出任何玩家名稱、ID 或個別資料。
//   - /api/guide 提供操作手冊用的遊戲設定，數值即時讀自資料庫與規則套件，管理員在後台調整後網站會跟著更新。
//     /api/guide/{part} 只回傳其中一部分（services.GuideParts）：首頁只讀 summary，手冊欄位打開時才讀對應的部分。
//   - 查詢都在唯讀交易中執行並快取（統計 StatsCacheSeconds、手冊 GuideCacheSeconds 秒），訪客再多也不會增加資料庫負擔；
//     同一時間只有一個請求會查詢資料庫，其他請求等待並共用結果（與 Python 的 threading.Lock 相同）。
//   - 沒有任何寫入端點；頁面為靜態檔（static 是 Python 前端的原樣複製，以 go:embed 打包），前端只用 textContent 填入資料。
//   - 公開網站，不做 Host 檢查（與 Python 相同；Python 的介紹網站沒有 TrustedHostMiddleware）。
package site

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"farmgm/internal/numfmt"
	"farmgm/internal/services"
)

//go:embed static
var staticFS embed.FS

const (
	StatsCacheSeconds = 30
	GuideCacheSeconds = 60
	// InvitePermissions 邀請 Bot 需要的權限：檢視頻道、傳送訊息、嵌入連結、附加檔案、讀取訊息紀錄、連接語音、說話
	InvitePermissions = 1024 + 2048 + 16384 + 32768 + 65536 + 1048576 + 2097152

	guideUnavailable = "遊戲設定暫時無法取得"
	statsUnavailable = "統計資料暫時無法取得"
)

// SecurityHeaders 所有回應都帶的安全標頭（Python SECURITY_HEADERS；允許 Google Fonts）。
var SecurityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Referrer-Policy":        "no-referrer",
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; " +
		"font-src https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
}

// Music 音樂說明用的設定值（不含任何 Token 或機密）；音樂仍由 Python 版執行。
type Music struct {
	Enabled        bool
	ApplicationID  int64 // 獨立音樂 Bot 的應用程式 ID（有設定 MUSIC_BOT_TOKEN 時）；0 = 沒有
	PrefixCommands bool
	Prefix         string
	PlaylistLimit  int
	IdleTimeout    int     // 秒
	Volume         float64 // 0–2
}

// DefaultMusic Python create_site_app 的預設值。
func DefaultMusic() Music {
	return Music{Enabled: true, Prefix: "!", PlaylistLimit: 20, IdleTimeout: 300, Volume: 0.1}
}

// Options 建立網站的設定（Python create_site_app 的參數）。
type Options struct {
	// Service nil 時統計與手冊回 503（Python session_factory=None）。
	Service       *services.SiteService
	SiteName      string // 空字串：油祥農場
	ApplicationID int64  // 0 = 沒有邀請連結
	SupportURL    string // 空字串 = null
	Music         *Music // nil：DefaultMusic()
}

// App 介紹網站。
type App struct {
	opt    Options
	music  Music
	stats  *cache
	guide  *cache
	parts  map[string]*cache // 操作手冊的各部分（services.GuideParts），各自快取
	mux    *http.ServeMux
	static fs.FS
	etags  map[string]string
}

// New 建立網站。
func New(opt Options) *App {
	if opt.SiteName == "" {
		opt.SiteName = "油祥農場"
	}
	music := DefaultMusic()
	if opt.Music != nil {
		music = *opt.Music
	}
	static, _ := fs.Sub(staticFS, "static")
	a := &App{opt: opt, music: music, mux: http.NewServeMux(), static: static, etags: map[string]string{}}
	a.stats = &cache{ttl: StatsCacheSeconds * time.Second, load: a.loadStats}
	a.guide = &cache{ttl: GuideCacheSeconds * time.Second, load: a.loadGuide}
	a.parts = map[string]*cache{}
	for part := range services.GuideParts {
		a.parts[part] = &cache{ttl: GuideCacheSeconds * time.Second, load: func(ctx context.Context) ([]byte, error) { return a.loadGuidePart(ctx, part) }}
	}
	_ = fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, err := fs.ReadFile(static, p); err == nil {
				sum := sha256.Sum256(data)
				a.etags[p] = `"` + hex.EncodeToString(sum[:16]) + `"`
			}
		}
		return nil
	})
	a.mux.HandleFunc("GET /api/info", a.info)
	a.mux.HandleFunc("GET /api/guide", func(w http.ResponseWriter, r *http.Request) {
		a.serveCached(w, r, a.guide, "介紹網站手冊資料查詢失敗", guideUnavailable)
	})
	// 分段讀取：首頁只讀 summary，操作手冊的欄位打開時才讀對應的部分（2026-11-14）
	a.mux.HandleFunc("GET /api/guide/{part}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := a.parts[r.PathValue("part")]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"detail": "沒有這個手冊部分。"})
			return
		}
		a.serveCached(w, r, c, "介紹網站手冊資料查詢失敗", guideUnavailable)
	})
	a.mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		a.serveCached(w, r, a.stats, "介紹網站統計查詢失敗", statsUnavailable)
	})
	a.mux.HandleFunc("GET /static/{file...}", func(w http.ResponseWriter, r *http.Request) {
		a.serveStatic(w, r, r.PathValue("file"))
	})
	a.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		a.serveStatic(w, r, "favicon.svg")
	})
	a.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		a.serveStatic(w, r, "index.html")
	})
	return a
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	for k, v := range SecurityHeaders {
		h.Set(k, v)
	}
	// 網站更新後訪客立即看到新版：瀏覽器可快取，但每次先向伺服器確認（ETag）
	h.Set("Cache-Control", "no-cache")
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			slog.Error("介紹網站處理請求時發生未預期的錯誤", "path", r.URL.Path, "panic", p)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "伺服器發生未預期的錯誤。"})
		}
	}()
	a.mux.ServeHTTP(w, r)
}

// InviteURL Bot 的邀請連結；applicationID 為 0 時回傳 nil。
func InviteURL(applicationID int64) *string {
	if applicationID == 0 {
		return nil
	}
	u := fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%d&scope=bot+applications.commands&permissions=%d",
		applicationID, InvitePermissions)
	return &u
}

func (a *App) info(w http.ResponseWriter, r *http.Request) {
	var support *string
	if a.opt.SupportURL != "" {
		support = &a.opt.SupportURL
	}
	m := a.music
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       a.opt.SiteName,
		"invite_url": InviteURL(a.opt.ApplicationID),
		// 音樂改用獨立 Bot 時，另外提供音樂 Bot 的邀請連結
		"music_invite_url": InviteURL(m.ApplicationID),
		"support_url":      support,
		"music_enabled":    m.Enabled,
		"music": map[string]any{
			"prefix_commands": m.PrefixCommands, "prefix": m.Prefix, "playlist_limit": m.PlaylistLimit,
			"idle_minutes":       numfmt.RoundN(float64(m.IdleTimeout)/60, 1),
			"default_volume_pct": numfmt.Round(m.Volume * 100),
		},
	})
}

// cache 結果快取（Python StatsCache／GuideCache）：存 JSON 本文；查詢時持有鎖，其他請求等待並共用結果。
// 查詢失敗不快取，下一個請求再試。
type cache struct {
	ttl  time.Duration
	load func(ctx context.Context) ([]byte, error)

	mu    sync.Mutex
	value []byte
	at    time.Time
}

func (c *cache) get(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != nil && time.Since(c.at) < c.ttl {
		return c.value, nil
	}
	v, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	c.value, c.at = v, time.Now()
	return v, nil
}

var errNoService = fmt.Errorf("沒有資料庫")

func (a *App) loadStats(ctx context.Context) ([]byte, error) {
	if a.opt.Service == nil {
		return nil, errNoService
	}
	st, err := a.opt.Service.Stats(ctx)
	if err != nil {
		return nil, err
	}
	return marshal(struct {
		services.SiteStats
		CachedSeconds int `json:"cached_seconds"`
	}{st, StatsCacheSeconds})
}

func (a *App) loadGuide(ctx context.Context) ([]byte, error) {
	if a.opt.Service == nil {
		return nil, errNoService
	}
	g, err := a.opt.Service.Guide(ctx)
	if err != nil {
		return nil, err
	}
	return marshal(g.Set("cached_seconds", GuideCacheSeconds))
}

func (a *App) loadGuidePart(ctx context.Context, part string) ([]byte, error) {
	if a.opt.Service == nil {
		return nil, errNoService
	}
	g, err := a.opt.Service.GuidePart(ctx, part)
	if err != nil {
		return nil, err
	}
	return marshal(g.Set("cached_seconds", GuideCacheSeconds))
}

func (a *App) serveCached(w http.ResponseWriter, r *http.Request, c *cache, logMsg, detail string) {
	body, err := c.get(context.WithoutCancel(r.Context())) // 用戶端離開不中斷查詢：結果仍會快取給其他請求
	if err != nil {
		if err != errNoService {
			slog.Warn(logMsg, "type", fmt.Sprintf("%T", err))
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"detail": detail})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// contentTypes 靜態檔的 Content-Type（不依賴作業系統的 MIME 設定）。
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".png":  "image/png",
}

// serveStatic 只讀嵌入的檔案（不會讀到目錄以外）；帶 ETag，瀏覽器以 If-None-Match 確認時回 304。
func (a *App) serveStatic(w http.ResponseWriter, r *http.Request, name string) {
	name = path.Clean("/" + name)[1:]
	etag, ok := a.etags[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(a.static, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct, ok := contentTypes[path.Ext(name)]; ok {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := marshal(v)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"detail":"伺服器發生未預期的錯誤。"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
