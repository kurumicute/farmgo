// Package music 音樂播放（對應 Python app/music/player.py，原 YTDiscordBot 移植）：YouTube／Spotify 點歌、待播清單、
// 循環、控制按鈕與閒置自動離開。音訊由 Lavalink 解析與播放，Bot 只控制。
//
//   - 指令只有斜線指令（Python 的前綴指令需要 Message Content 特權 Intent，正式環境沒有開啟，不移植）。
//   - 每個伺服器的播放狀態在記憶體中；音量與前綴存在 data/music_guild_settings.json（與 Python 相同格式）。
//   - 與 Discord、Lavalink 的互動經 Discord 與 Node 介面（discordio、Client 實作），測試用假的實作。
//   - 同一個伺服器的操作（指令、按鈕、Lavalink 事件、計時器）以伺服器的鎖依序執行（Python 在單一事件迴圈中）。
package music

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"time"

	"farmgm/internal/bot/ui"
)

// 顏色與標籤。
const (
	ColorYouTube = 0xFF0033
	ColorSpotify = 0x1DB954
	ColorInfo    = 0x5865F2
	ColorError   = 0xED4245
)

var (
	loopLabels = map[string]string{"off": "關閉", "song": "單曲循環", "queue": "清單循環"}
	loopNext   = map[string]string{"off": "song", "song": "queue", "queue": "off"}
	sources    = map[string]struct {
		name, icon string
		color      int
	}{"youtube": {"YouTube", "🔴", ColorYouTube}, "spotify": {"Spotify", "🟢", ColorSpotify}}
	// B 站已停止支援：貼上 B 站連結時直接提示
	bilibiliRE = regexp.MustCompile(`(?i)^https?://(?:[\w-]+\.)*(?:bilibili\.com|b23\.tv|bili2233\.cn)/`)
)

const msgUnavailable = "音樂伺服器（Lavalink）目前無法連線，請稍後再試。"

// Discord 音樂需要的 Discord 操作（discordio 實作）。
type Discord interface {
	JoinVoice(guildID, channelID string) error // 送出加入語音（self_deaf）
	LeaveVoice(guildID string) error
	UserVoiceChannel(guildID, userID string) string // 使用者所在的語音頻道（"" = 不在）
	BotVoiceChannel(guildID string) string          // Discord 認為 Bot 所在的語音頻道
	ChannelHumans(guildID, channelID string) int    // 頻道內非 Bot 的人數
	Guild(guildID string) (name, iconURL string)
	Send(ctx context.Context, channelID string, m ui.Message) (string, error)
	Edit(ctx context.Context, channelID, messageID string, m ui.Message) error
}

// Node Lavalink 節點（Client 實作）。
type Node interface {
	Ready() bool
	Load(ctx context.Context, identifier string) (LoadResult, error)
	Play(ctx context.Context, guildID string, t Track, volume int) error
	Stop(ctx context.Context, guildID string) error
	Pause(ctx context.Context, guildID string, paused bool) error
	Volume(ctx context.Context, guildID string, volume int) error
	Voice(ctx context.Context, guildID string, v VoiceState) error
	Destroy(ctx context.Context, guildID string) error
}

// Config 設定（.env 的 MUSIC_*）。
type Config struct {
	Volume        float64 // 預設音量（1.0 = 100%）
	PlaylistLimit int
	Prefix        string
	IdleTimeout   time.Duration
}

// Song 一首歌。
type Song struct {
	Title       string
	URL         string // 影片網址（Spotify 歌曲播放前才到 YouTube 搜尋）
	RequesterID string
	Duration    *int // 秒；直播或未知為 nil
	Thumbnail   string
	Author      string
	Source      string // youtube／spotify
	PageURL     string
	SearchQuery string
	Track       *Track
}

func (s *Song) link() string {
	if s.PageURL != "" {
		return s.PageURL
	}
	return s.URL
}

func (s *Song) source() (name, icon string, color int) {
	src := sources[s.Source]
	return src.name, src.icon, src.color
}

