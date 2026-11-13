package bot

// 子面板：特賣與升級（Python special_shop_panel.py）、成就與圖鑑（progress_panel.py）、古代遺跡（ruins_panel.py）。

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/ruins"
	"farmgm/internal/services"
)

// Line 子面板的分隔線。
const Line = "━━━━━━━━━━━━━━"

// ======================================================================
// 特賣與升級（商店的第三個分頁）
// ======================================================================

func (p *Panel) loadShopSpecial(ctx context.Context) (any, error) {
	return p.svc().SpecialShop(ctx, p.playerID)
}

func (p *Panel) buildShopSpecial(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.SpecialShopView)
	selected := ""
	if p.picker != nil {
		selected = p.picker.Key
	}
	mark := func(key string) string {
		if key == selected {
			return "▶ "
		}
		return ""
	}
	g := d.Greenhouse
	var greenhouse string
	if g.NextPrice == nil {
		greenhouse = fmt.Sprintf("🏡 **溫室**　Lv **%d/%d**（生長時間 -%d%%）　✅ 已達最高等級", g.Level, g.Max, g.Pct)
	} else {
		greenhouse = fmt.Sprintf("%s🏡 **溫室**　Lv **%d/%d**（目前 -%d%%）　💰 升級 **%s** 金 → -%d%%", mark("greenhouse"),
			g.Level, g.Max, g.Pct, money(*g.NextPrice), g.NextPct)
	}
	greenhouse += fmt.Sprintf("\n　📝 永久升級：每一級讓之後澆水的作物生長時間再少 %d%%（與限時活動的加速相加）", g.PerLevel)
	w := d.Weekly
	var deals []string
	for _, o := range w.Offers {
		left := o.Limit - o.Bought
		tag := fmt.Sprintf("🏷️ 原價 %d", o.OriginalPrice)
		if o.Rare {
			tag = "🌟 稀有種子（平常不販售）"
		}
		soldOut := ""
		if left <= 0 {
			soldOut = "　❌ 本週已買完"
		}
		deals = append(deals, fmt.Sprintf("%s🌰 **%s**　💰 **%s** 金　%s\n　🛒 本週已買 %d/%d%s", mark("weekly:"+o.CropCode), o.Name,
			money(o.Price), tag, o.Bought, o.Limit, soldOut))
	}
	titles := make([]string, len(d.Titles))
	for i, t := range d.Titles {
		own := fmt.Sprintf("💰 **%s** 金", money(t.Price))
		if t.Owned {
			own = "✅ 已擁有"
		}
		titles[i] = fmt.Sprintf("%s🎖️ **%s**　%s", mark("title:"+t.Title), t.Title, own)
	}
	dealText := strings.Join(deals, "\n")
	if dealText == "" {
		dealText = "本週沒有特賣。"
	}
	e := &ui.Embed{Title: "🎁 ✦ 特賣與升級 ✦", Color: Gold, Description: runes(fmt.Sprintf(
		"💰 你的金幣：**%s**\n%s\n**🏡 溫室**\n%s\n\n**🎁 本週特賣**（%s換新商品，每人每週限購）\n%s\n\n**🎖️ 稱號商店**（買到後到「🏅 成就圖鑑 → 🎖️ 稱號」選用）\n%s",
		money(d.Gold), Line, greenhouse, tsv(w.EndsAt), dealText, strings.Join(titles, "\n")), 4000)}
	if p.picker != nil {
		hint := "👇 按下方的數量按鈕購買"
		if p.picker.Confirm {
			hint = "👇 按「確認購買」完成購買"
		}
		e.AddField("🛒 已選擇", fmt.Sprintf("**%s**（%s）\n%s", p.picker.Label, p.picker.PriceText, hint), false)
	} else {
		e.Footer = "👇 先從選單挑一樣商品"
	}

	pick := func(ctx context.Context, it ui.Interaction, key string) error {
		kind, code, _ := strings.Cut(key, ":")
		pid := p.playerID
		switch kind {
		case "greenhouse":
			price := 0
			if g.NextPrice != nil {
				price = *g.NextPrice
			}
			p.picker = &Picker{Key: key, Label: fmt.Sprintf("🏡 溫室升到 %d 級", g.Level+1), PriceText: money(price) + " 金", Confirm: true, Max: 1,
				Run: p.callQty("upgrade_greenhouse", func(ctx context.Context, _ int) (services.ActionResult, error) {
					return p.svc().UpgradeGreenhouse(ctx, pid)
				})}
		case "title":
			i := slices.IndexFunc(d.Titles, func(t services.TitleShopItem) bool { return t.Title == code })
			if i < 0 {
				return gameerr.NotFoundf("找不到這個商品。")
			}
			p.picker = &Picker{Key: key, Label: fmt.Sprintf("🎖️ 稱號「%s」", code), PriceText: money(d.Titles[i].Price) + " 金", Confirm: true, Max: 1,
				Run: p.callQty("buy_title", func(ctx context.Context, _ int) (services.ActionResult, error) {
					return p.svc().BuyTitle(ctx, pid, code)
				})}
		default:
			i := slices.IndexFunc(w.Offers, func(o services.WeeklyOfferView) bool { return o.CropCode == code })
			if i < 0 {
				return gameerr.NotFoundf("找不到這個商品。")
			}
			o := w.Offers[i]
			p.picker = &Picker{Key: key, Label: fmt.Sprintf("🌰 %s（本週特賣）", o.Name), PriceText: fmt.Sprintf("每顆 %d 金", o.Price), Verb: "買",
				Max: max(1, min(o.Limit-o.Bought, d.Gold/max(1, o.Price))), Presets: []int{1, 3, 10}, Keep: true,
				Run: p.callQty("buy_weekly_deal", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().BuyWeeklyDeal(ctx, pid, code, q)
				})}
		}
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}
	var options []option
	if g.NextPrice != nil {
		options = append(options, option{fmt.Sprintf("溫室升到 %d 級", g.Level+1), "greenhouse",
			fmt.Sprintf("🏡 生長時間 -%d%%｜💰 %s 金", g.NextPct, money(*g.NextPrice)), "🏡"})
	}
	for _, o := range w.Offers {
		if o.Limit > o.Bought {
			options = append(options, option{o.Name + "（本週特賣）", "weekly:" + o.CropCode,
				fmt.Sprintf("🎁 剩 %d 顆｜💰 %d 金", o.Limit-o.Bought, o.Price), "🌰"})
		}
	}
	for _, t := range d.Titles {
		if !t.Owned {
			options = append(options, option{fmt.Sprintf("稱號「%s」", t.Title), "title:" + t.Title, fmt.Sprintf("🎖️ 稱號｜💰 %s 金", money(t.Price)), "🎖️"})
		}
	}
	p.selectMenu("🛒 選擇要買的商品…", options, pick, 0, selected)
	p.quantityButtons(1)
	p.nav("農場商店", "shop", btn{emoji: "🌾", row: 4})
	p.nav("釣具商店", "fish_shop", btn{emoji: "🎣", row: 4})
	p.button("特賣與升級", p.noop, btn{emoji: "🎁", row: 4, style: ui.Primary, disabled: true})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 4})
	return e, nil
}

