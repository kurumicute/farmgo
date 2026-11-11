package db

// 交易內快取（對應 Python app/db/txcache.py）：一鍵操作的逐格快取（Bulk／Memo）與讀取整頁的讀取快取（ReadScope／ReadMemo）。
//
// 種滿、全部澆水／收成／清除與自動播種會逐格處理，每格都重查同樣的設定（品質設定、限時活動、工具、蚯蚓、群組成員、
// 寵物等級、進行中的任務與競賽、倉庫用量…）。Bulk() 期間這些「同一筆交易中不會被迴圈改變」的查詢只做一次；
// 只包住逐格迴圈，離開就清掉，其他方法照常每次查詢。

import "context"

// Bulk 在逐格快取中執行 fn；已在外層 Bulk 中時直接執行。成功離開時送出延後的寫入，失敗時丟棄
// （交易或 SAVEPOINT 會回滾）。
func (t *Tx) Bulk(ctx context.Context, fn func() error) error {
	if t.bulk != nil {
		return fn()
	}
	t.bulk = map[any]any{}
	defer func() { t.bulk = nil }()
	if err := fn(); err != nil {
		t.discardWrites()
		return err
	}
	return t.FlushWrites(ctx)
}

// InBulk 是否在 Bulk() 中。
func (t *Tx) InBulk() bool { return t.bulk != nil }

// Memo Bulk() 期間同一個 key 只計算一次；不在 Bulk() 中時每次都計算。key 必須可比較（字串、struct、陣列）。
func Memo[T any](t *Tx, key any, compute func() (T, error)) (T, error) {
	if t.bulk == nil {
		return compute()
	}
	if v, ok := t.bulk[key]; ok {
		return v.(T), nil
	}
	v, err := compute()
	if err != nil {
		return v, err
	}
	t.bulk[key] = v
	return v, nil
}

// Adjust 快取中的整數跟著寫入一起變動（例如倉庫用量）；沒有快取時不做任何事。
func (t *Tx) Adjust(key any, delta int) {
	if t.bulk == nil {
		return
	}
	if v, ok := t.bulk[key].(int); ok {
		t.bulk[key] = v + delta
	}
}

// ---------- 延後的寫入：一鍵操作逐格累積，離開 Bulk 或下一次查詢前整批送出 ----------
// 安全規則：這筆交易的任何 Exec／Query／QueryRow 都會先送出，所以讀到的一定包含已累積的寫入；
// 失敗時連同交易一起作廢。buffer 由各 repository 定義，送出順序依第一次累積的先後。

// Flusher 延後寫入的累積內容；Flush 必須只用 *NoFlush 方法寫入。
type Flusher interface {
	Flush(ctx context.Context, t *Tx) error
}

// Deferred 取得 key 對應的累積內容；不在 Bulk() 中時回傳 nil（呼叫端照常立即寫入）。
func Deferred[F Flusher](t *Tx, key any, factory func() F) (F, bool) {
	var zero F
	if t.bulk == nil {
		return zero, false
	}
	t.clearReads() // 與立即寫入相同：讀取快取不能再用
	if t.bufs == nil {
		t.bufs = map[any]Flusher{}
	}
	if f, ok := t.bufs[key]; ok {
		return f.(F), true
	}
	f := factory()
	t.bufs[key] = f
	t.order = append(t.order, key)
	return f, true
}

// FlushWrites 送出所有延後的寫入。
func (t *Tx) FlushWrites(ctx context.Context) error {
	if t.flushing || len(t.order) == 0 {
		return nil
	}
	order, bufs := t.order, t.bufs
	t.order, t.bufs = nil, nil
	t.flushing = true
	defer func() { t.flushing = false }()
	for _, key := range order {
		if err := bufs[key].Flush(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) discardWrites() {
	t.order, t.bufs = nil, nil
}

// ---------- 讀取快取：面板讀取整頁時，同一筆交易中重複的讀取只查一次 ----------
// 安全規則：只要這筆交易有任何寫入，整個讀取快取立刻清空；加鎖的查詢（FOR UPDATE）不使用快取。

// ReadScope 在讀取快取中執行 fn；已在外層範圍中時直接執行。
func (t *Tx) ReadScope(fn func() error) error {
	if t.read != nil {
		return fn()
	}
	t.read = map[any]any{}
	defer func() { t.read = nil }()
	return fn()
}

// ReadMemo 讀取範圍中同一個 key 只查一次。回傳切片時呼叫端不可修改內容（需要時自行複製）。
func ReadMemo[T any](t *Tx, key any, compute func() (T, error)) (T, error) {
	if t.read == nil {
		return compute()
	}
	if v, ok := t.read[key]; ok {
		return v.(T), nil
	}
	v, err := compute()
	if err != nil {
		return v, err
	}
	if t.read != nil { // compute 中有寫入時快取已清空，但範圍仍在
		t.read[key] = v
	}
	return v, nil
}

func (t *Tx) clearReads() {
	if len(t.read) > 0 {
		t.read = map[any]any{}
	}
}
