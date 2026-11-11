package discordio

// Bot 本體（對應 Python app/discord_bot/bot.py 的 FarmBot）：登入、同步斜線指令、加入伺服器時建立對應群組、
// 處理互動，以及（設定時）背景結算與面板延遲彙總。音樂（internal/music）在同一個程序內執行（沒有設定 MUSIC_BOT_TOKEN 時，與 Python 相同）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot"
	"farmgm/internal/bot/latency"
	"farmgm/internal/bot/ui"
	"farmgm/internal/config"
	"farmgm/internal/music"
	"farmgm/internal/services"
)

// SlowInteraction Discord 要求 3 秒內回應；送達就超過 2 秒時記錄一行警告。
const SlowInteraction = 2 * time.Second

// SweepInterval 檢查面板逾時的間隔。
const SweepInterval = 15 * time.Second

// Bot Discord Bot。
type Bot struct {
	S        *discordgo.Session
	Settings config.Settings
	Cmds     *bot.Commands
	Svc      *services.AdminService
	Reg      *ui.Registry
	Log      *slog.Logger

	syncOnce sync.Once
	mu       sync.Mutex
	known    map[string]bool // 登入時已加入的伺服器（之後的 GUILD_CREATE 才是新加入）
	ready    bool

	// 音樂（沒有設定時為 nil）：登入後才知道 Bot 的使用者 ID，所以在第一次 READY 時建立並連線 Lavalink
	music     *music.Music
	musicOnce sync.Once
	ctx       context.Context
}

// New 建立 Bot（尚未連線）。Intents 只需要伺服器基本資料（名稱、加入事件），不需要特權 Intent。
func New(settings config.Settings, cmds *bot.Commands, svc *services.AdminService) (*Bot, error) {
	s, err := discordgo.New("Bot " + settings.DiscordBotToken)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuilds
	if musicEnabled(settings) {
		s.Identify.Intents |= discordgo.IntentsGuildVoiceStates // 加入語音頻道、偵測頻道沒人時自動離開（非特權）
	}
	s.StateEnabled = true
	b := &Bot{S: s, Settings: settings, Cmds: cmds, Svc: svc, Reg: cmds.Registry, Log: slog.Default(), known: map[string]bool{}}
	s.AddHandler(b.onReady)
	s.AddHandler(b.onGuildCreate)
	s.AddHandler(b.onInteraction)
	s.AddHandler(b.onVoiceState)
	s.AddHandler(b.onVoiceServer)
	s.AddHandler(b.onGuildDelete)
	return b, nil
}

// Run 連線並執行直到 ctx 取消：啟動時清理逾時的釣魚、定期檢查面板逾時、彙總延遲，BACKGROUND_TASKS_RUNNER=bot 時背景結算。
func (b *Bot) Run(ctx context.Context) error {
	b.ctx = ctx
	b.cleanupFishing(ctx)
	if err := b.S.Open(); err != nil {
		return openError(err)
	}
	defer b.S.Close()
	var wg sync.WaitGroup
	loops := []func(context.Context){func(ctx context.Context) { b.Reg.Run(ctx, SweepInterval) }, b.latencyLoop}
	if b.Settings.BackgroundTasksRunner == "bot" {
		b.Log.Info("背景結算由 Bot 執行", "interval_seconds", b.Settings.RankingSettleIntervalSeconds)
		loops = append(loops, b.settleLoop)
	}
	for _, loop := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx)
		}()
	}
	<-ctx.Done()
	b.Log.Info("收到結束訊號，正在關閉 Bot。")
	wg.Wait()
	return nil
}

// openError 登入失敗的訊息（不含 Token；Discord 的關閉代碼 4004 = Token 無效、4014 = 未啟用的特權 Intent）。
func openError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "4004"):
		return fmt.Errorf("DISCORD_BOT_TOKEN 無效或已被重設，請到 Discord Developer Portal 重新產生並更新 .env。")
	case strings.Contains(msg, "4014"):
		return fmt.Errorf("Bot 要求了未在 Developer Portal 啟用的特權 Intent。")
	}
	return fmt.Errorf("無法連線到 Discord（%T）", err)
}

