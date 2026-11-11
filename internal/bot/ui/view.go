package ui

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultDirectReply 換頁／操作在這段時間內準備好就直接以新畫面回覆互動（少一次 defer 往返）；超過才先 defer，
// 避免超過 Discord 3 秒的首次回覆期限（Python DIRECT_REPLY_SECONDS）。
const DefaultDirectReply = 1500 * time.Millisecond

// DefaultModalTimeout discord.py Modal 預設 timeout=300。
const DefaultModalTimeout = 5 * time.Minute

// View 元件的集合（discord.py ui.View）。
type View struct {
	Timeout time.Duration // 0 表示不逾時；從最後一次互動起算（discord.py 相同）
	// Check 互動前檢查（discord.py interaction_check）；回傳 false 時不執行回呼（Check 自行回覆使用者）。
	Check func(ctx context.Context, it Interaction) bool
	// OnTimeout 逾時後呼叫（已從 Registry 移除）。
	OnTimeout func(ctx context.Context)
	// OnError 回呼回傳錯誤或 panic 時呼叫（discord.py on_error）。nil 時只記錄。
	OnError func(ctx context.Context, it Interaction, err error)

	items []Item

	// Registry 管理的狀態
	reg      *Registry
	id       string
	ids      []string // 目前登記的 custom_id
	run      sync.Mutex
	lastUsed atomic.Int64 // UnixNano
	stopped  atomic.Bool
}

// NewView 建立 View。
func NewView(timeout time.Duration) *View { return &View{Timeout: timeout} }

// Clear 移除所有元件（discord.py clear_items）。
func (v *View) Clear() { v.items = nil }

// Items 目前的元件（依加入順序）。
func (v *View) Items() []Item { return v.items }

// Buttons 目前的按鈕（依加入順序）。
func (v *View) Buttons() []*Button {
	var out []*Button
	for _, it := range v.items {
		if b, ok := it.(*Button); ok {
			out = append(out, b)
		}
	}
	return out
}

// Selects 目前的下拉選單（依加入順序）。
func (v *View) Selects() []*Select {
	var out []*Select
	for _, it := range v.items {
		if s, ok := it.(*Select); ok {
			out = append(out, s)
		}
	}
	return out
}

func weight(it Item) int {
	if _, ok := it.(*Select); ok {
		return MaxRowItems // 下拉選單獨佔一列
	}
	return 1
}

// Add 加入元件；該列放不下時 panic(ErrRowFull)（discord.py add_item 丟出 ValueError，面板重繪時改顯示錯誤畫面）。
func (v *View) Add(it Item) Item {
	row := it.ItemRow()
	if row < 0 || row >= MaxRows {
		panic(ErrRowFull{row})
	}
	used := 0
	for _, x := range v.items {
		if x.ItemRow() == row {
			used += weight(x)
		}
	}
	if used+weight(it) > MaxRowItems {
		panic(ErrRowFull{row})
	}
	v.items = append(v.items, it)
	return it
}

