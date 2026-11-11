package music

// 音樂的斜線指令與按鈕（對應 Python player.py 的 hybrid commands、PlayerControls、QueueView、on_command_error）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
)

// Command 一次斜線指令（discordio 從互動取出）。
type Command struct {
	Name           string
	Options        map[string]any // 字串、float64（數字）、int64（整數）
	GuildID        string
	ChannelID      string
	UserID         string
	UserName       string // 顯示名稱
	AvatarURL      string
	CanManageGuild bool
}

// Option 指令參數。
type Option struct {
	Name, Description string
	Type              string // string／integer／number
	Required          bool
	Choices           []string
}

// Spec 要註冊的斜線指令。
type Spec struct {
	Name, Description string
	GuildOnly         bool
	Options           []Option
}

// Specs 音樂指令（與 Python 的 COMMANDS 相同順序與說明）。
func Specs() []Spec {
	return []Spec{
		{Name: "join", Description: "讓機器人加入你所在的語音頻道", GuildOnly: true},
		{Name: "play", Description: "播放歌曲（關鍵字、YouTube / Spotify 連結）", GuildOnly: true, Options: []Option{
			{Name: "query", Description: "關鍵字（搜尋 YouTube），或 YouTube / Spotify 的歌曲、播放清單連結", Type: "string", Required: true}}},
		{Name: "nowplaying", Description: "顯示目前播放的歌曲與控制面板", GuildOnly: true},
		{Name: "queue", Description: "顯示目前的播放清單", GuildOnly: true},
		{Name: "skip", Description: "跳過目前播放的歌曲", GuildOnly: true},
		{Name: "pause", Description: "暫停播放", GuildOnly: true},
		{Name: "resume", Description: "繼續播放", GuildOnly: true},
		{Name: "stop", Description: "停止播放並清空待播清單", GuildOnly: true},
		{Name: "loop", Description: "設定循環模式", GuildOnly: true, Options: []Option{
			{Name: "mode", Description: "off = 關閉、song = 單曲循環、queue = 清單循環（不填則切換）", Type: "string", Choices: []string{"off", "song", "queue"}}}},
		{Name: "shuffle", Description: "隨機打亂待播清單", GuildOnly: true},
		{Name: "remove", Description: "從待播清單移除指定歌曲", GuildOnly: true, Options: []Option{
			{Name: "index", Description: "歌曲在待播清單中的編號（用 /queue 查看）", Type: "integer", Required: true}}},
		{Name: "clear", Description: "清空待播清單（不影響目前播放的歌）", GuildOnly: true},
		{Name: "leave", Description: "離開語音頻道並清空播放清單", GuildOnly: true},
		{Name: "volume", Description: "設定音量（例如 50 = 50%，最高 200）", GuildOnly: true, Options: []Option{
			{Name: "percent", Description: "音量百分比，例如 50 代表 50%（0 ~ 200）", Type: "number"}}},
		{Name: "prefix", Description: "設定此伺服器的前綴指令符號", GuildOnly: true, Options: []Option{
			{Name: "new_prefix", Description: "新的前綴符號，例如 ! 或 ?", Type: "string", Required: true}}},
		{Name: "music", Description: "音樂指令說明"},
	}
}

// IsCommand 是否為音樂指令。
func IsCommand(name string) bool {
	for _, s := range Specs() {
		if s.Name == name {
			return true
		}
	}
	return false
}

// reply 回覆指令：還沒回應時直接回應，已 defer 時用 followup（Python ctx.send）。
func reply(ctx context.Context, it ui.Interaction, m ui.Message) error {
	if it.Responded() {
		_, err := it.Followup(ctx, m)
		return err
	}
	return it.SendMessage(ctx, m)
}

func (m *Music) replyEmbed(ctx context.Context, it ui.Interaction, e *ui.Embed, ephemeral bool) {
	if err := reply(ctx, it, ui.Message{Embed: e, Ephemeral: ephemeral}); err != nil {
		m.log.Warn("回覆音樂指令失敗", "type", fmt.Sprintf("%T", err))
	}
}