// cleanupFishing Bot 重啟後，把已超過期限仍標記為進行中的釣魚判定為失敗；失敗不影響啟動（讀取時仍會依時間判定）。
func (b *Bot) cleanupFishing(ctx context.Context) {
	n, err := bot.Call(ctx, b.Cmds.Runner, "cleanup_fishing_sessions", func(ctx context.Context) (int, error) {
		return b.Svc.CleanupFishingSessions(ctx)
	})
	if err != nil {
		b.Log.Warn("清理釣魚 Session 失敗", "type", fmt.Sprintf("%T", err))
		return
	}
	if n > 0 {
		b.Log.Info("已清理逾時的釣魚 Session", "count", n)
	}
}

func (b *Bot) onReady(s *discordgo.Session, r *discordgo.Ready) {
	b.mu.Lock()
	for _, g := range r.Guilds {
		b.known[g.ID] = true
	}
	b.ready = true
	b.mu.Unlock()
	b.Log.Info("Bot 已登入", "user", r.User.Username, "id", r.User.ID, "guilds", len(r.Guilds))
	b.musicOnce.Do(func() { b.startMusic(r.User.ID) })
	activity := "/farm 種田"
	if b.music != nil {
		activity += " ・ /play 聽歌"
	}
	if err := s.UpdateGameStatus(0, activity); err != nil {
		b.Log.Warn("設定狀態失敗", "type", fmt.Sprintf("%T", err))
	}
	b.syncOnce.Do(func() { b.syncCommands(r.User.ID) })
}

// syncCommands 同步斜線指令：開發伺服器（立即生效）或正式環境全域同步（Python sync_commands）。
func (b *Bot) syncCommands(userID string) {
	st := b.Settings
	if !st.DiscordSyncCommands {
		b.Log.Info("DISCORD_SYNC_COMMANDS=false：略過指令同步")
		return
	}
	appID := userID
	if st.DiscordApplicationID != 0 {
		appID = strconv.FormatInt(st.DiscordApplicationID, 10)
	}
	guildID := ""
	switch {
	case st.DiscordDevGuildID != 0:
		guildID = strconv.FormatInt(st.DiscordDevGuildID, 10)
	case st.AppEnv == "production":
	default:
		b.Log.Warn("開發環境未設定 DISCORD_DEV_GUILD_ID，未同步指令。請設定開發伺服器 ID，或以 APP_ENV=production 全域同步。")
		return
	}
	url := API + "applications/" + appID + "/commands"
	if guildID != "" {
		url = API + "applications/" + appID + "/guilds/" + guildID + "/commands"
	}
	body, err := client{b.S}.send(context.Background(), http.MethodPut, url, CommandsPayload(musicEnabled(st)), nil)
	var synced []json.RawMessage
	if err == nil {
		err = json.Unmarshal(body, &synced)
	}
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil {
			if rest.Response.StatusCode == http.StatusForbidden {
				b.Log.Error("指令同步失敗：Bot 沒有該伺服器的 applications.commands 權限，請用含此 scope 的邀請連結重新邀請。")
				return
			}
			body := string(rest.ResponseBody)
			if len(body) > 1500 {
				body = body[:1500]
			}
			b.Log.Error("指令同步失敗", "status", rest.Response.StatusCode, "body", body) // Discord 的錯誤說明，不含 Token
			return
		}
		b.Log.Error("指令同步失敗", "type", fmt.Sprintf("%T", err))
		return
	}
	if guildID != "" {
		b.Log.Info("已同步指令到開發伺服器（立即生效）", "count", len(synced), "guild", guildID)
	} else {
		b.Log.Info("已全域同步指令（Discord 端可能需要數分鐘才會顯示）", "count", len(synced))
	}
}

