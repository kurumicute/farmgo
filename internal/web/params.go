package web

// 查詢參數與路徑參數的驗證（對應 FastAPI 的 Query／Path 限制）：格式或範圍不符時回 422。
// FastAPI 的 422 回傳驗證錯誤陣列；前端只顯示字串 detail，因此 Go 版一律回中文字串。

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/clock"
)

// MaxBigint PostgreSQL bigint 上限（Python MAX_BIGINT）。
const MaxBigint = math.MaxInt64

// paramError 參數不合法（422）。
type paramError struct{ msg string }

func (e *paramError) Error() string { return e.msg }

func badParam(format string, args ...any) error { return &paramError{fmt.Sprintf(format, args...)} }

// params 收集第一個參數錯誤；處理函式最後呼叫 ok() 決定是否回 422。
type params struct {
	w   http.ResponseWriter
	r   *http.Request
	err error
}

func newParams(w http.ResponseWriter, r *http.Request) *params { return &params{w: w, r: r} }

func (p *params) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

// ok 沒有錯誤時回傳 true；有錯誤時回 422 並回傳 false。
func (p *params) ok() bool {
	if p.err != nil {
		writeDetail(p.w, http.StatusUnprocessableEntity, p.err.Error())
		return false
	}
	return true
}

func parseInt(name, raw string, lo, hi int64) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, badParam("參數 %s 必須是整數。", name)
	}
	if v < lo || v > hi {
		return 0, badParam("參數 %s 必須在 %d–%d 之間。", name, lo, hi)
	}
	return v, nil
}

// pathID 路徑中的 ID（1 ～ bigint 上限）。
func (p *params) pathID(name string) int64 {
	v, err := parseInt(name, p.r.PathValue(name), 1, MaxBigint)
	if err != nil {
		p.fail(err)
	}
	return v
}

// queryInt 查詢參數整數；沒有提供時用 def。
func (p *params) queryInt(name string, def, lo, hi int64) int64 {
	raw, has := p.query(name)
	if !has {
		return def
	}
	v, err := parseInt(name, raw, lo, hi)
	if err != nil {
		p.fail(err)
	}
	return v
}

// queryOptInt 選填的整數查詢參數；沒有提供時 nil。
func (p *params) queryOptInt(name string, lo, hi int64) *int64 {
	raw, has := p.query(name)
	if !has {
		return nil
	}
	v, err := parseInt(name, raw, lo, hi)
	if err != nil {
		p.fail(err)
		return nil
	}
	return &v
}

// queryStr 字串查詢參數；超過 maxLen 個字元時錯誤。沒有提供時回傳 ""。
func (p *params) queryStr(name string, maxLen int) string {
	raw, _ := p.query(name)
	if utf8.RuneCountInString(raw) > maxLen {
		p.fail(badParam("參數 %s 最多 %d 個字。", name, maxLen))
	}
	return raw
}

// queryEnum 只接受 choices 中的值；沒有提供時用 def（def 為 "" 表示 None）。
func (p *params) queryEnum(name, def string, choices ...string) string {
	raw, has := p.query(name)
	if !has {
		return def
	}
	if !slices.Contains(choices, raw) {
		p.fail(badParam("參數 %s 只能是 %s。", name, strings.Join(choices, "、")))
		return def
	}
	return raw
}

// queryDate 日期（YYYY-MM-DD），回傳台灣時間 00:00；沒有提供時 nil。
func (p *params) queryDate(name string) *time.Time {
	raw, has := p.query(name)
	if !has {
		return nil
	}
	d, err := time.ParseInLocation("2006-01-02", raw, clock.TaipeiTZ)
	if err != nil {
		p.fail(badParam("參數 %s 必須是日期（YYYY-MM-DD）。", name))
		return nil
	}
	return &d
}

func (p *params) query(name string) (string, bool) {
	q := p.r.URL.Query()
	if !q.Has(name) {
		return "", false
	}
	return q.Get(name), true
}

// queryBool 布林查詢參數（與 FastAPI 相同：true／false、1／0、yes／no、on／off，不分大小寫）；沒有提供時用 def。
func (p *params) queryBool(name string, def bool) bool {
	raw, has := p.query(name)
	if !has {
		return def
	}
	switch strings.ToLower(raw) {
	case "true", "1", "yes", "on", "t", "y":
		return true
	case "false", "0", "no", "off", "f", "n":
		return false
	}
	p.fail(badParam("參數 %s 必須是 true 或 false。", name))
	return def
}
