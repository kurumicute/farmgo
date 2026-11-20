package bot

// 面板頁面：NPC 限時訂單、藏寶圖（Go 版新增，2026-11-20；從總覽的「🧭 更多功能」進入）。

import (
	"context"
	"fmt"
	"strings"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/npcorder"
	"farmgm/internal/game/treasure"
	"farmgm/internal/services"
)

// ======================================================================
// NPC 訂單
// ======================================================================

func (p *Panel) loadOrders(ctx context.Context) (any, error) {
	return p.svc().NpcOrders(ctx, p.playerID)
}

func (p *Panel) buildOrders(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.NpcOrdersView)
	e := &ui.Embed{Title: p.s("🧑‍🌾 ✦ NPC 訂單 ✦"), Color: Teal}
	parts := []any{p.f("客人會來下單，期限內交貨就能拿到比市價高的報酬（%d%%～%d%%），還有機會撿到🧩藏寶圖碎片。",
		npcorder.MultMin, npcorder.MultMax)}
	var codes []string
	for i, o := range v.Orders {
		codes = append(codes, o.NPC.Code)
		var lines []string
		lines = append(lines, "> "+p.s(o.NPC.Line))
		for _, l := range o.Items {
			mark := "✅"
			if l.Have < l.Qty {
				mark = "❌"
			}
			lines = append(lines, p.f("%s %s ×%d（持有 %d）", mark, p.s(l.Name), l.Qty, l.Have))
		}
		lines = append(lines, p.f("💰 報酬 **%s** 金（直接賣約 %s）　⏰ %s到期", money(o.Reward), money(o.Value), tsv(o.Expires)))
		title := p.f("#%d %s %s", i+1, o.NPC.Emoji, p.s(o.NPC.Name))
		if o.NPC.VIP {
			title += p.s("　✨ VIP")
		}
		parts = append(parts, Section{Title: title, Lines: lines})
	}
	if len(v.Orders) == 0 {
		parts = append(parts, p.s("目前沒有客人。"))
	}
	streak := p.f("🔥 連續完成 %d／%d 張（第 %d 張多 %d%% 報酬；有訂單過期就重新計算）",
		v.Streak, npcorder.StreakTarget, npcorder.StreakTarget, npcorder.StreakBonusPct)
	refill := p.f("🕓 每 %d 小時補到 %d 張，下次補單 %s", npcorder.RefillSeconds/3600, npcorder.Slots, tsv(v.RefillAt))
	parts = append(parts, streak, refill)
	e.Description = runes(sections(parts...), 4096)
	fee := p.s("今天的免費換單還沒用")
	if v.RerollFee > 0 {
		fee = p.f("換單費 %d 金幣", v.RerollFee)
	}
	e.Footer = p.f("交貨時先交品質最低的作物；黃金、珍稀變種不會被交出去。%s。", fee)
	if v.Rainbow {
		e.Footer += "\n" + p.s("🌈 彩虹日：藏寶圖碎片掉率加倍")
	}
	p.attachImage(e, p.image(p.s("NPC 訂單"), func() ([]byte, error) { return farmimage.PortraitsPNG(codes) }))

	for i, o := range v.Orders {
		id := o.ID
		style := ui.Secondary
		if o.Ready {
			style = ui.Success
		}
		p.button(p.f("交貨 #%d", i+1), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("deliver_npc_order", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().DeliverNpcOrder(ctx, p.playerID, id)
			}), actOpts{})
			return nil
		}, btn{emoji: o.NPC.Emoji, style: style, row: 0, disabled: !o.Ready})
	}
	p.button(p.s("重新整理"), func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "orders", "")
		return nil
	}, btn{emoji: "🔄", row: 0})
	var options []option
	for i, o := range v.Orders {
		desc := p.s("今天的免費換單")
		if v.RerollFee > 0 {
			desc = p.f("換單費 %d 金幣", v.RerollFee)
		}
		options = append(options, option{p.f("換掉 #%d %s", i+1, p.s(o.NPC.Name)), fmt.Sprint(o.ID), desc, "🔁"})
	}
	p.selectMenu(p.s("🔁 不想做這張？換一張訂單…"), options, func(ctx context.Context, it ui.Interaction, value string) error {
		id, ok := parseDigits(value)
		if !ok {
			return nil
		}
		p.act(ctx, it, p.call("reroll_npc_order", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().RerollNpcOrder(ctx, p.playerID, id)
		}), actOpts{noProgress: true})
		return nil
	}, 1, "")
	p.nav(p.s("藏寶圖"), "treasure", btn{emoji: "🗺️", row: 2})
	p.navBack(2)
	return e, nil
}

// ======================================================================
// 藏寶圖
// ======================================================================

