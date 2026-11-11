// Package featuresettings 新功能規則設定（feature_settings）的讀寫與程序內快取（對應 Python app/repositories/feature_settings.py）。
//
// 各程序（Bot、網站）每隔 RefreshInterval 在第一筆交易時重新讀取一次（4 列以內），因此管理員在網站修改後，
// Bot 最晚 RefreshInterval 內套用；網站本身在儲存後立即套用（featurecfg.SetCurrent）。
package featuresettings

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/featurecfg"
)

// RefreshInterval 重新讀取的間隔。
var RefreshInterval = 10 * time.Second

var (
	mu       sync.Mutex
	loadedAt time.Time // 零值 = 尚未讀取
)

// Refresh 距離上次讀取超過 RefreshInterval 時，從資料庫重新載入目前設定。force 預設 false。
func Refresh(ctx context.Context, t *db.Tx, force bool) error {
	now := time.Now()
	mu.Lock()
	if !force && !loadedAt.IsZero() && now.Sub(loadedAt) < RefreshInterval {
		mu.Unlock()
		return nil
	}
	loadedAt = now // 先記時間：讀取失敗時其他 goroutine 不會同時重試
	mu.Unlock()
	rows, err := db.QueryStructs[models.FeatureSetting](ctx, t,
		"SELECT key, value, updated_at, updated_by FROM feature_settings")
	if err != nil {
		return err
	}
	stored := make(map[string]featurecfg.Value, len(rows))
	for _, r := range rows {
		v, err := featurecfg.Decode(r.Value)
		if err != nil { // 不是 JSON 物件：當作不合法設定，用預設值
			slog.Warn("新功能設定不是 JSON 物件，改用預設值", "key", r.Key)
			continue
		}
		stored[r.Key] = v
	}
	featurecfg.Apply(stored)
	return nil
}

// Changed 管理員儲存或還原設定（已提交）後呼叫（cachebus 設定為透過 Redis 通知其他程序重新讀取；nil 時不通知）。
var Changed func()

// NotifyChanged 設定已提交：通知其他程序。
func NotifyChanged() {
	if Changed != nil {
		Changed()
	}
}

// Invalidate 下一筆交易重新讀取（測試清空資料表後使用）。
func Invalidate() {
	mu.Lock()
	defer mu.Unlock()
	loadedAt = time.Time{}
}

// Rows key → 設定列。
func Rows(ctx context.Context, t *db.Tx) (map[string]models.FeatureSetting, error) {
	rows, err := db.QueryStructs[models.FeatureSetting](ctx, t,
		"SELECT key, value, updated_at, updated_by FROM feature_settings")
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.FeatureSetting, len(rows))
	for _, r := range rows {
		out[r.Key] = r
	}
	return out, nil
}

// Save 寫入（或覆蓋）一組設定。
func Save(ctx context.Context, t *db.Tx, key string, value featurecfg.Value, actor string, now time.Time) error {
	_, err := t.Exec(ctx, "INSERT INTO feature_settings (key, value, updated_at, updated_by) VALUES ($1, $2, $3, $4) "+
		"ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at, "+
		"updated_by = EXCLUDED.updated_by", key, value, now, actor)
	return err
}

func Remove(ctx context.Context, t *db.Tx, key string) error {
	_, err := t.Exec(ctx, "DELETE FROM feature_settings WHERE key = $1", key)
	return err
}