// checkFail 指令的前置檢查失敗（Python on_command_error 以私密訊息回覆）。
func (m *Music) checkFail(ctx context.Context, it ui.Interaction, msg string) {
	m.replyEmbed(ctx, it, errorEmbed(msg), true)
}

// requireSameChannel 只有和機器人在同一個語音頻道的人可以操作。
func (m *Music) requireSameChannel(c Command) string {
	ch := m.voiceChannel(c.GuildID)
	if ch == "" {
		return "機器人目前不在任何語音頻道中"
	}
	if m.dc.UserVoiceChannel(c.GuildID, c.UserID) != ch {
		return "你需要和機器人在同一個語音頻道才能操作"
	}
	return ""
}

func optString(c Command, name string) (string, bool) {
	v, ok := c.Options[name].(string)
	return v, ok
}

func optFloat(c Command, name string) (float64, bool) {
	switch v := c.Options[name].(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// Handle 處理音樂指令。
func (m *Music) Handle(ctx context.Context, it ui.Interaction, c Command) {
	spec := Spec{}
	for _, s := range Specs() {
		if s.Name == c.Name {
			spec = s
		}
	}
	if spec.GuildOnly && c.GuildID == "" {
		m.checkFail(ctx, it, "這個指令只能在伺服器中使用")
		return
	}
	switch c.Name {
	case "skip", "pause", "resume", "stop", "shuffle", "remove", "clear":
		if msg := m.requireSameChannel(c); msg != "" {
			m.checkFail(ctx, it, msg)
			return
		}
	case "prefix":
		if !c.CanManageGuild {
			m.checkFail(ctx, it, "你沒有權限使用這個指令（需要「管理伺服器」權限）")
			return
		}
	}
	if c.Name == "music" {
		m.help(ctx, it, c)
		return
	}
	g := m.state(c.GuildID)
	if c.Name == "join" || c.Name == "play" {
		_ = it.Defer(ctx, false)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch c.Name {
	case "join":
		ch, ok := m.ensureVoice(ctx, it, c, g)
		if !ok {
			return
		}
		g.textChannel = c.ChannelID
		m.replyEmbed(ctx, it, infoEmbed("🎶 已加入語音頻道：<#"+ch+">"), false)
	case "play":
		q, _ := optString(c, "query")
		m.enqueue(ctx, it, c, g, q)
	case "nowplaying":
		if g.current == nil {
			m.replyEmbed(ctx, it, errorEmbed("目前沒有音樂在播放！"), false)
			return
		}
		g.textChannel = c.ChannelID
		m.clearNP(ctx, g)
		m.sendNowPlaying(ctx, g, it)
	case "queue":
		m.showQueue(ctx, it, c.UserID, g, false)
	case "skip":
		if m.skipCurrent(ctx, g) { // TrackEnd 事件會自動播放下一首
			m.replyEmbed(ctx, it, infoEmbed("⏭️ THE 跳！"), false)
		} else {
			m.replyEmbed(ctx, it, errorEmbed("目前沒有音樂在播放！"), false)
		}
	case "pause":
		if g.isPlaying() && m.pause(ctx, g) == nil {
			m.replyEmbed(ctx, it, infoEmbed("⏸️ 已暫停"), false)
		} else {
			m.replyEmbed(ctx, it, errorEmbed("目前沒有音樂在播放！"), false)
		}
	case "resume":
		if g.isPaused() && m.resume(ctx, g) == nil {
			m.replyEmbed(ctx, it, infoEmbed("▶️ 繼續播放"), false)
		} else {
			m.replyEmbed(ctx, it, errorEmbed("目前沒有暫停中的音樂！"), false)
		}
	case "stop":
		m.stopPlayback(ctx, g)
		m.replyEmbed(ctx, it, infoEmbed("⏹️ 已停止播放並清空待播清單"), false)
	case "loop":
		if mode, ok := optString(c, "mode"); ok && loopLabels[mode] != "" {
			g.loop = mode
		} else {
			g.loop = loopNext[g.loop]
		}
		m.replyEmbed(ctx, it, infoEmbed(fmt.Sprintf("🔁 循環模式：**%s**", loopLabels[g.loop])), false)
	case "shuffle":
		if len(g.queue) < 2 {
			m.replyEmbed(ctx, it, errorEmbed("待播清單的歌曲太少，無法打亂"), false)
			return
		}
		shuffleQueue(g)
		m.replyEmbed(ctx, it, infoEmbed(fmt.Sprintf("🔀 已打亂 %d 首歌曲", len(g.queue))), false)
	case "remove":
		idx, _ := optFloat(c, "index")
		i := int(idx)
		if i < 1 || i > len(g.queue) {
			m.replyEmbed(ctx, it, errorEmbed(fmt.Sprintf("編號要在 1 ~ %d 之間", len(g.queue))), false)
			return
		}
		song := g.queue[i-1]
		g.queue = append(g.queue[:i-1:i-1], g.queue[i:]...)
		if g.prefetched == song {
			g.prefetched = nil
		}
		m.replyEmbed(ctx, it, infoEmbed(fmt.Sprintf("🗑️ 已移除：**%s**", escape(song.Title))), false)
	case "clear":
		n := len(g.queue)
		g.queue, g.prefetched = nil, nil
		m.replyEmbed(ctx, it, infoEmbed(fmt.Sprintf("🧹 已清空 %d 首歌曲", n)), false)
	case "leave":
		if m.dc.BotVoiceChannel(c.GuildID) == "" && !m.connected(c.GuildID) {
			m.replyEmbed(ctx, it, errorEmbed("機器人目前不在任何語音頻道中"), false)
			return
		}
		g.reset()
		m.disconnect(ctx, c.GuildID)
		m.clearNP(ctx, g)
		m.replyEmbed(ctx, it, infoEmbed("👋 已離開語音頻道並清空播放清單！"), false)
	case "volume":
		pct, ok := optFloat(c, "percent")
		if !ok {
			m.replyEmbed(ctx, it, infoEmbed(fmt.Sprintf("🔊 目前音量：%.0f%%", g.volume*100)), false)
			return
		}
		value := pct // 相容舊用法：輸入 0 ~ 2 視為倍率（例如 0.5 = 50%），大於 2 視為百分比
		if pct > 2 {
			value = pct / 100
		}
		m.changeVolume(ctx, g, value)
		m.replyEmbed(ctx, it, infoEmbed(fmt.Sprintf("🔊 音量已設置為 %.0f%%（此伺服器會記住這個設定）", g.volume*100)), false)
	case "prefix":
		p, _ := optString(c, "new_prefix")
		p = strings.TrimSpace(p)
		if p == "" || len([]rune(p)) > 5 || strings.Contains(p, " ") {
			m.replyEmbed(ctx, it, errorEmbed("前綴需為 1 ~ 5 個字元，且不能有空白"), false)
			return
		}
		if err := m.settings.Set(c.GuildID, "prefix", p); err != nil {
			m.log.Warn("儲存前綴失敗", "type", fmt.Sprintf("%T", err))
		}
		m.replyEmbed(ctx, it, infoEmbed(fmt.Sprintf("✅ 此伺服器的前綴已改為 `%s`（斜線指令照常可用）", p)), false)
	}
}

// ensureVoice 確保機器人在使用者所在的語音頻道（Python ensure_voice）；失敗時已回覆。
func (m *Music) ensureVoice(ctx context.Context, it ui.Interaction, c Command, g *guildState) (string, bool) {
	userCh := m.dc.UserVoiceChannel(c.GuildID, c.UserID)
	if userCh == "" {
		m.replyEmbed(ctx, it, errorEmbed("你需要先加入一個語音頻道！"), false)
		return "", false
	}
	if cur := m.voiceChannel(c.GuildID); cur != "" {
		if cur == userCh {
			return cur, true
		}
		if g.busy() {
			m.replyEmbed(ctx, it, errorEmbed(fmt.Sprintf("機器人正在 <#%s> 播放中，請到同一個頻道點歌！", cur)), false)
			return "", false
		}
		if err := m.join(ctx, c.GuildID, userCh); err != nil {
			m.replyEmbed(ctx, it, errorEmbed("加入語音頻道失敗："+err.Error()), false)
			return "", false
		}
		return userCh, true
	}
	if !m.node.Ready() {
		m.replyEmbed(ctx, it, errorEmbed(msgUnavailable), false)
		return "", false
	}
	m.clearStaleVoice(c.GuildID, m.staleWait)
	if err := m.join(ctx, c.GuildID, userCh); err != nil {
		m.log.Warn("加入語音頻道失敗", "guild", c.GuildID, "error", err.Error())
		m.vmu.Lock()
		delete(m.voices, c.GuildID)
		m.vmu.Unlock()
		m.clearStaleVoice(c.GuildID, 0) // 逾時時 Discord 端可能已加入，不留殘留狀態
		m.replyEmbed(ctx, it, errorEmbed("加入語音頻道失敗："+err.Error()), false)
		return "", false
	}
	return userCh, true
}

// clearStaleVoice Discord 認為機器人還在語音頻道、程式卻沒有連線時（斷線殘留），先離開再連。
func (m *Music) clearStaleVoice(guildID string, wait time.Duration) {
	if m.connected(guildID) || m.dc.BotVoiceChannel(guildID) == "" {
		return
	}
	m.log.Warn("清除殘留的語音狀態", "guild", guildID)
	_ = m.dc.LeaveVoice(guildID)
	deadline := time.Now().Add(wait)
	for m.dc.BotVoiceChannel(guildID) != "" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
}

// enqueue 點歌（Python enqueue）。
func (m *Music) enqueue(ctx context.Context, it ui.Interaction, c Command, g *guildState, query string) {
	if _, ok := m.ensureVoice(ctx, it, c, g); !ok {
		return
	}
	g.textChannel = c.ChannelID
	query = strings.Trim(strings.TrimSpace(query), "<>")
	if bilibiliRE.MatchString(query) {
		m.replyEmbed(ctx, it, errorEmbed("目前不支援 B 站，請改用 YouTube 或 Spotify 的關鍵字、連結。"), false)
		return
	}
	var (
		name, playlistURL, cover string
		songs                    []*Song
		err                      error
	)
	errMsg := "未能取得音樂，請檢查網址或關鍵字。"
	if IsSpotify(query) {
		name, playlistURL, cover, songs, err = m.spotifySongs(ctx, query, c.UserID)
	} else {
		name, songs, err = m.getSongs(ctx, query, c.UserID)
		if name != "" {
			playlistURL = query
		}
	}
	if err != nil {
		songs = nil
		if se, ok := err.(SpotifyError); ok {
			errMsg = string(se)
		} else {
			errMsg = msgUnavailable
		}
	}
	if len(songs) == 0 {
		m.replyEmbed(ctx, it, errorEmbed(errMsg), false)
		return
	}
	position := len(g.queue)
	if g.current != nil {
		position++
	}
	g.queue = append(g.queue, songs...)
	m.replyEmbed(ctx, it, addedEmbed(songs, c.UserName, c.AvatarURL, position, name, playlistURL, cover), false)
	if !g.busy() {
		m.playNext(ctx, g)
	} else if len(g.queue) == len(songs) {
		m.prefetchNext(g)
	}
}

// help /music（Python help_command）。
func (m *Music) help(ctx context.Context, it ui.Interaction, c Command) {
	p := m.cfg.Prefix
	if c.GuildID != "" {
		p = m.settings.String(c.GuildID, "prefix", m.cfg.Prefix)
	}
	e := &ui.Embed{Title: "🎵 音樂機器人指令", Color: ColorInfo,
		Description: fmt.Sprintf("所有指令都可以用 `/` 或 `%s` 開頭\n支援 **YouTube** 與 **Spotify**（歌曲、專輯、播放清單、歌手熱門歌曲）", p),
		Footer:      "「正在播放」訊息下方的按鈕也可以直接控制播放"}
	e.AddField("▶️ 播放", fmt.Sprintf("`play <關鍵字或網址>`（`%sp`）點歌\n`nowplaying`（`%snp`）正在播放 + 控制面板\n`queue`（`%sq`）播放清單", p, p, p), false)
	e.AddField("🎛️ 控制", fmt.Sprintf("`pause` / `resume` 暫停 / 繼續\n`skip`（`%ss`）跳過 · `stop` 停止並清空\n`loop [off/song/queue]` 循環 · `shuffle` 隨機\n`remove <編號>` 移除 · `clear` 清空待播\n`volume <0~200>`（`%svol`）音量", p, p), false)
	e.AddField("⚙️ 其他", fmt.Sprintf("`join` 加入語音 · `leave`（`%sdc`）離開\n`prefix <符號>` 修改此伺服器的前綴（需管理伺服器權限）", p), false)
	m.replyEmbed(ctx, it, e, false)
}

// showQueue 播放清單（多頁時附翻頁按鈕，只有下指令的人可以翻頁，2 分鐘後移除按鈕）。
func (m *Music) showQueue(ctx context.Context, it ui.Interaction, userID string, g *guildState, ephemeral bool) {
	pages := queuePages(g)
	if len(pages) == 1 {
		m.replyEmbed(ctx, it, pages[0], ephemeral)
		return
	}
	page := 0
	v := ui.NewView(120 * time.Second)
	prev := &ui.Button{Emoji: "⬅️", Style: ui.Secondary}
	next := &ui.Button{Emoji: "➡️", Style: ui.Secondary}
	update := func() { prev.Disabled, next.Disabled = page == 0, page >= len(pages)-1 }
	update()
	turn := func(step int) ui.Handler {
		return func(ctx context.Context, bit ui.Interaction) error {
			page += step
			update()
			return bit.Show(ctx, ui.Message{Embed: pages[page], View: v})
		}
	}
	prev.OnClick, next.OnClick = turn(-1), turn(1)
	v.Add(prev)
	v.Add(next)
	v.Check = func(ctx context.Context, bit ui.Interaction) bool {
		if bit.UserID() != userID {
			_ = bit.SendMessage(ctx, ui.Message{Content: ptr("只有下指令的人可以翻頁喔！"), Ephemeral: true})
			return false
		}
		return true
	}
	if !ephemeral {
		v.OnTimeout = func(ctx context.Context) { _ = it.EditOriginal(ctx, ui.Message{View: ui.NewView(0)}) }
	}
	if err := reply(ctx, it, ui.Message{Embed: pages[0], View: v, Ephemeral: ephemeral}); err != nil {
		m.log.Warn("回覆播放清單失敗", "type", fmt.Sprintf("%T", err))
	}
}

// controls 「正在播放」訊息下方的控制按鈕（Python PlayerControls；沒有逾時，換歌時移除）。
func (m *Music) controls(g *guildState) *ui.View {
	v := ui.NewView(0)
	guildID := g.guildID
	refresh := func(ctx context.Context, it ui.Interaction) error {
		embed := m.nowPlayingEmbed(g)
		if g.np != nil && g.np.view == v {
			g.np.embed = embed
		}
		m.syncControls(v, g)
		return it.Show(ctx, ui.Message{Embed: embed, View: v})
	}
	locked := func(fn func(ctx context.Context, it ui.Interaction) error) ui.Handler {
		return func(ctx context.Context, it ui.Interaction) error {
			g.mu.Lock()
			defer g.mu.Unlock()
			return fn(ctx, it)
		}
	}
	add := func(emoji, label string, style ui.Style, row int, fn func(ctx context.Context, it ui.Interaction) error) *ui.Button {
		b := &ui.Button{Emoji: emoji, Label: label, Style: style, Row: row, OnClick: locked(fn)}
		v.Add(b)
		return b
	}
	add("⏸️", "", ui.Secondary, 0, func(ctx context.Context, it ui.Interaction) error {
		if g.isPaused() {
			_ = m.resume(ctx, g)
		} else if g.isPlaying() {
			_ = m.pause(ctx, g)
		}
		return refresh(ctx, it)
	})
	add("⏭️", "", ui.Secondary, 0, func(ctx context.Context, it ui.Interaction) error {
		m.skipCurrent(ctx, g)
		if err := it.SendMessage(ctx, ui.Message{Embed: infoEmbed(fmt.Sprintf("⏭️ <@%s> 跳過了這首歌", it.UserID()))}); err != nil {
			return err
		}
		time.AfterFunc(m.deleteAfter, func() { _ = it.DeleteOriginal(context.Background()) })
		return nil
	})
	add("⏹️", "", ui.Danger, 0, func(ctx context.Context, it ui.Interaction) error {
		m.stopPlayback(ctx, g)
		return it.SendMessage(ctx, ui.Message{Embed: infoEmbed(fmt.Sprintf("⏹️ <@%s> 停止播放並清空了待播清單", it.UserID()))})
	})
	add("➡️", "", ui.Secondary, 0, func(ctx context.Context, it ui.Interaction) error {
		g.loop = loopNext[g.loop]
		return refresh(ctx, it)
	})
	add("🔀", "", ui.Secondary, 0, func(ctx context.Context, it ui.Interaction) error {
		shuffleQueue(g)
		return refresh(ctx, it)
	})
	add("🔉", "", ui.Secondary, 1, func(ctx context.Context, it ui.Interaction) error {
		m.changeVolume(ctx, g, g.volume-0.05)
		return refresh(ctx, it)
	})
	add("🔊", "", ui.Secondary, 1, func(ctx context.Context, it ui.Interaction) error {
		m.changeVolume(ctx, g, g.volume+0.05)
		return refresh(ctx, it)
	})
	add("🔄", "", ui.Secondary, 1, refresh)
	add("📜", "清單", ui.Secondary, 1, func(ctx context.Context, it ui.Interaction) error {
		m.showQueue(ctx, it, it.UserID(), g, true)
		return nil
	})
	v.Check = func(ctx context.Context, it ui.Interaction) bool {
		ch := m.voiceChannel(guildID)
		if ch == "" || m.dc.UserVoiceChannel(guildID, it.UserID()) != ch {
			_ = it.SendMessage(ctx, ui.Message{Content: ptr("你需要和機器人在同一個語音頻道才能操作喔！"), Ephemeral: true})
			return false
		}
		return true
	}
	m.syncControls(v, g)
	return v
}

// syncControls 暫停／循環按鈕的圖示依目前狀態更新。
func (m *Music) syncControls(v *ui.View, g *guildState) {
	buttons := v.Buttons()
	if len(buttons) < 4 {
		return
	}
	pause, loop := buttons[0], buttons[3]
	if g.isPaused() {
		pause.Emoji, pause.Style = "▶️", ui.Success
	} else {
		pause.Emoji, pause.Style = "⏸️", ui.Secondary
	}
	loop.Emoji = map[string]string{"off": "➡️", "song": "🔂", "queue": "🔁"}[g.loop]
	loop.Style = ui.Primary
	if g.loop == "off" {
		loop.Style = ui.Secondary
	}
}
