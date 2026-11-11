package music

// Lavalink v4 客戶端（對應 Python 使用的 wavelink）：REST（解析音軌、更新／刪除播放器）與 WebSocket（ready、事件、統計）。
//
//   - WebSocket 斷線時自動重連（1 秒起，最多 30 秒間隔）；每次連上都是新的 Session。
//   - 監看：每 30 秒以 REST 確認 Session 仍有效，Lavalink 回 404（例如 Lavalink 重啟）時關閉連線重連
//     （Python 以 _lavalink_watchdog 處理 wavelink 沒有重連的情況）。
//   - 事件以 Handler 回呼，在 WebSocket 讀取的 goroutine 依序執行。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WatchdogInterval 確認 Session 仍有效的間隔。
const WatchdogInterval = 30 * time.Second

// TrackInfo 音軌資料。
type TrackInfo struct {
	Identifier string  `json:"identifier"`
	IsSeekable bool    `json:"isSeekable"`
	Author     string  `json:"author"`
	Length     int64   `json:"length"` // 毫秒
	IsStream   bool    `json:"isStream"`
	Position   int64   `json:"position"`
	Title      string  `json:"title"`
	URI        *string `json:"uri"`
	ArtworkURL *string `json:"artworkUrl"`
	ISRC       *string `json:"isrc"`
	SourceName string  `json:"sourceName"`
}

// Track Lavalink 音軌（encoded 是播放時送回 Lavalink 的識別）。
type Track struct {
	Encoded string    `json:"encoded"`
	Info    TrackInfo `json:"info"`
}

// LoadResult 解析結果：Tracks 為單曲或搜尋結果；Playlist 不為 nil 時是播放清單。
type LoadResult struct {
	Tracks   []Track
	Playlist *Playlist
}

// Playlist 播放清單。Selected 為網址指定的那一首（-1 表示沒有）。
type Playlist struct {
	Name     string
	Selected int
	Tracks   []Track
}

// VoiceState 送給 Lavalink 的 Discord 語音連線資料。
type VoiceState struct {
	Token     string `json:"token"`
	Endpoint  string `json:"endpoint"`
	SessionID string `json:"sessionId"`
	ChannelID string `json:"channelId,omitempty"`
}

// FrameStats 每分鐘的音訊封包統計（每個播放器平均）。
type FrameStats struct {
	Sent    int `json:"sent"`
	Nulled  int `json:"nulled"`
	Deficit int `json:"deficit"`
}

// Event Lavalink 的播放事件。
type Event struct {
	Type        string `json:"type"` // TrackStartEvent／TrackEndEvent／TrackExceptionEvent／TrackStuckEvent／WebSocketClosedEvent
	GuildID     string `json:"guildId"`
	Track       *Track `json:"track"`
	Reason      string `json:"reason"` // TrackEnd：finished／loadFailed／stopped／replaced／cleanup；WebSocketClosed：原因文字
	ThresholdMs int64  `json:"thresholdMs"`
	Code        int    `json:"code"`
	ByRemote    bool   `json:"byRemote"`
	Exception   *struct {
		Message  string `json:"message"`
		Severity string `json:"severity"`
	} `json:"exception"`
}

// Handler Lavalink 事件的回呼。
type Handler interface {
	OnReady(sessionID string, resumed, reconnected bool)
	OnEvent(e Event)
	OnStats(f *FrameStats)
}

// Client Lavalink 節點。
type Client struct {
	base     string // http(s)://host:port
	password string
	userID   string
	http     *http.Client
	log      *slog.Logger
	handler  Handler

	mu        sync.RWMutex
	sessionID string
	conn      *websocket.Conn
}

// ErrUnavailable Lavalink 未連線（Python MusicUnavailable）。
var ErrUnavailable = errors.New("音樂伺服器（Lavalink）目前無法連線")

// LoadError Lavalink 解析失敗（Python LavalinkLoadException：記錄後當作沒有結果）。
type LoadError struct{ Message string }

