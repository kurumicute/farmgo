package music

// 音樂 Bot 端的壓力測試（對應 Python scripts/music_stresstest.py 的第 1 部分；Go 版新增）：N 個伺服器同時播放並不斷切歌。
// 播放器是假的 Lavalink 節點，但走正式的 playNext → Lavalink TrackEndEvent → OnEvent → onSongEnd 流程
// （含「正在播放」Embed 與控制按鈕），量測每次切歌的處理時間與排程延遲。音訊解碼在 Lavalink，不在 Bot 程序。
//
// 預設略過；執行：FARMGM_STRESS=1 go test -run TestStressMusic -v ./internal/music
// 參數：FARMGM_STRESS_GUILDS（預設 200）、FARMGM_STRESS_SONGS（每個伺服器的歌曲數，預設 5）、
// FARMGM_STRESS_SONG_MS（每首歌幾毫秒後結束，預設 200，與 Python 的 --song-seconds 0.2 相同）。

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"farmgm/internal/bot/ui"
)

func stressInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// stressDiscord 假的 Discord：只計算送出與編輯的訊息數（實際送出由 Discord HTTP 處理，這裡只量測 Bot 端產生訊息的成本）。
type stressDiscord struct{ sent, edited atomic.Int64 }

func (d *stressDiscord) JoinVoice(string, string) error         { return nil }
func (d *stressDiscord) LeaveVoice(string) error                { return nil }
func (d *stressDiscord) UserVoiceChannel(string, string) string { return "" }
func (d *stressDiscord) BotVoiceChannel(string) string          { return "voice" }
func (d *stressDiscord) ChannelHumans(string, string) int       { return 1 }
func (d *stressDiscord) Guild(id string) (string, string)       { return "壓測伺服器" + id, "" }
func (d *stressDiscord) Send(context.Context, string, ui.Message) (string, error) {
	return strconv.FormatInt(d.sent.Add(1), 10), nil
}
func (d *stressDiscord) Edit(context.Context, string, string, ui.Message) error {
	d.edited.Add(1)
	return nil
}

// stressNode 假的 Lavalink：Play 之後經過 songDur 送出 TrackEndEvent（finished）。
type stressNode struct {
	m       *Music
	songDur time.Duration
	mu      sync.Mutex
	switchs []time.Duration
	timers  sync.Map // guildID → *time.Timer
}

func (n *stressNode) Ready() bool                                      { return true }
func (n *stressNode) Load(context.Context, string) (LoadResult, error) { return LoadResult{}, nil }
func (n *stressNode) Pause(context.Context, string, bool) error        { return nil }
func (n *stressNode) Volume(context.Context, string, int) error        { return nil }
func (n *stressNode) Voice(context.Context, string, VoiceState) error  { return nil }
func (n *stressNode) Destroy(context.Context, string) error            { return nil }
func (n *stressNode) Stop(_ context.Context, guildID string) error     { n.cancel(guildID); return nil }
func (n *stressNode) cancel(guildID string) {
	if t, ok := n.timers.Load(guildID); ok {
		t.(*time.Timer).Stop()
	}
}

func (n *stressNode) Play(_ context.Context, guildID string, t Track, _ int) error {
	tr := t
	n.cancel(guildID)
	n.timers.Store(guildID, time.AfterFunc(n.songDur, func() {
		ended := time.Now()
		n.m.OnEvent(Event{Type: "TrackEndEvent", GuildID: guildID, Track: &tr, Reason: "finished"})
		d := time.Since(ended) // 從 TrackEnd 到下一首開始播放（或清單結束）的處理時間
		n.mu.Lock()
		n.switchs = append(n.switchs, d)
		n.mu.Unlock()
	}))
	return nil
}

func stressMusicPct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	return ds[min(len(ds)-1, int(float64(len(ds))*p))]
}

