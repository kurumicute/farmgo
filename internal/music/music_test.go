package music

// 音樂的測試（Python 的音樂只有 test_music.py 的指令註冊與 Intent 測試；這裡另外以假的 Discord 與 Lavalink
// 測試播放流程，不連到真實 Discord 或 Lavalink）。

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/bot/ui/uitest"
)

const (
	guild = "100"
	text  = "200"
	vc1   = "300"
	vc2   = "301"
	alice = "1"
	bob   = "2"
	botID = "999"
)

// fakeDiscord 假的 Discord：記錄送出的頻道訊息；JoinVoice 時模擬 Discord 送回語音狀態與語音伺服器資料。
type fakeDiscord struct {
	mu      sync.Mutex
	m       *Music
	users   map[string]string // 使用者 → 語音頻道
	bot     string
	humans  map[string]int
	sent    []ui.Message
	edits   []ui.Message
	noVoice bool // 模擬 Discord 沒有回應（逾時）
}

func (d *fakeDiscord) JoinVoice(guildID, channelID string) error {
	d.mu.Lock()
	before := d.bot
	d.bot = channelID
	noVoice := d.noVoice
	d.mu.Unlock()
	if !noVoice {
		go func() {
			d.m.OnVoiceState(guildID, botID, before, channelID, "sess")
			d.m.OnVoiceServer(guildID, "tok", "endpoint")
		}()
	}
	return nil
}

func (d *fakeDiscord) LeaveVoice(string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bot = ""
	return nil
}

func (d *fakeDiscord) UserVoiceChannel(_, user string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.users[user]
}

func (d *fakeDiscord) BotVoiceChannel(string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.bot
}

func (d *fakeDiscord) ChannelHumans(_, ch string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.humans[ch]
}

func (d *fakeDiscord) Guild(string) (string, string) {
	return "測試伺服器", "https://cdn/icon.png"
}

func (d *fakeDiscord) Send(_ context.Context, _ string, m ui.Message) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.View != nil {
		d.m.reg.Bind(m.View)
	}
	d.sent = append(d.sent, m)
	return fmt.Sprint(len(d.sent)), nil
}

func (d *fakeDiscord) Edit(_ context.Context, _, _ string, m ui.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edits = append(d.edits, m)
	return nil
}

func (d *fakeDiscord) lastSent(t *testing.T) ui.Message {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sent) == 0 {
		t.Fatal("沒有送出訊息")
	}
	return d.sent[len(d.sent)-1]
}

// fakeNode 假的 Lavalink：關鍵字回傳一首歌，記錄播放、停止等呼叫。
type fakeNode struct {
	mu      sync.Mutex
	ready   bool
	loads   []string
	results map[string]LoadResult
	loadErr error
	calls   []string
	played  []Track
}

func track(id, title string, seconds int) Track {
	u := "https://youtu.be/" + id
	return Track{Encoded: "enc-" + id, Info: TrackInfo{Identifier: id, Title: title, Author: "歌手", Length: int64(seconds) * 1000, URI: &u}}
}

func (n *fakeNode) Ready() bool { return n.ready }

func (n *fakeNode) Load(_ context.Context, id string) (LoadResult, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.loads = append(n.loads, id)
	if n.loadErr != nil {
		return LoadResult{}, n.loadErr
	}
	if r, ok := n.results[id]; ok {
		return r, nil
	}
	q := strings.TrimPrefix(id, "ytsearch:")
	return LoadResult{Tracks: []Track{track(q, q, 200)}}, nil
}

func (n *fakeNode) call(s string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, s)
}

func (n *fakeNode) Play(_ context.Context, _ string, t Track, vol int) error {
	n.mu.Lock()
	n.played = append(n.played, t)
	n.mu.Unlock()
	n.call(fmt.Sprintf("play %s vol=%d", t.Info.Identifier, vol))
	return nil
}
func (n *fakeNode) Stop(context.Context, string) error { n.call("stop"); return nil }
func (n *fakeNode) Pause(_ context.Context, _ string, p bool) error {
	n.call(fmt.Sprintf("pause %v", p))
	return nil
}
func (n *fakeNode) Volume(_ context.Context, _ string, v int) error {
	n.call(fmt.Sprintf("volume %d", v))
	return nil
}
func (n *fakeNode) Voice(_ context.Context, _ string, v VoiceState) error {
	n.call("voice " + v.ChannelID)
	return nil
}
func (n *fakeNode) Destroy(context.Context, string) error { n.call("destroy"); return nil }

