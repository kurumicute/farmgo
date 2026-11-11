package ui_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/bot/ui/uitest"
)

func TestRowsAndLimits(t *testing.T) {
	v := ui.NewView(0)
	for i := range 5 {
		v.Add(&ui.Button{Label: string(rune('a' + i)), Row: 0})
	}
	v.Add(&ui.Select{Placeholder: "選單", Row: 1})
	v.Add(&ui.Button{Label: "z", Row: 2})
	func() {
		defer func() {
			if _, ok := recover().(ui.ErrRowFull); !ok {
				t.Fatal("第 6 個按鈕應該失敗")
			}
		}()
		v.Add(&ui.Button{Label: "f", Row: 0})
	}()
	func() {
		defer func() {
			if _, ok := recover().(ui.ErrRowFull); !ok {
				t.Fatal("下拉選單獨佔一列")
			}
		}()
		v.Add(&ui.Button{Label: "g", Row: 1})
	}()
	rows := v.Rows()
	if len(rows) != 3 || len(rows[0]) != 5 || len(rows[1]) != 1 || len(rows[2]) != 1 {
		t.Fatal(rows)
	}
	e := ui.Embed{Title: "標題", Description: "說明", Footer: "頁尾"}
	e.AddField("名", "值", false)
	if e.Len() != 8 { // Python len(embed) 以字元計算
		t.Fatal(e.Len())
	}
}

func TestBindDispatchAndStaleIDs(t *testing.T) {
	reg := ui.NewRegistry()
	reg.DirectReply = -1
	var clicked []string
	v := ui.NewView(time.Minute)
	v.Check = func(ctx context.Context, it ui.Interaction) bool {
		if it.UserID() != "owner" {
			_ = it.SendMessage(ctx, ui.Text("這是別人的面板"))
			return false
		}
		return true
	}
	b := &ui.Button{Label: "按我", OnClick: func(ctx context.Context, it ui.Interaction) error {
		clicked = append(clicked, "按我")
		return it.Show(ctx, ui.Text("已更新"))
	}}
	v.Add(b)
	s := &ui.Select{Placeholder: "選", Row: 1, OnSelect: func(_ context.Context, _ ui.Interaction, value string) error {
		clicked = append(clicked, "選:"+value)
		return nil
	}}
	v.Add(s)
	multi := &ui.Select{Placeholder: "複選", Row: 2, MinValues: 1, MaxValues: 3,
		OnSelectMany: func(_ context.Context, _ ui.Interaction, values []string) error {
			clicked = append(clicked, "複選:"+strings.Join(values, ","))
			return nil
		}}
	v.Add(multi)
	reg.Bind(v)
	old := b.CustomID()
	if old == "" || s.CustomID() == "" || old == s.CustomID() {
		t.Fatal(old, s.CustomID())
	}

	other := uitest.New(t, reg, "intruder", "g")
	if !reg.DispatchComponent(context.Background(), other, old, nil) || len(clicked) != 0 || other.Kinds()[0] != "send" {
		t.Fatal("非擁有者不能操作", clicked, other.Kinds())
	}
	it := uitest.New(t, reg, "owner", "g")
	reg.DispatchComponent(context.Background(), it, old, nil)
	reg.DispatchComponent(context.Background(), uitest.New(t, reg, "owner", "g"), s.CustomID(), []string{"x"})
	reg.DispatchComponent(context.Background(), uitest.New(t, reg, "owner", "g"), multi.CustomID(), []string{"a", "b"})
	if !slices.Equal(clicked, []string{"按我", "選:x", "複選:a,b"}) || it.Entries()[0].Direct != true {
		t.Fatal(clicked, it.Entries())
	}
	// 重新綁定（重繪）：舊的 ID 失效
	reg.Bind(v)
	if reg.Has(old) || !reg.Has(b.CustomID()) || reg.DispatchComponent(context.Background(), it, old, nil) {
		t.Fatal("舊 ID 應失效")
	}
	v.Stop()
	if reg.Has(b.CustomID()) || reg.Live() != 0 {
		t.Fatal("Stop 後應移除")
	}
}

