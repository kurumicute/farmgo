package discordio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot/ui"
)

// API Discord REST API 的基底網址（測試改成本機的假伺服器）。
var API = discordgo.EndpointAPI

// client 送出 Discord API 請求（discordgo.Session 處理速率限制與重試）。
type client struct {
	s *discordgo.Session
}

// send 以 JSON 或（有檔案時）multipart 送出；回傳回應內容。
func (c client) send(ctx context.Context, method, url string, payload any, files []ui.File) ([]byte, error) {
	opts := []discordgo.RequestOption{discordgo.WithContext(ctx)}
	if len(files) == 0 {
		var body []byte
		if payload != nil {
			var err error
			if body, err = json.Marshal(payload); err != nil {
				return nil, err
			}
		}
		return c.s.RequestRaw(method, url, "application/json", body, "", 0, opts...)
	}
	df := make([]*discordgo.File, len(files))
	for i, f := range files {
		df[i] = &discordgo.File{Name: f.Name, ContentType: contentType(f.Name), Reader: bytes.NewReader(f.Data)}
	}
	ct, body, err := discordgo.MultipartBodyWithJSON(payload, df)
	if err != nil {
		return nil, err
	}
	return c.s.RequestRaw(method, url, ct, body, "", 0, opts...)
}

func contentType(name string) string {
	if len(name) > 4 && name[len(name)-4:] == ".png" {
		return "image/png"
	}
	return "application/octet-stream"
}

// Interaction 一次 Discord 互動（ui.Interaction 的實作）。首次回應與「已回應」的判斷在鎖內完成，
// 與 Registry 的自動 defer 不會互相搶（Show 的原子性）。
type Interaction struct {
	c   client
	i   *discordgo.Interaction
	reg *ui.Registry

	mu        sync.Mutex
	responded bool
}

// NewInteraction 包裝 discordgo 的互動。
func NewInteraction(s *discordgo.Session, i *discordgo.Interaction, reg *ui.Registry) *Interaction {
	return &Interaction{c: client{s}, i: i, reg: reg}
}

func (it *Interaction) user() *discordgo.User {
	if it.i.Member != nil && it.i.Member.User != nil {
		return it.i.Member.User
	}
	return it.i.User
}

// UserID 互動的使用者 ID。
func (it *Interaction) UserID() string {
	if u := it.user(); u != nil {
		return u.ID
	}
	return ""
}

// UserName 顯示名稱（伺服器暱稱優先，其次全域顯示名稱、使用者名稱；discord.py display_name）。
func (it *Interaction) UserName() string {
	if it.i.Member != nil && it.i.Member.User != nil {
		return it.i.Member.DisplayName()
	}
	if u := it.i.User; u != nil {
		return u.DisplayName()
	}
	return ""
}

// AvatarURL 頭像（伺服器頭像優先；discord.py display_avatar）。
func (it *Interaction) AvatarURL() string {
	if it.i.Member != nil && it.i.Member.User != nil {
		return it.i.Member.AvatarURL("")
	}
	if u := it.i.User; u != nil {
		return u.AvatarURL("")
	}
	return ""
}

// GuildID 伺服器 ID（私訊為 ""）。
func (it *Interaction) GuildID() string { return it.i.GuildID }

// GuildName 伺服器名稱，取自連線狀態的快取（discord.py interaction.guild.name；不在快取時為 ""）。
func (it *Interaction) GuildName() string {
	if it.i.GuildID == "" || it.c.s.State == nil {
		return ""
	}
	if g, err := it.c.s.State.Guild(it.i.GuildID); err == nil && g != nil {
		return g.Name
	}
	return ""
}

// MessageAttachments 元件所在訊息目前的附件檔名。
func (it *Interaction) MessageAttachments() []string {
	if it.i.Message == nil {
		return nil
	}
	out := make([]string, 0, len(it.i.Message.Attachments))
	for _, a := range it.i.Message.Attachments {
		out = append(out, a.Filename)
	}
	return out
}

// Responded 是否已首次回應。
func (it *Interaction) Responded() bool {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.responded
}

func (it *Interaction) callbackURL() string {
	return API + "interactions/" + it.i.ID + "/" + it.i.Token + "/callback"
}

func (it *Interaction) webhookURL(suffix string) string {
	return API + "webhooks/" + it.i.AppID + "/" + it.i.Token + suffix
}