func (n *fakeNode) has(s string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.calls {
		if c == s {
			return true
		}
	}
	return false
}

type env struct {
	t    *testing.T
	m    *Music
	dc   *fakeDiscord
	node *fakeNode
	reg  *ui.Registry
	log  *bytes.Buffer
	file string
}

func setup(t *testing.T) *env {
	t.Helper()
	reg := ui.NewRegistry()
	reg.DirectReply = -1
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	dc := &fakeDiscord{users: map[string]string{alice: vc1}, humans: map[string]int{vc1: 1}}
	node := &fakeNode{ready: true, results: map[string]LoadResult{}}
	file := filepath.Join(t.TempDir(), "data", "music_guild_settings.json")
	m := New(Config{Volume: 0.1, PlaylistLimit: 20, Prefix: "!", IdleTimeout: time.Hour}, dc, node, reg, LoadSettings(file), botID, log)
	m.joinTimeout, m.staleWait, m.deleteAfter = time.Second, 0, time.Millisecond
	dc.m = m
	return &env{t: t, m: m, dc: dc, node: node, reg: reg, log: &buf, file: file}
}

func (e *env) cmd(user, name string, opts map[string]any) *uitest.Interaction {
	e.t.Helper()
	it := uitest.New(e.t, e.reg, user, guild)
	it.Name = "玩家" + user
	if opts == nil {
		opts = map[string]any{}
	}
	e.m.Handle(context.Background(), it, Command{Name: name, Options: opts, GuildID: guild, ChannelID: text, UserID: user,
		UserName: "玩家" + user, CanManageGuild: user == alice})
	return it
}

// fullText 訊息文字，包含 Embed 的作者列與頁尾（音樂的狀態文字放在這兩處）。
func fullText(m ui.Message) string {
	parts := []string{uitest.Entry{Message: m}.Text()}
	if m.Embed != nil {
		parts = append(parts, m.Embed.AuthorName, m.Embed.Footer)
	}
	return strings.Join(parts, "\n")
}

func lastText(it *uitest.Interaction) string { return fullText(it.Last().Message) }

func allText(it *uitest.Interaction) string {
	var parts []string
	for _, en := range it.Entries() {
		parts = append(parts, fullText(en.Message))
	}
	return strings.Join(parts, "\n")
}

func (e *env) state() *guildState { return e.m.state(guild) }

func (e *env) end(t Track, reason string) {
	tr := t
	e.m.OnEvent(Event{Type: "TrackEndEvent", GuildID: guild, Track: &tr, Reason: reason})
}

func TestPlayJoinsVoiceAndQueues(t *testing.T) {
	e := setup(t)
	it := e.cmd(alice, "play", map[string]any{"query": "第一首"})
	if k := it.Kinds(); k[0] != "defer" {
		t.Fatal(k)
	}
	if !strings.Contains(lastText(it), "已加入待播清單") || !strings.Contains(lastText(it), "即將播放") {
		t.Fatal(lastText(it))
	}
	if !e.node.has("voice "+vc1) || !e.node.has("play 第一首 vol=10") || e.dc.BotVoiceChannel(guild) != vc1 {
		t.Fatal(e.node.calls)
	}
	np := e.dc.lastSent(t) // 「正在播放」訊息附控制按鈕
	if np.Embed.AuthorName != "🎵 正在播放" || len(np.View.Buttons()) != 9 || np.Embed.Footer != "測試伺服器" {
		t.Fatalf("%+v", np.Embed)
	}
	it2 := e.cmd(alice, "play", map[string]any{"query": "第二首"})
	if !strings.Contains(lastText(it2), "第 2 首") {
		t.Fatal(lastText(it2))
	}
	g := e.state()
	if len(g.queue) != 1 || g.current.Title != "第一首" {
		t.Fatal(len(g.queue))
	}
	// 播完：播放下一首，上一則「正在播放」標示已播放並移除按鈕
	e.end(track("第一首", "第一首", 200), "finished")
	if !e.node.has("play 第二首 vol=10") || g.current.Title != "第二首" || len(g.queue) != 0 {
		t.Fatal(e.node.calls)
	}
	if len(e.dc.edits) == 0 || e.dc.edits[0].Embed.AuthorName != "✔️ 已播放" || len(e.dc.edits[0].View.Items()) != 0 {
		t.Fatalf("%+v", e.dc.edits)
	}
	// 換歌（replaced）與不是目前歌曲的結束事件不處理
	e.end(track("第一首", "第一首", 200), "finished")
	e.end(track("第二首", "第二首", 200), "replaced")
	if g.current == nil || g.current.Title != "第二首" {
		t.Fatal("不應換歌")
	}
}

