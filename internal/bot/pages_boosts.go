package bot

// 增益道具頁（Go 版新增，2026-11-18；從「🎒 背包」進入）：雨衣稻草人、灑水器、幸運香包、寵物零食的持有數量、
// 效果狀態與使用按鈕。雨衣稻草人、灑水器在道具商店購買；幸運香包、寵物零食用加工製作。

import (
	"context"
	"fmt"

	"farmgm/internal/bot/ui"
	"farmgm/internal/services"
)

func (p *Panel) loadBoosts(ctx context.Context) (any, error) { return p.svc().Boosts(ctx, p.playerID) }

func (p *Panel) buildBoosts(_ context.Context, data any) (*ui.Embed, error) {
	list := data.([]services.BoostView)
	blocks := make([]string, len(list))
	for i, b := range list {
		from := fmt.Sprintf("🏪 道具商店 %s 金", money(b.Price))
		if b.Recipe != "" {
			from = "🏭 " + b.Recipe
		}
		state := "💤 未使用"
		if b.ActiveUntil != nil {
			state = "✨ 效果中，" + ts(b.ActiveUntil) + "結束"
		}
		blocks[i] = fmt.Sprintf("%s **%s**　持有 **%d**\n　📝 %s\n　%s\n　%s", b.Emoji, b.Name, b.Have, b.Desc, from, state)
	}
	e := &ui.Embed{Title: "✨ ✦ 增益道具 ✦", Color: Blue, Description: sections(
		Section{Lines: blocks, Sep: "\n\n"},
		"效果期間再使用一次可以延長時間。",
	)}
	for _, b := range list {
		label := "使用" + b.Name
		if b.ActiveUntil != nil {
			label = "延長" + b.Name
		}
		p.button(fmt.Sprintf("%s（%d）", label, b.Have), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("use_boost", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().UseBoost(ctx, p.playerID, b.Code)
			}), actOpts{})
			return nil
		}, btn{emoji: b.Emoji, row: 0, style: ui.Primary, disabled: b.Have == 0})
	}
	p.nav("商店", "shop", btn{emoji: "🏪", row: 1})
	p.nav("加工", "craft", btn{emoji: "🏭", row: 1})
	p.nav("回背包", "backpack", btn{emoji: "🎒", row: 1})
	p.navBack(1)
	return e, nil
}