// respond 首次回應（呼叫端持有鎖）。
func (it *Interaction) respond(ctx context.Context, kind int, e *encoded) error {
	if it.responded {
		return fmt.Errorf("互動已經回應過")
	}
	resp := wireResponse{Type: kind}
	var files []ui.File
	if e != nil {
		resp.Data, files = e.msg, e.files
	}
	if _, err := it.c.send(ctx, http.MethodPost, it.callbackURL(), resp, files); err != nil {
		return err
	}
	it.responded = true
	return nil
}

func (it *Interaction) bind(m ui.Message) {
	if m.View != nil && it.reg != nil {
		it.reg.Bind(m.View)
	}
}

// SendMessage 以新訊息回應。
func (it *Interaction) SendMessage(ctx context.Context, m ui.Message) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.bind(m)
	e := encode(m, false)
	return it.respond(ctx, respMessage, &e)
}

// SendModal 以對話框回應（Registry 指定 custom_id 並登記，所屬面板取自 ctx）。
func (it *Interaction) SendModal(ctx context.Context, m *ui.Modal) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.responded {
		return fmt.Errorf("互動已經回應過")
	}
	if it.reg != nil {
		it.reg.BindModal(ctx, m)
	}
	if _, err := it.c.send(ctx, http.MethodPost, it.callbackURL(), wireResponse{Type: respModal, Data: modalOf(m)}, nil); err != nil {
		return err
	}
	it.responded = true
	return nil
}

func (it *Interaction) isCommand() bool { return it.i.Type == discordgo.InteractionApplicationCommand }

// Defer 指令：顯示思考中（ephemeral 決定之後的訊息是否私密）；元件與對話框：之後編輯原訊息。
func (it *Interaction) Defer(ctx context.Context, ephemeral bool) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.isCommand() {
		e := encoded{msg: &wireMessage{}}
		if ephemeral {
			e.msg.Flags = flagEphemeral
		}
		return it.respond(ctx, respDeferMessage, &e)
	}
	return it.respond(ctx, respDeferUpdate, nil)
}

// DeferUpdateIfPending 尚未回應時先 defer；已回應時不做任何事。
func (it *Interaction) DeferUpdateIfPending(ctx context.Context) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.responded {
		return nil
	}
	if it.isCommand() {
		return it.respond(ctx, respDeferMessage, &encoded{msg: &wireMessage{}})
	}
	return it.respond(ctx, respDeferUpdate, nil)
}

// EditMessage 元件互動：直接以新內容更新原訊息。
func (it *Interaction) EditMessage(ctx context.Context, m ui.Message) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.bind(m)
	e := encode(m, true)
	return it.respond(ctx, respUpdateMessage, &e)
}

// Show 尚未回應時直接回應（UPDATE_MESSAGE），已 defer 時編輯原訊息。
func (it *Interaction) Show(ctx context.Context, m ui.Message) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.bind(m)
	e := encode(m, true)
	if !it.responded {
		return it.respond(ctx, respUpdateMessage, &e)
	}
	_, err := it.c.send(ctx, http.MethodPatch, it.webhookURL("/messages/@original"), e.msg, e.files)
	return err
}

// EditOriginal 編輯原訊息（回應之後）。
func (it *Interaction) EditOriginal(ctx context.Context, m ui.Message) error {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.bind(m)
	e := encode(m, true)
	_, err := it.c.send(ctx, http.MethodPatch, it.webhookURL("/messages/@original"), e.msg, e.files)
	return err
}

// DeleteOriginal 刪除原訊息。
func (it *Interaction) DeleteOriginal(ctx context.Context) error {
	_, err := it.c.send(ctx, http.MethodDelete, it.webhookURL("/messages/@original"), nil, nil)
	return err
}

// Followup 送出後續訊息；回傳的訊息之後可以編輯（例如面板逾時時停用元件）。
func (it *Interaction) Followup(ctx context.Context, m ui.Message) (ui.SentMessage, error) {
	it.mu.Lock()
	it.bind(m)
	it.mu.Unlock()
	e := encode(m, false)
	body, err := it.c.send(ctx, http.MethodPost, it.webhookURL("?wait=true"), e.msg, e.files)
	if err != nil {
		return nil, err
	}
	var msg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &msg); err != nil || msg.ID == "" {
		return nil, fmt.Errorf("followup 回應沒有訊息 ID")
	}
	return &sentMessage{it, msg.ID}, nil
}

type sentMessage struct {
	it *Interaction
	id string
}

// Edit 編輯 followup 訊息（互動權杖有效 15 分鐘）。
func (s *sentMessage) Edit(ctx context.Context, m ui.Message) error {
	s.it.bind(m)
	e := encode(m, true)
	_, err := s.it.c.send(ctx, http.MethodPatch, s.it.webhookURL("/messages/"+s.id), e.msg, e.files)
	return err
}