func TestStressMusic(t *testing.T) {
	if os.Getenv("FARMGM_STRESS") == "" {
		t.Skip("壓力測試：設 FARMGM_STRESS=1 才執行")
	}
	guilds := stressInt("FARMGM_STRESS_GUILDS", 200)
	songs := stressInt("FARMGM_STRESS_SONGS", 5)
	songDur := time.Duration(stressInt("FARMGM_STRESS_SONG_MS", 200)) * time.Millisecond

	reg := ui.NewRegistry()
	dc := &stressDiscord{}
	node := &stressNode{songDur: songDur}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(Config{Volume: 1, PlaylistLimit: 100, Prefix: "!", IdleTimeout: time.Hour}, dc, node, reg,
		LoadSettings(filepath.Join(t.TempDir(), "music_guild_settings.json")), "999", log)
	node.m = m

	ids := make([]string, guilds)
	for i := range ids {
		id := strconv.Itoa(i + 1)
		ids[i] = id
		m.voices[id] = &voiceConn{channelID: "voice", connected: true, ready: make(chan struct{})}
		g := m.state(id)
		g.textChannel = "text-" + id
		for s := range songs {
			uri := fmt.Sprintf("https://www.youtube.com/watch?v=%s-%d", id, s)
			g.queue = append(g.queue, songFromTrack(Track{Encoded: "enc-" + id + "-" + strconv.Itoa(s), Info: TrackInfo{
				Identifier: id, Title: fmt.Sprintf("壓測歌曲 %s-%d", id, s), URI: &uri, Length: 200_000,
				Author: "壓測歌手", SourceName: "youtube"}}, ""))
		}
	}

	// 排程延遲（對應 Python 的事件迴圈延遲）：每 50ms 醒來一次，量測實際晚了多久
	var lags []time.Duration
	stop, hbDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(hbDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			t0 := time.Now()
			time.Sleep(50 * time.Millisecond)
			lags = append(lags, time.Since(t0)-50*time.Millisecond)
		}
	}()

	// 暖機：slog 第一次寫入有一次性的初始化（約 40ms，正式 Bot 啟動時就已發生），不算進切歌時間
	m.log.Info("暖機")
	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	cpu0 := processCPU()
	t0 := time.Now()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g := m.state(id)
			g.mu.Lock()
			defer g.mu.Unlock()
			m.playNext(context.Background(), g)
		}()
	}
	wg.Wait()
	busy := func() bool {
		for _, id := range ids {
			g := m.state(id)
			g.mu.Lock()
			b := len(g.queue) > 0 || g.current != nil
			g.mu.Unlock()
			if b {
				return true
			}
		}
		return false
	}
	for busy() {
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(t0)
	cpu := processCPU() - cpu0
	close(stop)
	<-hbDone
	var ms1 runtime.MemStats
	runtime.ReadMemStats(&ms1)
	for _, id := range ids {
		g := m.state(id)
		g.mu.Lock()
		g.cancelTasks()
		g.mu.Unlock()
	}

	node.mu.Lock()
	sw := slices.Clone(node.switchs)
	node.mu.Unlock()
	slices.Sort(sw)
	slices.Sort(lags)
	ms := func(d time.Duration) string { return fmt.Sprintf("%.2f ms", float64(d)/float64(time.Millisecond)) }
	t.Logf("[%d 個伺服器同時播放] 共 %d 首（每首 %v 就結束）耗時 %.1f 秒", guilds, guilds*songs, songDur, elapsed.Seconds())
	t.Logf("切歌處理 P50 %s、P95 %s、P99 %s、最大 %s（%d 次）｜排程延遲 P95 %s、最大 %s", ms(stressMusicPct(sw, .5)),
		ms(stressMusicPct(sw, .95)), ms(stressMusicPct(sw, .99)), ms(sw[len(sw)-1]), len(sw),
		ms(stressMusicPct(lags, .95)), ms(lags[len(lags)-1]))
	t.Logf("Bot 程序 CPU %.0f%%（單核 100%%，CPU 時間 %.3f 秒）；送出訊息 %d 則、編輯 %d 則", 100*cpu.Seconds()/elapsed.Seconds(),
		cpu.Seconds(), dc.sent.Load(), dc.edited.Load())
	t.Logf("記憶體：Go runtime 向系統取得 %.1f MB、heap %.1f MB → %.1f MB、累計配置 %.1f MB、goroutine %d",
		float64(ms1.Sys)/(1<<20), float64(ms0.HeapAlloc)/(1<<20), float64(ms1.HeapAlloc)/(1<<20),
		float64(ms1.TotalAlloc-ms0.TotalAlloc)/(1<<20), runtime.NumGoroutine())
	if len(sw) != guilds*songs {
		t.Errorf("切歌次數 %d，應為 %d", len(sw), guilds*songs)
	}
	if want := int64(guilds * songs); dc.sent.Load() != want {
		t.Errorf("「正在播放」訊息 %d 則，應為 %d", dc.sent.Load(), want)
	}
}

// TestStressMusicSwitchCost 單次切歌的平均 CPU 成本：單一伺服器、不經計時器，連續觸發 TrackEnd N 次取平均
// （Windows 上 time.Now 的解析度不足以量測單次不到 1ms 的處理）。對照 Python 以同樣方式量測。
// 參數：FARMGM_STRESS_SWITCHES（預設 5000）。
func TestStressMusicSwitchCost(t *testing.T) {
	if os.Getenv("FARMGM_STRESS") == "" {
		t.Skip("壓力測試：設 FARMGM_STRESS=1 才執行")
	}
	n := stressInt("FARMGM_STRESS_SWITCHES", 5000)
	dc := &stressDiscord{}
	node := &stressNode{songDur: time.Hour} // 不會自動結束，由迴圈手動送 TrackEnd
	m := New(Config{Volume: 1, PlaylistLimit: 100, Prefix: "!", IdleTimeout: time.Hour}, dc, node, ui.NewRegistry(),
		LoadSettings(filepath.Join(t.TempDir(), "music_guild_settings.json")), "999", slog.New(slog.NewTextHandler(io.Discard, nil)))
	node.m = m
	m.log.Info("暖機")
	m.voices["1"] = &voiceConn{channelID: "voice", connected: true, ready: make(chan struct{})}
	g := m.state("1")
	g.textChannel = "text-1"
	for s := range n + 1 {
		uri := fmt.Sprintf("https://www.youtube.com/watch?v=%d", s)
		g.queue = append(g.queue, songFromTrack(Track{Encoded: "enc-" + strconv.Itoa(s), Info: TrackInfo{
			Title: fmt.Sprintf("壓測歌曲 %d", s), URI: &uri, Length: 200_000, Author: "壓測歌手", SourceName: "youtube"}}, ""))
	}
	g.mu.Lock()
	m.playNext(context.Background(), g)
	g.mu.Unlock()
	cpu0, t0 := processCPU(), time.Now()
	for range n {
		tr := *g.current.Track
		m.OnEvent(Event{Type: "TrackEndEvent", GuildID: "1", Track: &tr, Reason: "finished"})
	}
	elapsed, cpu := time.Since(t0), processCPU()-cpu0
	node.cancel("1")
	g.mu.Lock()
	g.cancelTasks()
	g.mu.Unlock()
	t.Logf("連續切歌 %d 次：平均每次 %.1f µs（牆鐘），CPU %.1f µs", n, float64(elapsed.Microseconds())/float64(n),
		float64(cpu.Microseconds())/float64(n))
	if dc.sent.Load() != int64(n+1) {
		t.Errorf("「正在播放」訊息 %d 則，應為 %d", dc.sent.Load(), n+1)
	}
}
