package web

// 對應 Python tests/test_auth.py 的 API 測試，以及 tests/test_monitor_api.py 的管理 API 寫入防護。
// 雜湊、session、鎖定的單元測試在 internal/auth。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"farmgm/internal/auth"
)

// 測試專用的管理員帳密（執行時產生雜湊，與正式 .env 無關）。
const (
	testAdminUser     = "admin"
	testAdminPassword = "correct-horse-123"
)

var testHash = sync.OnceValue(func() string {
	h, err := auth.HashPassword(testAdminPassword)
	if err != nil {
		panic(err)
	}
	return h
})

func testAuth() *auth.AdminAuth { return auth.New(testAdminUser, testHash(), auth.Options{}) }

var adminHeader = map[string]string{AdminHeader: "1"}

func (e *webEnv) send(method, target, body string, headers map[string]string) *http.Response {
	e.t.Helper()
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	return e.request(method, target, r, headers).Result()
}

func loginJSON(user, pw string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	return string(b)
}

func detailOf(t *testing.T, res *http.Response) string {
	t.Helper()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	s, _ := body["detail"].(string)
	return s
}

func (e *webEnv) me() map[string]any {
	e.t.Helper()
	var m map[string]any
	e.getJSON("/api/auth/me", nil, &m)
	return m
}

func TestLoginAPIFlow(t *testing.T) {
	e := setupWeb(t, 0.2)
	if m := e.me(); m["enabled"] != true || m["logged_in"] != false || m["username"] != nil {
		t.Fatal(m)
	}
	body := loginJSON(testAdminUser, testAdminPassword)
	// 登入請求同樣需要防 CSRF 標頭，且不接受跨來源
	if res := e.send("POST", "/api/auth/login", body, nil); res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	if res := e.send("POST", "/api/auth/login", body, map[string]string{AdminHeader: "1", "Origin": "http://evil.example"}); res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	res := e.send("POST", "/api/auth/login", loginJSON("admin", "nope"), adminHeader)
	if res.StatusCode != 401 || detailOf(t, res) != "帳號或密碼錯誤。" {
		t.Fatal(res.StatusCode)
	}
	res = e.send("POST", "/api/auth/login", loginJSON("nobody", testAdminPassword), adminHeader)
	if detailOf(t, res) != "帳號或密碼錯誤。" { // 不透露帳號是否存在
		t.Fatal(res.StatusCode)
	}

	rec := e.request("POST", "/api/auth/login", strings.NewReader(body), adminHeader)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cookie := strings.ToLower(rec.Header().Get("Set-Cookie"))
	if !strings.Contains(cookie, "httponly") || !strings.Contains(cookie, "samesite=strict") || !strings.Contains(cookie, "path=/api") ||
		strings.Contains(cookie, "secure") { // 測試是 http，不設 Secure
		t.Fatal(cookie)
	}
	if strings.Contains(rec.Body.String(), testAdminPassword) || strings.Contains(rec.Body.String(), "token") {
		t.Fatal(rec.Body.String())
	}
	if e.me()["logged_in"] != true {
		t.Fatal("應已登入")
	}

	on := `{"active": true}`
	if res := e.send("PUT", "/api/admin/crops/wheat/active", on, adminHeader); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, detailOf(t, res))
	}
	if res := e.send("POST", "/api/auth/logout", "", adminHeader); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	if e.me()["logged_in"] != false {
		t.Fatal("應已登出")
	}
	if res := e.send("PUT", "/api/admin/crops/wheat/active", on, adminHeader); res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
}

func TestForgedCookieRejected(t *testing.T) {
	e := setupWeb(t, 0.2)
	e.cookies = map[string]string{CookieName: "forged"}
	if res := e.send("PUT", "/api/admin/crops/wheat/active", `{"active": false}`, adminHeader); res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	if res := e.send("DELETE", "/api/admin/events/1", "", adminHeader); res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
}

func TestLoginLockoutAPI(t *testing.T) {
	e := setupWebAuth(t, 0.2, auth.New(testAdminUser, testHash(), auth.Options{MaxFailures: 3}))
	for range 3 {
		if res := e.send("POST", "/api/auth/login", loginJSON("admin", "x"), adminHeader); res.StatusCode != 401 {
			t.Fatal(res.StatusCode)
		}
	}
	res := e.send("POST", "/api/auth/login", loginJSON(testAdminUser, testAdminPassword), adminHeader)
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatal(res.StatusCode, res.Header)
	}
}

func TestAdminDisabledWithoutConfig(t *testing.T) {
	e := setupWebAuth(t, 0.2, nil)
	if e.me()["enabled"] != false {
		t.Fatal("未設定帳號時應停用")
	}
	if res := e.send("POST", "/api/auth/login", loginJSON("a", "b"), adminHeader); res.StatusCode != 503 {
		t.Fatal(res.StatusCode)
	}
	if res := e.send("PUT", "/api/admin/crops/wheat/active", `{"active": false}`, adminHeader); res.StatusCode != 503 {
		t.Fatal(res.StatusCode)
	}
	if e.get("/api/monitor/groups", nil).Code != 200 { // 唯讀監控不受影響
		t.Fatal("監控 API 應可使用")
	}
}