// Rows 依列分組（列號由小到大；每列內依加入順序），給轉接層組成 Discord 的 ActionsRow。
func (v *View) Rows() [][]Item {
	var rows [][]Item
	for r := 0; r < MaxRows; r++ {
		var row []Item
		for _, it := range v.items {
			if it.ItemRow() == r {
				row = append(row, it)
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// DisableAll 停用所有元件（逾時時使用）。
func (v *View) DisableAll() {
	for _, it := range v.items {
		switch x := it.(type) {
		case *Button:
			x.Disabled = true
		case *Select:
			x.Disabled = true
		}
	}
}

// Stopped 已逾時或已停止（discord.py is_finished）。
func (v *View) Stopped() bool { return v.stopped.Load() }

// Stop 停止接收互動（從 Registry 移除，不呼叫 OnTimeout）。
func (v *View) Stop() {
	v.stopped.Store(true)
	if v.reg != nil {
		v.reg.unbind(v)
	}
}

// ---------- Registry ----------

type itemRef struct {
	view *View
	item Item
}

type modalRef struct {
	modal   *Modal
	view    *View // 開啟對話框的面板；送出時同樣依序執行（nil 表示不屬於任何面板）
	expires time.Time
}

// Registry 依 custom_id 找到元件與對話框的處理函式（discord.py 的 ViewStore）。可同時被多個 goroutine 使用。
type Registry struct {
	// DirectReply 元件互動在這段時間內沒有回應就自動先 defer；負值表示不自動 defer。
	DirectReply time.Duration
	Now         func() time.Time

	mu     sync.Mutex
	seq    uint64
	views  map[string]*View
	items  map[string]itemRef
	modals map[string]modalRef
}

// NewRegistry 建立 Registry（DirectReply 預設 1.5 秒）。
func NewRegistry() *Registry {
	return &Registry{DirectReply: DefaultDirectReply, Now: time.Now, views: map[string]*View{},
		items: map[string]itemRef{}, modals: map[string]modalRef{}}
}

func (r *Registry) next(prefix string) string {
	r.seq++
	return prefix + strconv.FormatUint(r.seq, 36)
}

// Bind 送出含有 v 的訊息前呼叫：替目前的元件指定新的 custom_id 並登記（舊的 ID 失效）。轉接層與測試模擬會自動呼叫。
func (r *Registry) Bind(v *View) {
	if v == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if v.reg == nil {
		v.reg, v.id = r, r.next("v")
		v.lastUsed.Store(r.Now().UnixNano())
	}
	for _, id := range v.ids {
		delete(r.items, id)
	}
	v.ids = v.ids[:0]
	if v.stopped.Load() {
		return
	}
	r.views[v.id] = v
	for _, it := range v.items {
		id := r.next("c")
		switch x := it.(type) {
		case *Button:
			if x.URL != "" {
				continue
			}
			x.customID = id
		case *Select:
			x.customID = id
		}
		r.items[id] = itemRef{v, it}
		v.ids = append(v.ids, id)
	}
}

func (r *Registry) unbind(v *View) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range v.ids {
		delete(r.items, id)
	}
	v.ids = nil
	delete(r.views, v.id)
}

type viewKey struct{}

// CurrentView 正在執行回呼的 View（對話框由此得知所屬面板）。
func CurrentView(ctx context.Context) *View {
	v, _ := ctx.Value(viewKey{}).(*View)
	return v
}

// BindModal 送出對話框前呼叫：指定 custom_id 並登記（所屬面板取自 ctx）。
func (r *Registry) BindModal(ctx context.Context, m *Modal) {
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = DefaultModalTimeout
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m.customID = r.next("m")
	r.modals[m.customID] = modalRef{m, CurrentView(ctx), r.Now().Add(timeout)}
}

// DispatchComponent 處理元件互動；找不到元件（舊訊息、已逾時）時回傳 false，由呼叫端決定如何回覆。
func (r *Registry) DispatchComponent(ctx context.Context, it Interaction, customID string, values []string) bool {
	r.mu.Lock()
	ref, ok := r.items[customID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	r.run(ctx, it, ref.view, func(ctx context.Context) error {
		switch x := ref.item.(type) {
		case *Button:
			if x.OnClick != nil {
				return x.OnClick(ctx, it)
			}
		case *Select:
			if x.OnSelectMany != nil {
				return x.OnSelectMany(ctx, it, values)
			}
			if x.OnSelect != nil {
				value := ""
				if len(values) > 0 {
					value = values[0]
				}
				return x.OnSelect(ctx, it, value)
			}
		}
		return nil
	}, true)
	return true
}

// DispatchModal 處理對話框送出；找不到（已逾時或已送出）時回傳 false。
func (r *Registry) DispatchModal(ctx context.Context, it Interaction, customID string, values []string) bool {
	r.mu.Lock()
	ref, ok := r.modals[customID]
	if ok {
		delete(r.modals, customID) // 送出後即結束（discord.py Modal 送出後 stop）
	}
	r.mu.Unlock()
	if !ok || ref.modal.OnSubmit == nil {
		return ok
	}
	r.run(ctx, it, ref.view, func(ctx context.Context) error { return ref.modal.OnSubmit(ctx, it, values) }, false)
	return true
}

// RunFor 在 v 的回呼順序中執行 fn（與元件互動相同：依序執行、逾時自動 defer、錯誤交給 OnError）；不做 Check。
// 用於程式主動更新面板（例如釣魚畫面的自動更新）與測試。
func (r *Registry) RunFor(ctx context.Context, it Interaction, v *View, fn func(ctx context.Context) error) {
	r.run(ctx, it, v, fn, false)
}

// run 依序執行同一面板的回呼。等待與執行超過 DirectReply 仍未回應時自動 defer（Python _in_time）。
func (r *Registry) run(ctx context.Context, it Interaction, v *View, fn func(ctx context.Context) error, check bool) {
	if r.DirectReply >= 0 {
		timer := time.AfterFunc(r.DirectReply, func() {
			if err := it.DeferUpdateIfPending(context.WithoutCancel(ctx)); err != nil {
				slog.Warn("延後回覆失敗", "type", fmt.Sprintf("%T", err))
			}
		})
		defer timer.Stop()
	}
	if v == nil {
		r.call(ctx, it, nil, fn)
		return
	}
	v.lastUsed.Store(r.Now().UnixNano())
	v.run.Lock()
	defer v.run.Unlock()
	if check && v.Check != nil && !v.Check(ctx, it) {
		return
	}
	r.call(context.WithValue(ctx, viewKey{}, v), it, v, fn)
}

func (r *Registry) call(ctx context.Context, it Interaction, v *View, fn func(ctx context.Context) error) {
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		return fn(ctx)
	}()
	if err == nil {
		return
	}
	if v != nil && v.OnError != nil {
		v.OnError(ctx, it, err)
		return
	}
	slog.Error("互動處理失敗", "user", it.UserID(), "error", err.Error())
}

// Sweep 移除逾時的面板與對話框，並呼叫面板的 OnTimeout（依序執行，不與回呼同時）。
func (r *Registry) Sweep(ctx context.Context) {
	now := r.Now()
	r.mu.Lock()
	var expired []*View
	for _, v := range r.views {
		if v.Timeout > 0 && now.Sub(time.Unix(0, v.lastUsed.Load())) >= v.Timeout {
			expired = append(expired, v)
		}
	}
	for id, m := range r.modals {
		if !now.Before(m.expires) {
			delete(r.modals, id)
		}
	}
	r.mu.Unlock()
	for _, v := range expired {
		v.run.Lock()
		if !v.stopped.Swap(true) {
			r.unbind(v)
			if v.OnTimeout != nil {
				v.OnTimeout(ctx)
			}
		}
		v.run.Unlock()
	}
}

// Run 每 interval 執行一次 Sweep，直到 ctx 取消。
func (r *Registry) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Sweep(ctx)
		}
	}
}

// Live 目前登記的面板數（測試與監控用）。
func (r *Registry) Live() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.views)
}

// Has 測試用：custom_id 是否仍有效。
func (r *Registry) Has(customID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.items[customID]
	if !ok {
		_, ok = r.modals[customID]
	}
	return ok
}
