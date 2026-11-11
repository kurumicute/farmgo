package db

// 遊戲目錄的程序內快取（對應 Python app/db/catalog_cache.py）：作物、動物、魚種與品質設定幾乎不變，每筆交易卻都要查。
//
//   - 最多 CatalogTTL 讀一次；Bot 與網站是不同程序，管理網站改完後 Bot 最晚 CatalogTTL 內套用。
//   - 回傳的是複製的切片（資料列為值複製），唯讀：結構中的 map／切片欄位與快取共用，不可修改。
//     要修改目錄一律用加鎖的查詢（FOR UPDATE），不要改快取拿到的資料。
//   - 同一程序內的寫入：這筆交易寫過目錄（crop_types、animal_types、fish_types、crop_genetics_settings）後，
//     到交易結束前都不用快取，結束時（不論提交或回滾）清空。測試或腳本繞過 Tx 寫入時要呼叫 CatalogClear()。

import (
	"slices"
	"sync"
	"time"
)

// CatalogTTL 快取有效時間；0 = 不快取（測試預設）。
var CatalogTTL = 10 * time.Second

type catalogEntry struct {
	at  time.Time
	val any
}

var (
	catalogMu    sync.Mutex
	catalogCache = map[string]catalogEntry{}
)

// CatalogChanged 寫入遊戲目錄的交易提交後呼叫（cachebus 設定為透過 Redis 通知其他程序清空快取；nil 時不通知）。
var CatalogChanged func()

// CatalogClear 清空目錄快取。
func CatalogClear() {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	catalogCache = map[string]catalogEntry{}
}

func catalogLookup(t *Tx, key string, compute func() (any, error)) (any, error) {
	ttl := CatalogTTL
	if ttl <= 0 || t.catalogDirty {
		return compute()
	}
	now := time.Now()
	catalogMu.Lock()
	hit, ok := catalogCache[key]
	catalogMu.Unlock()
	if ok && now.Sub(hit.at) < ttl {
		return hit.val, nil
	}
	v, err := compute()
	if err != nil {
		return nil, err
	}
	catalogMu.Lock()
	catalogCache[key] = catalogEntry{now, v}
	catalogMu.Unlock()
	return v, nil
}

// CatalogRows 快取一串目錄資料列；回傳新的切片（呼叫端可排序或過濾）。
func CatalogRows[T any](t *Tx, key string, compute func() ([]T, error)) ([]T, error) {
	v, err := catalogLookup(t, key, func() (any, error) { return compute() })
	if err != nil {
		return nil, err
	}
	return slices.Clone(v.([]T)), nil
}

// CatalogValue 快取純資料（例如設定）；唯讀。
func CatalogValue[T any](t *Tx, key string, compute func() (T, error)) (T, error) {
	v, err := catalogLookup(t, key, func() (any, error) { return compute() })
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}