func TestLoopSkipAndStop(t *testing.T) {
	e := setup(t)
	e.cmd(alice, "play", map[string]any{"query": "A"})
	e.cmd(alice, "play", map[string]any{"query": "B"})
	g := e.state()
	e.cmd(alice, "loop", map[string]any{"mode": "song"})
	e.end(track("A", "A", 1), "finished") // 單曲循環：再播一次 A
	if g.current.Title != "A" || len(g.queue) != 1 {
		t.Fatal(g.current.Title, len(g.queue))
	}
	it := e.cmd(alice, "skip", nil) // 跳過：單曲循環也不重播
	if !strings.Contains(lastText(it), "THE 跳") || !e.node.has("stop") {
		t.Fatal(lastText(it))
	}
	e.end(track("A", "A", 1), "stopped")
	if g.current.Title != "B" || len(g.queue) != 0 {
		t.Fatal(g.current.Title)
	}
	e.cmd(alice, "loop", map[string]any{"mode": "queue"})
	e.end(track("B", "B", 1), "finished") // 清單循環：B 放回清單尾端後再播
	if g.current.Title != "B" {
		t.Fatal(g.current.Title)
	}
	it = e.cmd(alice, "loop", nil) // 不填：切換（queue → off）
	if g.loop != "off" || !strings.Contains(lastText(it), "關閉") {
		t.Fatal(g.loop)
	}
	it = e.cmd(alice, "stop", nil)
	if !strings.Contains(lastText(it), "已停止播放") || len(g.queue) != 0 {
		t.Fatal(lastText(it))
	}
	e.end(track("B", "B", 1), "stopped")
	if g.current != nil || g.idle == nil { // 清單空了：開始閒置計時
		t.Fatal("應進入閒置")
	}
}

func TestSameChannelRequiredAndPermissions(t *testing.T) {
	e := setup(t)
	it := e.cmd(alice, "skip", nil)
	if !it.Last().Message.Ephemeral || !strings.Contains(lastText(it), "機器人目前不在任何語音頻道中") {
		t.Fatal(lastText(it))
	}
	e.cmd(alice, "play", map[string]any{"query": "A"})
	it = e.cmd(bob, "skip", nil) // bob 不在語音頻道
	if !it.Last().Message.Ephemeral || !strings.Contains(lastText(it), "同一個語音頻道") {
		t.Fatal(lastText(it))
	}
	it = e.cmd(bob, "prefix", map[string]any{"new_prefix": "?"})
	if !strings.Contains(lastText(it), "管理伺服器") {
		t.Fatal(lastText(it))
	}
	it = e.cmd(alice, "prefix", map[string]any{"new_prefix": "too long!"})
	if !strings.Contains(lastText(it), "1 ~ 5 個字元") {
		t.Fatal(lastText(it))
	}
	e.cmd(alice, "prefix", map[string]any{"new_prefix": "?"})
	it = e.cmd(bob, "music", nil)
	if !strings.Contains(lastText(it), "`?p`") {
		t.Fatal(lastText(it))
	}
	dm := uitest.New(t, e.reg, alice, "")
	e.m.Handle(context.Background(), dm, Command{Name: "play", Options: map[string]any{"query": "x"}, UserID: alice})
	if !strings.Contains(lastText(dm), "只能在伺服器中使用") {
		t.Fatal(lastText(dm))
	}
	it = e.cmd(bob, "play", map[string]any{"query": "x"})
	if !strings.Contains(lastText(it), "你需要先加入一個語音頻道") {
		t.Fatal(lastText(it))
	}
	e.dc.users[bob] = vc2
	it = e.cmd(bob, "play", map[string]any{"query": "x"}) // 機器人正在別的頻道播放
	if !strings.Contains(lastText(it), "播放中，請到同一個頻道點歌") {
		t.Fatal(lastText(it))
	}
}

