package discordio

// 音樂的 Discord 接線（music.Discord 的實作）：語音頻道加入／離開（Gateway op 4，音訊由 Lavalink 連線）、
// 以連線狀態快取判斷使用者與 Bot 所在的語音頻道、送出與編輯頻道訊息，並把語音事件轉給音樂模組。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot/ui"
	"farmgm/internal/config"
	"farmgm/internal/music"
)

// musicEnabled Go 版 Bot 是否載入音樂：MUSIC_ENABLED、沒有另外的音樂 Bot（MUSIC_BOT_TOKEN）、有 Lavalink 密碼。
func musicEnabled(s config.Settings) bool {
	return s.MusicEnabled && !s.MusicBotConfigured && s.LavalinkPassword != ""
}

type musicDiscord struct {
	s   *discordgo.Session
	reg *ui.Registry
}

func (d musicDiscord) botID() string {
	if d.s.State != nil && d.s.State.User != nil {
		return d.s.State.User.ID
	}
	return ""
}

func (d musicDiscord) JoinVoice(guildID, channelID string) error {
	return d.s.ChannelVoiceJoinManual(guildID, channelID, false, true)
}

func (d musicDiscord) LeaveVoice(guildID string) error {
	return d.s.ChannelVoiceJoinManual(guildID, "", false, true)
}

func (d musicDiscord) voiceChannelOf(guildID, userID string) string {
	if d.s.State == nil || userID == "" {
		return ""
	}
	if vs, err := d.s.State.VoiceState(guildID, userID); err == nil && vs != nil {
		return vs.ChannelID
	}
	return ""
}

func (d musicDiscord) UserVoiceChannel(guildID, userID string) string {
	return d.voiceChannelOf(guildID, userID)
}
func (d musicDiscord) BotVoiceChannel(guildID string) string {
	return d.voiceChannelOf(guildID, d.botID())
}

// ChannelHumans 頻道內非 Bot 的人數；不知道是不是 Bot 時當作真人（寧可晚點離開）。
func (d musicDiscord) ChannelHumans(guildID, channelID string) int {
	if d.s.State == nil {
		return 0
	}
	bot := d.botID()
	d.s.State.RLock()
	defer d.s.State.RUnlock()
	var g *discordgo.Guild
	for _, x := range d.s.State.Guilds {
		if x.ID == guildID {
			g = x
		}
	}
	if g == nil {
		return 0
	}
	n := 0
	for _, vs := range g.VoiceStates {
		if vs.ChannelID != channelID || vs.UserID == bot {
			continue
		}
		isBot := false
		if vs.Member != nil && vs.Member.User != nil {
			isBot = vs.Member.User.Bot
		}
		if !isBot {
			n++
		}
	}
	return n
}

func (d musicDiscord) Guild(guildID string) (string, string) {
	if d.s.State == nil {
		return "", ""
	}
	g, err := d.s.State.Guild(guildID)
	if err != nil || g == nil {
		return "", ""
	}
	return g.Name, g.IconURL("")
}

// Send 送出頻道訊息（含元件時先登記，與互動回應相同）。
func (d musicDiscord) Send(ctx context.Context, channelID string, m ui.Message) (string, error) {
	if m.View != nil && d.reg != nil {
		d.reg.Bind(m.View)
	}
	e := encode(m, false)
	body, err := client{d.s}.send(ctx, http.MethodPost, API+"channels/"+channelID+"/messages", e.msg, e.files)
	if err != nil {
		return "", err
	}
	var msg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &msg); err != nil || msg.ID == "" {
		return "", fmt.Errorf("送出訊息沒有回傳 ID")
	}
	return msg.ID, nil
}

// Edit 編輯頻道訊息。
func (d musicDiscord) Edit(ctx context.Context, channelID, messageID string, m ui.Message) error {
	if m.View != nil && d.reg != nil {
		d.reg.Bind(m.View)
	}
	e := encode(m, true)
	_, err := client{d.s}.send(ctx, http.MethodPatch, API+"channels/"+channelID+"/messages/"+messageID, e.msg, e.files)
	return err
}

// musicCommand 斜線指令 → music.Command。
func musicCommand(i *discordgo.Interaction, it *Interaction) music.Command {
	c := music.Command{Name: i.ApplicationCommandData().Name, Options: map[string]any{}, GuildID: i.GuildID, ChannelID: i.ChannelID,
		UserID: it.UserID(), UserName: it.UserName(), AvatarURL: it.AvatarURL()}
	for _, o := range i.ApplicationCommandData().Options {
		switch o.Type {
		case discordgo.ApplicationCommandOptionString:
			c.Options[o.Name] = o.StringValue()
		case discordgo.ApplicationCommandOptionInteger:
			c.Options[o.Name] = o.IntValue()
		case discordgo.ApplicationCommandOptionNumber:
			c.Options[o.Name] = o.FloatValue()
		}
	}
	if i.Member != nil {
		p := i.Member.Permissions
		c.CanManageGuild = p&discordgo.PermissionManageGuild != 0 || p&discordgo.PermissionAdministrator != 0
	}
	return c
}

// wireOptionOf 音樂指令參數 → Discord 指令格式。
func wireOptionOf(o music.Option) wireCmdOption {
	t := 3 // STRING
	switch o.Type {
	case "integer":
		t = 4
	case "number":
		t = 10
	}
	w := wireCmdOption{Type: t, Name: o.Name, Description: o.Description, Required: o.Required}
	for _, c := range o.Choices {
		w.Choices = append(w.Choices, wireChoice{c, c})
	}
	return w
}
