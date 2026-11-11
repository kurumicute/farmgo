// Package uitest 模擬 Discord 互動（對應 Python tests/test_discord_bot.py 的 FakeInteraction），不連到真實 Discord。
// 首次回應只能一次（重複回應會讓測試失敗），與 Discord 相同。
package uitest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"farmgm/internal/bot/ui"
)

// Entry 一筆回應紀錄。Kind：send、modal、defer、edit（Direct 表示直接回應元件互動）、followup、delete、message_edit。
type Entry struct {
	Kind           string
	Message        ui.Message
	Modal          *ui.Modal
	Direct         bool
	DeferEphemeral bool
}

// Text Content 與 Embed 的文字。
func (e Entry) Text() string {
	var parts []string
	if e.Message.Content != nil && *e.Message.Content != "" {
		parts = append(parts, *e.Message.Content)
	}
	if t := EmbedText(e.Message.Embed); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n")
}

// EmbedText 標題、說明與欄位（Python embed_text）。
func EmbedText(e *ui.Embed) string {
	if e == nil {
		return ""
	}
	parts := []string{e.Title, e.Description}
	for _, f := range e.Fields {
		parts = append(parts, f.Name+"\n"+f.Value)
	}
	return strings.Join(parts, "\n")
}

// Interaction 模擬的互動。
type Interaction struct {
	T        testing.TB
	Reg      *ui.Registry
	User     string
	Name     string
	Avatar   string
	Guild    string
	GuildNm  string
	Attached []string // 元件所在訊息目前的附件檔名

	mu   sync.Mutex
	done bool
	Log  []Entry
}

// New 建立模擬互動（guild 為 "" 表示私訊）。
func New(t testing.TB, reg *ui.Registry, user, guild string) *Interaction {
	return &Interaction{T: t, Reg: reg, User: user, Name: "玩家", Guild: guild, GuildNm: "測試伺服器"}
}

func (f *Interaction) UserID() string               { return f.User }
func (f *Interaction) UserName() string             { return f.Name }
func (f *Interaction) AvatarURL() string            { return f.Avatar }
func (f *Interaction) GuildID() string              { return f.Guild }
func (f *Interaction) GuildName() string            { return f.GuildNm }
func (f *Interaction) MessageAttachments() []string { return f.Attached }

func (f *Interaction) Responded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *Interaction) first(kind string) error {
	if f.done {
		f.T.Errorf("同一互動只能首次回應一次（%s）", kind)
		return fmt.Errorf("已經回應過")
	}
	f.done = true
	return nil
}

func (f *Interaction) bind(m ui.Message) {
	if m.View != nil && f.Reg != nil {
		f.Reg.Bind(m.View)
	}
}

func (f *Interaction) add(e Entry) {
	f.Log = append(f.Log, e)
}

func (f *Interaction) SendMessage(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.first("send"); err != nil {
		return err
	}
	f.bind(m)
	f.add(Entry{Kind: "send", Message: m})
	return nil
}

func (f *Interaction) SendModal(ctx context.Context, m *ui.Modal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.first("modal"); err != nil {
		return err
	}
	if f.Reg != nil {
		f.Reg.BindModal(ctx, m)
	}
	f.add(Entry{Kind: "modal", Modal: m})
	return nil
}

func (f *Interaction) Defer(_ context.Context, ephemeral bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.first("defer"); err != nil {
		return err
	}
	f.add(Entry{Kind: "defer", DeferEphemeral: ephemeral})
	return nil
}

func (f *Interaction) DeferUpdateIfPending(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.done {
		f.done = true
		f.add(Entry{Kind: "defer"})
	}
	return nil
}

func (f *Interaction) EditMessage(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.first("edit_message"); err != nil {
		return err
	}
	f.bind(m)
	f.add(Entry{Kind: "edit", Message: m, Direct: true})
	return nil
}

func (f *Interaction) Show(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bind(m)
	f.add(Entry{Kind: "edit", Message: m, Direct: !f.done})
	f.done = true
	return nil
}

func (f *Interaction) EditOriginal(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bind(m)
	f.add(Entry{Kind: "edit", Message: m})
	return nil
}

func (f *Interaction) DeleteOriginal(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.add(Entry{Kind: "delete"})
	return nil
}

func (f *Interaction) Followup(_ context.Context, m ui.Message) (ui.SentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bind(m)
	f.add(Entry{Kind: "followup", Message: m})
	return &sent{f}, nil
}

type sent struct{ f *Interaction }

func (s *sent) Edit(_ context.Context, m ui.Message) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.bind(m)
	s.f.add(Entry{Kind: "message_edit", Message: m})
	return nil
}

// Kinds 依序列出回應種類（Python [k for k, _c, _kw in log]）。
func (f *Interaction) Kinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Log))
	for i, e := range f.Log {
		out[i] = e.Kind
	}
	return out
}

// Text 所有回應的文字（Python FakeInteraction.text）。
func (f *Interaction) Text() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var parts []string
	for _, e := range f.Log {
		if t := e.Text(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// Last 最後一筆 send 或 followup（Python last_kwargs）。
func (f *Interaction) Last() Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.Log) - 1; i >= 0; i-- {
		if k := f.Log[i].Kind; k == "send" || k == "followup" {
			return f.Log[i]
		}
	}
	f.T.Fatal("沒有 send 或 followup")
	return Entry{}
}

// Entries 回應紀錄的複本。
func (f *Interaction) Entries() []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Entry(nil), f.Log...)
}

var _ ui.Interaction = (*Interaction)(nil)
