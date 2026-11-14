package music

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type recordHandler struct {
	mu      sync.Mutex
	ready   []string
	events  []Event
	stats   int
	readyCh chan struct{}
}

func (h *recordHandler) OnReady(sid string, resumed, reconnected bool) {
	h.mu.Lock()
	h.ready = append(h.ready, sid)
	h.mu.Unlock()
	select {
	case h.readyCh <- struct{}{}:
	default:
	}
}
func (h *recordHandler) OnEvent(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
}
func (h *recordHandler) OnStats(*FrameStats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stats++
}

// fakeLavalink 假的 Lavalink v4：WebSocket 送 ready 與一個事件；REST 記錄請求。
type fakeLavalink struct {
	mu   sync.Mutex
	reqs []string
	body []map[string]any
	auth []string
}

func (f *fakeLavalink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if r.URL.Path == "/v4/websocket" {
		if r.Header.Get("User-Id") != "999" || r.Header.Get("Client-Name") == "" {
			http.Error(w, "bad headers", 400)
			return
		}
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"op":"ready","resumed":false,"sessionId":"S1"}`))
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"op":"event","type":"TrackEndEvent","guildId":"100","reason":"finished","track":{"encoded":"e1","info":{"identifier":"id1","title":"t"}}}`))
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"op":"stats","players":1,"frameStats":{"sent":3000,"nulled":0,"deficit":0}}`))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI())
	f.body = append(f.body, body)
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/v4/loadtracks"):
		w.Write([]byte(`{"loadType":"search","data":[{"encoded":"e1","info":{"identifier":"id1","title":"歌","length":61000,"isStream":false,"uri":"https://youtu.be/id1"}}]}`))
	default:
		w.Write([]byte(`{}`))
	}
}

func TestLavalinkClientConnectsAndControlsPlayer(t *testing.T) {
	fake := &fakeLavalink{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := &recordHandler{readyCh: make(chan struct{}, 1)}
	c := NewClient(srv.URL, "pw", "999", h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Load(ctx, "ytsearch:x"); err != ErrUnavailable { // 還沒連線
		t.Fatal(err)
	}
	go c.Run(ctx)
	select {
	case <-h.readyCh:
	case <-time.After(3 * time.Second):
		t.Fatal("沒有連上")
	}
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	if len(h.ready) != 1 || h.ready[0] != "S1" || len(h.events) != 1 || h.events[0].Reason != "finished" || h.events[0].Track.Info.Identifier != "id1" || h.stats != 1 {
		t.Fatalf("%+v", h)
	}
	h.mu.Unlock()
	res, err := c.Load(ctx, "ytsearch:新 歌")
	if err != nil || len(res.Tracks) != 1 || res.Tracks[0].Info.Length != 61000 {
		t.Fatal(res, err)
	}
	if err := c.Play(ctx, "100", res.Tracks[0], 15); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "100"); err != nil {
		t.Fatal(err)
	}
	_ = c.Pause(ctx, "100", true)
	_ = c.Voice(ctx, "100", VoiceState{Token: "t", Endpoint: "e", SessionID: "s", ChannelID: "300"})
	_ = c.Destroy(ctx, "100")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	want := []string{"PATCH /v4/sessions/S1", "GET /v4/loadtracks?identifier=ytsearch%3A%E6%96%B0+%E6%AD%8C", "PATCH /v4/sessions/S1/players/100?noReplace=false",
		"PATCH /v4/sessions/S1/players/100?noReplace=false", "PATCH /v4/sessions/S1/players/100?noReplace=false",
		"PATCH /v4/sessions/S1/players/100?noReplace=false", "DELETE /v4/sessions/S1/players/100"}
	if strings.Join(fake.reqs, "\n") != strings.Join(want, "\n") {
		t.Fatal(fake.reqs)
	}
	if r := fake.body[0]; r["resuming"] != true || r["timeout"] != float64(ResumeTimeout) {
		t.Fatal("連上後要開啟 Session 續接", r)
	}
	play, stop, pause, voice := fake.body[2], fake.body[3], fake.body[4], fake.body[5]
	if play["track"].(map[string]any)["encoded"] != "e1" || play["volume"] != float64(15) || play["paused"] != false {
		t.Fatal(play)
	}
	if v, ok := stop["track"].(map[string]any)["encoded"]; !ok || v != nil || stop["volume"] != nil { // 停止：encoded 為 null
		t.Fatal(stop)
	}
	if pause["paused"] != true || pause["track"] != nil {
		t.Fatal(pause)
	}
	if voice["voice"].(map[string]any)["channelId"] != "300" {
		t.Fatal(voice)
	}
	for _, a := range fake.auth {
		if a != "pw" {
			t.Fatal("每個請求都要帶密碼")
		}
	}
}

// TestLavalinkResumesSessionAfterDisconnect WebSocket 斷線（例如 Cloudflare 斷開）後帶 Session-Id 重連，接回原本的 Session。
func TestLavalinkResumesSessionAfterDisconnect(t *testing.T) {
	var mu sync.Mutex
	var hdrs []string
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4/websocket" {
			w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		conns++
		n := conns
		hdrs = append(hdrs, r.Header.Get("Session-Id"))
		mu.Unlock()
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if n == 1 {
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"op":"ready","resumed":false,"sessionId":"S1"}`))
			time.Sleep(50 * time.Millisecond)
			return // 斷線
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"op":"ready","resumed":true,"sessionId":"S1"}`))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	h := &recordHandler{readyCh: make(chan struct{}, 2)}
	c := NewClient(srv.URL, "pw", "999", h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	for i := 0; i < 2; i++ {
		select {
		case <-h.readyCh:
		case <-time.After(5 * time.Second):
			t.Fatal("沒有重新連上")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hdrs) < 2 || hdrs[0] != "" || hdrs[1] != "S1" {
		t.Fatal("重連時要帶上一個 Session-Id", hdrs)
	}
	if !c.Ready() || c.session() != "S1" {
		t.Fatal("續接後應沿用原本的 Session")
	}
}

func TestParseLoadTypes(t *testing.T) {
	r, err := parseLoad([]byte(`{"loadType":"playlist","data":{"info":{"name":"清單","selectedTrack":2},"tracks":[{"encoded":"a"},{"encoded":"b"}]}}`))
	if err != nil || r.Playlist == nil || r.Playlist.Name != "清單" || r.Playlist.Selected != 2 || len(r.Playlist.Tracks) != 2 {
		t.Fatal(r, err)
	}
	r, err = parseLoad([]byte(`{"loadType":"track","data":{"encoded":"a","info":{"isStream":true}}}`))
	if err != nil || len(r.Tracks) != 1 || !r.Tracks[0].Info.IsStream {
		t.Fatal(r, err)
	}
	if r, err = parseLoad([]byte(`{"loadType":"empty","data":{}}`)); err != nil || len(r.Tracks) != 0 || r.Playlist != nil {
		t.Fatal(r, err)
	}
	if _, err = parseLoad([]byte(`{"loadType":"error","data":{"message":"沒有這部影片"}}`)); err == nil || err.(LoadError).Message != "沒有這部影片" {
		t.Fatal(err)
	}
	if u, _ := wsURL("https://lavalink.example.com"); u != "wss://lavalink.example.com/v4/websocket" {
		t.Fatal(u)
	}
	if u, _ := wsURL("http://127.0.0.1:2333/"); u != "ws://127.0.0.1:2333/v4/websocket" {
		t.Fatal(u)
	}
}

func TestResolveSpotifyEmbedPage(t *testing.T) {
	page := func(entity string) string {
		return `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"state":{"data":{"entity":` + entity + `}}}}}</script></html>`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed/album/A1":
			w.Write([]byte(page(`{"name":"專輯 名","coverArt":{"sources":[{"url":"small","width":64},{"url":"big","width":640}]},
				"trackList":[{"title":"一","subtitle":"甲","duration":61000,"uri":"spotify:track:T1"},
				{"title":"","subtitle":"x"},{"title":"Pod","entityType":"episode"},{"title":"二","subtitle":"乙","uri":"spotify:track:T2"},
				{"title":"三","subtitle":"丙","uri":"spotify:track:T3"}]}`)))
		case "/embed/track/T9":
			w.Write([]byte(page(`{"name":"單曲","artists":[{"name":"A"},{"name":"B"}],"duration":200000}`)))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := SpotifyBase
	SpotifyBase = srv.URL
	defer func() { SpotifyBase = old }()
	ctx := context.Background()
	r, err := ResolveSpotify(ctx, "https://open.spotify.com/intl-ja/album/A1?si=x", 2)
	if err != nil || r.Name != "專輯 名" || r.Cover != "big" || len(r.Tracks) != 2 || r.Tracks[0].URL != "https://open.spotify.com/track/T1" ||
		*r.Tracks[0].Duration != 61 || r.Tracks[1].Title != "二" || r.Tracks[1].Duration != nil {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = ResolveSpotify(ctx, "spotify:track:T9", 20)
	if err != nil || r.Kind != "track" || r.Tracks[0].Artist != "A, B" || *r.Tracks[0].Duration != 200 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = ResolveSpotify(ctx, "https://open.spotify.com/playlist/NOPE", 20); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatal(err)
	}
	if _, err = ResolveSpotify(ctx, "https://open.spotify.com/show/abc", 20); err == nil || !strings.Contains(err.Error(), "不支援這種 Spotify 連結") {
		t.Fatal(err)
	}
	if !IsSpotify("https://open.spotify.com/show/abc") || !IsSpotify("https://spotify.link/abc") || IsSpotify("https://youtube.com/x") {
		t.Fatal("IsSpotify")
	}
}

func TestSettingsFileCompatibleWithPython(t *testing.T) {
	path := t.TempDir() + "/s.json"
	s := LoadSettings(path)
	if err := s.Set("100", "volume", 0.35); err != nil {
		t.Fatal(err)
	}
	_ = s.Set("100", "prefix", "?")
	again := LoadSettings(path)
	if again.Float("100", "volume", 0) != 0.35 || again.String("100", "prefix", "!") != "?" || again.Float("200", "volume", 0.1) != 0.1 {
		t.Fatal("設定沒有存好")
	}
	var raw map[string]map[string]any
	data, _ := io.ReadAll(bytes.NewReader(mustRead(t, path)))
	if err := json.Unmarshal(data, &raw); err != nil || raw["100"]["prefix"] != "?" {
		t.Fatal(string(data))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestRealLavalink 連到真的 Lavalink（FARMGM_TEST_LAVALINK_URL／FARMGM_TEST_LAVALINK_PASSWORD；沒有設定時略過）：
// 連線、取得 Session、以關鍵字搜尋 YouTube。
func TestRealLavalink(t *testing.T) {
	u, pw := os.Getenv("FARMGM_TEST_LAVALINK_URL"), os.Getenv("FARMGM_TEST_LAVALINK_PASSWORD")
	if u == "" || pw == "" {
		t.Skip("沒有設定 FARMGM_TEST_LAVALINK_URL")
	}
	h := &recordHandler{readyCh: make(chan struct{}, 1)}
	c := NewClient(u, pw, "1558428174814154822", h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	select {
	case <-h.readyCh:
	case <-time.After(10 * time.Second):
		t.Fatal("連不上 Lavalink")
	}
	res, err := c.Load(ctx, "ytsearch:lofi hip hop")
	if err != nil || len(res.Tracks) == 0 || res.Tracks[0].Encoded == "" || res.Tracks[0].Info.Title == "" {
		t.Fatalf("%+v %v", res, err)
	}
	t.Logf("搜尋結果：%s（%s，%d 秒）", res.Tracks[0].Info.Title, res.Tracks[0].Info.Author, res.Tracks[0].Info.Length/1000)
}
