package site

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/services"
)

// 介紹網站壓力測試（2026-11-19，中英文切換）：設 FARMGM_STRESS=1 才執行。
// FARMGM_STRESS_USERS 位訪客同時瀏覽（預設 1000，同時進行中的請求最多 200 個），一半中文、一半英文；每位訪客依序開首頁、site.js、
// 首頁摘要與 5 個手冊欄位、統計，最後用 ?lang= 切換語言、帶著 Cookie 再讀一個手冊欄位。使用真實時鐘與正式環境的快取設定。
func TestStressSite(t *testing.T) {
	if os.Getenv("FARMGM_STRESS") == "" {
		t.Skip("壓力測試：設 FARMGM_STRESS=1 才執行")
	}
	e := setup(t)
	users := 1000
	if n, err := strconv.Atoi(os.Getenv("FARMGM_STRESS_USERS")); err == nil && n > 0 {
		users = n
	}
	a := New(Options{Service: services.NewSiteService(e.d, clock.System{})})
	srv := httptest.NewServer(a)
	defer srv.Close()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 256}, Timeout: 30 * time.Second}

	parts := make([]string, 0, len(services.GuideParts))
	for p := range services.GuideParts {
		if p != "summary" {
			parts = append(parts, p)
		}
	}
	slices.Sort(parts)

	var mu sync.Mutex
	lat := map[string][]float64{}
	var order []string
	var errs []string
	var bytes int64
	// 同時進行中的請求最多 200 個：Windows 本機一次撥上千條連線會超過 listen 佇列（連線被拒，與網站無關）；
	// 正式環境前面有反向代理，也會限制同時連到程式的數量
	sem := make(chan struct{}, 200)
	get := func(step, path, accept string, cookie *http.Cookie) *http.Response {
		sem <- struct{}{}
		defer func() { <-sem }()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if accept != "" {
			req.Header.Set("Accept-Language", accept)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		start := time.Now()
		res, err := client.Do(req)
		var n int64
		if err == nil {
			n, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		ms := float64(time.Since(start).Microseconds()) / 1000
		mu.Lock()
		defer mu.Unlock()
		if _, ok := lat[step]; !ok {
			order = append(order, step)
		}
		lat[step] = append(lat[step], ms)
		bytes += n
		if err != nil {
			errs = append(errs, step+"："+err.Error())
		} else if res.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s：HTTP %d", step, res.StatusCode))
		}
		return res
	}
	visitor := func(i int) {
		accept, other := "zh-TW,zh;q=0.9", "en"
		if i%2 == 1 {
			accept, other = "en-US,en;q=0.9", "zh-TW"
		}
		get("首頁", "/", accept, nil)
		get("site.js", "/static/site.js?lang="+map[bool]string{true: "en", false: "zh-TW"}[i%2 == 1], "", nil)
		get("手冊摘要", "/api/guide/summary", accept, nil)
		for k := range 5 {
			get("手冊欄位", "/api/guide/"+parts[(i*7+k*3)%len(parts)], accept, nil)
		}
		get("統計", "/api/stats", accept, nil)
		res := get("切換語言", "/?lang="+other, accept, nil) // 設定 Cookie
		var c *http.Cookie
		if res != nil {
			for _, x := range res.Cookies() {
				if x.Name == LangCookie {
					c = x
				}
			}
		}
		get("手冊欄位（切換後）", "/api/guide/"+parts[i%len(parts)], accept, c)
	}

	t0 := time.Now()
	var wg sync.WaitGroup
	for i := range users {
		wg.Add(1)
		go func() { defer wg.Done(); visitor(i) }()
	}
	wg.Wait()
	elapsed := time.Since(t0)

	total := 0
	for _, xs := range lat {
		total += len(xs)
	}
	fmt.Printf("===== 介紹網站壓力測試：%d 位訪客同時瀏覽（一半中文、一半英文）=====\n", users)
	fmt.Printf("%d 個請求，%.1f 秒（%.0f 個/秒），傳輸 %.1f MB\n", total, elapsed.Seconds(), float64(total)/elapsed.Seconds(), float64(bytes)/(1<<20))
	for _, step := range order {
		xs := slices.Clone(lat[step])
		slices.Sort(xs)
		pct := func(p float64) float64 { return xs[min(len(xs)-1, int(float64(len(xs))*p))] }
		var sum float64
		for _, x := range xs {
			sum += x
		}
		fmt.Printf("  %-12s n=%-5d 平均 %6.0f  P50 %6.0f  P95 %6.0f  P99 %6.0f  最大 %6.0f ms\n",
			step, len(xs), sum/float64(len(xs)), pct(.5), pct(.95), pct(.99), xs[len(xs)-1])
	}
	if len(errs) > 0 {
		t.Errorf("錯誤 %d 個（例如：%s）", len(errs), errs[0])
	} else {
		fmt.Println("錯誤：0")
	}
}
