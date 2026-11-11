package cachebus

// 需要 Redis 的測試讀環境變數 FARMGM_TEST_REDIS_URL（沒有設定時略過）；端對端的部分另外需要測試資料庫。

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"farmgm/internal/db"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/repositories/featuresettings"
	"farmgm/internal/services"
	"farmgm/internal/services/svctest"
)

func redisURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("FARMGM_TEST_REDIS_URL")
	if u == "" {
		t.Skip("沒有設定 FARMGM_TEST_REDIS_URL")
	}
	return u
}

func quietLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// watch 記錄 onHandled 收到的通知種類。
func watch(t *testing.T) chan string {
	ch := make(chan string, 10)
	onHandled = func(kind string) { ch <- kind }
	t.Cleanup(func() { onHandled = nil })
	return ch
}

func expect(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("沒有收到通知", want)
	}
}

func expectNone(t *testing.T, ch chan string) {
	t.Helper()
	select {
	case got := <-ch:
		t.Fatal("不應收到通知", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWithoutRedisNothingChanges(t *testing.T) {
	log, buf := quietLog()
	if b := StartFromSettings(context.Background(), "", log); b != nil {
		t.Fatal("沒有設定 REDIS_URL 時不應使用 Redis")
	}
	if b := StartFromSettings(context.Background(), "not a url", log); b != nil || !strings.Contains(buf.String(), "每 10 秒重新讀取") {
		t.Fatal(buf.String())
	}
	if b := StartFromSettings(context.Background(), "redis://:secret@127.0.0.1:1/0", log); b != nil || strings.Contains(buf.String(), "secret") {
		t.Fatal(buf.String()) // 連不上：照常啟動，訊息不含密碼
	}
	if db.CatalogTTL != DefaultTTL || featuresettings.RefreshInterval != DefaultTTL || db.CatalogChanged != nil {
		t.Fatal("沒有 Redis 時應維持原本的設定")
	}
	var nilBus *Bus
	nilBus.Close() // 沒有 Redis 時 defer bus.Close() 也安全
}

func TestOtherProcessNotificationsClearCaches(t *testing.T) {
	url := redisURL(t)
	log, _ := quietLog()
	b, err := Start(context.Background(), url, log)
	if err != nil {
		t.Fatal(err)
	}
	if db.CatalogTTL != Fallback || featuresettings.RefreshInterval != Fallback || db.CatalogChanged == nil || featuresettings.Changed == nil {
		t.Fatal("有 Redis 時應拉長保存時間並設定通知")
	}
	ch := watch(t)
	opt, _ := redis.ParseURL(url)
	other := redis.NewClient(opt)
	defer other.Close()
	ctx := context.Background()
	// 其他程序的通知：清空對應的快取
	for _, kind := range []string{KindCatalog, KindFeatures, "unknown"} {
		if err := other.Publish(ctx, Channel, kind+" other-process").Err(); err != nil {
			t.Fatal(err)
		}
		expect(t, ch, kind)
	}
	// 自己發布的：本程序在提交時已經清空，不再處理
	b.Publish(KindCatalog)
	featuresettings.NotifyChanged()
	expectNone(t, ch)
	b.Close()
	if db.CatalogTTL != DefaultTTL || featuresettings.RefreshInterval != DefaultTTL || db.CatalogChanged != nil || featuresettings.Changed != nil {
		t.Fatal("關閉後應還原")
	}
	b.Close() // 可重複關閉
}

func TestCommittedWritesPublishToOtherProcesses(t *testing.T) {
	url := redisURL(t)
	d := svctest.Prepare(t, services.ClearNewsCache)
	log, _ := quietLog()
	b, err := Start(context.Background(), url, log)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// 另一個程序的訂閱者
	opt, _ := redis.ParseURL(url)
	other := redis.NewClient(opt)
	defer other.Close()
	ctx := context.Background()
	sub := other.Subscribe(ctx, Channel)
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := sub.Channel()
	next := func() string {
		t.Helper()
		select {
		case m := <-msgs:
			kind, _, _ := strings.Cut(m.Payload, " ")
			return kind
		case <-time.After(3 * time.Second):
			t.Fatal("沒有發布通知")
			return ""
		}
	}
	admin := services.NewAdminService(d, services.Options{})
	// 管理員修改遊戲目錄（停用作物）：提交後通知
	if _, err := admin.SetCropActive(ctx, "test", "corn", false); err != nil {
		t.Fatal(err)
	}
	if k := next(); k != KindCatalog {
		t.Fatal(k)
	}
	// 修改失敗（不存在的作物）：沒有提交，不通知
	if _, err := admin.SetCropActive(ctx, "test", "no-such-crop", false); err == nil {
		t.Fatal("應失敗")
	}
	// 管理員儲存規則設定：通知
	key := featurecfg.Keys()[0]
	if _, err := admin.UpdateFeatureSettings(ctx, "test", key, featurecfg.Defaults(key)); err != nil {
		t.Fatal(err)
	}
	if k := next(); k != KindFeatures {
		t.Fatal(k)
	}
	select {
	case m := <-msgs:
		t.Fatal("多出的通知", m.Payload)
	case <-time.After(300 * time.Millisecond):
	}
}