func (p *Panel) loadTreasure(ctx context.Context) (any, error) {
	return p.svc().TreasureMap(ctx, p.playerID)
}

func (p *Panel) pieceName(code string) string { return p.s(treasure.PieceNames[code]) }

func (p *Panel) buildTreasure(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.TreasureView)
	e := &ui.Embed{Title: p.s("🗺️ ✦ 藏寶圖 ✦"), Color: Teal}
	var pieces []string
	for _, c := range treasure.Pieces {
		mark := "🧩"
		if v.Pieces[c] == 0 {
			mark = "▫️"
		}
		pieces = append(pieces, p.f("%s %s ×%d", mark, p.pieceName(c), v.Pieces[c]))
	}
	site := v.Site
	status := p.s("集滿東、南、西、北各 1 片碎片，就能拼成藏寶圖出發尋寶。")
	switch {
	case v.Done:
		site = v.HuntSite
		status = p.f("🎁 尋寶隊從 %s %s回來了，快開箱！", site.Emoji, p.s(site.Name))
	case v.Hunting:
		site = v.HuntSite
		status = p.f("🚶 尋寶隊正在 %s %s挖寶，%s回來。", site.Emoji, p.s(site.Name), ts(v.HuntEnd))
	case v.Complete:
		status = p.s("✨ 碎片湊齊了！可以出發尋寶。")
	}
	sources := p.f("探險 %s%%、手動釣魚 %s%%、偷菜成功 %s%%、NPC 訂單 %s%%", bpPct(treasure.DropBP[treasure.SourceExplore]),
		bpPct(treasure.DropBP[treasure.SourceFish]), bpPct(treasure.DropBP[treasure.SourceSteal]), bpPct(treasure.DropBP[treasure.SourceOrder]))
	rainbow := ""
	if v.Rainbow {
		rainbow = p.s("🌈 今天是彩虹日：碎片掉率加倍")
		if !v.RainbowClaimed {
			rainbow += p.s("，還可以免費領 1 片！")
		}
	}
	e.Description = runes(sections(
		Section{Title: p.s("持有的碎片"), Lines: []string{strings.Join(pieces, p.s("　"))}},
		Section{Title: p.s("本週寶藏地點"), Lines: []string{p.f("%s **%s**：%s", v.Site.Emoji, p.s(v.Site.Name), p.s(v.Site.Description))}},
		Section{Title: p.s("碎片來源"), Lines: []string{sources, rainbow}},
		p.f("🧭 已去過 %d／%d 個寶藏地點", len(v.Found), len(treasure.SiteOrder)),
		status,
	), 4096)
	e.Footer = p.f("重複的碎片：%d 片相同的碎片加 %d 金幣，可以換 1 片指定方位的碎片。寶藏地點每週一換。",
		treasure.ExchangeCost, treasure.ExchangeFee)
	p.attachImage(e, p.image(p.s("寶藏地點"), func() ([]byte, error) { return farmimage.TreasurePNG(site) }))

	if v.Hunting {
		p.button(p.s("開箱"), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("open_treasure", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().OpenTreasure(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🎁", style: ui.Success, row: 0, disabled: !v.Done})
	} else {
		p.button(p.s("拼好藏寶圖，出發尋寶"), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("start_treasure_hunt", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().StartTreasureHunt(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🗺️", style: ui.Success, row: 0, disabled: !v.Complete})
	}
	if v.Rainbow && !v.RainbowClaimed {
		p.button(p.s("領彩虹碎片"), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("claim_rainbow_piece", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().ClaimRainbowPiece(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🌈", style: ui.Primary, row: 0})
	}
	p.button(p.s("重新整理"), func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "treasure", "")
		return nil
	}, btn{emoji: "🔄", row: 0})
	var options []option
	for _, from := range treasure.Pieces {
		if v.Pieces[from] < treasure.ExchangeCost {
			continue
		}
		for _, to := range treasure.Pieces {
			if to != from {
				options = append(options, option{p.f("%d 片%s換 1 片%s", treasure.ExchangeCost, p.pieceName(from), p.pieceName(to)),
					from + ":" + to, p.f("手續費 %d 金幣", treasure.ExchangeFee), "🧩"})
			}
		}
	}
	p.selectMenu(p.s("🧩 用重複的碎片換需要的方位…"), options, func(ctx context.Context, it ui.Interaction, value string) error {
		from, to, ok := strings.Cut(value, ":")
		if !ok {
			return nil
		}
		p.act(ctx, it, p.call("exchange_pieces", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().ExchangePieces(ctx, p.playerID, from, to)
		}), actOpts{noProgress: true})
		return nil
	}, 1, "")
	p.nav(p.s("NPC 訂單"), "orders", btn{emoji: "🧑‍🌾", row: 2})
	p.navBack(2)
	return e, nil
}