// ======================================================================
// 成就與圖鑑
// ======================================================================

const noTitle = "__none__"

var achievementStateIcon = map[string]string{"claimed": "✅", "ready": "🎁", "progress": "⬜"}

// pctOf Python _pct：整數百分比（無條件捨去）。
func pctOf(value, total int) int {
	if total <= 0 {
		return 0
	}
	return value * 100 / total
}

func (p *Panel) loadProgress(ctx context.Context) (any, error) {
	return p.svc().ProgressOverview(ctx, p.playerID)
}

func (p *Panel) buildProgress(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.ProgressOverview)
	c, k, a, t := d.Codex, d.Checkin, d.Achievements, d.Titles
	sign := fmt.Sprintf("連續 **%d** 天（開啟面板自動簽到）", k.Streak)
	if k.CheckedToday {
		sign = fmt.Sprintf("✅ 今天已簽到｜連續 **%d** 天", k.Streak)
	}
	cats := make([]string, len(c.Categories))
	for i, x := range c.Categories {
		cats[i] = fmt.Sprintf("%s %s　%s **%d/%d**", x.Emoji, x.Name, bar(x.Discovered, x.Total, 8), x.Discovered, x.Total)
	}
	ready := ""
	if a.Ready != 0 {
		ready = fmt.Sprintf("　🎁 **%d 個可領取！**", a.Ready)
	}
	title := "未設定"
	if t.Current != nil && *t.Current != "" {
		title = "**" + *t.Current + "**"
	}
	e := &ui.Embed{Title: "🏅 ✦ 成就與圖鑑 ✦", Color: Purple, Description: runes(fmt.Sprintf(
		"🏅 **成就**　%s %d/%d%s\n🎖️ 目前稱號：%s（擁有 %d 個）\n%s\n📅 每日簽到：%s\n🎁 下一次：%s\n%s\n📖 **圖鑑**　%d/%d（%d%%）\n%s",
		bar(a.Claimed, a.Total, 8), a.Claimed, a.Total, ready, title, len(t.Owned), Line, sign, k.NextRewardText, Line,
		c.Discovered, c.Total, pctOf(c.Discovered, c.Total), strings.Join(cats, "\n")), 4000),
		Footer: "成就進度包含過去的紀錄；達成後到「成就」頁領取獎勵。取得物品會自動登記到圖鑑。"}
	p.nav("成就", "achievements", btn{emoji: "🏅", row: 0, style: ui.Primary})
	p.nav("稱號", "titles", btn{emoji: "🎖️", row: 0})
	p.nav("圖鑑", "codex", btn{emoji: "📖", row: 0, style: ui.Primary})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 0})
	return e, nil
}

