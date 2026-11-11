package web

// 宣告式的請求本文驗證（對應 pydantic BaseModel）：檢查必填、型別、字串長度與格式、列舉值、整數範圍，
// 通過後輸出與 Python model_dump() 相同鍵的 map（沒送的欄位填入預設值、未知欄位忽略），交給服務層做業務驗證。
//
// 輸出的型別：字串 string、整數 int、布林 bool、時間 time.Time（沒有時區時視為台灣時間，同 Python _aware）、
// dict[str, int] 為 map[string]any（值為 int）、dict[str, dict[str, int]] 為 map[string]any、list[int]／list[str] 為 []any、
// 任意物件（dict）為 featurecfg 解析後的 map[string]any（整數為 json.Number）。可為 None 的欄位以 nil 表示。

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/clock"
)

type fieldKind int

const (
	kStr fieldKind = iota
	kInt
	kBool
	kTime
	kIntMap    // dict[str, int]
	kIntMapMap // dict[str, dict[str, int]]
	kIntList   // list[int]
	kStrList   // list[str]
	kObject    // dict（任意內容）
	kDate      // date（YYYY-MM-DD，或時間為 00:00 的日期時間）
	kModel     // 巢狀 model
	kModelList // list[model]
	kModelMap  // dict[鍵, model]
)

// field 一個欄位的規則；以 str()、integer() 等建立，再串接 opt()、null()、length() 等設定。
type field struct {
	name     string
	kind     fieldKind
	required bool
	def      any
	nullable bool
	minLen   int // 字串長度（字元數）或清單長度
	maxLen   int // 0 = 不限
	re       *regexp.Regexp
	choices  []string
	ranged   bool
	lo, hi   int64   // 整數（或整數清單、整數 dict 每一項）的範圍
	sub      []field // 巢狀 model 的欄位
	key      *field  // dict 鍵的規則（字串長度與格式）
}

func str(name string) field     { return field{name: name, kind: kStr, required: true} }
func integer(name string) field { return field{name: name, kind: kInt, required: true} }
func boolean(name string) field { return field{name: name, kind: kBool, required: true} }
func datetime(name string) field {
	return field{name: name, kind: kTime, required: true}
}
func intMap(name string) field    { return field{name: name, kind: kIntMap, required: true} }
func intMapMap(name string) field { return field{name: name, kind: kIntMapMap, required: true} }
func intList(name string) field   { return field{name: name, kind: kIntList, required: true} }
func strList(name string) field   { return field{name: name, kind: kStrList, required: true} }
func object(name string) field    { return field{name: name, kind: kObject, required: true} }
func date(name string) field      { return field{name: name, kind: kDate, required: true} }

// model 巢狀 model；listOf、mapOf 為 model 的清單與 dict。
func model(name string, sub ...field) field {
	return field{name: name, kind: kModel, required: true, sub: sub}
}
func listOf(name string, sub ...field) field {
	return field{name: name, kind: kModelList, required: true, sub: sub}
}
func mapOf(name string, sub ...field) field {
	return field{name: name, kind: kModelMap, required: true, sub: sub}
}

// keys dict 鍵的規則（以字串欄位表示長度與格式）。
func (f field) keys(k field) field { f.key = &k; return f }

func enum(name string, choices ...string) field {
	return field{name: name, kind: kStr, required: true, choices: choices}
}

// opt 選填，沒送時為 def。
func (f field) opt(def any) field { f.required, f.def = false, def; return f }

// null 可為 null（Python X | None）。
func (f field) null() field { f.nullable = true; return f }

// length 字串或清單的長度範圍（max 為 0 表示不限）。
func (f field) length(lo, hi int) field { f.minLen, f.maxLen = lo, hi; return f }

// pattern 字串須符合的格式。
func (f field) pattern(re *regexp.Regexp) field { f.re = re; return f }

// rng 整數範圍（pydantic ge／le）。
func (f field) rng(lo, hi int64) field { f.ranged, f.lo, f.hi = true, lo, hi; return f }

