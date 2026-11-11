package discordio

// 轉接層測試：以本機的假 Discord API（httptest）檢查送出的請求（回應類型、JSON 欄位、附件、multipart），
// 並從斜線指令 /farm 一路走到點擊面板按鈕（真的面板與測試資料庫）。不連到真實 Discord。

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot"
	"farmgm/internal/bot/ui"
	"farmgm/internal/clock"
	"farmgm/internal/config"
	"farmgm/internal/services"
	"farmgm/internal/services/svctest"
)

// request 假伺服器收到的一個請求。
type request struct {
	Method, Path string
	Query        string
	Payload      map[string]any
	Files        []string
}

type fakeDiscord struct {
	t    *testing.T
	mu   sync.Mutex
	reqs []request
	srv  *httptest.Server
	seq  int
}

func newFake(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	old := API
	API = f.srv.URL + "/api/v10/"
	t.Cleanup(func() { API = old; f.srv.Close() })
	return f
}

func (f *fakeDiscord) handle(w http.ResponseWriter, r *http.Request) {
	req := request{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api/v10/"), Query: r.URL.RawQuery}
	ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var raw []byte
	if ct == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(p)
			if p.FormName() == "payload_json" {
				raw = data
			} else {
				req.Files = append(req.Files, p.FileName())
			}
		}
	} else {
		raw, _ = io.ReadAll(r.Body)
	}
	if len(raw) > 0 && raw[0] == '[' {
		if !json.Valid(raw) {
			f.t.Errorf("JSON 格式錯誤：%s", raw)
		}
		req.Payload = map[string]any{"list": json.RawMessage(raw)}
	} else if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req.Payload); err != nil {
			f.t.Errorf("JSON 格式錯誤：%s", raw)
		}
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.seq++
	id := f.seq
	f.mu.Unlock()
	if strings.HasSuffix(req.Path, "/callback") || r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(req.Path, "/commands") {
		_, _ = w.Write([]byte(`[{"id":"1"},{"id":"2"}]`))
		return
	}
	_, _ = w.Write([]byte(`{"id":"9000` + string(rune('0'+id%10)) + `"}`))
}

func (f *fakeDiscord) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request{}, f.reqs...)
}

func (f *fakeDiscord) last() request {
	r := f.requests()
	if len(r) == 0 {
		f.t.Fatal("沒有請求")
	}
	return r[len(r)-1]
}

