package bot

// 自動販售機設定頁（2026-11-22 使用者要求，從背包頁移出）：
//   - 第 1 列：勾選要自動賣的東西（每種作物、雜交作物、自動釣魚機的魚）。
//   - 第 2、3 列：保留條件。先選作物（或「所有作物」），再勾選要保留的品質／突變／可以加工的；
//     符合條件的收成放進倉庫，其他的自動賣出。例如小麥只保留可以加工的。

import (
	"context"
	"strings"

	"farmgm/internal/bot/ui"
	"farmgm/internal/services"
)

func (p *Panel) loadAutoSeller(ctx context.Context) (any, error) {
	return p.svc().AutoSeller(ctx, p.playerID)
}

// autoSellerSummary 販售項目與保留條件的摘要（背包頁與設定頁共用）。
func (p *Panel) autoSellerSummary(sv services.AutoSellerView) string {
	var picked []string
	for _, o := range sv.Options {
		if o.Selected {
			picked = append(picked, p.s(o.Name))
		}
	}
	text := p.s("賣：") + p.s("全部")
	if !sv.All {
		text = p.s("賣：") + strings.Join(picked, p.s("、"))
	}
	if !sv.Enabled {
		text += p.s("（目前停用）")
	}
	text = runes(text, 700)
	if keep := p.keepLines(sv); len(keep) > 0 {
		text += "\n" + p.s("保留：") + runes(strings.Join(keep, p.s("；")), 300)
	}
	return text
}

// keepLines 每條保留條件一段：「所有作物：可以加工的」「小麥：不保留」。
func (p *Panel) keepLines(sv services.AutoSellerView) []string {
	var out []string
	line := func(name string, conds []string) string {
		if len(conds) == 0 {
			return p.s(name) + p.s("：") + p.s("不保留")
		}
		names := make([]string, len(conds))
		for i, c := range conds {
			names[i] = p.s(services.KeepName(c))
		}
		return p.s(name) + p.s("：") + strings.Join(names, p.s("、"))
	}
	if conds, ok := sv.Keep[services.AutoSellKeepAll]; ok {
		out = append(out, line("所有作物", conds))
	}
	for _, o := range sv.Options {
		if conds, ok := sv.Keep[o.Code]; ok {
			out = append(out, line(o.Name, conds))
		}
	}
	return out
}