// wireCommand 斜線指令（CHAT_INPUT）。Contexts [0] = 只能在伺服器中使用（discord.py guild_only）。
type wireCommand struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Type        int             `json:"type"`
	Options     []wireCmdOption `json:"options,omitempty"`
	Contexts    []int           `json:"contexts,omitempty"`
}

type wireCmdOption struct {
	Type        int          `json:"type"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Required    bool         `json:"required,omitempty"`
	Choices     []wireChoice `json:"choices,omitempty"`
}

type wireChoice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CommandsPayload 要註冊的斜線指令：/farm、/help，音樂啟用時加上音樂指令（以 PUT 整批覆寫，舊的指令會被移除）。
func CommandsPayload(withMusic bool) []wireCommand {
	var out []wireCommand
	for _, c := range bot.Specs() {
		out = append(out, wireCommand{Name: c.Name, Description: c.Description, Type: 1})
	}
	if withMusic {
		for _, s := range music.Specs() {
			w := wireCommand{Name: s.Name, Description: s.Description, Type: 1}
			if s.GuildOnly {
				w.Contexts = []int{0}
			}
			for _, o := range s.Options {
				w.Options = append(w.Options, wireOptionOf(o))
			}
			out = append(out, w)
		}
	}
	return out
}

// onGuildCreate 登入後才收到的 GUILD_CREATE 是新加入的伺服器：建立對應群組（Python on_guild_join）。
// 失敗只記錄，之後第一次使用指令時仍會建立。
func (b *Bot) onGuildCreate(_ *discordgo.Session, g *discordgo.GuildCreate) {
	if !b.newGuild(g.ID) {
		return
	}
	ctx := context.Background()
	id, err := bot.Call(ctx, b.Cmds.Runner, "ensure_guild_group", func(ctx context.Context) (int64, error) {
		return b.Svc.EnsureGuildGroup(ctx, g.ID, g.Name)
	})
	if err != nil {
		b.Log.Warn("建立伺服器對應群組失敗", "type", fmt.Sprintf("%T", err))
		return
	}
	b.Log.Info("加入伺服器", "guild", g.ID, "group", id)
}

// newGuild 記下伺服器，回傳是否為登入後才加入的新伺服器。
func (b *Bot) newGuild(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	joined := b.ready && !b.known[id]
	b.known[id] = true
	return joined
}

// onInteraction 處理斜線指令、元件與對話框。discordgo 每個事件在自己的 goroutine 執行；同一面板的回呼由 Registry 依序執行。
func (b *Bot) onInteraction(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	i := ic.Interaction
	b.logSlow(i, time.Now())
	ctx := context.Background()
	it := NewInteraction(s, i, b.Reg)
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		name := i.ApplicationCommandData().Name
		if b.music != nil && music.IsCommand(name) {
			b.music.Handle(ctx, it, musicCommand(i, it))
			return
		}
		b.Cmds.Handle(ctx, it, name)
	case discordgo.InteractionMessageComponent:
		d := i.MessageComponentData()
		// 找不到元件（舊訊息、已逾時）時不回應，Discord 顯示互動失敗（discord.py 相同）
		b.Reg.DispatchComponent(ctx, it, d.CustomID, d.Values)
	case discordgo.InteractionModalSubmit:
		d := i.ModalSubmitData()
		b.Reg.DispatchModal(ctx, it, d.CustomID, modalValues(d.Components))
	}
}

// modalValues 依輸入框順序取出值（custom_id 為 f0、f1…）。
func modalValues(rows []discordgo.MessageComponent) []string {
	var out []string
	for _, r := range rows {
		row, ok := r.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, c := range row.Components {
			if in, ok := c.(*discordgo.TextInput); ok {
				out = append(out, in.Value)
			}
		}
	}
	return out
}

// logSlow 互動送到 Bot 的延遲（Discord 建立互動 → Bot 收到）超過門檻才記錄，用來分辨 10062 是網路慢還是 Bot 慢。
func (b *Bot) logSlow(i *discordgo.Interaction, now time.Time) {
	created, err := discordgo.SnowflakeTimestamp(i.ID)
	if err != nil {
		return
	}
	delay := max(0, now.Sub(created)) // 主機時鐘稍有誤差時可能為負，視為 0
	if delay < SlowInteraction {
		return
	}
	what := "-"
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		what = i.ApplicationCommandData().Name
	case discordgo.InteractionMessageComponent:
		what = i.MessageComponentData().CustomID
	case discordgo.InteractionModalSubmit:
		what = i.ModalSubmitData().CustomID
	}
	if r := []rune(what); len(r) > 40 {
		what = string(r[:40])
	}
	user := "-"
	if i.Member != nil && i.Member.User != nil {
		user = i.Member.User.ID
	} else if i.User != nil {
		user = i.User.ID
	}
	b.Log.Warn("互動送達延遲", "seconds", fmt.Sprintf("%.1f", delay.Seconds()), "type", i.Type.String(), "what", what, "user", user)
}

// settleLoop 定期結算排名獎勵、競賽與世界 Boss；advisory lock 與唯一限制保證不會重複發獎（與網站的 autoSettle 相同）。
func (b *Bot) settleLoop(ctx context.Context) {
	interval := time.Duration(b.Settings.RankingSettleIntervalSeconds * float64(time.Second))
	for {
		settled, err := b.Svc.RunBackgroundSettlement(ctx, "system")
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			b.Log.Warn("自動結算失敗", "type", fmt.Sprintf("%T", err))
		case settled != nil && (len(settled.Rankings) > 0 || len(settled.Competitions) > 0 || len(settled.WorldBoss) > 0):
			b.Log.Info("已自動結算", "rankings", settled.Rankings, "competitions", settled.Competitions, "world_boss", settled.WorldBoss)
		}
		if !sleep(ctx, interval) {
			return
		}
	}
}

// latencyLoop 定期把面板延遲統計寫入日誌後歸零。
func (b *Bot) latencyLoop(ctx context.Context) {
	for sleep(ctx, latency.LogIntervalSeconds*time.Second) {
		b.Cmds.Latency.LogAndReset()
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// startMusic 建立音樂模組並在背景連線 Lavalink（登入後才知道 Bot 的使用者 ID）。
func (b *Bot) startMusic(botID string) {
	st := b.Settings
	if !musicEnabled(st) {
		if st.MusicEnabled && st.LavalinkPassword == "" && !st.MusicBotConfigured {
			b.Log.Warn("未設定 LAVALINK_PASSWORD，音樂功能無法使用")
		}
		return
	}
	cfg := music.Config{Volume: st.MusicVolume, PlaylistLimit: st.MusicPlaylistLimit, Prefix: st.MusicPrefix,
		IdleTimeout: time.Duration(st.MusicIdleTimeout) * time.Second}
	m := music.New(cfg, musicDiscord{b.S, b.Reg}, nil, b.Reg, music.LoadSettings(st.MusicSettingsFile), botID, b.Log)
	node := music.NewClient(st.LavalinkURI, st.LavalinkPassword, botID, m, b.Log)
	m.SetNode(node)
	b.music = m
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go node.Run(ctx)
	b.Log.Info("音樂功能已啟用", "lavalink", st.LavalinkURI)
}

func (b *Bot) onVoiceState(_ *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	if b.music == nil || v.VoiceState == nil {
		return
	}
	before := ""
	if v.BeforeUpdate != nil {
		before = v.BeforeUpdate.ChannelID
	}
	b.music.OnVoiceState(v.GuildID, v.UserID, before, v.ChannelID, v.SessionID)
}

func (b *Bot) onVoiceServer(_ *discordgo.Session, v *discordgo.VoiceServerUpdate) {
	if b.music != nil {
		b.music.OnVoiceServer(v.GuildID, v.Token, v.Endpoint)
	}
}

func (b *Bot) onGuildDelete(_ *discordgo.Session, g *discordgo.GuildDelete) {
	if b.music != nil && g.Guild != nil && !g.Unavailable {
		b.music.OnGuildRemove(g.ID)
	}
}
