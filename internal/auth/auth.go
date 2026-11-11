// Package auth 管理員登入（對應 Python app/auth.py）：密碼雜湊（scrypt）、伺服器端 session 與登入失敗鎖定。
//
//   - 密碼只以雜湊形式存放於 .env 的 ADMIN_PASSWORD_HASH，原始碼與設定中不保存明文。
//   - 雜湊格式：scrypt:N:r:p:salt:hash（base64url、無補位；刻意不用 $，避免 Docker Compose 讀 .env 時被展開）。
//     與 Python 版產生的雜湊互通，不需要重設密碼。
//   - Session 存在記憶體，服務重啟後需重新登入。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// scrypt 參數（與 Python 相同）。
const (
	ScryptN     = 1 << 14
	ScryptR     = 8
	ScryptP     = 1
	ScryptDKLen = 32

	// scryptMaxMem 驗證時允許的最大記憶體（Python hashlib.scrypt 預設 maxmem 32 MiB）：避免被設定錯誤的參數拖垮。
	scryptMaxMem = 32 << 20
)

var b64 = base64.RawURLEncoding

func unb64(s string) ([]byte, error) { return b64.DecodeString(strings.TrimRight(s, "=")) }

// HashPassword 產生新的雜湊（每次使用不同的 salt）。
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest, err := scrypt.Key([]byte(password), salt, ScryptN, ScryptR, ScryptP, ScryptDKLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt:%d:%d:%d:%s:%s", ScryptN, ScryptR, ScryptP, b64.EncodeToString(salt), b64.EncodeToString(digest)), nil
}

// VerifyPassword 比對密碼；雜湊格式不正確時回傳 false。比對使用固定時間。
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	n, err1 := strconv.Atoi(parts[1])
	r, err2 := strconv.Atoi(parts[2])
	p, err3 := strconv.Atoi(parts[3])
	salt, err4 := unb64(parts[4])
	expected, err5 := unb64(parts[5])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || len(expected) == 0 ||
		n <= 1 || r <= 0 || p <= 0 || int64(128)*int64(n)*int64(r) > scryptMaxMem || int64(r)*int64(p) >= 1<<30 {
		return false
	}
	digest, err := scrypt.Key([]byte(password), salt, n, r, p, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(digest, expected) == 1
}

// dummyHash 帳號不存在時仍計算一次雜湊，避免以回應時間推測帳號是否正確（第一次用到時才產生）。
var dummyHash = sync.OnceValue(func() string {
	token := make([]byte, 16)
	_, _ = rand.Read(token)
	h, err := HashPassword(b64.EncodeToString(token))
	if err != nil {
		panic(err)
	}
	return h
})

// LoginLockedError 登入失敗次數過多，RetryAfter 秒後才能再試。
type LoginLockedError struct{ RetryAfter int }

func (e *LoginLockedError) Error() string {
	return fmt.Sprintf("登入已鎖定，%d 秒後再試", e.RetryAfter)
}

type session struct {
	username  string
	expiresAt time.Time
}

type failure struct {
	count       int
	lockedUntil time.Time
}

// Options AdminAuth 的選項；零值使用與 Python 相同的預設值。
type Options struct {
	SessionDuration time.Duration    // 預設 8 小時
	MaxFailures     int              // 預設 5 次
	LockDuration    time.Duration    // 預設 5 分鐘
	Now             func() time.Time // 預設 time.Now（含單調時鐘）；測試可注入
}

// AdminAuth 管理員帳號、session 與登入失敗鎖定。可同時被多個請求使用。
type AdminAuth struct {
	username, passwordHash string
	sessionDuration        time.Duration
	maxFailures            int
	lockDuration           time.Duration
	now                    func() time.Time

	mu       sync.Mutex
	sessions map[string]session
	failures map[string]failure // 來源 → 失敗次數與鎖定到期時間
}

// New username 或 passwordHash 為空時管理功能停用（fail closed）。
func New(username, passwordHash string, opt Options) *AdminAuth {
	if opt.SessionDuration <= 0 {
		opt.SessionDuration = 8 * time.Hour
	}
	if opt.MaxFailures <= 0 {
		opt.MaxFailures = 5
	}
	if opt.LockDuration <= 0 {
		opt.LockDuration = 5 * time.Minute
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &AdminAuth{username: username, passwordHash: passwordHash, sessionDuration: opt.SessionDuration,
		maxFailures: opt.MaxFailures, lockDuration: opt.LockDuration, now: opt.Now,
		sessions: map[string]session{}, failures: map[string]failure{}}
}

// Enabled 是否已設定管理員帳號。
func (a *AdminAuth) Enabled() bool { return a.username != "" && a.passwordHash != "" }

// Username 設定的管理員帳號。
func (a *AdminAuth) Username() string { return a.username }

// SessionDuration session 有效時間（Cookie 的 Max-Age）。
func (a *AdminAuth) SessionDuration() time.Duration { return a.sessionDuration }

// Login 成功回傳 session token；帳號或密碼錯誤回傳 ""；被鎖定時回傳 *LoginLockedError。
// 不區分帳號或密碼錯誤；帳號錯誤時仍計算一次雜湊。
func (a *AdminAuth) Login(username, password, client string) (string, error) {
	now := a.now()
	a.mu.Lock()
	f := a.failures[client]
	if f.lockedUntil.After(now) {
		a.mu.Unlock()
		return "", &LoginLockedError{int(f.lockedUntil.Sub(now)/time.Second) + 1}
	}
	a.mu.Unlock()

	userOK := a.Enabled() && subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	hash := dummyHash()
	if userOK {
		hash = a.passwordHash
	}
	pwOK := VerifyPassword(password, hash)

	a.mu.Lock()
	defer a.mu.Unlock()
	if !userOK || !pwOK {
		f.count++
		f.lockedUntil = time.Time{}
		if f.count >= a.maxFailures {
			f.lockedUntil = now.Add(a.lockDuration)
			f.count = 0
		}
		a.failures[client] = f
		return "", nil
	}
	delete(a.failures, client)
	a.purge(now)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := b64.EncodeToString(raw)
	a.sessions[token] = session{a.username, now.Add(a.sessionDuration)}
	return token, nil
}

// Validate 有效的 token 回傳帳號，否則回傳 ""（過期的 session 一併刪除）。
func (a *AdminAuth) Validate(token string) string {
	if token == "" {
		return ""
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[token]
	if !ok {
		return ""
	}
	if !s.expiresAt.After(now) {
		delete(a.sessions, token)
		return ""
	}
	return s.username
}

// Logout 刪除 session。
func (a *AdminAuth) Logout(token string) {
	if token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

func (a *AdminAuth) purge(now time.Time) {
	for t, s := range a.sessions {
		if !s.expiresAt.After(now) {
			delete(a.sessions, t)
		}
	}
}