func (p *Panel) loadAchievements(ctx context.Context) (any, error) {
	return p.svc().Achievements(ctx, p.playerID)
}

func (p *Panel) buildAchievements(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.AchievementView)
	cat := achievements.Categories[0]
	for _, c := range achievements.Categories {
		if c.Code == p.achCategory {
			cat = c
		}
	}
	var blocks []string
	type count struct{ done, total int }
	counts := map[string]*count{}
	for _, r := range d.Rows {
		if counts[r.Category] == nil {
			counts[r.Category] = &count{}
		}
		counts[r.Category].total++
		if r.State == "claimed" {
			counts[r.Category].done++
		}
		if r.Category != cat.Code {
			continue
		}
		var status string
		switch r.State {
		case "progress":
			status = fmt.Sprintf("%s %s/%s %s", bar(r.Value, r.Target, 8), money(r.Value), money(r.Target), r.Unit)
		case "ready":
			status = "**可以領取！**"
		default:
			status = "已領取"
		}
		blocks = append(blocks, fmt.Sprintf("%s %s **%s**　%s\n　%s　🎁 %s", achievementStateIcon[r.State], r.Emoji, r.Name, r.Description, status, r.RewardText))
	}
	ready := ""
	if d.Ready != 0 {
		ready = fmt.Sprintf("　🎁 可領取 **%d** 個", d.Ready)
	}
	e := &ui.Embed{Title: fmt.Sprintf("🏅 ✦ 成就｜%s %s ✦", cat.Emoji, cat.Name), Color: Gold, Description: runes(fmt.Sprintf(
		"已完成 **%d/%d**%s\n%s\n%s", d.Claimed, d.Total, ready, Line, strings.Join(blocks, "\n\n")), 4000),
		Footer: "✅ 已領取　🎁 可領取　⬜ 進行中｜進度包含成就上線前的紀錄。"}
	options := make([]option, len(achievements.Categories))
	for i, c := range achievements.Categories {
		n := counts[c.Code]
		if n == nil {
			n = &count{}
		}
		options[i] = option{fmt.Sprintf("%s（%d/%d）", c.Name, n.done, n.total), c.Code, "", c.Emoji}
	}
	p.selectMenu("選擇成就分類", options, func(ctx context.Context, it ui.Interaction, value string) error {
		p.achCategory = value
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, 0, cat.Code)
	p.button(fmt.Sprintf("領取全部（%d）", d.Ready), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("claim_achievements", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().ClaimAchievements(ctx, p.playerID)
		}), actOpts{})
		return nil
	}, btn{emoji: "🎁", row: 1, style: ui.Success, disabled: d.Ready == 0})
	p.nav("稱號", "titles", btn{emoji: "🎖️", row: 1})
	p.nav("成就與圖鑑", "progress", btn{emoji: "🏅", row: 1})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 1})
	return e, nil
}

func (p *Panel) loadTitles(ctx context.Context) (any, error) { return p.svc().Titles(ctx, p.playerID) }