func (e LoadError) Error() string { return "Lavalink 解析失敗：" + e.Message }

// NewClient 建立客戶端；Run 之後才連線。
func NewClient(uri, password, userID string, h Handler, log *slog.Logger) *Client {
	return &Client{base: strings.TrimRight(uri, "/"), password: password, userID: userID, handler: h, log: log,
		http: &http.Client{Timeout: 20 * time.Second}}
}

// Ready 是否已連線（有 Session）。
func (c *Client) Ready() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID != ""
}

func (c *Client) session() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID
}

// Run 維持 WebSocket 連線與監看，直到 ctx 取消。
func (c *Client) Run(ctx context.Context) {
	go c.watchdog(ctx)
	delay, first := time.Second, true
	for ctx.Err() == nil {
		err := c.connect(ctx, !first)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.log.Warn("連線 Lavalink 失敗，稍後重試", "uri", c.base, "type", fmt.Sprintf("%T", err), "retry", delay.String())
		} else {
			first, delay = false, time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func wsURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v4/websocket"
	return u.String(), nil
}

// connect 連線並讀取訊息，直到連線中斷（回傳 nil 表示連上過）。
func (c *Client) connect(ctx context.Context, reconnected bool) error {
	addr, err := wsURL(c.base)
	if err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Authorization", c.password)
	h.Set("User-Id", c.userID)
	h.Set("Client-Name", "farmgm-go/1.0")
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, addr, h)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer func() {
		conn.Close()
		c.mu.Lock()
		c.conn, c.sessionID = nil, ""
		c.mu.Unlock()
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				c.log.Warn("Lavalink 連線中斷", "type", fmt.Sprintf("%T", err))
			}
			return nil
		}
		c.dispatch(data, reconnected)
	}
}

