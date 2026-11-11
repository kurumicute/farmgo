package services

import (
	"bytes"
	"encoding/json"
)

// OrderedMap 依插入順序輸出的 JSON 物件（Python dict 保留插入順序；Go map 輸出時依鍵排序）。
// 前端以 Object.entries 顯示的物件（品質名稱、建築數量、表單欄位範圍等）要用它，顯示順序才與 Python 版相同。
type OrderedMap struct {
	keys []string
	vals map[string]any
}

// NewOrderedMap 建立空的 OrderedMap。
func NewOrderedMap() *OrderedMap { return &OrderedMap{vals: map[string]any{}} }

// Set 新增或更新；已存在的鍵保留原本的位置（與 Python dict 相同）。
func (m *OrderedMap) Set(key string, v any) *OrderedMap {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
	return m
}

// Len 鍵的數量。
func (m *OrderedMap) Len() int { return len(m.keys) }

// Get 取得值。
func (m *OrderedMap) Get(key string) (any, bool) {
	v, ok := m.vals[key]
	return v, ok
}

// orderedStrings 依 order 的順序取出 m 的值（Python 模組層級 dict 在 Go 以 map＋順序切片表示）。
func orderedStrings(order []string, m map[string]string) *OrderedMap {
	out := NewOrderedMap()
	for _, k := range order {
		out.Set(k, m[k])
	}
	return out
}

func (m *OrderedMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := marshalNoEscape(m.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