// parseBody 讀取 JSON 物件並依 fields 驗證；失敗時已回 422（或 413），回傳 ok = false。
func parseBody(w http.ResponseWriter, r *http.Request, fields ...field) (map[string]any, bool) {
	var raw map[string]any
	if !decodeBody(w, r, &raw) {
		return nil, false
	}
	if raw == nil {
		writeDetail(w, http.StatusUnprocessableEntity, "請求內容必須是 JSON 物件。")
		return nil, false
	}
	out, msg := validateFields(raw, fields)
	if msg != "" {
		writeDetail(w, http.StatusUnprocessableEntity, msg)
		return nil, false
	}
	return out, true
}

func validateFields(raw map[string]any, fields []field) (map[string]any, string) {
	return validateAt(raw, fields, "")
}

// validateAt prefix 是巢狀欄位的路徑（例如 "settings.effects."），用在錯誤訊息。
func validateAt(raw map[string]any, fields []field, prefix string) (map[string]any, string) {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		label := prefix + f.name
		v, present := raw[f.name]
		switch {
		case !present && f.required:
			return nil, "缺少 " + label + "。"
		case !present:
			out[f.name] = f.defaultValue()
			continue
		case v == nil && f.nullable:
			out[f.name] = nil
			continue
		case v == nil:
			return nil, label + " 不可為 null。"
		}
		x, msg := f.convert(label, v)
		if msg != "" {
			return nil, msg
		}
		out[f.name] = x
	}
	return out, ""
}

// defaultValue 預設值；map 與清單每次回傳新的副本（pydantic default_factory）。
func (f field) defaultValue() any {
	switch d := f.def.(type) {
	case map[string]any:
		out := make(map[string]any, len(d))
		for k, v := range d {
			out[k] = v
		}
		return out
	case []any:
		return append([]any{}, d...)
	}
	return f.def
}

func (f field) convert(label string, v any) (any, string) {
	switch f.kind {
	case kStr:
		s, ok := v.(string)
		if !ok {
			return nil, label + " 必須是字串。"
		}
		return s, f.checkStr(label, s)
	case kInt:
		n, msg := toInt(label, v)
		if msg == "" && f.ranged && (n < f.lo || n > f.hi) {
			msg = f.rangeMsg(label)
		}
		return int(n), msg
	case kBool:
		b, ok := v.(bool)
		if !ok {
			return nil, label + " 必須是 true 或 false。"
		}
		return b, ""
	case kTime:
		s, ok := v.(string)
		if !ok {
			return nil, label + " 必須是日期時間字串。"
		}
		t, ok := parseDatetime(s)
		if !ok {
			return nil, label + " 的日期時間格式不正確。"
		}
		return t, ""
	case kDate:
		s, ok := v.(string)
		if !ok {
			return nil, label + " 必須是日期字串。"
		}
		d, ok := parseDate(s)
		if !ok {
			return nil, label + " 的日期格式不正確。"
		}
		return d, ""
	case kIntMap, kModelMap:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, label + " 必須是物件。"
		}
		if f.maxLen > 0 && len(m) > f.maxLen {
			return nil, label + " 最多 " + strconv.Itoa(f.maxLen) + " 項。"
		}
		out := make(map[string]any, len(m))
		for k, item := range m {
			itemLabel := label + "." + k
			if f.key != nil {
				if msg := f.key.checkStr(label+" 的鍵「"+k+"」", k); msg != "" {
					return nil, msg
				}
			}
			if f.kind == kModelMap {
				x, msg := f.model(itemLabel, item)
				if msg != "" {
					return nil, msg
				}
				out[k] = x
				continue
			}
			n, msg := toInt(itemLabel, item)
			if msg == "" && f.ranged && (n < f.lo || n > f.hi) {
				msg = f.rangeMsg(itemLabel)
			}
			if msg != "" {
				return nil, msg
			}
			out[k] = int(n)
		}
		return out, ""
	case kIntMapMap:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, label + " 必須是物件。"
		}
		out := make(map[string]any, len(m))
		for k, inner := range m {
			x, msg := intMap(k).convert(label+"."+k, inner)
			if msg != "" {
				return nil, msg
			}
			out[k] = x
		}
		return out, ""
	case kIntList, kStrList, kModelList:
		list, ok := v.([]any)
		if !ok {
			return nil, label + " 必須是陣列。"
		}
		if len(list) < f.minLen || (f.maxLen > 0 && len(list) > f.maxLen) {
			if f.maxLen > 0 {
				return nil, label + " 必須有 " + strconv.Itoa(f.minLen) + "–" + strconv.Itoa(f.maxLen) + " 項。"
			}
			return nil, label + " 至少要有 " + strconv.Itoa(f.minLen) + " 項。"
		}
		out := make([]any, len(list))
		for i, item := range list {
			itemLabel := label + "[" + strconv.Itoa(i) + "]"
			switch f.kind {
			case kStrList:
				s, ok := item.(string)
				if !ok {
					return nil, itemLabel + " 必須是字串。"
				}
				out[i] = s
			case kModelList:
				x, msg := f.model(itemLabel, item)
				if msg != "" {
					return nil, msg
				}
				out[i] = x
			default:
				n, msg := toInt(itemLabel, item)
				if msg == "" && f.ranged && (n < f.lo || n > f.hi) {
					msg = f.rangeMsg(itemLabel)
				}
				if msg != "" {
					return nil, msg
				}
				out[i] = int(n)
			}
		}
		return out, ""
	case kModel:
		return f.model(label, v)
	case kObject:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, label + " 必須是物件。"
		}
		return m, ""
	}
	return nil, label + " 的格式不正確。"
}