func TestVolumeIsSavedPerGuild(t *testing.T) {
	e := setup(t)
	e.cmd(alice, "play", map[string]any{"query": "A"})
	it := e.cmd(alice, "volume", map[string]any{"percent": float64(50)})
	if !strings.Contains(lastText(it), "50%") || !e.node.has("volume 50") {
		t.Fatal(lastText(it), e.node.calls)
	}
	e.cmd(alice, "volume", map[string]any{"percent": 0.3}) // 0 ~ 2 視為倍率
	if e.state().volume != 0.3 {
		t.Fatal(e.state().volume)
	}
	e.cmd(alice, "volume", map[string]any{"percent": float64(900)}) // 上限 200%
	if e.state().volume != 2 {
		t.Fatal(e.state().volume)
	}
	if v := LoadSettings(e.file).Float(guild, "volume", 0); v != 2 {
		t.Fatal(v)
	}
	it = e.cmd(alice, "volume", nil)
	if !strings.Contains(lastText(it), "目前音量：200%") {
		t.Fatal(lastText(it))
	}
}

func TestQueueRemoveClearShuffle(t *testing.T) {
	e := setup(t)
	for _, q := range []string{"A", "B", "C", "D"} {
		e.cmd(alice, "play", map[string]any{"query": q})
	}
	g := e.state()
	it := e.cmd(alice, "queue", nil)
	if !strings.Contains(lastText(it), "正在播放") || !strings.Contains(lastText(it), "`3.`") {
		t.Fatal(lastText(it))
	}
	it = e.cmd(alice, "remove", map[string]any{"index": int64(2)})
	if !strings.Contains(lastText(it), "已移除：**C**") || len(g.queue) != 2 {
		t.Fatal(lastText(it))
	}
	it = e.cmd(alice, "remove", map[string]any{"index": int64(9)})
	if !strings.Contains(lastText(it), "編號要在 1 ~ 2 之間") {
		t.Fatal(lastText(it))
	}
	it = e.cmd(alice, "shuffle", nil)
	if !strings.Contains(lastText(it), "已打亂 2 首") {
		t.Fatal(lastText(it))
	}
	it = e.cmd(alice, "clear", nil)
	if !strings.Contains(lastText(it), "已清空 2 首") || len(g.queue) != 0 {
		t.Fatal(lastText(it))
	}
	it = e.cmd(alice, "shuffle", nil)
	if !strings.Contains(lastText(it), "太少") {
		t.Fatal(lastText(it))
	}
}

func TestQueuePagesHaveButtonsOnlyForAuthor(t *testing.T) {
	e := setup(t)
	for i := range 15 {
		e.cmd(alice, "play", map[string]any{"query": fmt.Sprint("歌", i)})
	}
	it := e.cmd(alice, "queue", nil)
	v := it.Last().Message.View
	if v == nil || len(v.Buttons()) != 2 || !v.Buttons()[0].Disabled || v.Buttons()[1].Disabled {
		t.Fatal("多頁時應有翻頁按鈕")
	}
	other := uitest.New(t, e.reg, bob, guild)
	e.reg.DispatchComponent(context.Background(), other, v.Buttons()[1].CustomID(), nil)
	if !strings.Contains(allText(other), "只有下指令的人可以翻頁") {
		t.Fatal(allText(other))
	}
	mine := uitest.New(t, e.reg, alice, guild)
	e.reg.DispatchComponent(context.Background(), mine, v.Buttons()[1].CustomID(), nil)
	if !strings.Contains(allText(mine), "第 2/2 頁") {
		t.Fatal(allText(mine))
	}
}

func TestControlButtons(t *testing.T) {
	e := setup(t)
	e.cmd(alice, "play", map[string]any{"query": "A"})
	e.cmd(alice, "play", map[string]any{"query": "B"})
	view := e.dc.lastSent(t).View
	click := func(user string, i int) *uitest.Interaction {
		it := uitest.New(t, e.reg, user, guild)
		if !e.reg.DispatchComponent(context.Background(), it, view.Buttons()[i].CustomID(), nil) {
			t.Fatal("按鈕沒有登記")
		}
		return it
	}
	if it := click(bob, 0); !strings.Contains(allText(it), "同一個語音頻道") { // 不在同一個頻道不能操作
		t.Fatal(allText(it))
	}
	it := click(alice, 0) // 暫停
	if !e.node.has("pause true") || !strings.Contains(allText(it), "⏸️ 已暫停") || view.Buttons()[0].Emoji != "▶️" {
		t.Fatal(allText(it))
	}
	click(alice, 0) // 繼續
	if !e.node.has("pause false") || view.Buttons()[0].Emoji != "⏸️" {
		t.Fatal(e.node.calls)
	}
	click(alice, 3) // 循環 off → song
	if e.state().loop != "song" || view.Buttons()[3].Emoji != "🔂" {
		t.Fatal(e.state().loop)
	}
	click(alice, 6) // 音量 +5%
	if e.state().volume != 0.15 {
		t.Fatal(e.state().volume)
	}
	it = click(alice, 8) // 清單（私密）
	if !it.Last().Message.Ephemeral || !strings.Contains(allText(it), "待播清單") {
		t.Fatal(allText(it))
	}
	it = click(alice, 1) // 跳過：公開訊息，之後自動刪除
	if !strings.Contains(allText(it), "跳過了這首歌") || !e.node.has("stop") {
		t.Fatal(allText(it))
	}
	time.Sleep(50 * time.Millisecond)
	if k := it.Kinds(); k[len(k)-1] != "delete" {
		t.Fatal(k)
	}
}

