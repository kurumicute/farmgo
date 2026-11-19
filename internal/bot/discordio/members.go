package discordio

// 清除已離開伺服器的群組成員：啟動時與每天台灣時間 00:00 各比對一次。
// 使用「取得單一成員」API（GET /guilds/{guild}/members/{user}），不需要特權 Intent GUILD_MEMBERS；
// 只有 Discord 明確回覆 Unknown Member（10007）才移除，其他錯誤（網路、權限、Bot 已不在該伺服器）一律略過，避免誤刪。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot"
	"farmgm/internal/clock"
	"farmgm/internal/repositories/groups"
)

// memberSweepLoop 等 Bot 登入後先比對一次，之後每天台灣時間 00:00 比對。
func (b *Bot) memberSweepLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-b.readyCh:
	}
	b.sweepLeftMembers(ctx, "啟動")
	for {
		now := time.Now()
		if !sleep(ctx, nextTaipeiMidnight(now).Sub(now)) {
			return
		}
		b.sweepLeftMembers(ctx, "每日")
	}
}

// nextTaipeiMidnight now 之後的下一個台灣時間 00:00。
func nextTaipeiMidnight(now time.Time) time.Time {
	l := now.In(clock.TaipeiTZ)
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, clock.TaipeiTZ)
}

// memberCheck 查詢成員的結果。
type memberCheck int

const (
	memberPresent memberCheck = iota
	memberGone                // 確定已不在伺服器
	guildSkip                 // Bot 無法查詢該伺服器（已不在伺服器或沒有權限），略過整個伺服器
	checkFailed               // 其他錯誤，這次略過
)

func classifyMemberErr(err error) memberCheck {
	if err == nil {
		return memberPresent
	}
	var rest *discordgo.RESTError
	if !errors.As(err, &rest) {
		return checkFailed
	}
	if rest.Message != nil {
		switch rest.Message.Code {
		case discordgo.ErrCodeUnknownMember:
			return memberGone
		case discordgo.ErrCodeUnknownGuild, discordgo.ErrCodeMissingAccess:
			return guildSkip
		}
	}
	if rest.Response != nil && rest.Response.StatusCode == http.StatusForbidden {
		return guildSkip
	}
	return checkFailed
}

// sweepLeftMembers 逐一確認群組成員是否仍在對應的 Discord 伺服器，已離開的移出群組（農場與紀錄保留）。
// 同時只會執行一次；Discord 的速率限制由 discordgo 自動等待。
func (b *Bot) sweepLeftMembers(ctx context.Context, reason string) {
	if !b.sweepMu.TryLock() {
		return
	}
	defer b.sweepMu.Unlock()
	list, err := bot.Call(ctx, b.Cmds.Runner, "external_guild_members", func(ctx context.Context) ([]groups.ExternalMember, error) {
		return b.Svc.ExternalGuildMembers(ctx, bot.Platform)
	})
	if err != nil {
		b.Log.Warn("讀取伺服器群組成員失敗", "reason", reason, "type", fmt.Sprintf("%T", err))
		return
	}
	skip := map[string]bool{}
	var checked, removed, failed int
	for _, m := range list {
		if ctx.Err() != nil {
			return
		}
		if skip[m.GuildID] {
			continue
		}
		_, err := b.S.GuildMember(m.GuildID, m.UserID, discordgo.WithContext(ctx))
		checked++
		switch classifyMemberErr(err) {
		case memberGone:
			ok, err := bot.Call(ctx, b.Cmds.Runner, "leave_guild_group", func(ctx context.Context) (bool, error) {
				return b.Svc.LeaveExternalGroup(ctx, bot.Platform, m.UserID, m.GuildID)
			})
			if err != nil {
				failed++
			} else if ok {
				removed++
			}
		case guildSkip:
			skip[m.GuildID] = true
		case checkFailed:
			failed++
		}
	}
	b.Log.Info("已比對離開伺服器的群組成員", "reason", reason, "checked", checked, "removed", removed,
		"failed", failed, "skipped_guilds", len(skip))
}
