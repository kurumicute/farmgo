// Package cachebus 以 Redis Pub/Sub 讓各程序（網站、Bot、介紹網站）的程序內快取同步失效（Go 版新增，Python 沒有）。
//
// 背景：遊戲目錄（作物、動物、魚種、品質設定）與新功能規則設定在每個程序內快取。沒有 Redis 時每 10 秒重新讀取一次，
// 管理網站修改後 Bot 最晚 10 秒才套用。設定 REDIS_URL 時：
//   - 寫入目錄的交易提交後、管理員儲存或還原規則設定後，發布一則通知；
//   - 所有程序（包含發布者自己以外的）收到後立即清空快取，下一筆交易重新讀取；
//   - 因此快取的保存時間可以拉長（Fallback），只是防止漏掉通知（Redis 重啟、斷線）的安全網；
//   - 訂閱（重新）連上時也清空一次，避免斷線期間漏掉的通知。
//
// Redis 只放這些通知，不保存遊戲資料；Redis 掛掉時遊戲照常運作，只是回到依保存時間重新讀取。
package cachebus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"farmgm/internal/db"
	"farmgm/internal/repositories/featuresettings"
)

// Channel 通知頻道。訊息格式「種類 發布者ID」。
const Channel = "farmgm:cache"

// 通知種類。
const (
	KindCatalog  = "catalog"
	KindFeatures = "features"
)

// Fallback 有 Redis 時快取的保存時間（沒有 Redis 時是 10 秒）：只用來防止漏掉通知。
const Fallback = 60 * time.Second

// Bus 一個程序的訂閱與發布。
type Bus struct {
	rdb    *redis.Client
	id     string
	log    *slog.Logger
	cancel context.CancelFunc
	sub    *redis.PubSub
	done   chan struct{}
	mu     sync.Mutex
	closed bool
}

// Start 連線 Redis、開始訂閱，並把 db.CatalogChanged、featuresettings.Changed 設為發布通知。
// url 為 redis://… 或 rediss://…；連不上時回傳錯誤（呼叫端記錄後照常啟動，沒有 Redis 也能運作）。
func Start(ctx context.Context, url string, log *slog.Logger) (*Bus, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL 格式不正確")
	}
	rdb := redis.NewClient(opt)
	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPing()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("無法連線到 Redis（%T）", err)
	}
	b := &Bus{rdb: rdb, id: newID(), log: log, done: make(chan struct{})}
	subCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	b.cancel = cancel
	b.sub = rdb.Subscribe(subCtx, Channel)
	ready := make(chan struct{})
	go b.listen(subCtx, ready)
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		b.Close()
		return nil, fmt.Errorf("Redis 訂閱逾時")
	}
	db.CatalogTTL = Fallback
	featuresettings.RefreshInterval = Fallback
	db.CatalogChanged = func() { b.Publish(KindCatalog) }
	featuresettings.Changed = func() { b.Publish(KindFeatures) }
	return b, nil
}

func newID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// listen 訂閱通知直到 ctx 取消；go-redis 斷線後會自動重新訂閱，每次（重新）訂閱成功時清空快取。
func (b *Bus) listen(ctx context.Context, ready chan struct{}) {
	defer close(b.done)
	sub := b.sub
	first := true
	for {
		msg, err := sub.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.Warn("Redis 訂閱中斷，稍後重試", "type", fmt.Sprintf("%T", err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		switch m := msg.(type) {
		case *redis.Subscription:
			if m.Kind != "subscribe" {
				continue
			}
			clearAll() // 斷線期間可能漏掉通知
			if first {
				first = false
				close(ready)
			} else {
				b.log.Info("已重新訂閱 Redis 快取通知")
			}
		case *redis.Message:
			b.handle(m.Payload)
		}
	}
}

// handle 處理一則通知；自己發布的不處理（本程序在提交時已經清空）。
func (b *Bus) handle(payload string) {
	kind, from, _ := strings.Cut(payload, " ")
	if from == b.id {
		return
	}
	if onHandled != nil {
		defer onHandled(kind)
	}
	switch kind {
	case KindCatalog:
		db.CatalogClear()
	case KindFeatures:
		featuresettings.Invalidate()
	default:
		clearAll()
	}
}

// onHandled 測試用：處理完一則（不是自己發布的）通知後呼叫。
var onHandled func(kind string)

func clearAll() {
	db.CatalogClear()
	featuresettings.Invalidate()
}

// Publish 發布一則通知；失敗只記錄（其他程序最晚 Fallback 後自行重新讀取）。
func (b *Bus) Publish(kind string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.rdb.Publish(ctx, Channel, kind+" "+b.id).Err(); err != nil {
		b.log.Warn("發布快取通知失敗", "kind", kind, "type", fmt.Sprintf("%T", err))
	}
}

// Close 停止訂閱、還原沒有 Redis 時的設定並關閉連線。
func (b *Bus) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	db.CatalogChanged, featuresettings.Changed = nil, nil
	db.CatalogTTL, featuresettings.RefreshInterval = DefaultTTL, DefaultTTL
	b.cancel()
	_ = b.sub.Close() // Receive 等待訊息時不理會 context：關閉訂閱連線讓它返回
	<-b.done
	_ = b.rdb.Close()
}

// DefaultTTL 沒有 Redis 時的保存時間（與 db.CatalogTTL、featuresettings.RefreshInterval 的預設相同）。
const DefaultTTL = 10 * time.Second

// StartFromSettings 程式進入點使用：url 為空時不使用 Redis；連不上時記錄警告後照常啟動（回傳 nil）。
func StartFromSettings(ctx context.Context, url string, log *slog.Logger) *Bus {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	b, err := Start(ctx, url, log)
	if err != nil {
		log.Warn("Redis 無法使用，快取改為每 10 秒重新讀取", "reason", err.Error())
		return nil
	}
	log.Info("已連線 Redis：管理網站修改目錄與規則設定後，各程序立即套用")
	return b
}