func TestIdleAndAloneDisconnect(t *testing.T) {
	e := setup(t)
	e.m.cfg.IdleTimeout = 20 * time.Millisecond
	e.cmd(alice, "play", map[string]any{"query": "A"})
	e.end(track("A", "A", 1), "finished")
	time.Sleep(100 * time.Millisecond)
	if !e.node.has("destroy") || e.dc.BotVoiceChannel(guild) != "" || !strings.Contains(e.dc.lastSent(t).Embed.Description, "閒置過久") {
		t.Fatal(e.node.calls)
	}
	// 頻道只剩機器人：一段時間後自動離開
	e2 := setup(t)
	e2.m.aloneDelay = 20 * time.Millisecond
	e2.cmd(alice, "play", map[string]any{"query": "A"})
	e2.dc.mu.Lock()
	e2.dc.humans[vc1] = 0
	e2.dc.mu.Unlock()
	e2.m.OnVoiceState(guild, alice, vc1, "", "")
	time.Sleep(100 * time.Millisecond)
	g2 := e2.state()
	g2.mu.Lock()
	cur := g2.current
	g2.mu.Unlock()
	if cur != nil || !strings.Contains(e2.dc.lastSent(t).Embed.Description, "語音頻道沒有人了") {
		t.Fatal(e2.dc.lastSent(t).Embed.Description)
	}
}

func TestBotKickedResetsState(t *testing.T) {
	e := setup(t)
	e.cmd(alice, "play", map[string]any{"query": "A"})
	e.cmd(alice, "play", map[string]any{"query": "B"})
	e.dc.mu.Lock()
	e.dc.bot = "" // Discord 端已不在頻道
	e.dc.mu.Unlock()
	e.m.OnVoiceState(guild, botID, vc1, "", "")
	g := e.state()
	if g.current != nil || len(g.queue) != 0 || e.m.connected(guild) || !e.node.has("destroy") {
		t.Fatal("被踢出時應清除狀態")
	}
	it := e.cmd(alice, "leave", nil)
	if !strings.Contains(lastText(it), "不在任何語音頻道") {
		t.Fatal(lastText(it))
	}
}

func TestLavalinkUnavailableAndLoadFailures(t *testing.T) {
	e := setup(t)
	e.node.ready = false
	it := e.cmd(alice, "play", map[string]any{"query": "A"})
	if !strings.Contains(lastText(it), "Lavalink") {
		t.Fatal(lastText(it))
	}
	e.node.ready = true
	e.node.loadErr = LoadError{"boom"}
	it = e.cmd(alice, "play", map[string]any{"query": "A"})
	if !strings.Contains(lastText(it), "未能取得音樂") || !strings.Contains(e.log.String(), "Lavalink 解析失敗") {
		t.Fatal(lastText(it))
	}
	e.node.loadErr = ErrUnavailable
	it = e.cmd(alice, "play", map[string]any{"query": "A"})
	if !strings.Contains(lastText(it), "Lavalink") {
		t.Fatal(lastText(it))
	}
	e.node.loadErr = nil
	it = e.cmd(alice, "play", map[string]any{"query": "https://www.bilibili.com/video/BV1"})
	if !strings.Contains(lastText(it), "不支援 B 站") {
		t.Fatal(lastText(it))
	}
}

