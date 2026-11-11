package web

// 請求本文欄位驗證（對應 pydantic 的 Field 限制）：缺少必填欄位、型別或範圍不符時回 422。
// 欄位以指標或 json.Number 表示，才分得出「沒送」與零值。

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type bodyCheck struct{ err string }

func newBodyCheck() *bodyCheck { return &bodyCheck{} }

func (v *bodyCheck) fail(format string, args ...any) {
	if v.err == "" {
		v.err = fmt.Sprintf(format, args...)
	}
}

// ok 沒有錯誤時回傳 true；有錯誤時回 422 並回傳 false。
func (v *bodyCheck) ok(w http.ResponseWriter) bool {
	if v.err != "" {
		writeDetail(w, http.StatusUnprocessableEntity, v.err)
		return false
	}
	return true
}

// str 必填字串：長度 lo–hi 個字，re 不為 nil 時須符合。
func (v *bodyCheck) str(name string, s *string, lo, hi int, re *regexp.Regexp) string {
	if s == nil {
		v.fail("缺少 %s。", name)
		return ""
	}
	v.checkStr(name, *s, lo, hi, re)
	return *s
}

// optStr 選填字串：沒送時回傳 def。
func (v *bodyCheck) optStr(name string, s *string, def string, lo, hi int, re *regexp.Regexp) string {
	if s == nil {
		return def
	}
	v.checkStr(name, *s, lo, hi, re)
	return *s
}

func (v *bodyCheck) checkStr(name, s string, lo, hi int, re *regexp.Regexp) {
	if !strLen(s, lo, hi) {
		v.fail("%s 須為 %d–%d 個字。", name, lo, hi)
	} else if re != nil && !re.MatchString(s) {
		v.fail("%s 的格式不正確。", name)
	}
}

// enum 選填的列舉值：沒送時回傳 def。
func (v *bodyCheck) enum(name string, s *string, def string, choices []string) string {
	if s == nil {
		return def
	}
	if !slices.Contains(choices, *s) {
		v.fail("%s 只能是 %s。", name, strings.Join(choices, "、"))
		return def
	}
	return *s
}

// int 整數欄位（pydantic 寬鬆模式也接受小數部分為 0 的數字）；required 為 false 且沒送時回傳 0。
func (v *bodyCheck) int(name string, n json.Number, required bool, lo, hi int64) int64 {
	if n == "" {
		if required {
			v.fail("缺少 %s。", name)
		}
		return 0
	}
	x, ok := parseJSONInt(n)
	if !ok {
		v.fail("%s 必須是整數。", name)
		return 0
	}
	if x < lo || x > hi {
		v.fail("%s 必須在 %d–%d 之間。", name, lo, hi)
	}
	return x
}

// optInt 選填整數：沒送或 null 時回傳 nil。
func (v *bodyCheck) optInt(name string, n json.Number, lo, hi int64) *int64 {
	if n == "" {
		return nil
	}
	x := v.int(name, n, true, lo, hi)
	return &x
}

func parseJSONInt(n json.Number) (int64, bool) {
	if x, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return x, true
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}