func (p *Panel) buildAutoSeller(_ context.Context, data any) (*ui.Embed, error) {
	sv := data.(services.AutoSellerView)
	e := &ui.Embed{Title: p.s("🏧 ✦ 自動販售機設定 ✦"), Color: Blue,
		Description: p.s("有勾選販售的作物收成時直接賣掉；符合**保留條件**的放進倉庫（例如小麥只保留可以加工的）。\n沒勾選的作物、魚放進倉庫與魚簍。")}
	if !sv.Owned {
		e.AddField(p.s("🏧 自動販售機"), p.s("你還沒有自動販售機，可以到商店購買。"), false)
		p.nav(p.s("返回背包"), "backpack", btn{emoji: "↩️", row: 0})
		return e, nil
	}
	e.AddField(p.s("🧾 目前設定"), p.autoSellerSummary(sv), false)
	key := p.sellerKeepKey
	if key == "" {
		key = services.AutoSellKeepAll
	}
	keyName := p.s("所有作物")
	for _, o := range sv.Options {
		if o.Code == key {
			keyName = p.s(o.Name)
		}
	}
	conds, own := sv.Keep[key]
	if !own && key != services.AutoSellKeepAll {
		conds = sv.Keep[services.AutoSellKeepAll]
	}
	cur := p.s("不保留")
	if len(conds) > 0 {
		names := make([]string, len(conds))
		for i, c := range conds {
			names[i] = p.s(services.KeepName(c))
		}
		cur = strings.Join(names, p.s("、"))
	}
	if !own && key != services.AutoSellKeepAll {
		cur += p.s("（跟著所有作物）")
	}
	e.AddField(p.f("🔒 %s的保留條件", keyName), cur+p.s("\n👇 第 2 個選單選作物，第 3 個選單勾選要保留的（可複選）"), false)

	// 第 1 列：販售項目
	opts := sv.Options[:min(len(sv.Options), ui.MaxOptions)]
	sell := &ui.Select{Placeholder: p.s("🏧 勾選自動販售機要賣的東西（可複選）…"), Row: 0, MinValues: 1, MaxValues: len(opts),
		OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
			p.act(ctx, it, p.call("set_auto_seller_items", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetAutoSellerItems(ctx, p.playerID, values)
			}), actOpts{})
			return nil
		}}
	for _, o := range opts {
		sell.Options = append(sell.Options, ui.SelectOption{Label: p.s(o.Name), Value: o.Code, Emoji: sellEmoji(o.Code), Default: o.Selected})
	}
	p.add(sell)

	// 第 2 列：要設定保留條件的作物（所有作物＋有勾選販售的作物，不含魚）
	pick := &ui.Select{Placeholder: p.s("🌾 選擇要設定保留條件的作物…"), Row: 1,
		OnSelect: func(ctx context.Context, it ui.Interaction, value string) error {
			p.sellerKeepKey = value
			if value == services.AutoSellKeepAll {
				p.sellerKeepKey = ""
			}
			p.goTo(ctx, it, "auto_seller", "")
			return nil
		}}
	pick.Options = append(pick.Options, ui.SelectOption{Label: p.s("所有作物（沒有自己設定的作物）"), Value: services.AutoSellKeepAll, Emoji: "🌐",
		Default: key == services.AutoSellKeepAll})
	for _, o := range sv.Options {
		if o.Code == services.AutoSellFish || !o.Selected || len(pick.Options) >= ui.MaxOptions {
			continue
		}
		pick.Options = append(pick.Options, ui.SelectOption{Label: p.s(o.Name), Value: o.Code, Emoji: sellEmoji(o.Code), Default: o.Code == key})
	}
	p.add(pick)

	// 第 3 列：保留條件（可複選）
	keepSel := &ui.Select{Placeholder: p.f("🔒 %s要保留哪些（可複選）…", keyName), Row: 2, MinValues: 1,
		OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
			p.act(ctx, it, p.call("set_auto_seller_keep", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetAutoSellerKeep(ctx, p.playerID, key, values)
			}), actOpts{})
			return nil
		}}
	keepSel.Options = append(keepSel.Options, ui.SelectOption{Label: p.s("不保留（全部賣出）"), Value: services.KeepNone, Emoji: "💰",
		Default: own && len(conds) == 0})
	if key != services.AutoSellKeepAll {
		keepSel.Options = append(keepSel.Options, ui.SelectOption{Label: p.s("跟著「所有作物」的設定"), Value: services.KeepInherit, Emoji: "🌐", Default: !own})
	}
	for _, c := range services.KeepChoices() {
		selected := false
		for _, x := range conds {
			selected = selected || x == c.Code
		}
		keepSel.Options = append(keepSel.Options, ui.SelectOption{Label: p.s(c.Name), Value: c.Code, Emoji: keepEmoji(c.Code),
			Default: selected && (own || key == services.AutoSellKeepAll)})
	}
	keepSel.MaxValues = len(keepSel.Options)
	p.add(keepSel)

	p.nav(p.s("返回背包"), "backpack", btn{emoji: "↩️", row: 3})
	p.navBack(3)
	return e, nil
}

func sellEmoji(code string) string {
	switch code {
	case services.AutoSellHybrid:
		return "🧬"
	case services.AutoSellFish:
		return "🐟"
	}
	return cropEmoji(code)
}

func keepEmoji(code string) string {
	switch {
	case code == services.KeepCraftable:
		return "⚙️"
	case strings.HasPrefix(code, "q:"):
		return "⭐"
	}
	return "✨"
}