func TestCallbacksOfOneViewRunInOrderAndAutoDefer(t *testing.T) {
	reg := ui.NewRegistry()
	reg.DirectReply = 30 * time.Millisecond
	var running, maxRunning atomic.Int32
	v := ui.NewView(0)
	b := &ui.Button{Label: "慢", OnClick: func(ctx context.Context, it ui.Interaction) error {
		n := running.Add(1)
		if n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		time.Sleep(60 * time.Millisecond)
		running.Add(-1)
		return it.Show(ctx, ui.Text("完成"))
	}}
	v.Add(b)
	reg.Bind(v)
	var wg sync.WaitGroup
	its := make([]*uitest.Interaction, 3)
	for i := range its {
		its[i] = uitest.New(t, reg, "owner", "g")
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.DispatchComponent(context.Background(), its[i], b.CustomID(), nil)
		}()
	}
	wg.Wait()
	if maxRunning.Load() != 1 {
		t.Fatal("同一面板的回呼不能同時執行", maxRunning.Load())
	}
	for _, it := range its {
		// 超過時限：先 defer，之後編輯原訊息
		if k := it.Kinds(); !slices.Equal(k, []string{"defer", "edit"}) || it.Entries()[1].Direct {
			t.Fatal(k)
		}
	}
	fast := uitest.New(t, reg, "owner", "g")
	quick := &ui.Button{Label: "快", OnClick: func(ctx context.Context, it ui.Interaction) error { return it.Show(ctx, ui.Text("快")) }}
	v.Add(quick)
	reg.Bind(v)
	reg.DispatchComponent(context.Background(), fast, quick.CustomID(), nil)
	if k := fast.Kinds(); !slices.Equal(k, []string{"edit"}) || !fast.Entries()[0].Direct {
		t.Fatal(k)
	}
}

func TestModalIsOneShotAndBelongsToView(t *testing.T) {
	reg := ui.NewRegistry()
	reg.DirectReply = -1
	v := ui.NewView(0)
	var got []string
	var owner *ui.View
	opener := &ui.Button{Label: "自訂數量", OnClick: func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, &ui.Modal{Title: "數量", Inputs: []ui.TextInput{{Label: "數量"}},
			OnSubmit: func(ctx context.Context, it ui.Interaction, values []string) error {
				got, owner = values, ui.CurrentView(ctx)
				return nil
			}})
	}}
	v.Add(opener)
	reg.Bind(v)
	it := uitest.New(t, reg, "owner", "g")
	reg.DispatchComponent(context.Background(), it, opener.CustomID(), nil)
	m := it.Entries()[0].Modal
	if m == nil || m.CustomID() == "" {
		t.Fatal(it.Kinds())
	}
	if !reg.DispatchModal(context.Background(), uitest.New(t, reg, "owner", "g"), m.CustomID(), []string{"7"}) ||
		!slices.Equal(got, []string{"7"}) || owner != v {
		t.Fatal(got, owner)
	}
	if reg.DispatchModal(context.Background(), uitest.New(t, reg, "owner", "g"), m.CustomID(), []string{"8"}) {
		t.Fatal("對話框送出後應失效")
	}
}

func TestTimeoutAndErrors(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	reg := ui.NewRegistry()
	reg.DirectReply = -1
	reg.Now = func() time.Time { return now }
	v := ui.NewView(15 * time.Minute)
	timedOut := 0
	v.OnTimeout = func(context.Context) { timedOut++; v.DisableAll() }
	var handled error
	v.OnError = func(_ context.Context, _ ui.Interaction, err error) { handled = err }
	boom := &ui.Button{Label: "錯", OnClick: func(context.Context, ui.Interaction) error { panic("boom") }}
	fail := &ui.Button{Label: "失敗", OnClick: func(context.Context, ui.Interaction) error { return errors.New("x") }}
	v.Add(boom)
	v.Add(fail)
	reg.Bind(v)
	reg.DispatchComponent(context.Background(), uitest.New(t, reg, "u", "g"), boom.CustomID(), nil)
	if handled == nil || handled.Error() != "panic: boom" {
		t.Fatal(handled)
	}
	reg.DispatchComponent(context.Background(), uitest.New(t, reg, "u", "g"), fail.CustomID(), nil)
	if handled.Error() != "x" {
		t.Fatal(handled)
	}
	now = now.Add(14 * time.Minute) // 最後一次互動後 14 分鐘：還沒逾時
	reg.Sweep(context.Background())
	if timedOut != 0 {
		t.Fatal("太早逾時")
	}
	now = now.Add(time.Minute)
	reg.Sweep(context.Background())
	reg.Sweep(context.Background())
	if timedOut != 1 || !boom.Disabled || reg.Has(fail.CustomID()) || reg.Live() != 0 {
		t.Fatal(timedOut, boom.Disabled)
	}
}