func (p *Panel) buildTitles(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.TitlesView)
	current := deref(d.Current)
	lines := make([]string, len(d.Owned))
	for i, t := range d.Owned {
		mark := "🎖️"
		if t.Title == current {
			mark = "👉"
		}
		lines[i] = fmt.Sprintf("%s **%s**　來源：%s", mark, t.Title, t.Source)
	}
	cur := "未設定"
	if current != "" {
		cur = "**" + current + "**"
	}
	list := strings.Join(lines, "\n")
	if list == "" {
		list = "還沒有稱號。完成成就，或在世界 Boss 擊敗 Boss 並名列前茅就能取得。"
	}
	e := &ui.Embed{Title: "🎖️ ✦ 稱號 ✦", Color: Purple, Description: runes(fmt.Sprintf("目前顯示：%s\n%s\n%s", cur, Line, list), 4000),
		Footer: "稱號會顯示在總覽、排行榜與世界 Boss 貢獻排行。"}
	if len(d.Owned) > 0 {
		options := []option{{"不顯示稱號", noTitle, "", "🚫"}}
		for _, t := range d.Owned[:min(24, len(d.Owned))] {
			options = append(options, option{t.Title, t.Title, t.Source, "🎖️"})
		}
		selected := current
		if selected == "" {
			selected = noTitle
		}
		p.selectMenu("選擇要顯示的稱號", options, func(ctx context.Context, it ui.Interaction, value string) error {
			title := value
			if value == noTitle {
				title = ""
			}
			p.act(ctx, it, p.call("set_title", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetTitle(ctx, p.playerID, title)
			}), actOpts{})
			return nil
		}, 0, selected)
	}
	p.nav("成就", "achievements", btn{emoji: "🏅", row: 1})
	p.nav("成就與圖鑑", "progress", btn{emoji: "🏅", row: 1})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 1})
	return e, nil
}

func (p *Panel) loadCodex(ctx context.Context) (any, error) { return p.svc().Codex(ctx, p.playerID) }

func (p *Panel) buildCodex(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.CodexView)
	if len(d.Categories) == 0 {
		return nil, fmt.Errorf("圖鑑沒有分類")
	}
	cat := d.Categories[0]
	for _, c := range d.Categories {
		if c.Key == p.codexCategory {
			cat = c
		}
	}
	var lines []string
	for _, x := range cat.Entries {
		if x.Got {
			best := ""
			if x.BestQuality != "" {
				best = "　最高 " + x.BestQuality
			}
			lines = append(lines, fmt.Sprintf("✅ **%s**　×%s%s", x.Name, money(x.Quantity), best))
		} else {
			lines = append(lines, "❔ ？？？")
		}
	}
	list := strings.Join(lines, "\n")
	if list == "" {
		list = "這個分類目前沒有項目。"
	}
	e := &ui.Embed{Title: fmt.Sprintf("📖 ✦ 圖鑑｜%s %s ✦", cat.Emoji, cat.Name), Color: Gold, Description: runes(fmt.Sprintf(
		"已收集 **%d/%d**　%s\n%s\n%s", cat.Discovered, cat.Total, bar(cat.Discovered, cat.Total, 10), Line, list), 4000),
		Footer: "❔ 是還沒取得過的物品。數量是累計取得數（收成、偷菜、收集、加工、探險、釣魚）。"}
	options := make([]option, len(d.Categories))
	for i, x := range d.Categories {
		options[i] = option{fmt.Sprintf("%s（%d/%d）", x.Name, x.Discovered, x.Total), x.Key, "", x.Emoji}
	}
	p.selectMenu("選擇圖鑑分類", options, func(ctx context.Context, it ui.Interaction, value string) error {
		p.codexCategory = value
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, 0, cat.Key)
	p.nav("成就與圖鑑", "progress", btn{emoji: "🏅", row: 1})
	if cat.Key == "lab" || cat.Key == "mushroom" { // 從種子實驗室進來的圖鑑分類
		p.labNav(1)
	}
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 1})
	return e, nil
}

// ======================================================================
// 古代遺跡（山洞 →「🏛️ 修復遺跡」）
// ======================================================================

func progressBar(pct int) string {
	const slots = 10
	filled := max(0, min(slots, pct*slots/100))
	return strings.Repeat("▰", filled) + strings.Repeat("▱", slots-filled) + fmt.Sprintf(" %d%%", pct)
}