func (c *Client) dispatch(data []byte, reconnected bool) {
	var msg struct {
		Op         string      `json:"op"`
		SessionID  string      `json:"sessionId"`
		Resumed    bool        `json:"resumed"`
		FrameStats *FrameStats `json:"frameStats"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	switch msg.Op {
	case "ready":
		c.mu.Lock()
		c.sessionID = msg.SessionID
		c.mu.Unlock()
		c.log.Info("已連線 Lavalink", "uri", c.base, "resumed", msg.Resumed)
		c.handler.OnReady(msg.SessionID, msg.Resumed, reconnected)
	case "event":
		var e Event
		if err := json.Unmarshal(data, &e); err == nil {
			c.handler.OnEvent(e)
		}
	case "stats":
		c.handler.OnStats(msg.FrameStats)
	}
}

// watchdog 定期確認 Session 仍有效；Lavalink 回 404 時關閉連線讓 Run 重新連線。
func (c *Client) watchdog(ctx context.Context) {
	t := time.NewTicker(WatchdogInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sid := c.session()
		if sid == "" {
			continue
		}
		status, _, err := c.do(ctx, http.MethodGet, "/v4/sessions/"+sid+"/players", nil)
		if err == nil && status == http.StatusNotFound {
			c.log.Warn("Lavalink Session 已失效（Lavalink 可能已重啟），重新連線")
			c.mu.Lock()
			if c.conn != nil {
				c.conn.Close()
			}
			c.mu.Unlock()
		}
	}
}

// do 送出 REST 請求，回傳狀態碼與回應內容。
func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", c.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, data, err
}

// Load 解析關鍵字（ytsearch:…）或網址（Python wavelink.Playable.search）。
func (c *Client) Load(ctx context.Context, identifier string) (LoadResult, error) {
	if !c.Ready() {
		return LoadResult{}, ErrUnavailable
	}
	status, data, err := c.do(ctx, http.MethodGet, "/v4/loadtracks?identifier="+url.QueryEscape(identifier), nil)
	if err != nil {
		return LoadResult{}, ErrUnavailable
	}
	if status != http.StatusOK {
		return LoadResult{}, LoadError{fmt.Sprintf("HTTP %d", status)}
	}
	return parseLoad(data)
}

func parseLoad(data []byte) (LoadResult, error) {
	var res struct {
		LoadType string          `json:"loadType"`
		Data     json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return LoadResult{}, LoadError{"回應格式錯誤"}
	}
	switch res.LoadType {
	case "track":
		var t Track
		if err := json.Unmarshal(res.Data, &t); err != nil {
			return LoadResult{}, LoadError{"回應格式錯誤"}
		}
		return LoadResult{Tracks: []Track{t}}, nil
	case "search":
		var ts []Track
		if err := json.Unmarshal(res.Data, &ts); err != nil {
			return LoadResult{}, LoadError{"回應格式錯誤"}
		}
		return LoadResult{Tracks: ts}, nil
	case "playlist":
		var p struct {
			Info struct {
				Name          string `json:"name"`
				SelectedTrack int    `json:"selectedTrack"`
			} `json:"info"`
			Tracks []Track `json:"tracks"`
		}
		if err := json.Unmarshal(res.Data, &p); err != nil {
			return LoadResult{}, LoadError{"回應格式錯誤"}
		}
		return LoadResult{Playlist: &Playlist{Name: p.Info.Name, Selected: p.Info.SelectedTrack, Tracks: p.Tracks}}, nil
	case "error":
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(res.Data, &e)
		return LoadResult{}, LoadError{e.Message}
	}
	return LoadResult{}, nil // empty
}

// playerPatch 更新播放器（只送有值的欄位）。
type playerPatch struct {
	Track *struct {
		Encoded *string `json:"encoded"`
	} `json:"track,omitempty"`
	Volume *int        `json:"volume,omitempty"`
	Paused *bool       `json:"paused,omitempty"`
	Voice  *VoiceState `json:"voice,omitempty"`
}

func (c *Client) patch(ctx context.Context, guildID string, p playerPatch) error {
	sid := c.session()
	if sid == "" {
		return ErrUnavailable
	}
	status, data, err := c.do(ctx, http.MethodPatch, "/v4/sessions/"+sid+"/players/"+guildID+"?noReplace=false", p)
	if err != nil {
		return ErrUnavailable
	}
	if status >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("Lavalink 回應 HTTP %d：%s", status, e.Message)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// Play 播放音軌（取代目前的音軌），設定音量並取消暫停（Python vc.play(track, volume=…)）。
func (c *Client) Play(ctx context.Context, guildID string, t Track, volume int) error {
	enc := t.Encoded
	return c.patch(ctx, guildID, playerPatch{Track: &struct {
		Encoded *string `json:"encoded"`
	}{&enc}, Volume: &volume, Paused: ptr(false)})
}

// Stop 停止目前的音軌（Lavalink 送出 TrackEndEvent reason=stopped）。
func (c *Client) Stop(ctx context.Context, guildID string) error {
	return c.patch(ctx, guildID, playerPatch{Track: &struct {
		Encoded *string `json:"encoded"`
	}{nil}})
}

// Pause 暫停或繼續。
func (c *Client) Pause(ctx context.Context, guildID string, paused bool) error {
	return c.patch(ctx, guildID, playerPatch{Paused: &paused})
}

// Volume 設定音量（100 = 原始音量）。
func (c *Client) Volume(ctx context.Context, guildID string, volume int) error {
	return c.patch(ctx, guildID, playerPatch{Volume: &volume})
}

// Voice 送出 Discord 語音連線資料（加入或移動頻道後）。
func (c *Client) Voice(ctx context.Context, guildID string, v VoiceState) error {
	return c.patch(ctx, guildID, playerPatch{Voice: &v})
}

// Destroy 刪除播放器（離開語音頻道時）。
func (c *Client) Destroy(ctx context.Context, guildID string) error {
	sid := c.session()
	if sid == "" {
		return nil
	}
	_, _, err := c.do(ctx, http.MethodDelete, "/v4/sessions/"+sid+"/players/"+guildID, nil)
	return err
}