// 登入的請求本文驗證（pydantic LoginBody：帳號 1–64、密碼 1–128 個字）。
func TestLoginBodyValidation(t *testing.T) {
	e := setupWeb(t, 0.2)
	for _, body := range []string{"", "{", `{"username": "admin"}`, `{"username": "", "password": "x"}`,
		loginJSON(strings.Repeat("a", 65), "x"), loginJSON("admin", strings.Repeat("p", 129)), `{"username": 1, "password": "x"}`} {
		if res := e.send("POST", "/api/auth/login", body, adminHeader); res.StatusCode != 422 {
			t.Errorf("%q：%d", body, res.StatusCode)
		}
	}
	big := `{"username": "admin", "password": "` + strings.Repeat("x", maxBodyBytes) + `"}`
	if res := e.send("POST", "/api/auth/login", big, adminHeader); res.StatusCode != 413 {
		t.Fatal(res.StatusCode)
	}
}

// 對應 test_monitor_api.py 的 test_admin_write_protection_and_group_endpoints_removed。
func TestAdminWriteProtectionAndGroupEndpointsRemoved(t *testing.T) {
	e, w := setupSocial(t)
	goldBefore := e.gold(w.c)
	body := `{"item_code": "fertilizer", "quantity": 1}`
	url := "/api/admin/players/" + itoa(int(w.c)) + "/inventory"
	// 缺少管理標頭或跨來源請求一律拒絕
	if res := e.send("PUT", url, body, nil); res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	if res := e.send("PUT", url, body, map[string]string{AdminHeader: "1", "Origin": "http://evil.example"}); res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	// 未登入不能修改
	if res := e.send("PUT", url, body, adminHeader); res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	if res := e.send("POST", "/api/auth/login", loginJSON(testAdminUser, testAdminPassword), adminHeader); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	if res := e.send("PUT", url, body, adminHeader); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, detailOf(t, res))
	}
	// 群組建立與成員管理改由 Discord 處理，網站不再提供
	for _, c := range []struct{ method, target, body string }{
		{"POST", "/api/admin/groups", `{"name": "三號群"}`},
		{"PUT", "/api/admin/groups/" + itoa(int(w.g1)) + "/members/" + itoa(int(w.c)), ""},
		{"PUT", "/api/admin/farms/" + itoa(int(w.c)), `{"gold": 99999}`},
		{"POST", "/api/monitor/players/1/backpack", `{}`},
	} {
		if res := e.send(c.method, c.target, c.body, adminHeader); res.StatusCode != 404 && res.StatusCode != 405 {
			t.Fatal(c.target, res.StatusCode)
		}
	}
	// 調整背包不影響金幣
	if e.gold(w.c) != goldBefore {
		t.Fatal(e.gold(w.c), goldBefore)
	}
}

// 管理 API 的參數與本文驗證在權限檢查之後（未登入一律 401，不透露驗證細節）。
func TestAdminValidationAfterAuth(t *testing.T) {
	e, w := setupSocial(t)
	url := "/api/admin/players/" + itoa(int(w.c)) + "/inventory"
	if res := e.send("PUT", "/api/admin/players/0/inventory", "{", adminHeader); res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	e.send("POST", "/api/auth/login", loginJSON(testAdminUser, testAdminPassword), adminHeader)
	for _, c := range []struct{ target, body string }{
		{"/api/admin/players/0/inventory", `{"item_code": "fertilizer", "quantity": 1}`},
		{url, `{"item_code": "Fertilizer", "quantity": 1}`},
		{url, `{"item_code": "fertilizer"}`},
		{url, `{"item_code": "fertilizer", "quantity": -1}`},
		{url, `{"item_code": "fertilizer", "quantity": 1.5}`},
		{url, `{"item_code": "fertilizer", "quality": "epic", "quantity": 1}`},
		{"/api/admin/crops/W/active", `{"active": true}`},
		{"/api/admin/crops/wheat/active", `{}`},
	} {
		if res := e.send("PUT", c.target, c.body, adminHeader); res.StatusCode != 422 {
			t.Errorf("%s %s：%d", c.target, c.body, res.StatusCode)
		}
	}
	if res := e.send("PUT", "/api/admin/crops/nothing/active", `{"active": true}`, adminHeader); res.StatusCode != 404 {
		t.Fatal(res.StatusCode, detailOf(t, res))
	}
	if res := e.send("DELETE", "/api/admin/events/999", "", adminHeader); res.StatusCode != 404 {
		t.Fatal(res.StatusCode)
	}
}

func (e *webEnv) gold(pid int64) int {
	e.t.Helper()
	return e.scalar("SELECT gold FROM farms WHERE player_id = $1", pid)
}
