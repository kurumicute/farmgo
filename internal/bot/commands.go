package bot

// 斜線指令（對應 Python app/discord_bot/commands.py 的預設註冊：/farm 與 /help）。
//
// 身分一律取自互動來源：玩家 = 互動的使用者 ID，群組 = 互動所在的伺服器；不接受玩家輸入的 ID。
// 進階文字指令（DISCORD_ADVANCED_COMMANDS：/farm-cmd 等約 40 個）依賴 CMD（app/cli），CMD 決定不移植，因此不提供
// （預設本來就關閉）。對應的 Python 回覆流程（respond：2 秒內直接回覆、否則先 defer、太長改附件）只有進階指令使用，
// 一併不移植。

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"farmgm/internal/bot/latency"
	"farmgm/internal/bot/ui"
	"farmgm/internal/services"
)

// Platform 外部帳號的平台名稱。
const Platform = "discord"

const (
	MsgNeedGuild = "這個指令需要在 Discord 伺服器中使用（伺服器就是遊戲中的群組）；私訊中無法判斷群組。"
	MsgNoFarm    = "你還沒有農場，請在 Discord 伺服器中使用 /farm start 建立（農場會屬於該伺服器的群組）。"
)

// Who 從互動取出的身分資料（Python Who）。GuildID 為 "" 表示私訊。
type Who struct {
	UserID, UserName, GuildID, GuildName string
}

func whoOf(it ui.Interaction) Who {
	return Who{it.UserID(), it.UserName(), it.GuildID(), it.GuildName()}
}

func (w Who) guildPtr() *string {
	if w.GuildID == "" {
		return nil
	}
	g := w.GuildID
	return &g
}

// Commands 指令與面板共用的依賴（Python FarmCommands）。
type Commands struct {
	Service  *services.AdminService
	Runner   *Runner
	Registry *ui.Registry
	Latency  *latency.Stats
	// AutoCheckin 開啟面板時自動每日簽到（Python FarmPanel.auto_checkin；測試預設關閉，避免改變其他測試的金幣與庫存）
	AutoCheckin bool
	// Now 面板判斷「今天已簽到」用的時間（Python datetime.now(UTC)）；nil 為系統時間
	Now func() time.Time

	onPanel func(*Panel)  // 測試用：取得剛開啟的面板
	images  chan struct{} // 同時產生圖片的上限（CPU 核心數）
}

// NewCommands 建立指令（AutoCheckin 預設開啟）。
func NewCommands(svc *services.AdminService, runner *Runner, reg *ui.Registry) *Commands {
	return &Commands{Service: svc, Runner: runner, Registry: reg, Latency: latency.New(), AutoCheckin: true,
		images: make(chan struct{}, runtime.GOMAXPROCS(0))}
}

func (c *Commands) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// CommandSpec 要向 Discord 註冊的斜線指令（Python register 的預設：只有 /farm 與 /help）。
type CommandSpec struct {
	Name, Description string
}

// Specs 預設註冊的指令。
func Specs() []CommandSpec {
	return []CommandSpec{
		{"farm", "打開你的農場面板（種田、背包、商店、動物、排行…全部用按鈕操作）"},
		{"help", "遊戲玩法說明"},
	}
}

// Handle 依指令名稱處理斜線指令；不認得的指令回傳 false。
func (c *Commands) Handle(ctx context.Context, it ui.Interaction, name string) bool {
	switch name {
	case "farm":
		c.OpenPanel(ctx, it)
	case "help":
		embed := HelpEmbed()
		if err := it.SendMessage(ctx, ui.Message{Embed: embed, Ephemeral: true}); err != nil {
			slog.Warn("回覆說明失敗", "type", fmt.Sprintf("%T", err))
		}
	default:
		return false
	}
	return true
}

// OpenPanel 開啟私密的農場面板（Python open_panel）。先 defer（3 秒規則），讀取資料後以 followup 送出面板。
func (c *Commands) OpenPanel(ctx context.Context, it ui.Interaction) {
	w := whoOf(it)
	started := time.Now()
	if err := it.Defer(ctx, true); err != nil {
		// 互動送到 Bot 時已超過 Discord 的 3 秒期限（通常是網路延遲），已無法回覆；玩家再輸入一次 /farm 即可
		slog.Warn("開啟面板逾時（互動已失效）", "user", w.UserID, "type", fmt.Sprintf("%T", err))
		return
	}
	pctx, err := Call(ctx, c.Runner, "external_context", func(ctx context.Context) (services.ExternalContext, error) {
		return c.Service.ExternalContext(ctx, Platform, w.UserID, w.guildPtr(), w.GuildName)
	})
	if err != nil {
		logUnexpected(err, "開啟面板失敗", "user", w.UserID)
		msg := "❌ " + PlayerMessage(err)
		_, _ = it.Followup(ctx, ui.Message{Content: &msg, Ephemeral: true})
		return
	}
	p := NewPanel(c, w, pctx)
	p.AvatarURL = it.AvatarURL()
	if c.onPanel != nil {
		c.onPanel(p)
	}
	embed := p.render(ctx, "", nil) // 面板還沒送出，沒有其他互動會同時修改它
	msg := ui.Message{Embed: embed, View: p.View, Ephemeral: true, Files: p.files}
	sent, err := it.Followup(ctx, msg)
	if err != nil {
		slog.Warn("送出面板失敗", "type", fmt.Sprintf("%T", err))
		return
	}
	p.message = sent
	p.sentDigest = p.imageDigest
	c.Latency.Record("開啟面板", msSince(started), false)
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }
