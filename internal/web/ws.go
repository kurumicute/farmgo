package web

// 監控 WebSocket（對應 Python app/api/monitor.py 的 monitor_ws 與 SnapshotCache）。
//
// gorilla/websocket 不支援並行讀寫：每條連線由一個 goroutine 負責讀（只用來偵測用戶端的「重新整理」與斷線），
// 處理函式本身是唯一的寫入者。

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"farmgm/internal/services"
)

const (
	// ForceMaxAge 使用者按「重新整理」時：半秒內剛查過的結果仍可沿用（多分頁同時重新整理只查一次）。
	ForceMaxAge = 500 * time.Millisecond
	// StaleMax 資料沒變時沿用快照的最長時間（土地成熟、枯萎是隨時間變化的）。
	StaleMax = 60 * time.Second

	wsWriteTimeout = 10 * time.Second
	snapshotFarms  = 200
)

// SnapshotCache 所有 WebSocket 連線共用的監控快照：同時開多個分頁時，同一段時間內只查詢一次資料庫。
//
// 快照在 maxAge 內直接沿用；同時有多個連線要更新時，由鎖保證只有一個去查詢，其餘等它完成後沿用結果。
// 超過 maxAge 時先查「資料版本」：沒有任何遊戲操作就沿用上一份快照（只更新 server_time），
// 但最長 StaleMax 仍會重新查詢一次。查詢失敗不快取（下一個連線會重試）。
type SnapshotCache struct {
	mu      sync.Mutex
	value   *services.Snapshot
	at      time.Time // 上一次回傳（含沿用）的時間
	builtAt time.Time // 上一次完整查詢的時間
	version services.DataVersion
	builds  int // 完整查詢的次數（測試用）
}

// Get force：使用者按「重新整理」，一定重新查詢（但 ForceMaxAge 內剛查過的仍沿用）。回傳值不可修改。
func (c *SnapshotCache) Get(ctx context.Context, m *services.MonitorService, maxAge time.Duration, force bool) (*services.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != nil && time.Since(c.at) < maxAge {
		return c.value, nil
	}
	version, err := m.DataVersion(ctx)
	if err != nil {
		return nil, err
	}
	if !force && c.value != nil && version == c.version && time.Since(c.builtAt) < StaleMax {
		// 沒有任何遊戲操作：沿用上一份（generated_at 不變，前端不重繪），只更新伺服器時間
		v := *c.value
		v.ServerTime = m.Now()
		c.value, c.at = &v, time.Now()
		return c.value, nil
	}
	snap, err := m.Snapshot(ctx, snapshotFarms)
	if err != nil {
		return nil, err
	}
	c.value, c.version = snap, version
	c.at = time.Now()
	c.builtAt = c.at
	c.builds++
	return snap, nil
}

// sameOrigin 瀏覽器的 WebSocket 不受同源政策限制，因此要檢查 Origin，防止其他網站讀取監控資料。
// 沒有 Origin 的是非瀏覽器用戶端（測試或命令列工具）。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

var upgrader = websocket.Upgrader{CheckOrigin: sameOrigin}

type wsMessage struct {
	Type    string             `json:"type"`
	Data    *services.Snapshot `json:"data,omitempty"`
	Message string             `json:"message,omitempty"`
}

func (a *App) monitorWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil) // Origin 不符時回 403，不建立連線
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, untrack := a.trackWS()
	defer untrack()

	// 讀取端：用戶端送出任何訊息（例如 "refresh"）就立即重新整理；讀取失敗代表斷線
	refresh := make(chan struct{}, 1)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
			select {
			case refresh <- struct{}{}:
			default:
			}
		}
	}()

	// 定時推送：比更新間隔稍短的快照仍算新鮮，讓多個分頁共用同一次查詢，又不會把舊資料推兩輪
	normal := a.refresh * 8 / 10
	maxAge := normal
	for {
		msg := wsMessage{Type: "snapshot"}
		snap, err := a.snapshot.Get(ctx, a.monitor, maxAge, maxAge == ForceMaxAge)
		if err != nil {
			slog.Warn("監控快照讀取失敗", "type", fmt.Sprintf("%T", err))
			msg = wsMessage{Type: "error", Message: DBErrorMessage}
		} else {
			msg.Data = snap
		}
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		if err := conn.WriteJSON(msg); err != nil {
			return
		}
		timer := time.NewTimer(a.refresh)
		select {
		case <-refresh:
			maxAge = ForceMaxAge
		case <-timer.C:
			maxAge = normal
		case <-closed:
			timer.Stop()
			return
		case <-done:
			timer.Stop()
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, ""), time.Now().Add(time.Second))
			return
		}
		timer.Stop()
	}
}
