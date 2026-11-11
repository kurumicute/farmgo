package web

// 管理員登入／登出（對應 Python app/api/auth.py）。Session token 只放在 HttpOnly + SameSite=Strict cookie，前端 JS 讀不到。

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"farmgm/internal/auth"
)

const (
	// CookieName session cookie 名稱。
	CookieName = "farmgm_admin"
	// AdminHeader 寫入類請求必須帶的自訂標頭（跨站表單無法送出）。
	AdminHeader = "X-Farmgm-Admin"

	maxBodyBytes = 1 << 20 // 管理 API 請求本文上限（Python 沒有限制；表單資料遠小於此）
)

func (a *App) authRoutes() {
	a.mux.HandleFunc("POST /api/auth/login", a.login)
	a.mux.HandleFunc("POST /api/auth/logout", a.logout)
	a.mux.HandleFunc("GET /api/auth/me", a.me)
}

// checkCSRF 寫入類請求必須帶 X-Farmgm-Admin: 1，且 Origin（若有）須與 Host 相同。通過時回傳 true。
func checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(AdminHeader) != "1" {
		writeDetail(w, http.StatusForbidden, "缺少管理請求標頭。")
		return false
	}
	if origin, has := r.Header["Origin"]; has {
		u, err := url.Parse(origin[0])
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeDetail(w, http.StatusForbidden, "不允許跨來源的管理請求。")
			return false
		}
	}
	return true
}

const disabledMessage = "尚未設定管理員帳號，群組管理功能停用。"

// requireSession 已登入時回傳帳號；否則回 503（未設定帳號）或 401 並回傳 ""。
func (a *App) requireSession(w http.ResponseWriter, r *http.Request) string {
	if !a.auth.Enabled() {
		writeDetail(w, http.StatusServiceUnavailable, disabledMessage)
		return ""
	}
	username := a.auth.Validate(sessionToken(r))
	if username == "" {
		writeDetail(w, http.StatusUnauthorized, "請先登入管理員帳號。")
	}
	return username
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// decodeBody 讀取 JSON 本文（大小上限 maxBodyBytes；未知欄位忽略，與 pydantic 預設相同）。失敗時回 422 並回傳 false。
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeDetail(w, http.StatusRequestEntityTooLarge, "請求內容太大。")
		case errors.Is(err, io.EOF):
			writeDetail(w, http.StatusUnprocessableEntity, "缺少請求內容。")
		default:
			writeDetail(w, http.StatusUnprocessableEntity, "請求內容格式不正確。")
		}
		return false
	}
	return true
}

// strLen 字串長度（字元數）在 lo–hi 之間。
func strLen(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

type loginBody struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !checkCSRF(w, r) {
		return
	}
	var body loginBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Username == nil || body.Password == nil || !strLen(*body.Username, 1, 64) || !strLen(*body.Password, 1, 128) {
		writeDetail(w, http.StatusUnprocessableEntity, "帳號須為 1–64 個字、密碼須為 1–128 個字。")
		return
	}
	if !a.auth.Enabled() {
		writeDetail(w, http.StatusServiceUnavailable, disabledMessage)
		return
	}
	client := r.RemoteAddr
	if host, _, err := net.SplitHostPort(client); err == nil {
		client = host
	}
	token, err := a.auth.Login(*body.Username, *body.Password, client)
	var locked *auth.LoginLockedError
	if errors.As(err, &locked) {
		w.Header().Set("Retry-After", strconv.Itoa(locked.RetryAfter))
		writeDetail(w, http.StatusTooManyRequests, "登入失敗次數過多，請 "+strconv.Itoa(locked.RetryAfter)+" 秒後再試。")
		return
	}
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if token == "" {
		writeDetail(w, http.StatusUnauthorized, "帳號或密碼錯誤。") // 不區分帳號或密碼錯誤
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/api", MaxAge: int(a.auth.SessionDuration().Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	writeJSON(w, http.StatusOK, map[string]string{"username": a.auth.Username()})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if !checkCSRF(w, r) {
		return
	}
	a.auth.Logout(sessionToken(r))
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/api", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"logged_in": false})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	var username *string
	if a.auth.Enabled() {
		if u := a.auth.Validate(sessionToken(r)); u != "" {
			username = &u
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": a.auth.Enabled(), "logged_in": username != nil, "username": username})
}