func songFromTrack(t Track, requester string) *Song {
	title := t.Info.Title
	uri := deref(t.Info.URI)
	if title == "" {
		title = uri
	}
	if title == "" {
		title = "未知歌曲"
	}
	var dur *int
	if !t.Info.IsStream {
		dur = intPtr(int(t.Info.Length / 1000))
	}
	tr := t
	return &Song{Title: title, URL: uri, RequesterID: requester, Duration: dur, Thumbnail: deref(t.Info.ArtworkURL),
		Author: t.Info.Author, Source: "youtube", Track: &tr}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// npMessage 「正在播放」訊息（附控制按鈕）。
type npMessage struct {
	embed *ui.Embed
	view  *ui.View
	edit  func(ctx context.Context, m ui.Message) error
}

// voiceConn Bot 在某伺服器的語音連線（Discord 傳來的資料，轉給 Lavalink）。
type voiceConn struct {
	channelID, sessionID, token, endpoint string
	connected                             bool
	ready                                 chan struct{}
}

// guildState 一個伺服器的播放狀態（Python GuildState）。
type guildState struct {
	mu            sync.Mutex
	guildID       string
	queue         []*Song
	current       *Song
	volume        float64
	loop          string
	textChannel   string
	prefetched    *Song // 已預先解析的下一首
	idle          *time.Timer
	skipRequested bool
	np            *npMessage
	startedAt     time.Time
	pausedAt      *time.Time
	playing       *Track // Lavalink 播放器目前的音軌（Python vc.current）
	paused        bool
}

// Music 音樂功能。
type Music struct {
	cfg      Config
	dc       Discord
	node     Node
	reg      *ui.Registry
	settings *Settings
	log      *slog.Logger
	botID    string

	now            func() time.Time
	resolveSpotify func(ctx context.Context, link string, limit int) (SpotifyResult, error)
	joinTimeout    time.Duration
	staleWait      time.Duration
	aloneDelay     time.Duration
	deleteAfter    time.Duration

	mu     sync.Mutex
	states map[string]*guildState
	vmu    sync.Mutex
	voices map[string]*voiceConn
}

// New 建立音樂功能。
func New(cfg Config, dc Discord, node Node, reg *ui.Registry, settings *Settings, botID string, log *slog.Logger) *Music {
	return &Music{cfg: cfg, dc: dc, node: node, reg: reg, settings: settings, log: log, botID: botID, now: time.Now,
		resolveSpotify: ResolveSpotify, joinTimeout: 10 * time.Second, staleWait: 5 * time.Second, aloneDelay: 60 * time.Second,
		deleteAfter: 10 * time.Second, states: map[string]*guildState{}, voices: map[string]*voiceConn{}}
}

// SetNode 設定 Lavalink 節點（Client 需要 Music 當事件處理者，所以建立後再設定）。
func (m *Music) SetNode(n Node) { m.node = n }

func (m *Music) state(guildID string) *guildState {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.states[guildID]
	if g == nil {
		g = &guildState{guildID: guildID, volume: m.settings.Float(guildID, "volume", m.cfg.Volume), loop: "off"}
		m.states[guildID] = g
	}
	return g
}

func (g *guildState) elapsed(now time.Time) int {
	if g.current == nil {
		return 0
	}
	if g.pausedAt != nil {
		now = *g.pausedAt
	}
	return max(0, int(now.Sub(g.startedAt).Seconds()))
}

func (g *guildState) cancelTasks() {
	if g.idle != nil {
		g.idle.Stop()
		g.idle = nil
	}
	g.prefetched = nil
}

func (g *guildState) reset() {
	g.cancelTasks()
	g.queue, g.current, g.loop, g.pausedAt = nil, nil, "off", nil
	g.playing, g.paused = nil, false
}

func (g *guildState) busy() bool     { return g.playing != nil }
func (g *guildState) isPaused() bool { return g.busy() && g.paused }
func (g *guildState) isPlaying() bool {
	return g.busy() && !g.paused
}

func lavalinkVolume(v float64) int { return int(math.RoundToEven(v * 100)) }

func (m *Music) connected(guildID string) bool {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	v := m.voices[guildID]
	return v != nil && v.connected
}

func (m *Music) voiceChannel(guildID string) string {
	m.vmu.Lock()
	defer m.vmu.Unlock()
	if v := m.voices[guildID]; v != nil && v.connected {
		return v.channelID
	}
	return ""
}

// ---------- Embed ----------

func infoEmbed(text string) *ui.Embed { return &ui.Embed{Description: text, Color: ColorInfo} }
func errorEmbed(text string) *ui.Embed {
	return &ui.Embed{Description: "❌ " + text, Color: ColorError}
}

func (m *Music) nowPlayingEmbed(g *guildState) *ui.Embed {
	song := g.current
	if song == nil {
		return &ui.Embed{Description: "目前沒有播放任何歌曲", Color: ColorInfo}
	}
	status := "🎵 正在播放"
	if g.isPaused() {
		status = "⏸️ 已暫停"
	}
	name, icon, color := song.source()
	e := &ui.Embed{Title: short(song.Title, 200), URL: song.link(), Color: color, AuthorName: status}
	el := g.elapsed(m.now())
	e.Description = fmt.Sprintf("`%s` %s `%s`", fmtDuration(&el), progressBar(el, song.Duration), fmtDuration(song.Duration))
	if song.Author != "" {
		e.AddField("演出者", escape(short(song.Author, 100)), true)
	}
	e.AddField("來源", icon+" "+name, true)
	req := "—"
	if song.RequesterID != "" {
		req = "<@" + song.RequesterID + ">"
	}
	e.AddField("點播", req, true)
	e.AddField("音量", fmt.Sprintf("%.0f%%", g.volume*100), true)
	e.AddField("循環", loopLabels[g.loop], true)
	next := "無"
	if len(g.queue) > 0 {
		next = g.queue[0].Title
	}
	e.AddField(fmt.Sprintf("下一首（剩 %d 首）", len(g.queue)), escape(short(next, 100)), true)
	e.Thumbnail = song.Thumbnail
	e.Footer, e.FooterIcon = m.dc.Guild(g.guildID)
	return e
}

func addedEmbed(songs []*Song, requesterName, requesterAvatar string, position int, playlistName, playlistURL, cover string) *ui.Embed {
	first := songs[0]
	_, _, color := first.source()
	var e *ui.Embed
	if len(songs) == 1 {
		e = &ui.Embed{Title: short(first.Title, 200), URL: first.link(), Color: color, AuthorName: "✅ 已加入待播清單"}
		e.AddField("長度", fmtDuration(first.Duration), true)
		pos := "即將播放"
		if position != 0 {
			pos = fmt.Sprintf("第 %d 首", position+1)
		}
		e.AddField("順位", pos, true)
		e.Thumbnail = first.Thumbnail
	} else {
		total := 0
		for _, s := range songs {
			if s.Duration != nil {
				total += *s.Duration
			}
		}
		name := playlistName
		if name == "" {
			name = "播放清單"
		}
		e = &ui.Embed{Title: short(name, 200), URL: playlistURL, Color: color, AuthorName: fmt.Sprintf("✅ 已加入 %d 首歌曲", len(songs))}
		lines := make([]string, 0, 5)
		for i, s := range songs[:min(5, len(songs))] {
			lines = append(lines, fmt.Sprintf("`%d.` %s", i+1, escape(short(s.Title, 60))))
		}
		preview := strings.Join(lines, "\n")
		if len(songs) > 5 {
			preview += fmt.Sprintf("\n…還有 %d 首", len(songs)-5)
		}
		e.Description = preview
		e.AddField("總長度", fmtDuration(&total), true)
		if cover != "" {
			e.Thumbnail = cover
		} else {
			e.Thumbnail = first.Thumbnail
		}
	}
	e.Footer, e.FooterIcon = "由 "+requesterName+" 點播", requesterAvatar
	return e
}

func queuePages(g *guildState) []*ui.Embed {
	total := 0
	for _, s := range g.queue {
		if s.Duration != nil {
			total += *s.Duration
		}
	}
	header := ""
	if c := g.current; c != nil {
		_, icon, _ := c.source()
		header = fmt.Sprintf("**正在播放**\n%s [%s](%s) `%s`\n\n", icon, escape(short(c.Title, 60)), c.link(), fmtDuration(c.Duration))
	}
	var pages []*ui.Embed
	const per = 10
	for i := 0; i < max(len(g.queue), 1); i += per {
		chunk := g.queue[i:min(i+per, len(g.queue))]
		body := "📭 待播清單是空的"
		if len(chunk) > 0 {
			lines := make([]string, len(chunk))
			for idx, s := range chunk {
				_, icon, _ := s.source()
				lines[idx] = fmt.Sprintf("`%d.` %s %s `%s`", i+idx+1, icon, escape(short(s.Title, 55)), fmtDuration(s.Duration))
			}
			body = "**待播清單**\n" + strings.Join(lines, "\n")
		}
		pages = append(pages, &ui.Embed{Title: "🎶 播放清單", Description: header + body, Color: ColorInfo})
	}
	for n, e := range pages {
		e.Footer = fmt.Sprintf("第 %d/%d 頁 · 共 %d 首 · 總長 %s · 循環：%s", n+1, len(pages), len(g.queue), fmtDuration(&total), loopLabels[g.loop])
	}
	return pages
}

// ---------- 音源解析 ----------

// getSongs 搜尋關鍵字、單支影片或播放清單（Python get_songs）。
func (m *Music) getSongs(ctx context.Context, query, requester string) (string, []*Song, error) {
	isURL := strings.HasPrefix(query, "http")
	id := query
	if !isURL {
		id = "ytsearch:" + query
	}
	res, err := m.node.Load(ctx, id)
	if err != nil {
		var le LoadError
		if errors.As(err, &le) {
			m.log.Warn("Lavalink 解析失敗", "error", le.Message)
			return "", nil, nil
		}
		return "", nil, err
	}
	if p := res.Playlist; p != nil {
		// YouTube 影片網址帶 list= 時 Lavalink 會回傳整個清單；沒有 list= 的網址只播單曲
		if isURL && !strings.Contains(query, "list=") {
			if len(p.Tracks) == 0 {
				return "", nil, nil
			}
			return "", []*Song{songFromTrack(p.Tracks[max(p.Selected, 0)], requester)}, nil
		}
		start := 0
		if p.Selected >= 0 && p.Selected < len(p.Tracks) {
			start = p.Selected
		}
		var songs []*Song
		for _, t := range p.Tracks[start:min(start+m.cfg.PlaylistLimit, len(p.Tracks))] {
			songs = append(songs, songFromTrack(t, requester))
		}
		return p.Name, songs, nil
	}
	if len(res.Tracks) == 0 {
		return "", nil, nil
	}
	return "", []*Song{songFromTrack(res.Tracks[0], requester)}, nil
}

// resolveTrack 取得可播放的音軌；Spotify 歌曲在這裡才到 YouTube 搜尋同一首歌（Python resolve_track）。
func (m *Music) resolveTrack(ctx context.Context, s *Song) (*Track, error) {
	if s.Track != nil {
		return s.Track, nil
	}
	res, err := m.node.Load(ctx, "ytsearch:"+s.SearchQuery)
	if err != nil {
		var le LoadError
		if errors.As(err, &le) {
			m.log.Warn("Lavalink 解析失敗", "error", le.Message)
			return nil, nil
		}
		return nil, err
	}
	tracks := res.Tracks
	if res.Playlist != nil {
		tracks = res.Playlist.Tracks
	}
	if len(tracks) == 0 {
		return nil, nil
	}
	t := tracks[0]
	s.Track, s.URL = &t, deref(t.Info.URI)
	if s.Duration == nil && !t.Info.IsStream {
		s.Duration = intPtr(int(t.Info.Length / 1000))
	}
	if s.Thumbnail == "" {
		s.Thumbnail = deref(t.Info.ArtworkURL)
	}
	return &t, nil
}

func (m *Music) spotifySongs(ctx context.Context, link, requester string) (name, pageURL, cover string, songs []*Song, err error) {
	res, err := m.resolveSpotify(ctx, link, m.cfg.PlaylistLimit)
	if err != nil {
		return "", "", "", nil, err
	}
	for _, t := range res.Tracks {
		title := t.Title
		if t.Artist != "" {
			title = t.Artist + " - " + t.Title
		}
		songs = append(songs, &Song{Title: title, RequesterID: requester, Duration: t.Duration, Thumbnail: t.Thumbnail,
			Author: t.Artist, Source: "spotify", PageURL: t.URL, SearchQuery: t.Title + " " + t.Artist + " audio"})
	}
	if res.Kind != "track" {
		name = res.Name
	}
	return name, res.PageURL, res.Cover, songs, nil
}

// ---------- 播放控制（呼叫端持有 g.mu） ----------

func (m *Music) pause(ctx context.Context, g *guildState) error {
	if err := m.node.Pause(ctx, g.guildID, true); err != nil {
		return err
	}
	now := m.now()
	g.paused, g.pausedAt = true, &now
	return nil
}

func (m *Music) resume(ctx context.Context, g *guildState) error {
	if g.pausedAt != nil {
		g.startedAt = g.startedAt.Add(m.now().Sub(*g.pausedAt))
	}
	g.pausedAt = nil
	if err := m.node.Pause(ctx, g.guildID, false); err != nil {
		return err
	}
	g.paused = false
	return nil
}

func shuffleQueue(g *guildState) {
	rand.Shuffle(len(g.queue), func(i, j int) { g.queue[i], g.queue[j] = g.queue[j], g.queue[i] })
	g.prefetched = nil
}

func (m *Music) changeVolume(ctx context.Context, g *guildState, value float64) {
	g.volume = math.Round(max(0, min(value, 2))*100) / 100 // 限制音量在 0.0 到 2.0 之間
	if m.connected(g.guildID) {
		if err := m.node.Volume(ctx, g.guildID, lavalinkVolume(g.volume)); err != nil {
			m.log.Warn("設定音量失敗", "guild", g.guildID, "type", fmt.Sprintf("%T", err))
		}
	}
	if err := m.settings.Set(g.guildID, "volume", g.volume); err != nil {
		m.log.Warn("儲存音量失敗", "type", fmt.Sprintf("%T", err))
	}
}

// skipCurrent 跳過目前的歌；Lavalink 送出 TrackEnd（stopped）後由 onSongEnd 播下一首。
func (m *Music) skipCurrent(ctx context.Context, g *guildState) bool {
	if !g.busy() {
		return false
	}
	g.skipRequested = true
	if err := m.node.Stop(ctx, g.guildID); err != nil {
		m.log.Warn("跳過失敗", "guild", g.guildID, "type", fmt.Sprintf("%T", err))
	}
	return true
}

func (m *Music) stopPlayback(ctx context.Context, g *guildState) {
	g.cancelTasks()
	g.queue, g.loop = nil, "off"
	if g.busy() {
		if err := m.node.Stop(ctx, g.guildID); err != nil { // TrackEnd 事件會觸發 playNext，清單空了就進入閒置
			m.log.Warn("停止播放失敗", "guild", g.guildID, "type", fmt.Sprintf("%T", err))
		}
	}
}

func (m *Music) send(ctx context.Context, g *guildState, msg ui.Message) (string, error) {
	if g.textChannel == "" {
		return "", errors.New("沒有文字頻道")
	}
	id, err := m.dc.Send(ctx, g.textChannel, msg)
	if err != nil {
		m.log.Warn("傳送訊息失敗", "type", fmt.Sprintf("%T", err))
	}
	return id, err
}

// clearNP 移除上一則「正在播放」訊息的按鈕（標示為已播放）。
func (m *Music) clearNP(ctx context.Context, g *guildState) {
	np := g.np
	g.np = nil
	if np == nil {
		return
	}
	np.view.Stop()
	e := *np.embed
	e.AuthorName, e.Description = "✔️ 已播放", ""
	if err := np.edit(ctx, ui.Message{Embed: &e, View: ui.NewView(0)}); err != nil {
		m.log.Debug("更新已播放訊息失敗", "type", fmt.Sprintf("%T", err))
	}
}

// prefetchNext 預先解析下一首（只有 Spotify 歌曲需要到 YouTube 搜尋）。
func (m *Music) prefetchNext(g *guildState) {
	go func() {
		ctx := context.Background()
		g.mu.Lock()
		if len(g.queue) == 0 {
			g.mu.Unlock()
			return
		}
		song := g.queue[0]
		if song.Track != nil || g.prefetched == song {
			g.mu.Unlock()
			return
		}
		g.mu.Unlock()
		res, err := m.node.Load(ctx, "ytsearch:"+song.SearchQuery)
		if err != nil {
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if song.Track != nil {
			return
		}
		tracks := res.Tracks
		if res.Playlist != nil {
			tracks = res.Playlist.Tracks
		}
		if len(tracks) == 0 {
			return
		}
		t := tracks[0]
		song.Track, song.URL = &t, deref(t.Info.URI)
		if song.Duration == nil && !t.Info.IsStream {
			song.Duration = intPtr(int(t.Info.Length / 1000))
		}
		if song.Thumbnail == "" {
			song.Thumbnail = deref(t.Info.ArtworkURL)
		}
		if len(g.queue) > 0 && g.queue[0] == song {
			g.prefetched = song
		}
	}()
}

// scheduleIdle 播放清單空了，閒置一段時間後自動離開。
func (m *Music) scheduleIdle(g *guildState, delay time.Duration, alone bool) {
	if g.idle != nil {
		g.idle.Stop()
	}
	g.idle = time.AfterFunc(delay, func() {
		ctx := context.Background()
		g.mu.Lock()
		defer g.mu.Unlock()
		if alone {
			ch := m.voiceChannel(g.guildID)
			if ch == "" || m.dc.ChannelHumans(g.guildID, ch) > 0 {
				return
			}
			g.reset()
			m.disconnect(ctx, g.guildID)
			m.clearNP(ctx, g)
			_, _ = m.send(ctx, g, ui.Message{Embed: infoEmbed("👋 語音頻道沒有人了，已自動離開。")})
			return
		}
		if m.connected(g.guildID) && !g.busy() && len(g.queue) == 0 {
			m.disconnect(ctx, g.guildID)
			m.clearNP(ctx, g)
			_, _ = m.send(ctx, g, ui.Message{Embed: infoEmbed("💤 閒置過久，已自動離開語音頻道。")})
		}
	})
}

func (m *Music) onSongEnd(ctx context.Context, g *guildState, song *Song, loadFailed bool) {
	if loadFailed {
		m.log.Warn("播放時發生錯誤", "guild", g.guildID, "error", "load failed")
	}
	if g.current == song {
		if !g.skipRequested {
			switch g.loop {
			case "song":
				g.queue = append([]*Song{song}, g.queue...)
			case "queue":
				g.queue = append(g.queue, song)
			}
		} else if g.loop == "queue" {
			g.queue = append(g.queue, song)
		}
		g.current = nil
	}
	g.skipRequested = false
	m.playNext(ctx, g)
}

// playNext 播放待播清單的下一首（Python play_next）。
func (m *Music) playNext(ctx context.Context, g *guildState) {
	if !m.connected(g.guildID) {
		m.log.Info("不播下一首：語音未連線", "guild", g.guildID)
		g.current = nil
		return
	}
	if g.busy() {
		m.log.Info("不播下一首：播放器仍有音軌", "guild", g.guildID)
		return
	}
	for len(g.queue) > 0 {
		song := g.queue[0]
		g.queue = g.queue[1:]
		var track *Track
		if g.prefetched == song && song.Track != nil {
			track, g.prefetched = song.Track, nil
		} else {
			t, err := m.resolveTrack(ctx, song)
			if err != nil {
				_, _ = m.send(ctx, g, ui.Message{Embed: errorEmbed(msgUnavailable)})
				g.queue = append([]*Song{song}, g.queue...)
				break
			}
			track = t
		}
		if track == nil {
			_, _ = m.send(ctx, g, ui.Message{Embed: errorEmbed(fmt.Sprintf("無法取得 **%s** 的音訊，跳過此曲。", escape(song.Title)))})
			continue
		}
		if !m.connected(g.guildID) {
			g.current = nil
			return
		}
		g.current = song
		g.startedAt, g.pausedAt = m.now(), nil
		if g.idle != nil {
			g.idle.Stop()
			g.idle = nil
		}
		if err := m.node.Play(ctx, g.guildID, *track, lavalinkVolume(g.volume)); err != nil {
			g.current = nil
			m.log.Warn("Lavalink 播放失敗", "guild", g.guildID, "error", err.Error())
			_, _ = m.send(ctx, g, ui.Message{Embed: errorEmbed(fmt.Sprintf("播放 **%s** 時發生錯誤，跳過此曲。", escape(song.Title)))})
			continue
		}
		t := *track
		g.playing, g.paused = &t, false
		m.clearNP(ctx, g)
		m.sendNowPlaying(ctx, g, nil)
		if len(g.queue) > 0 {
			m.prefetchNext(g)
		}
		return
	}
	// 播放清單空了，閒置一段時間後自動斷線
	g.current = nil
	m.clearNP(ctx, g)
	m.scheduleIdle(g, m.cfg.IdleTimeout, false)
}

// sendNowPlaying 送出「正在播放」訊息與控制按鈕：it 為 nil 時送到文字頻道，否則回應指令（/nowplaying）。
func (m *Music) sendNowPlaying(ctx context.Context, g *guildState, it ui.Interaction) {
	embed := m.nowPlayingEmbed(g)
	view := m.controls(g)
	msg := ui.Message{Embed: embed, View: view}
	if it == nil {
		id, err := m.send(ctx, g, msg)
		if err != nil {
			view.Stop()
			return
		}
		ch := g.textChannel
		g.np = &npMessage{embed: embed, view: view, edit: func(ctx context.Context, m2 ui.Message) error { return m.dc.Edit(ctx, ch, id, m2) }}
		return
	}
	if err := it.SendMessage(ctx, msg); err != nil {
		view.Stop()
		return
	}
	g.np = &npMessage{embed: embed, view: view, edit: it.EditOriginal}
}

// ---------- 語音連線 ----------

// disconnect 離開語音頻道並刪除 Lavalink 播放器（Python vc.disconnect()）。
func (m *Music) disconnect(ctx context.Context, guildID string) {
	m.vmu.Lock()
	delete(m.voices, guildID)
	m.vmu.Unlock()
	if err := m.dc.LeaveVoice(guildID); err != nil {
		m.log.Warn("離開語音頻道失敗", "guild", guildID, "type", fmt.Sprintf("%T", err))
	}
	if err := m.node.Destroy(ctx, guildID); err != nil {
		m.log.Debug("刪除 Lavalink 播放器失敗", "guild", guildID, "type", fmt.Sprintf("%T", err))
	}
}

// join 加入（或移動到）語音頻道，等到 Discord 給齊連線資料並交給 Lavalink。
func (m *Music) join(ctx context.Context, guildID, channelID string) error {
	m.vmu.Lock()
	v := m.voices[guildID]
	if v == nil {
		v = &voiceConn{}
		m.voices[guildID] = v
	}
	v.channelID, v.connected, v.ready = channelID, false, make(chan struct{})
	v.token, v.endpoint = "", ""
	ready := v.ready
	m.vmu.Unlock()
	if err := m.dc.JoinVoice(guildID, channelID); err != nil {
		return err
	}
	select {
	case <-ready:
		return nil
	case <-time.After(m.joinTimeout):
		return errors.New("等待 Discord 語音連線逾時")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OnVoiceState Discord 的語音狀態更新（discordio 轉來）：Bot 自己的狀態交給 Lavalink；其他人離開時檢查頻道是否沒人。
func (m *Music) OnVoiceState(guildID, userID, before, after, sessionID string) {
	ctx := context.Background()
	if userID == m.botID {
		if before != "" && after == "" { // 機器人被踢出／斷線：清除狀態
			m.vmu.Lock()
			delete(m.voices, guildID)
			m.vmu.Unlock()
			g := m.state(guildID)
			g.mu.Lock()
			g.reset()
			m.clearNP(ctx, g)
			g.mu.Unlock()
			_ = m.node.Destroy(ctx, guildID)
			return
		}
		m.vmu.Lock()
		if v := m.voices[guildID]; v != nil {
			v.sessionID = sessionID
			if after != "" {
				v.channelID = after
			}
		}
		m.vmu.Unlock()
		m.pushVoice(ctx, guildID)
		return
	}
	ch := m.voiceChannel(guildID)
	if ch != "" && before == ch && after != ch && m.dc.ChannelHumans(guildID, ch) == 0 {
		g := m.state(guildID)
		g.mu.Lock()
		m.scheduleIdle(g, m.aloneDelay, true)
		g.mu.Unlock()
	}
}

// OnVoiceServer Discord 的語音伺服器資料（discordio 轉來）。
func (m *Music) OnVoiceServer(guildID, token, endpoint string) {
	m.vmu.Lock()
	if v := m.voices[guildID]; v != nil {
		v.token, v.endpoint = token, endpoint
	}
	m.vmu.Unlock()
	m.pushVoice(context.Background(), guildID)
}

// pushVoice 資料到齊時交給 Lavalink，並標示已連線。
func (m *Music) pushVoice(ctx context.Context, guildID string) {
	m.vmu.Lock()
	v := m.voices[guildID]
	if v == nil || v.sessionID == "" || v.token == "" || v.endpoint == "" {
		m.vmu.Unlock()
		return
	}
	vs := VoiceState{Token: v.token, Endpoint: v.endpoint, SessionID: v.sessionID, ChannelID: v.channelID}
	m.vmu.Unlock()
	if err := m.node.Voice(ctx, guildID, vs); err != nil {
		m.log.Warn("送出語音連線資料給 Lavalink 失敗", "guild", guildID, "type", fmt.Sprintf("%T", err))
		return
	}
	m.vmu.Lock()
	if v := m.voices[guildID]; v != nil && !v.connected {
		v.connected = true
		close(v.ready)
	}
	m.vmu.Unlock()
}

// OnGuildRemove Bot 被移出伺服器。
func (m *Music) OnGuildRemove(guildID string) {
	m.mu.Lock()
	g := m.states[guildID]
	delete(m.states, guildID)
	m.mu.Unlock()
	if g != nil {
		g.mu.Lock()
		g.cancelTasks()
		g.mu.Unlock()
	}
}

// ---------- Lavalink 事件 ----------

func sameTrack(a, b *Track) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Encoded == b.Encoded || (a.Info.Identifier != "" && a.Info.Identifier == b.Info.Identifier)
}

// OnReady Lavalink 連上；重新連線（新的 Session）時原本的播放器都不在了：清除狀態並離開語音頻道。
func (m *Music) OnReady(_ string, resumed, reconnected bool) {
	if !reconnected || resumed {
		return
	}
	m.vmu.Lock()
	var guilds []string
	for id := range m.voices {
		guilds = append(guilds, id)
	}
	m.vmu.Unlock()
	ctx := context.Background()
	for _, id := range guilds {
		g := m.state(id)
		g.mu.Lock()
		g.reset()
		m.clearNP(ctx, g)
		m.disconnect(ctx, id)
		_, _ = m.send(ctx, g, ui.Message{Embed: errorEmbed("音樂伺服器（Lavalink）重新連線，播放已中斷，請重新點歌。")})
		g.mu.Unlock()
	}
}

// OnEvent Lavalink 播放事件。
func (m *Music) OnEvent(e Event) {
	if e.GuildID == "" {
		return
	}
	ctx := context.Background()
	switch e.Type {
	case "TrackEndEvent":
		if e.Reason == "replaced" { // 換歌：不處理
			return
		}
		g := m.state(e.GuildID)
		g.mu.Lock()
		defer g.mu.Unlock()
		if sameTrack(g.playing, e.Track) {
			g.playing, g.paused = nil, false
		}
		matched := g.current != nil && sameTrack(g.current.Track, e.Track)
		m.log.Info("歌曲結束", "guild", e.GuildID, "reason", e.Reason, "current", matched, "queue", len(g.queue))
		if !matched {
			return
		}
		m.onSongEnd(ctx, g, g.current, e.Reason == "loadFailed")
	case "TrackExceptionEvent":
		g := m.state(e.GuildID)
		g.mu.Lock()
		defer g.mu.Unlock()
		msg := "未知錯誤"
		if e.Exception != nil && e.Exception.Message != "" {
			msg = e.Exception.Message
		}
		m.log.Warn("Lavalink 播放例外", "guild", e.GuildID, "message", msg)
		if g.current != nil && sameTrack(g.current.Track, e.Track) {
			_, _ = m.send(ctx, g, ui.Message{Embed: errorEmbed(fmt.Sprintf("播放 **%s** 時發生錯誤，跳過此曲。", escape(g.current.Title)))})
		}
	case "TrackStuckEvent": // 音軌卡住（來源網路中斷等）→ 直接跳到下一首
		g := m.state(e.GuildID)
		g.mu.Lock()
		defer g.mu.Unlock()
		m.log.Warn("Lavalink 音軌卡住，跳過", "guild", e.GuildID, "ms", e.ThresholdMs)
		m.skipCurrent(ctx, g)
	case "WebSocketClosedEvent": // Discord 語音連線被關閉（例如 4014 被踢出、4017 不支援 DAVE）
		m.log.Warn("Discord 語音連線關閉", "guild", e.GuildID, "code", e.Code, "reason", e.Reason, "by_remote", e.ByRemote)
	}
}

// OnStats 只在掉封包達 1% 以上時記錄（Python on_wavelink_stats_update）。
func (m *Music) OnStats(f *FrameStats) {
	if f == nil {
		return
	}
	lost := max(0, f.Nulled) + max(0, f.Deficit)
	total := max(0, f.Sent) + lost
	if lost > 0 && lost*100 >= max(total, 1) {
		m.log.Warn("音訊封包（最近 1 分鐘，每個播放器平均）", "sent", f.Sent, "nulled", f.Nulled, "deficit", f.Deficit)
	}
}