func session(t *testing.T) *discordgo.Session {
	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func component(id, custom string) *discordgo.Interaction {
	return &discordgo.Interaction{ID: id, AppID: "app", Token: "tok", Type: discordgo.InteractionMessageComponent,
		GuildID: "111", Member: &discordgo.Member{User: &discordgo.User{ID: "900", Username: "alice"}},
		Data: discordgo.MessageComponentInteractionData{CustomID: custom}}
}

func typeOf(r request) float64 {
	v, _ := r.Payload["type"].(float64)
	return v
}

func data(r request) map[string]any {
	d, _ := r.Payload["data"].(map[string]any)
	return d
}

// ---------- 訊息轉換 ----------

func TestEncodeOnlySendsPresentFields(t *testing.T) {
	v := ui.NewView(time.Minute)
	v.Add(&ui.Button{Emoji: "🐡", Style: ui.Secondary, Row: 0})
	v.Add(&ui.Button{Label: "連結", URL: "https://example.com", Row: 0})
	v.Add(&ui.Select{Placeholder: "選擇", Row: 1, Options: []ui.SelectOption{{Label: "甲", Value: "a"}, {Label: "乙", Value: "b", Description: "說明", Emoji: "🌾", Default: true}}})
	v.Add(&ui.Select{Placeholder: "複選", Row: 2, MinValues: 1, MaxValues: 2, Options: []ui.SelectOption{{Label: "甲", Value: "a"}, {Label: "乙", Value: "b"}}})
	ui.NewRegistry().Bind(v)
	e := encode(ui.Message{Embed: &ui.Embed{Title: "標題", Color: 0x57F287, Footer: "頁尾", Image: "attachment://farm.png"}, View: v}, true)
	raw, _ := json.Marshal(e.msg)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	rows := got["components"].([]any)
	if len(rows) != 3 {
		t.Fatal(string(raw))
	}
	buttons := rows[0].(map[string]any)["components"].([]any)
	emojiOnly := buttons[0].(map[string]any)
	if _, ok := emojiOnly["label"]; ok || emojiOnly["emoji"].(map[string]any)["name"] != "🐡" || emojiOnly["custom_id"] == "" || emojiOnly["style"] != float64(2) {
		t.Fatal("只有 emoji 的按鈕不應送 label：", emojiOnly)
	}
	link := buttons[1].(map[string]any)
	if link["style"] != float64(5) || link["url"] != "https://example.com" || link["custom_id"] != nil {
		t.Fatal(link)
	}
	sel := rows[1].(map[string]any)["components"].([]any)[0].(map[string]any)
	opts := sel["options"].([]any)
	if _, ok := opts[0].(map[string]any)["description"]; ok || sel["min_values"] != float64(1) || sel["max_values"] != float64(1) {
		t.Fatal(sel)
	}
	if o := opts[1].(map[string]any); o["description"] != "說明" || o["default"] != true || o["emoji"].(map[string]any)["name"] != "🌾" {
		t.Fatal(o)
	}
	if multi := rows[2].(map[string]any)["components"].([]any)[0].(map[string]any); multi["max_values"] != float64(2) {
		t.Fatal(multi)
	}
	if _, ok := got["attachments"]; ok { // 沒有指定附件：不變更
		t.Fatal(got)
	}
	if m := got["allowed_mentions"].(map[string]any); len(m["parse"].([]any)) != 0 {
		t.Fatal(m)
	}
	if _, ok := got["flags"]; ok {
		t.Fatal(got)
	}
}

func TestEncodeAttachments(t *testing.T) {
	file := ui.File{Name: "farm.png", Data: []byte{1, 2, 3}}
	keep := encode(ui.Message{Attachments: &ui.Attachments{Keep: true}}, true)
	if keep.msg.Attachments != nil || len(keep.files) != 0 { // 沿用：不送附件欄位
		t.Fatalf("%+v", keep)
	}
	replace := encode(ui.Message{Attachments: &ui.Attachments{Files: []ui.File{file}}}, true)
	if a := *replace.msg.Attachments; len(a) != 1 || a[0].ID != "0" || a[0].Filename != "farm.png" || len(replace.files) != 1 {
		t.Fatalf("%+v", replace)
	}
	remove := encode(ui.Message{Attachments: &ui.Attachments{}}, true)
	if a := remove.msg.Attachments; a == nil || len(*a) != 0 || len(remove.files) != 0 { // 移除所有附件
		t.Fatalf("%+v", remove)
	}
	fresh := encode(ui.Message{Files: []ui.File{file}, Ephemeral: true}, false)
	if len(fresh.files) != 1 || fresh.msg.Flags != flagEphemeral {
		t.Fatalf("%+v", fresh)
	}
}

// ---------- 互動回應 ----------

func TestComponentShowDeferEditAndFollowup(t *testing.T) {
	f := newFake(t)
	reg := ui.NewRegistry()
	it := NewInteraction(session(t), component("1", "c1"), reg)
	v := ui.NewView(time.Minute)
	v.Add(&ui.Button{Label: "按我", Row: 0})
	png := ui.File{Name: "farm.png", Data: []byte("png")}

	// 尚未回應：UPDATE_MESSAGE（type 7），附件以 multipart 上傳
	if err := it.Show(context.Background(), ui.Message{Embed: &ui.Embed{Title: "A"}, View: v, Attachments: &ui.Attachments{Files: []ui.File{png}}}); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	if r.Method != "POST" || r.Path != "interactions/1/tok/callback" || typeOf(r) != 7 || len(r.Files) != 1 || r.Files[0] != "farm.png" {
		t.Fatalf("%+v", r)
	}
	if v.Items()[0].(*ui.Button).CustomID() == "" || !reg.Has(v.Items()[0].(*ui.Button).CustomID()) { // 送出前已登記元件
		t.Fatal("元件沒有登記")
	}
	// 已回應：編輯原訊息
	if err := it.Show(context.Background(), ui.Message{Embed: &ui.Embed{Title: "B"}, View: v, Attachments: &ui.Attachments{Keep: true}}); err != nil {
		t.Fatal(err)
	}
	r = f.last()
	if r.Method != "PATCH" || r.Path != "webhooks/app/tok/messages/@original" || r.Payload["attachments"] != nil {
		t.Fatalf("%+v", r)
	}
	// 再次 defer 不做任何事；首次回應不能兩次
	n := len(f.requests())
	if err := it.DeferUpdateIfPending(context.Background()); err != nil || len(f.requests()) != n {
		t.Fatal("已回應時不應再 defer")
	}
	if err := it.SendMessage(context.Background(), ui.Text("x")); err == nil {
		t.Fatal("首次回應只能一次")
	}
	// followup：私密訊息，之後可以編輯
	sent, err := it.Followup(context.Background(), ui.Message{Content: ptr("hi"), Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	r = f.last()
	if r.Method != "POST" || r.Path != "webhooks/app/tok" || r.Query != "wait=true" || r.Payload["flags"] != float64(64) {
		t.Fatalf("%+v", r)
	}
	if err := sent.Edit(context.Background(), ui.Message{Content: ptr("bye")}); err != nil {
		t.Fatal(err)
	}
	if r = f.last(); r.Method != "PATCH" || !strings.HasPrefix(r.Path, "webhooks/app/tok/messages/9000") {
		t.Fatalf("%+v", r)
	}
}

func TestDeferKindsAndModal(t *testing.T) {
	f := newFake(t)
	reg := ui.NewRegistry()
	cmd := &discordgo.Interaction{ID: "2", AppID: "app", Token: "tok", Type: discordgo.InteractionApplicationCommand,
		User: &discordgo.User{ID: "900", Username: "alice", GlobalName: "愛麗絲"}}
	it := NewInteraction(session(t), cmd, reg)
	if it.UserName() != "愛麗絲" || it.GuildID() != "" {
		t.Fatal(it.UserName())
	}
	if err := it.Defer(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if r := f.last(); typeOf(r) != 5 || data(r)["flags"] != float64(64) { // 指令：思考中（私密）
		t.Fatalf("%+v", r)
	}
	comp := NewInteraction(session(t), component("3", "c1"), reg)
	if err := comp.DeferUpdateIfPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := f.last(); typeOf(r) != 6 { // 元件：之後編輯原訊息
		t.Fatalf("%+v", r)
	}
	// 對話框：custom_id 由 Registry 指定，送出的值依輸入框順序取回
	var got []string
	m := &ui.Modal{Title: "數量", Inputs: []ui.TextInput{{Label: "數量", Placeholder: "1–5", MaxLength: 6, Required: true}, {Label: "備註", Default: "x"}},
		OnSubmit: func(_ context.Context, _ ui.Interaction, values []string) error { got = values; return nil }}
	modalIt := NewInteraction(session(t), component("4", "c2"), reg)
	if err := modalIt.SendModal(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	r := f.last()
	d := data(r)
	if typeOf(r) != 9 || d["custom_id"] != m.CustomID() || d["title"] != "數量" {
		t.Fatalf("%+v", r)
	}
	inputs := d["components"].([]any)
	first := inputs[0].(map[string]any)["components"].([]any)[0].(map[string]any)
	second := inputs[1].(map[string]any)["components"].([]any)[0].(map[string]any)
	if first["custom_id"] != "f0" || first["required"] != true || first["max_length"] != float64(6) || second["required"] != false || second["value"] != "x" {
		t.Fatal(first, second)
	}
	values := modalValues([]discordgo.MessageComponent{
		&discordgo.ActionsRow{Components: []discordgo.MessageComponent{&discordgo.TextInput{CustomID: "f0", Value: "3"}}},
		&discordgo.ActionsRow{Components: []discordgo.MessageComponent{&discordgo.TextInput{CustomID: "f1", Value: "備"}}},
	})
	submit := NewInteraction(session(t), component("5", ""), reg)
	if !reg.DispatchModal(context.Background(), submit, m.CustomID(), values) || len(got) != 2 || got[0] != "3" || got[1] != "備" {
		t.Fatal(got)
	}
}

func TestCommandsPayloadIsFarmAndHelp(t *testing.T) {
	cmds := CommandsPayload()
	if len(cmds) != 2 || cmds[0].Name != "farm" || cmds[1].Name != "help" || cmds[0].Type != 1 {
		t.Fatalf("%+v", cmds)
	}
}

func TestOpenErrorHidesDetails(t *testing.T) {
	if e := openError(errString("websocket: close 4004: Authentication failed.")); !strings.Contains(e.Error(), "DISCORD_BOT_TOKEN 無效") {
		t.Fatal(e)
	}
	if e := openError(errString("websocket: close 4014: Disallowed intent(s).")); !strings.Contains(e.Error(), "特權 Intent") {
		t.Fatal(e)
	}
	if e := openError(errString("dial tcp secret")); strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func ptr[T any](v T) *T { return &v }

// ---------- 從 /farm 到點擊按鈕（真的面板與測試資料庫） ----------

func TestFarmCommandAndButtonClickEndToEnd(t *testing.T) {
	d := svctest.Prepare(t, services.ClearNewsCache)
	f := newFake(t)
	c := clock.NewManual(time.Time{})
	svc := services.NewAdminService(d, services.Options{Clock: c})
	reg := ui.NewRegistry()
	reg.DirectReply = -1
	cmds := bot.NewCommands(svc, bot.NewRunner(2), reg)
	cmds.AutoCheckin = false
	b, err := New(config.Settings{DiscordBotToken: "test-token"}, cmds, svc)
	if err != nil {
		t.Fatal(err)
	}
	member := &discordgo.Member{User: &discordgo.User{ID: "900000000000000001", Username: "alice"}, Nick: "阿明"}
	// 伺服器名稱取自連線狀態的快取
	if err := b.S.State.GuildAdd(&discordgo.Guild{ID: "111111111111111111", Name: "測試伺服器"}); err != nil {
		t.Fatal(err)
	}
	slash := &discordgo.Interaction{ID: "10", AppID: "app", Token: "t1", Type: discordgo.InteractionApplicationCommand,
		GuildID: "111111111111111111", Member: member, Data: discordgo.ApplicationCommandInteractionData{Name: "farm"}}
	b.onInteraction(b.S, &discordgo.InteractionCreate{Interaction: slash})
	reqs := f.requests()
	if len(reqs) != 2 || typeOf(reqs[0]) != 5 || data(reqs[0])["flags"] != float64(64) {
		t.Fatalf("%+v", reqs)
	}
	panel := reqs[1]
	if panel.Path != "webhooks/app/t1" || panel.Payload["flags"] != float64(64) {
		t.Fatalf("%+v", panel)
	}
	embed := panel.Payload["embeds"].([]any)[0].(map[string]any)
	if !strings.Contains(embed["title"].(string), "歡迎來到農場") || !strings.Contains(embed["description"].(string), "測試伺服器") {
		t.Fatal(embed)
	}
	// 找到「建立農場」按鈕並點擊：元件互動直接更新面板（附上農田圖）
	var create string
	for _, row := range panel.Payload["components"].([]any) {
		for _, c := range row.(map[string]any)["components"].([]any) {
			if btn := c.(map[string]any); btn["label"] == "建立農場" {
				create = btn["custom_id"].(string)
			}
		}
	}
	if create == "" {
		t.Fatal("找不到建立農場按鈕")
	}
	click := component("11", create)
	click.Token, click.GuildID, click.Member = "t2", "111111111111111111", member
	click.Message = &discordgo.Message{ID: "m1"}
	b.onInteraction(b.S, &discordgo.InteractionCreate{Interaction: click})
	reqs = f.requests()
	var shown *request
	for i := range reqs {
		if strings.HasPrefix(reqs[i].Path, "interactions/11/t2") || strings.HasPrefix(reqs[i].Path, "webhooks/app/t2") {
			shown = &reqs[i]
		}
	}
	if shown == nil {
		t.Fatalf("%+v", reqs)
	}
	payload := shown.Payload
	if strings.HasSuffix(shown.Path, "/callback") {
		payload = data(*shown)
	}
	text := payload["embeds"].([]any)[0].(map[string]any)["description"].(string)
	if !strings.Contains(text, "農場建立完成") || len(shown.Files) != 1 || shown.Files[0] != "farm.png" {
		t.Fatalf("%s %v", text, shown.Files)
	}
	// 再點一次舊的按鈕：已重繪，舊的 custom_id 失效，不送任何請求（Discord 顯示互動失敗）
	n := len(f.requests())
	stale := component("12", create)
	stale.Token, stale.Member = "t3", member
	b.onInteraction(b.S, &discordgo.InteractionCreate{Interaction: stale})
	if len(f.requests()) != n {
		t.Fatal("舊按鈕不應回應")
	}
	// /help：私密的說明訊息
	help := &discordgo.Interaction{ID: "13", AppID: "app", Token: "t4", Type: discordgo.InteractionApplicationCommand,
		Member: member, Data: discordgo.ApplicationCommandInteractionData{Name: "help"}}
	b.onInteraction(b.S, &discordgo.InteractionCreate{Interaction: help})
	if r := f.last(); typeOf(r) != 4 || data(r)["flags"] != float64(64) || !strings.Contains(data(r)["embeds"].([]any)[0].(map[string]any)["title"].(string), "怎麼玩") {
		t.Fatalf("%+v", r)
	}
}
