// Package featurecfg 可在管理網站調整的新功能規則（每日簽到、寵物、特賣與升級、成就）：預設值、驗證與目前生效的設定。
// 對應 Python app/game/feature_config.py。
//
//   - 各規則套件（checkin、pets、specialshop、achievements）在 init() 中 Register 自己的預設值與驗證函式，
//     並一律經 Get(key) 讀取目前設定（不直接用常數），因此管理員修改後不必改程式或重新部署。
//   - 設定存在資料表 feature_settings（每個 key 一列 JSON，只存有修改過的項目）；沒有資料時用預設值。
//     這裡只保存「目前生效的設定」，不碰資料庫。
//   - 存在資料庫的值不合法時記錄警告並改用預設值，遊戲不會因設定錯誤而壞掉。
//
// 設定值是 JSON 形狀的 map[string]any。從 JSON 解碼時請用 Decode（UseNumber），整數以 json.Number 表示；
// 驗證函式回傳的正規化結果中整數一律是 int、整數清單是 []int、道具是 map[string]int。
package featurecfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

// Value 一組設定（JSON 物件）。
type Value = map[string]any

type spec struct {
	label    string
	defaults func() Value
	validate func(Value) (Value, error)
}

var (
	mu       sync.RWMutex
	specs    = map[string]spec{}
	order    []string
	defaults = map[string]Value{}
	current  = map[string]Value{}
	version  int
)

// Register 登記一組設定：label 是管理網站顯示的名稱，validate(raw) 回傳正規化後的設定或錯誤。
func Register(key, label string, defaultsFn func() Value, validate func(Value) (Value, error)) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := specs[key]; !ok {
		order = append(order, key)
	}
	specs[key] = spec{label, defaultsFn, validate}
	delete(defaults, key)
}

func Keys() []string {
	mu.RLock()
	defer mu.RUnlock()
	return append([]string(nil), order...)
}

func Label(key string) string {
	mu.RLock()
	defer mu.RUnlock()
	return specs[key].label
}

func defaultsLocked(key string) Value {
	if v, ok := defaults[key]; ok {
		return v
	}
	v := specs[key].defaults()
	defaults[key] = v
	return v
}

// Defaults 預設值（呼叫端不可修改回傳的 map）。
func Defaults(key string) Value {
	mu.Lock()
	defer mu.Unlock()
	return defaultsLocked(key)
}

// Get 目前生效的設定（呼叫端不可修改回傳的 map）。
func Get(key string) Value {
	mu.Lock()
	defer mu.Unlock()
	if v, ok := current[key]; ok && len(v) > 0 {
		return v
	}
	return defaultsLocked(key)
}

func IsDefault(key string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := current[key]
	return !ok
}

// Validate 驗證並正規化設定；key 不存在時回傳錯誤（Python 為 KeyError）。
func Validate(key string, raw Value) (Value, error) {
	mu.RLock()
	s, ok := specs[key]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown feature setting key: %s", key)
	}
	cp, err := DeepCopy(raw)
	if err != nil {
		return nil, gameerr.Validationf("設定格式不正確。")
	}
	return s.validate(cp)
}

// Version 設定每次改變就加一；規則套件用來判斷衍生的快取是否要重建。
func Version() int {
	mu.RLock()
	defer mu.RUnlock()
	return version
}

// Apply 以資料庫讀到的設定（key → JSON）取代目前設定；沒有列出的 key 回到預設值。
func Apply(stored map[string]Value) {
	next := map[string]Value{}
	for key, raw := range stored {
		mu.RLock()
		_, ok := specs[key]
		mu.RUnlock()
		if !ok {
			continue
		}
		v, err := Validate(key, raw)
		if err != nil { // 設定不合法：用預設值，不讓遊戲壞掉
			slog.Warn("新功能設定不合法，改用預設值", "key", key, "error", err)
			continue
		}
		next[key] = v
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(next, current) {
		current = next
		version++
	}
}

// SetCurrent 管理員儲存（value）或還原預設（nil）後，在同一個程序中立即生效。
func SetCurrent(key string, value Value) {
	mu.Lock()
	defer mu.Unlock()
	if value == nil {
		delete(current, key)
	} else {
		current[key] = value
	}
	version++
}

// Reset 全部回到預設值（測試用）。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	current = map[string]Value{}
	version++
}

// Decode 以 UseNumber 解析 JSON 物件（整數保留為 json.Number，與 Python 的 int／float 區分一致）。
func Decode(data []byte) (Value, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v Value
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// DeepCopy 以 JSON 來回轉換複製設定。
func DeepCopy(v Value) (Value, error) {
	if v == nil {
		return Value{}, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// ---------- 驗證工具 ----------

// AsInt 取出整數（int、int64、整數形式的 json.Number）；bool 與浮點數不算整數（與 Python isinstance(int) 一致）。
func AsInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case json.Number:
		s := string(v)
		if strings.ContainsAny(s, ".eE") {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

func IntValue(value any, label string, low, high int) (int, error) {
	n, ok := AsInt(value)
	if !ok || n < low || n > high {
		return 0, gameerr.Validationf("%s必須是 %s–%s 之間的整數。", label, numfmt.Comma(low), numfmt.Comma(high))
	}
	return n, nil
}

// AsList 取出 JSON 陣列。
func AsList(value any) ([]any, bool) {
	switch v := value.(type) {
	case []any:
		return v, true
	case []int:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out, true
	}
	return nil, false
}

func IntList(value any, label string, low, high, minLen, maxLen int) ([]int, error) {
	list, ok := AsList(value)
	if !ok || len(list) < minLen || len(list) > maxLen {
		return nil, gameerr.Validationf("%s必須有 %d–%d 個數值。", label, minLen, maxLen)
	}
	out := make([]int, len(list))
	for i, v := range list {
		n, err := IntValue(v, fmt.Sprintf("%s第 %d 個", label, i+1), low, high)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

// AsMap 取出 JSON 物件。
func AsMap(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case map[string]int:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = x
		}
		return out, true
	}
	return nil, false
}

// ItemsValue 道具獎勵 {道具代碼: 數量}；道具必須是道具商店的道具。
func ItemsValue(value any, label string, maxKinds, maxQty int) (map[string]int, error) {
	if value == nil {
		return map[string]int{}, nil
	}
	m, ok := AsMap(value)
	if !ok || len(m) > maxKinds {
		return nil, gameerr.Validationf("%s最多 %d 種道具。", label, maxKinds)
	}
	out := map[string]int{}
	for code, qty := range m {
		item, ok := balance.Items[code]
		if !ok {
			return nil, gameerr.Validationf("%s：沒有「%s」這種道具。", label, code)
		}
		n, err := IntValue(qty, fmt.Sprintf("%s的%s數量", label, item.Name), 1, maxQty)
		if err != nil {
			return nil, err
		}
		out[code] = n
	}
	return out, nil
}

// TitleValue 稱號文字；空白或 nil 時 ok = false（required 時回傳錯誤）。
func TitleValue(value any, label string, maxLen int, required bool) (title string, ok bool, err error) {
	s, isStr := value.(string)
	if value == nil || (isStr && strings.TrimSpace(s) == "") {
		if required {
			return "", false, gameerr.Validationf("%s不可空白。", label)
		}
		return "", false, nil
	}
	if !isStr || utf8.RuneCountInString(strings.TrimSpace(s)) > maxLen {
		return "", false, gameerr.Validationf("%s最多 %d 字。", label, maxLen)
	}
	return strings.TrimSpace(s), true, nil
}
