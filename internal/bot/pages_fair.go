package bot

// 面板頁面：品評會（Go 版新增，2026-11-20，Migration 0048；從總覽的「🧭 更多功能」進入）。

import (
	"context"
	"fmt"
	"strings"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/fair"
	"farmgm/internal/services"
)

func (p *Panel) loadFair(ctx context.Context) (any, error) {
	return p.svc().Fair(ctx, p.playerID)
}

func (p *Panel) buildFair(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.FairView)
	e := &ui.Embed{Title: p.s("🎀 ✦ 品評會 ✦"), Color: Gold}
	if !v.HasGroup {
		e.Description = p.s("品評會以伺服器為單位舉辦，要先加入伺服器的群組才能參加。")
		p.navBack(0)
		return e, nil
	}
	medals := map[int]string{1: "🥇", 2: "🥈", 3: "🥉"}
	parts := []any{
		p.f("**%s**　本週：**%s**\n🧑‍⚖️ %s（符合的作品評審分數 +%d%%）", p.s(v.GroupName), p.s(fair.CategoryNames[v.Category]),
			p.s(v.Pref.Text), v.Pref.Pct),
		p.f("報名費 %d 金幣，作品會被收走；每人 %d 票投給別人的作品。名次 = 評審 %d 成＋人氣 %d 成，前三名分獎池（報名費 ×%d%%），其他人得到 1 片藏寶圖碎片；不到 %d 件流會退費。",
			fair.EntryFee, fair.VotesPerWeek, fair.JudgeWeight, fair.VoteWeight, fair.PoolPct, fair.MinEntries),
	}
	if len(v.Last) > 0 {
		var lines []string
		switch v.LastState {
		case "refunded":
			lines = append(lines, p.s("上週報名不足，流會（已退回報名費與作品）。"))
		case "pending":
			lines = append(lines, p.s("上週的結果結算中，稍後再來看。"))
		default:
			for _, x := range v.Last {
				if x.Rank == 0 || x.Rank > 3 {
					continue
				}
				lines = append(lines, p.f("%s %s：%s（評審 %d、👍 %d）💰 %s", medals[x.Rank], x.PlayerName, p.s(x.ItemName),
					x.JudgeScore, x.Votes, money(x.Reward)))
			}
		}
		parts = append(parts, Section{Title: p.s("上週結果"), Lines: lines})
	}
	var lines []string
	for _, x := range v.Entries {
		mark := "🖼️"
		switch {
		case x.Mine:
			mark = "⭐"
		case x.Voted:
			mark = "👍"
		}
		lines = append(lines, p.f("%s %s：%s　👍 %d", mark, x.PlayerName, p.s(x.ItemName), x.Votes))
	}
	if len(lines) == 0 {
		lines = []string{p.s("還沒有人報名，搶先展示你的作品吧！")}
	}
	parts = append(parts, Section{Title: p.f("本週作品（%d 件）", len(v.Entries)), Lines: lines})
	// 截止時間、我的狀態放在最後，貼近下方選單
	status := p.f("⏰ 報名與投票 %s截止，%s公布結果", tsv(v.CloseAt), tsv(v.ResultsAt))
	if !v.Open {
		status = p.f("⏰ 本週已截止，%s公布結果", tsv(v.ResultsAt))
	}
	if v.Mine != nil {
		status += "\n" + p.f("⭐ 你的作品：%s　👍 %d", p.s(v.Mine.ItemName), v.Mine.Votes)
	} else if v.Open {
		status += "\n" + p.s("你本週還沒報名。")
	}
	if v.Open {
		status += "\n" + p.f("🗳️ 還剩 %d 票", v.VotesLeft)
	}
	parts = append(parts, status)
	e.Description = runes(sections(parts...), 4096)

	if v.Open && v.Mine == nil {
		var options []option
		for _, o := range v.Options {
			emoji := "🏆"
			desc := p.f("評審分數約 %d（持有 %d）", o.Value, o.Have)
			if o.Liked {
				emoji, desc = "✨", desc+p.s("・符合評審偏好")
			}
			options = append(options, option{p.s(o.Name), o.Code + "|" + o.Quality + "|" + o.Mutation, desc, emoji})
		}
		holder := p.f("🏆 報名作品（報名費 %d 金幣）…", fair.EntryFee)
		if len(options) == 0 {
			e.Footer = p.f("倉庫裡沒有%s的作品可以報名。", p.s(fair.CategoryNames[v.Category]))
		}
		p.selectMenu(holder, options, func(ctx context.Context, it ui.Interaction, value string) error {
			var code, quality, mutation string
			if parts := strings.SplitN(value, "|", 3); len(parts) == 3 {
				code, quality, mutation = parts[0], parts[1], parts[2]
			} else {
				return nil
			}
			p.act(ctx, it, p.call("enter_fair", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().EnterFair(ctx, p.playerID, code, quality, mutation)
			}), actOpts{})
			return nil
		}, 0, "")
	}
	if v.Open && v.VotesLeft > 0 {
		var options []option
		for _, x := range v.Entries {
			if x.Mine || x.Voted {
				continue
			}
			options = append(options, option{p.f("%s：%s", x.PlayerName, p.s(x.ItemName)), fmt.Sprint(x.ID), p.f("目前 👍 %d", x.Votes), "👍"})
		}
		p.selectMenu(p.f("👍 投票給喜歡的作品（還剩 %d 票）…", v.VotesLeft), options, func(ctx context.Context, it ui.Interaction, value string) error {
			id, ok := parseDigits(value)
			if !ok {
				return nil
			}
			p.act(ctx, it, p.call("vote_fair", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().VoteFair(ctx, p.playerID, int64(id))
			}), actOpts{noProgress: true})
			return nil
		}, 1, "")
	}
	p.button(p.s("重新整理"), func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "fair", "")
		return nil
	}, btn{emoji: "🔄", row: 2})
	p.navBack(2)
	return e, nil
}