func (p *Panel) loadRuins(ctx context.Context) (any, error) {
	return p.svc().RuinsStatus(ctx, p.playerID)
}

// guardianText 本週守護者（Python _guardian_text）。
func guardianText(g *services.GuardianView) string {
	if g == nil {
		return ""
	}
	if !g.Active {
		return fmt.Sprintf("🗿 **%s** 會在 %s 出現（之後每週一個）。整週沒打倒，遺跡就會崩塌回第三階段。", g.Name, tsv(g.AppearsAt, "F"))
	}
	left := g.HPMax - g.Damage
	head := fmt.Sprintf("🗿 **本週%s**　HP %s（%s/%s）\n⏳ %s前要打倒，否則遺跡崩塌", g.Name, progressBar(left*100/g.HPMax),
		money(left), money(g.HPMax), tsv(g.EndsAt))
	if g.Defeated {
		head = fmt.Sprintf("🗿 **本週%s已被打倒！** 🎉 本週遺跡探索每次多 %d 份獎勵；下一隻 %s出現。", g.Name, g.BonusRolls, tsv(g.EndsAt))
	}
	tops := make([]string, len(g.Top))
	for i, h := range g.Top {
		tops[i] = fmt.Sprintf("%s %s", h.Name, money(h.Damage))
	}
	top := strings.Join(tops, "、")
	if top == "" {
		top = "還沒有人出手"
	}
	return fmt.Sprintf("%s\n⚔️ 捐素材攻擊（越稀有傷害越高），每次探索遺跡也會自動造成 %d 傷害\n🙋 我本週造成 **%s** 傷害　🏅 %s",
		head, g.ExpeditionDamage, money(g.Mine), top)
}

// quantityPresets Python tuple(sorted({q for q in (10, 50, most) if q <= most}))。
func quantityPresets(most int) []int {
	var out []int
	for _, q := range []int{10, 50, most} {
		if q <= most && !slices.Contains(out, q) {
			out = append(out, q)
		}
	}
	slices.Sort(out)
	return out
}

