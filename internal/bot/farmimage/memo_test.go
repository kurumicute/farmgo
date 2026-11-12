package farmimage

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 同一張圖同時只產生一次，所有請求拿到同一份位元組（Go 版新增：避免冷啟動時大量請求重複壓縮）。
func TestMemoConcurrentProducesOnce(t *testing.T) {
	cache := map[string][]byte{}
	var calls atomic.Int32
	release := make(chan struct{})
	produce := func() ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte("png"), nil
	}
	const n = 50
	out := make([][]byte, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := memo(&cache, "k", produce)
			if err != nil {
				t.Error(err)
			}
			out[i] = v
		}()
	}
	time.Sleep(50 * time.Millisecond) // 讓其他請求都進入等待
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("產生 %d 次，應該只有 1 次", got)
	}
	for _, v := range out {
		if &v[0] != &out[0][0] {
			t.Fatal("應該回傳同一份位元組")
		}
	}
	if _, ok := cache["k"]; !ok {
		t.Fatal("沒有寫入快取")
	}
}

// 失敗不快取：等待中的請求收到同一個錯誤，下一次重新產生。
func TestMemoErrorNotCached(t *testing.T) {
	cache := map[string][]byte{}
	boom := errors.New("boom")
	if _, err := memo(&cache, "k", func() ([]byte, error) { return []byte("x"), boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if len(cache) != 0 {
		t.Fatal("失敗的結果不應快取")
	}
	v, err := memo(&cache, "k", func() ([]byte, error) { return []byte("ok"), nil })
	if err != nil || string(v) != "ok" {
		t.Fatal(v, err)
	}
}

// 產生時 panic：panic 照常往上拋，等待中的請求收到錯誤而不是永遠卡住，之後可以重新產生。
func TestMemoPanicReleasesWaiters(t *testing.T) {
	cache := map[string][]byte{}
	started, release := make(chan struct{}), make(chan struct{})
	waiter := make(chan error, 1)
	go func() {
		defer func() { _ = recover() }()
		_, _ = memo(&cache, "k", func() ([]byte, error) {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started
	go func() {
		_, err := memo(&cache, "k", func() ([]byte, error) { return []byte("late"), nil })
		waiter <- err
	}()
	time.Sleep(50 * time.Millisecond) // 第二個請求進入等待
	close(release)
	select {
	case err := <-waiter:
		if !errors.Is(err, errMemoPanicked) {
			t.Fatalf("等待中的請求應收到 panic 錯誤：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待中的請求卡住了")
	}
	if v, err := memo(&cache, "k", func() ([]byte, error) { return []byte("again"), nil }); err != nil || string(v) != "again" {
		t.Fatal(v, err)
	}
}
