package discordio

// Bot 本體的測試（對應 Python tests/test_music.py 的 test_intents_and_music_toggle 中農場的部分、
// test_slow_interaction_delivery_is_logged；Go 版另加指令同步的四種情況與新加入伺服器的判斷）。

import (
	"bytes"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot"
	"farmgm/internal/bot/ui"
	"farmgm/internal/config"
)

func testBot(t *testing.T, s config.Settings) (*Bot, *bytes.Buffer) {
	t.Helper()
	s.DiscordBotToken = "test-token"
	b, err := New(s, bot.NewCommands(nil, bot.NewRunner(1), ui.NewRegistry()), nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	b.Log = slog.New(slog.NewTextHandler(&buf, nil))
	return b, &buf
}

func TestIntentsOnlyGuildsWithoutMusic(t *testing.T) {
	b, _ := testBot(t, config.Settings{})
	// 只有農場：只需要伺服器基本資料，不要求語音或訊息內容等特權 Intent（音樂不在 Go 版 Bot 內）
	if b.S.Identify.Intents != discordgo.IntentsGuilds {
		t.Fatal(b.S.Identify.Intents)
	}
}

func TestSyncCommands(t *testing.T) {
	cases := []struct {
		name     string
		settings config.Settings
		path     string // "" = 不同步
		log      string
	}{
		{"開發伺服器", config.Settings{DiscordSyncCommands: true, DiscordDevGuildID: 555, DiscordApplicationID: 777},
			"applications/777/guilds/555/commands", "已同步指令到開發伺服器"},
		{"正式環境全域", config.Settings{DiscordSyncCommands: true, AppEnv: "production"},
			"applications/bot-user/commands", "已全域同步指令"},
		{"開發環境沒有伺服器 ID", config.Settings{DiscordSyncCommands: true, AppEnv: "development"}, "", "未同步指令"},
		{"關閉同步", config.Settings{DiscordSyncCommands: false, AppEnv: "production"}, "", "略過指令同步"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			b, logs := testBot(t, c.settings)
			b.syncCommands("bot-user")
			reqs := f.requests()
			if c.path == "" {
				if len(reqs) != 0 {
					t.Fatal(reqs)
				}
			} else {
				if len(reqs) != 1 || reqs[0].Method != "PUT" || reqs[0].Path != c.path {
					t.Fatalf("%+v", reqs)
				}
			}
			if !strings.Contains(logs.String(), c.log) {
				t.Fatal(logs.String())
			}
		})
	}
}

func TestSyncCommandsForbiddenIsExplained(t *testing.T) {
	f := newFake(t)
	f.srv.Config.Handler = forbidden{}
	b, logs := testBot(t, config.Settings{DiscordSyncCommands: true, DiscordDevGuildID: 555})
	b.S.MaxRestRetries = 0
	b.syncCommands("bot-user")
	if !strings.Contains(logs.String(), "applications.commands 權限") || strings.Contains(logs.String(), "test-token") {
		t.Fatal(logs.String())
	}
}

type forbidden struct{}

func (forbidden) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(403)
	_, _ = w.Write([]byte(`{"message":"Missing Access","code":50001}`))
}

// snowflake 指定時間建立的 Discord ID。
func snowflake(t time.Time) string {
	return strconv.FormatInt((t.UnixMilli()-1420070400000)<<22, 10)
}

func TestSlowInteractionDeliveryIsLogged(t *testing.T) {
	b, logs := testBot(t, config.Settings{})
	now := time.Now()
	at := func(ago time.Duration, name string) *discordgo.Interaction {
		return &discordgo.Interaction{ID: snowflake(now.Add(-ago)), Type: discordgo.InteractionApplicationCommand,
			User: &discordgo.User{ID: "42"}, Data: discordgo.ApplicationCommandInteractionData{Name: name}}
	}
	b.logSlow(at(-time.Second, "farm"), now) // 時鐘誤差（負值）：視為 0
	b.logSlow(at(200*time.Millisecond, "farm"), now)
	if strings.Contains(logs.String(), "互動送達延遲") {
		t.Fatal(logs.String())
	}
	b.logSlow(at(3500*time.Millisecond, "farm"), now)
	if out := logs.String(); !strings.Contains(out, "互動送達延遲") || !strings.Contains(out, "farm") || !strings.Contains(out, "ApplicationCommand") ||
		!strings.Contains(out, "user=42") {
		t.Fatal(out)
	}
}

func TestOnlyGuildsJoinedAfterReadyAreNew(t *testing.T) {
	b, _ := testBot(t, config.Settings{DiscordSyncCommands: false})
	if b.newGuild("1") { // 登入前（啟動時的 GUILD_CREATE）
		t.Fatal("登入前不是新加入")
	}
	b.onReady(b.S, &discordgo.Ready{User: &discordgo.User{ID: "bot", Username: "bot"}, Guilds: []*discordgo.Guild{{ID: "2"}}})
	if b.newGuild("2") || b.newGuild("1") { // 登入時清單中的伺服器、之前收過的伺服器
		t.Fatal("已在的伺服器不是新加入")
	}
	if !b.newGuild("3") || b.newGuild("3") { // 登入後才收到：新加入（只算一次）
		t.Fatal("登入後收到的新伺服器應視為新加入一次")
	}
}