func (p *Panel) buildRuins(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.RuinsStatus)
	if d.GroupID == nil {
		p.nav("返回冒險介面", "cave", btn{emoji: "↩️", row: 0})
		return &ui.Embed{Title: "🏛️ ✦ 古代遺跡 ✦", Color: Blue, Description: "你的農場還沒有所屬群組，到 Discord 伺服器中開啟面板就會設定。"}, nil
	}
	stages := make([]string, len(d.StageNames))
	for i, name := range d.StageNames {
		mark := "🔒"
		if i < d.Stage {
			mark = "✅"
		} else if i == d.Stage {
			mark = "🔨"
		}
		stages[i] = fmt.Sprintf("%s 第 %d 階段：%s", mark, i+1, name)
	}
	var top []string
	for i, c := range d.Top {
		title := ""
		if c.Title != nil && *c.Title != "" {
			title = "〔" + *c.Title + "〕"
		}
		top = append(top, fmt.Sprintf("%d. %s%s　%s 個", i+1, c.Name, title, money(c.Quantity)))
	}
	if len(top) == 0 {
		top = []string{"還沒有人捐獻，當第一個吧！"}
	}
	var body string
	var mats []string
	if d.Completed {
		body = "🎉 **修復完成！** 群組每個人（Lv.4 起）每天都能到山洞**額外**探索一次「🏛️ 古代遺跡」，不佔一般探險的名額，立即拿到獎勵。\n" +
			fmt.Sprintf("出過力的成員都獲得了稱號「%s」。", d.Title)
	} else {
		body = fmt.Sprintf("🔨 目前階段：**%s**\n%s", deref(d.StageName), deref(d.StageDescription))
		if d.CollapsedAt != nil {
			body = fmt.Sprintf("🏚️ 遺跡在 %s 因為守護者沒被打倒而崩塌了，重新捐核心素材就能修好。\n", ts(d.CollapsedAt, "D")) + body
		}
		for _, m := range d.Materials {
			mark := "🧱"
			if m.Donated >= m.Need {
				mark = "✅"
			}
			mats = append(mats, fmt.Sprintf("%s %s %s　%s/%s　（背包 %s）", mark, lookupOr(MaterialEmoji, m.Code, "📦"), m.Name,
				money(m.Donated), money(m.Need), money(m.Have)))
		}
	}
	color := Blue
	if d.Completed {
		color = Gold
	}
	g := d.Guardian
	if g != nil && g.Active && !g.Defeated {
		color = Red
	}
	e := &ui.Embed{Title: fmt.Sprintf("🏛️ ✦ %s的古代遺跡 ✦", deref(d.GroupName)), Color: color, Description: sections(
		fmt.Sprintf("群組成員一起捐山洞素材修復遺跡；三個階段都完成後，所有成員都能進入新的探險區域，捐過素材的人還會得到稱號「%s」。", d.Title),
		fmt.Sprintf("📊 修復進度　%s\n%s", progressBar(d.Percent), strings.Join(stages, "\n")),
		body,
		Section{Title: "🧱 這個階段需要", Lines: mats},
		fmt.Sprintf("🙋 我累計捐了 **%s** 個素材", money(d.Mine)),
		Section{Title: "🏅 修復貢獻排行", Lines: top},
		guardianText(g),
	)}
	if p.picker != nil && (strings.HasPrefix(p.picker.Key, "ruins:") || strings.HasPrefix(p.picker.Key, "guardian:")) {
		e.AddField("🏛️ 已選擇", fmt.Sprintf("**%s**\n👇 選擇數量", p.picker.Label), false)
	}
	selected := ""
	if p.picker != nil {
		k := p.picker.Key
		if _, rest, ok := strings.Cut(k, ":"); ok {
			k = rest
		}
		selected = k
	}
	var options []option
	for _, m := range d.Materials {
		if m.Donated < m.Need && m.Have > 0 {
			options = append(options, option{m.Name, m.Code, fmt.Sprintf("還差 %d，背包 %d", m.Need-m.Donated, m.Have), lookupOr(MaterialEmoji, m.Code, "📦")})
		}
	}
	if len(options) > 0 {
		p.selectMenu("🧱 選擇要捐的素材…", options, func(ctx context.Context, it ui.Interaction, code string) error {
			i := slices.IndexFunc(d.Materials, func(m services.RuinsMaterial) bool { return m.Code == code })
			if i < 0 {
				return gameerr.NotFoundf("找不到這個項目。")
			}
			m := d.Materials[i]
			most := min(m.Have, m.Need-m.Donated, ruins.DonateMax)
			p.picker = &Picker{Key: "ruins:" + code, Label: fmt.Sprintf("捐 %s（最多 %d）", m.Name, most), Verb: "捐", Max: most,
				Presets: quantityPresets(most), Run: p.callQty("donate_ruins", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().DonateRuins(ctx, p.playerID, code, q)
				})}
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, 0, selected)
		p.quantityButtons(1)
	} else if g != nil && g.Active && !g.Defeated && len(g.Weapons) > 0 {
		var weapons []option
		for _, w := range g.Weapons {
			weapons = append(weapons, option{w.Name, w.Code, fmt.Sprintf("每個 %d 傷害，背包 %d", w.Damage, w.Have), lookupOr(MaterialEmoji, w.Code, "📦")})
		}
		p.selectMenu("⚔️ 選擇攻擊守護者的素材…", weapons, func(ctx context.Context, it ui.Interaction, code string) error {
			i := slices.IndexFunc(g.Weapons, func(w services.GuardianWeapon) bool { return w.Code == code })
			if i < 0 {
				return gameerr.NotFoundf("找不到這個項目。")
			}
			w := g.Weapons[i]
			left := g.HPMax - g.Damage
			most := min(w.Have, (left+w.Damage-1)/w.Damage, ruins.AttackMax)
			p.picker = &Picker{Key: "guardian:" + code, Label: fmt.Sprintf("用 %s 攻擊（每個 %d 傷害，最多 %d）", w.Name, w.Damage, most), Verb: "用",
				Max: most, Presets: quantityPresets(most), Run: p.callQty("attack_guardian", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().AttackGuardian(ctx, p.playerID, code, q)
				})}
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, 0, selected)
		p.quantityButtons(1)
	}
	p.nav("返回冒險介面", "cave", btn{emoji: "↩️", row: 2})
	p.navBack(2)
	return e, nil
}