// model 巢狀 model（f.sub 的欄位）。
func (f field) model(label string, v any) (map[string]any, string) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, label + " 必須是物件。"
	}
	return validateAt(m, f.sub, label+".")
}

func (f field) checkStr(label, s string) string {
	if f.choices != nil {
		for _, c := range f.choices {
			if s == c {
				return ""
			}
		}
		return label + " 只能是 " + strings.Join(f.choices, "、") + "。"
	}
	if (f.minLen > 0 || f.maxLen > 0) && !strLen(s, f.minLen, f.maxLen) {
		if f.minLen == 0 {
			return label + " 最多 " + strconv.Itoa(f.maxLen) + " 個字。"
		}
		return label + " 須為 " + strconv.Itoa(f.minLen) + "–" + strconv.Itoa(f.maxLen) + " 個字。"
	}
	if f.re != nil && !f.re.MatchString(s) {
		return label + " 的格式不正確。"
	}
	return ""
}

func (f field) rangeMsg(label string) string {
	return label + " 必須在 " + strconv.FormatInt(f.lo, 10) + "–" + strconv.FormatInt(f.hi, 10) + " 之間。"
}

// toInt pydantic int：接受整數與小數部分為 0 的數字；不接受布林與字串（比 pydantic 寬鬆模式嚴格，前端都送數字）。
func toInt(label string, v any) (int64, string) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, label + " 必須是整數。"
	}
	x, ok := parseJSONInt(n)
	if !ok {
		return 0, label + " 必須是整數。"
	}
	return x, ""
}

var datetimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02T15:04Z07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04",
	time.DateOnly, // pydantic v2 也接受只有日期（00:00）
}

// parseDate pydantic date：YYYY-MM-DD，或時間為 00:00 的日期時間（台灣日期，回傳台灣時間 00:00）。
func parseDate(s string) (time.Time, bool) {
	t, ok := parseDatetime(s)
	if !ok {
		return time.Time{}, false
	}
	if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
		return time.Time{}, false
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, clock.TaipeiTZ), true
}

// parseDatetime ISO 8601 日期時間（T 或空白分隔、秒與小數可省略；只有日期時為 00:00）；沒有時區時視為台灣時間（Python _aware）。
// 結果截到微秒（與 Python datetime 相同）。
func parseDatetime(s string) (time.Time, bool) {
	for _, layout := range datetimeLayouts {
		hasZone := strings.Contains(layout, "Z07")
		var t time.Time
		var err error
		if hasZone {
			t, err = time.Parse(layout, s)
		} else {
			t, err = time.ParseInLocation(layout, s, clock.TaipeiTZ)
		}
		if err == nil {
			return t.Truncate(time.Microsecond), true
		}
	}
	return time.Time{}, false
}
