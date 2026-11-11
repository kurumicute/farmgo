package auth

// 對應 Python tests/test_auth.py 的前四個測試（雜湊、session、鎖定、未設定）。API 行為的測試在 internal/web。

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const password = "correct-horse-123"

var hash = func() string {
	h, err := HashPassword(password)
	if err != nil {
		panic(err)
	}
	return h
}()

type fakeTime struct{ t time.Time }

func (f *fakeTime) now() time.Time { return f.t }

func newFake() *fakeTime { return &fakeTime{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestHashRoundtripAndFormat(t *testing.T) {
	if !strings.HasPrefix(hash, "scrypt:16384:8:1:") || strings.Contains(hash, "$") || strings.Contains(hash, password) {
		t.Fatal(hash)
	}
	if !VerifyPassword(password, hash) || VerifyPassword("wrong", hash) {
		t.Fatal("verify")
	}
	if again, _ := HashPassword(password); again == hash { // 每次使用不同的 salt
		t.Fatal("salt 應不同")
	}
	for _, bad := range []string{"", "plain", "md5:1:2:3:a:b", "scrypt:x:8:1:a:b", "scrypt:16384:8:1:a:", "scrypt:1073741824:8:1:YQ:YQ"} {
		if VerifyPassword(password, bad) {
			t.Fatal(bad)
		}
	}
}

// pythonHash Python 版 app.auth.hash_password("correct-horse-123") 實際產生的雜湊（2026-11-10，Python 3.12）。
const pythonHash = "scrypt:16384:8:1:6l9H0kUFGWbFo78o0C6W3Q:iPTz8RCH1mH27U0KrXOYx2_3WsE7FXRBYen6OL3Ycrk"

// Go 必須能驗證 Python 產生的雜湊，才能沿用現有 .env 的 ADMIN_PASSWORD_HASH。
func TestVerifiesPythonGeneratedHash(t *testing.T) {
	if !VerifyPassword(password, pythonHash) || VerifyPassword("wrong", pythonHash) {
		t.Fatal(pythonHash)
	}
}

func TestLoginSessionAndLogout(t *testing.T) {
	clock := newFake()
	a := New("admin", hash, Options{SessionDuration: 60 * time.Second, Now: clock.now})
	if tok, _ := a.Login("admin", "wrong", "c"); tok != "" {
		t.Fatal("錯誤密碼不可登入")
	}
	if tok, _ := a.Login("other", password, "c"); tok != "" {
		t.Fatal("錯誤帳號不可登入")
	}
	tok, err := a.Login("admin", password, "c")
	if err != nil || tok == "" || a.Validate(tok) != "admin" {
		t.Fatal(tok, err)
	}
	if a.Validate("forged-token") != "" || a.Validate("") != "" {
		t.Fatal("偽造的 token")
	}
	clock.t = clock.t.Add(61 * time.Second)
	if a.Validate(tok) != "" { // 過期
		t.Fatal("過期的 session 應無效")
	}
	tok, _ = a.Login("admin", password, "c")
	a.Logout(tok)
	if a.Validate(tok) != "" {
		t.Fatal("登出後 session 應無效")
	}
}

func TestLockoutAfterRepeatedFailures(t *testing.T) {
	clock := newFake()
	a := New("admin", hash, Options{MaxFailures: 3, LockDuration: 300 * time.Second, Now: clock.now})
	for range 3 {
		if tok, err := a.Login("admin", "wrong", "1.2.3.4"); tok != "" || err != nil {
			t.Fatal(tok, err)
		}
	}
	_, err := a.Login("admin", password, "1.2.3.4") // 鎖定期間正確密碼也不接受
	var locked *LoginLockedError
	if !errors.As(err, &locked) || locked.RetryAfter <= 0 {
		t.Fatal(err)
	}
	if tok, _ := a.Login("admin", password, "5.6.7.8"); tok == "" { // 其他來源不受影響
		t.Fatal("其他來源不應被鎖定")
	}
	clock.t = clock.t.Add(301 * time.Second)
	if tok, _ := a.Login("admin", password, "1.2.3.4"); tok == "" {
		t.Fatal("鎖定到期後應可登入")
	}
}

func TestDisabledWhenNotConfigured(t *testing.T) {
	a := New("", "", Options{})
	if a.Enabled() {
		t.Fatal("未設定時應停用")
	}
	if tok, _ := a.Login("", "", "c"); tok != "" {
		t.Fatal("停用時不可登入")
	}
}

// 登入成功清除失敗次數；失敗次數未達上限時不鎖定。
func TestSuccessResetsFailures(t *testing.T) {
	clock := newFake()
	a := New("admin", hash, Options{MaxFailures: 3, Now: clock.now})
	for range 2 {
		_, _ = a.Login("admin", "wrong", "c")
	}
	if tok, _ := a.Login("admin", password, "c"); tok == "" {
		t.Fatal("未達上限時應可登入")
	}
	for range 2 {
		_, _ = a.Login("admin", "wrong", "c")
	}
	if _, err := a.Login("admin", password, "c"); err != nil {
		t.Fatal("成功登入後失敗次數應歸零", err)
	}
}