func TestJoinTimeoutClearsStaleVoice(t *testing.T) {
	e := setup(t)
	e.dc.noVoice = true
	e.m.joinTimeout = 30 * time.Millisecond
	it := e.cmd(alice, "join", nil)
	if !strings.Contains(lastText(it), "加入語音頻道失敗") || e.dc.BotVoiceChannel(guild) != "" {
		t.Fatal(lastText(it))
	}
	e.dc.noVoice = false
	it = e.cmd(alice, "join", nil)
	if !strings.Contains(lastText(it), "已加入語音頻道：<#"+vc1+">") {
		t.Fatal(lastText(it))
	}
}

func TestPlaylistAndSpotify(t *testing.T) {
	e := setup(t)
	var tracks []Track
	for i := range 30 {
		tracks = append(tracks, track(fmt.Sprint("t", i), fmt.Sprint("歌", i), 60))
	}
	list := "https://www.youtube.com/playlist?list=PL1"
	e.node.results[list] = LoadResult{Playlist: &Playlist{Name: "我的清單", Selected: -1, Tracks: tracks}}
	single := "https://www.youtube.com/watch?v=t3"
	e.node.results[single] = LoadResult{Playlist: &Playlist{Name: "混合清單", Selected: 3, Tracks: tracks}}
	it := e.cmd(alice, "play", map[string]any{"query": "<" + list + ">"})
	if !strings.Contains(lastText(it), "已加入 20 首歌曲") || !strings.Contains(lastText(it), "我的清單") { // 最多 MUSIC_PLAYLIST_LIMIT 首
		t.Fatal(lastText(it))
	}
	it = e.cmd(alice, "play", map[string]any{"query": single}) // 沒有 list= 的網址只播選中的那首
	if !strings.Contains(lastText(it), "歌3") || !strings.Contains(lastText(it), "已加入待播清單") {
		t.Fatal(lastText(it))
	}
	e.m.resolveSpotify = func(context.Context, string, int) (SpotifyResult, error) {
		return SpotifyResult{Kind: "album", Name: "專輯", PageURL: "https://open.spotify.com/album/x",
			Tracks: []SpotifyTrack{{Title: "Song", Artist: "Singer", URL: "https://open.spotify.com/track/1"}}}, nil
	}
	e.cmd(alice, "play", map[string]any{"query": "https://open.spotify.com/album/x"})
	g := e.state()
	last := g.queue[len(g.queue)-1]
	if last.Source != "spotify" || last.Title != "Singer - Song" || last.SearchQuery != "Song Singer audio" || last.Track != nil {
		t.Fatalf("%+v", last)
	}
	e.m.resolveSpotify = func(context.Context, string, int) (SpotifyResult, error) {
		return SpotifyResult{}, SpotifyError("這個 Spotify 連結裡沒有可播放的歌曲")
	}
	it = e.cmd(alice, "play", map[string]any{"query": "https://open.spotify.com/track/y"})
	if !strings.Contains(lastText(it), "沒有可播放的歌曲") {
		t.Fatal(lastText(it))
	}
}

func TestLavalinkReconnectStopsPlayback(t *testing.T) {
	e := setup(t)
	e.cmd(alice, "play", map[string]any{"query": "A"})
	e.m.OnReady("new", false, true)
	if e.state().current != nil || e.m.connected(guild) || !strings.Contains(e.dc.lastSent(t).Embed.Description, "重新連線") {
		t.Fatal("Lavalink 重新連線後應清除播放")
	}
}

func TestFormatting(t *testing.T) {
	if fmtDuration(intPtr(65)) != "1:05" || fmtDuration(intPtr(3725)) != "1:02:05" || fmtDuration(nil) != "直播/未知" {
		t.Fatal("時間格式")
	}
	if progressBar(0, nil) != "🔴 LIVE" || progressBar(50, intPtr(100)) != strings.Repeat("▬", 8)+"🔘"+strings.Repeat("▬", 7) {
		t.Fatal(progressBar(50, intPtr(100)))
	}
	if short("一二三四五", 4) != "一二三…" || short("一二", 4) != "一二" {
		t.Fatal("截斷")
	}
	for in, want := range map[string]string{
		"a_b*c~d|e`f":                   `a\_b\*c\~d\|e\` + "`" + `f`,
		`back\slash`:                    `back\\slash`,
		"> 引言":                          `\> 引言`,
		"https://x.com/a_b_c":           "https://x.com/a_b_c", // 網址不跳脫
		"# 標題":                          `\# 標題`,
		"[連結](https://example.com/a_b)": `\[連結](https://example.com/a_b)`,
	} {
		if got := escape(in); got != want {
			t.Fatalf("%q → %q，應為 %q", in, got, want)
		}
	}
}
