package bot

// 面板頁面：📛 名牌、🏛️ 農會（Go 版新增，2026-11-19，金幣回收第二波；從「🏅 成就與圖鑑」進入）。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/auction"
	"farmgm/internal/game/cosmetics"
	"farmgm/internal/services"
)

const noBadge = "__none__"

func (p *Panel) loadBadges(ctx context.Context) (any, error) { return p.svc().Badges(ctx, p.playerID) }

func (p *Panel) buildBadges(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.BadgeView)
	var shop, special []string
	cur := "未裝備"
	for _, b := range v.Items {
		state := ""
		switch {
		case b.Owned:
			state = "✅ 已擁有（" + cosmetics.SourceNames[b.Source] + "）"
		case b.Price > 0:
			state = fmt.Sprintf("💰 **%s** 金", money(b.Price))
		default:
			state = "🔒 " + b.How
		}
		if b.Code == v.Equipped {
			cur = b.Emoji + " **" + b.Name + "**"
			state = "👉 裝備中"
		}
		line := fmt.Sprintf("%s **%s**　%s", b.Emoji, b.Name, state)
		if b.Price > 0 {
			shop = append(shop, line)
		} else {
			special = append(special, line)
		}
	}
	e := &ui.Embed{Title: "📛 ✦ 名牌 ✦", Color: Purple, Description: runes(sections(
		"名牌是名字前面的小圖示，顯示在排行榜、日報與世界 Boss 排行。只改外觀、沒有加成；買一次永久擁有，同時只能裝備一個。",
		Section{Title: "商店販售", Lines: shop},
		Section{Title: "特殊取得（農會聲望、每週拍賣）", Lines: special},
		"目前裝備："+cur,
	), 4000), Footer: fmt.Sprintf("💳 金幣 %s", money(v.Gold))}
	var buy []option
	for _, b := range v.Items {
		if !b.Owned && b.Price > 0 {
			buy = append(buy, option{fmt.Sprintf("%s（%s 金）", b.Name, money(b.Price)), b.Code, "買下後直接裝備", b.Emoji})
		}
	}
	p.selectMenu("🛒 購買名牌…", buy, func(ctx context.Context, it ui.Interaction, code string) error {
		p.badgeBuy = code
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, 0, p.badgeBuy)
	equip := []option{{"不顯示名牌", noBadge, "", "🚫"}}
	for _, b := range v.Items {
		if b.Owned {
			equip = append(equip, option{b.Name, b.Code, cosmetics.SourceNames[b.Source], b.Emoji})
		}
	}
	if len(equip) > 1 {
		selected := v.Equipped
		if selected == "" {
			selected = noBadge
		}
		p.selectMenu("📛 選擇要裝備的名牌…", equip, func(ctx context.Context, it ui.Interaction, code string) error {
			if code == noBadge {
				code = ""
			}
			p.act(ctx, it, p.call("equip_badge", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().EquipBadge(ctx, p.playerID, code)
			}), actOpts{noProgress: true})
			return nil
		}, 1, selected)
	}
	for _, b := range v.Items {
		if b.Code != p.badgeBuy || b.Owned || b.Price <= 0 {
			continue
		}
		code, price := b.Code, b.Price
		e.AddField("🛒 確認購買", fmt.Sprintf("%s **%s**\n💰 **%s** 金幣", b.Emoji, b.Name, money(price)), false)
		p.button(fmt.Sprintf("確認購買（%s 金）", money(price)), func(ctx context.Context, it ui.Interaction) error {
			p.badgeBuy = ""
			p.act(ctx, it, p.call("buy_badge", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuyBadge(ctx, p.playerID, code)
			}), actOpts{})
			return nil
		}, btn{emoji: "✅", row: 2, style: ui.Success, disabled: v.Gold < price})
		p.button("取消", func(ctx context.Context, it ui.Interaction) error {
			p.badgeBuy = ""
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 2})
	}
	p.nav("農會", "reputation", btn{emoji: "🏛️", row: 3})
	p.nav("回成就與圖鑑", "progress", btn{emoji: "📚", row: 3})
	p.navBack(3)
	return e, nil
}

func (p *Panel) loadReputation(ctx context.Context) (any, error) {
	return p.svc().Reputation(ctx, p.groupID, p.playerID)
}

// donateSteps 農會捐款的快速金額。
var donateSteps = []int{1_000, 10_000, 100_000}

func (p *Panel) buildReputation(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.ReputationView)
	span := max(1, v.NextNeed-v.LevelStart)
	progress := fmt.Sprintf("⭐ 聲望 **Lv.%d**　累積 **%s** 點\n%s　下一級還差 **%s**", v.Level, money(v.Points),
		bar(v.Points-v.LevelStart, span, 10), money(v.NextNeed-v.Points))
	next := "已拿到所有等級獎勵"
	if r := v.NextReward; r != nil {
		next = fmt.Sprintf("Lv.%d：%s", r.Level, services.RewardText(*r))
	}
	today := fmt.Sprintf("今天已捐 **%s** 金", money(v.DonatedToday))
	left := -1 // -1 = 不限
	if v.DailyCap > 0 {
		left = max(0, v.DailyCap-v.DonatedToday)
		today += fmt.Sprintf("（每天上限 %s，還可以捐 %s）", money(v.DailyCap), money(left))
	}
	var board []string
	for _, r := range v.Board {
		name := escapeMD(r.FarmName)
		if r.Badge != "" {
			name = r.Badge + " " + name
		}
		board = append(board, fmt.Sprintf("%s **%s**　Lv.%d（%s 點）", medalOr(r.Rank, fmt.Sprintf("`%2d.`", r.Rank)), name, r.Level, money(r.Points)))
	}
	e := &ui.Embed{Title: "🏛️ ✦ 農會 ✦", Color: Gold, Description: runes(sections(
		"把金幣捐給農會換聲望：1 金 = 1 點，捐出的金幣不會退還，也不會影響「淨賺金幣」排行。聲望只換外觀獎勵（稱號、名牌），沒有數值加成。",
		Section{Title: "聲望排行（本群組前 10 名）", Lines: board},
		progress,
		"🎁 下一個獎勵："+next,
		"💰 "+today,
	), 4000)}
	e.Footer = fmt.Sprintf("💳 金幣 %s　一次最少捐 %s", money(v.Gold), money(v.MinDonation))
	if p.confirm != "" && strings.HasPrefix(p.confirm, "donate:") {
		amount, _ := strconv.Atoi(strings.TrimPrefix(p.confirm, "donate:"))
		e.AddField("🏛️ 確認捐款", fmt.Sprintf("捐出 **%s** 金幣（不會退還），聲望 +%s", money(amount), money(amount)), false)
		p.button(fmt.Sprintf("確認捐 %s 金", money(amount)), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("donate_reputation", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().DonateReputation(ctx, p.playerID, amount)
			}), actOpts{})
			return nil
		}, btn{emoji: "✅", row: 1, style: ui.Danger})
		p.button("取消", func(ctx context.Context, it ui.Interaction) error {
			p.confirm = ""
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 1})
	}
	ask := func(amount int) func(ctx context.Context, it ui.Interaction) error {
		return func(ctx context.Context, it ui.Interaction) error {
			p.confirm = fmt.Sprintf("donate:%d", amount)
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}
	}
	for _, n := range donateSteps {
		can := n >= v.MinDonation && v.Gold >= n && (left < 0 || left >= n)
		p.button(fmt.Sprintf("捐 %s", money(n)), ask(n), btn{emoji: "🪙", row: 0, disabled: !can})
	}
	p.button("自訂金額", func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, &ui.Modal{
			Title:  "捐給農會",
			Inputs: []ui.TextInput{{Label: "捐款金額（金幣）", Placeholder: fmt.Sprintf("至少 %s", money(v.MinDonation)), MaxLength: 12, Required: true}},
			OnSubmit: func(ctx context.Context, mit ui.Interaction, values []string) error {
				n, ok := parseDigits(strings.ReplaceAll(strings.TrimSpace(first(values)), ",", ""))
				if !ok || n < 1 {
					return mit.SendMessage(ctx, ui.Message{Content: ptr("請輸入正整數的金額。"), Ephemeral: true})
				}
				return ask(n)(ctx, mit)
			},
		})
	}, btn{emoji: "✏️", row: 0, disabled: v.Gold < v.MinDonation || left == 0})
	p.nav("名牌", "badges", btn{emoji: "📛", row: 2})
	p.nav("回成就與圖鑑", "progress", btn{emoji: "📚", row: 2})
	p.navBack(2)
	return e, nil
}

// ---------- 🖼️ 外觀：農場主題、寵物帽子與顏色 ----------

func (p *Panel) loadLooks(ctx context.Context) (any, error) { return p.svc().Looks(ctx, p.playerID) }

func lookItem(items []services.CosmeticItem, code string) (services.CosmeticItem, bool) {
	for _, it := range items {
		if it.Code == code {
			return it, true
		}
	}
	return services.CosmeticItem{}, false
}

func (p *Panel) buildLooks(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.LooksView)
	list := func(items []services.CosmeticItem, current string) []string {
		var out []string
		for _, it := range items {
			state := fmt.Sprintf("💰 **%s** 金", money(it.Price))
			switch {
			case it.Code == current && current != "":
				state = "👉 使用中"
			case it.Owned:
				state = "✅ 已擁有"
			case it.Price <= 0:
				state = "🔒 目前不販售"
			}
			out = append(out, fmt.Sprintf("%s **%s**　%s", it.Emoji, it.Name, state))
		}
		return out
	}
	var pets []string
	for _, pt := range v.Pets {
		look := []string{}
		if h, ok := lookItem(v.Hats, pt.Hat); ok {
			look = append(look, h.Emoji+h.Name)
		}
		if c, ok := lookItem(v.Colors, pt.Color); ok {
			look = append(look, c.Emoji+c.Name)
		}
		text := "原本的樣子"
		if len(look) > 0 {
			text = strings.Join(look, "、")
		}
		pets = append(pets, fmt.Sprintf("%s **%s**：%s", pt.Emoji, pt.Name, text))
	}
	if len(pets) == 0 {
		pets = []string{"還沒有寵物（到「🐔 動物 → 🐾 寵物」領養）"}
	}
	e := &ui.Embed{Title: "🖼️ ✦ 農場外觀 ✦", Color: Purple, Description: runes(sections(
		"只改外觀、沒有加成，買一次永久擁有。主題會換掉農田外圍的地面與點綴；帽子與顏色所有寵物共用，每隻寵物各自設定。",
		Section{Title: "🖼️ 主題", Lines: list(v.Themes, v.Theme)},
		Section{Title: "🎩 寵物帽子", Lines: list(v.Hats, "")},
		Section{Title: "🎨 寵物顏色", Lines: list(v.Colors, "")},
		Section{Title: "🐾 我的寵物", Lines: pets},
	), 4000), Footer: fmt.Sprintf("💳 金幣 %s　回「🌱 農田」就能看到換好的外觀。", money(v.Gold))}

	// 第 0 列：主題（擁有的直接套用，沒有的先確認購買）
	themes := []option{{"預設外觀", "none", "原本的草地", "🌿"}}
	for _, it := range v.Themes {
		desc := "套用"
		if !it.Owned {
			if it.Price <= 0 {
				continue
			}
			desc = fmt.Sprintf("購買 %s 金", money(it.Price))
		}
		themes = append(themes, option{it.Name, it.Code, desc, it.Emoji})
	}
	selTheme := v.Theme
	if selTheme == "" {
		selTheme = "none"
	}
	p.selectMenu("🖼️ 選擇主題…", themes, func(ctx context.Context, it ui.Interaction, code string) error {
		if code == "none" {
			code = ""
		}
		if t, ok := lookItem(v.Themes, code); ok && !t.Owned {
			p.looksBuy = "theme:" + code
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}
		p.act(ctx, it, p.call("set_theme", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().SetTheme(ctx, p.playerID, code)
		}), actOpts{noProgress: true})
		return nil
	}, 0, selTheme)
	// 第 1 列：購買帽子或顏色
	var buy []option
	for _, x := range []struct {
		kind  string
		items []services.CosmeticItem
		what  string
	}{{"hat", v.Hats, "帽子"}, {"color", v.Colors, "顏色"}} {
		for _, it := range x.items {
			if !it.Owned && it.Price > 0 {
				buy = append(buy, option{fmt.Sprintf("%s：%s（%s 金）", x.what, it.Name, money(it.Price)), x.kind + ":" + it.Code, "所有寵物共用", it.Emoji})
			}
		}
	}
	p.selectMenu("🛒 購買寵物帽子或顏色…", buy, func(ctx context.Context, it ui.Interaction, value string) error {
		p.looksBuy = value
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, 1, p.looksBuy)
	// 第 2、3 列：選寵物 → 換帽子或顏色
	if len(v.Pets) > 0 {
		var petOpts []option
		for _, pt := range v.Pets {
			petOpts = append(petOpts, option{pt.Name, pt.Code, "換帽子或顏色", pt.Emoji})
		}
		p.selectMenu("🐾 選擇要打扮的寵物…", petOpts, func(ctx context.Context, it ui.Interaction, code string) error {
			p.looksPet = code
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, 2, p.looksPet)
	}
	for _, pt := range v.Pets {
		if pt.Code != p.looksPet {
			continue
		}
		pet := pt.Code
		opts := []option{{"拿掉帽子", "hat:", "", "🚫"}, {"恢復原本的顏色", "color:", "", "🔄"}}
		for _, h := range v.Hats {
			if h.Owned {
				opts = append(opts, option{"戴上" + h.Name, "hat:" + h.Code, "帽子", h.Emoji})
			}
		}
		for _, c := range v.Colors {
			if c.Owned {
				opts = append(opts, option{"換成" + c.Name, "color:" + c.Code, "顏色", c.Emoji})
			}
		}
		p.selectMenu(fmt.Sprintf("%s 幫%s換…", pt.Emoji, pt.Name), opts, func(ctx context.Context, it ui.Interaction, value string) error {
			kind, code, _ := strings.Cut(value, ":")
			p.act(ctx, it, p.call("set_pet_look", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetPetLook(ctx, p.playerID, pet, kind, code)
			}), actOpts{noProgress: true})
			return nil
		}, 3, "")
	}
	// 確認購買（第 4 列）
	kind, code, _ := strings.Cut(p.looksBuy, ":")
	items := map[string][]services.CosmeticItem{"theme": v.Themes, "hat": v.Hats, "color": v.Colors}[kind]
	if it, ok := lookItem(items, code); ok && !it.Owned && it.Price > 0 {
		price := it.Price
		e.AddField("🛒 確認購買", fmt.Sprintf("%s **%s**\n💰 **%s** 金幣", it.Emoji, it.Name, money(price)), false)
		p.button(fmt.Sprintf("確認購買（%s 金）", money(price)), func(ctx context.Context, it ui.Interaction) error {
			p.looksBuy = ""
			p.act(ctx, it, p.call("buy_cosmetic", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuyCosmetic(ctx, p.playerID, kind, code)
			}), actOpts{})
			return nil
		}, btn{emoji: "✅", row: 4, style: ui.Success, disabled: v.Gold < price})
		p.button("取消", func(ctx context.Context, it ui.Interaction) error {
			p.looksBuy = ""
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 4})
	}
	p.nav("庭院擺設", "decor", btn{emoji: "🎨", row: 4})
	p.nav("回農田", "plots", btn{emoji: "🌱", row: 4})
	p.navBack(4)
	return e, nil
}

// ---------- 🔨 每週拍賣 ----------

func (p *Panel) loadAuction(ctx context.Context) (any, error) {
	return p.svc().Auction(ctx, p.playerID)
}

func (p *Panel) buildAuction(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.AuctionView)
	xc := auction.Current()
	rules := fmt.Sprintf("每週一 00:00 開一場、週日 %d:00 結標，每次至少加價 %d%%。出價時金幣先凍結，被超過時全數退回；"+
		"結標前 %d 分鐘內有人出價會延後 %d 分鐘。得標金額全數回收，不會給任何玩家。", xc.EndHour, xc.MinIncrementPct, xc.ExtendMinutes, xc.ExtendMinutes)
	var status []string
	if !v.Open {
		status = append(status, "目前沒有進行中的拍賣，下週一 00:00 開新的一場。")
	} else {
		status = append(status, fmt.Sprintf("🎁 本週拍賣品：**%s**（%s）", v.ItemName, auction.Describe(v.ItemKind)),
			"⏰ 結標："+ts(&v.EndsAt))
		top := "還沒有人出價"
		if v.TopBid != nil {
			name := escapeMD(v.TopName)
			if v.TopBadge != "" {
				name = v.TopBadge + " " + name
			}
			top = fmt.Sprintf("**%s** 金（%s）", money(*v.TopBid), name)
		}
		status = append(status, "💰 目前最高價："+top, fmt.Sprintf("📈 下一次出價至少 **%s** 金（共 %d 次出價）", money(v.MinBid), v.BidCount))
		switch {
		case v.Owned:
			status = append(status, "✅ 你已經擁有這件拍賣品，不能再出價。")
		case v.Mine:
			status = append(status, "👑 **你是目前的最高出價者！** 加價只會再凍結差額。")
		case v.Outbid > 0:
			status = append(status, fmt.Sprintf("⚠️ 你的出價被超過了，凍結的 %s 金已經退回。", money(v.Outbid)))
		}
	}
	last := "還沒有結標過的拍賣"
	if v.LastName != "" {
		last = fmt.Sprintf("%s：流標", v.LastName)
		if v.LastWin != "" {
			last = fmt.Sprintf("%s：**%s** 以 %s 金得標", v.LastName, escapeMD(v.LastWin), money(v.LastPrice))
		}
	}
	e := &ui.Embed{Title: "🔨 ✦ 每週拍賣 ✦", Color: Gold, Description: runes(sections(
		rules,
		"📜 上一場："+last,
		Section{Title: "本週拍賣", Lines: status},
	), 4000), Footer: fmt.Sprintf("💳 金幣 %s", money(v.Gold))}
	if strings.HasPrefix(p.confirm, "bid:") {
		amount, _ := strconv.Atoi(strings.TrimPrefix(p.confirm, "bid:"))
		need := amount
		if v.Mine && v.TopBid != nil {
			need = amount - *v.TopBid
		}
		e.AddField("🔨 確認出價", fmt.Sprintf("出價 **%s** 金（這次凍結 %s 金，被超過時全數退回）", money(amount), money(need)), false)
		p.button(fmt.Sprintf("確認出價 %s 金", money(amount)), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("place_bid", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().PlaceBid(ctx, p.playerID, amount)
			}), actOpts{})
			return nil
		}, btn{emoji: "✅", row: 1, style: ui.Danger})
		p.button("取消", func(ctx context.Context, it ui.Interaction) error {
			p.confirm = ""
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 1})
	}
	ask := func(amount int) func(ctx context.Context, it ui.Interaction) error {
		return func(ctx context.Context, it ui.Interaction) error {
			p.confirm = fmt.Sprintf("bid:%d", amount)
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}
	}
	canBid := v.Open && !v.Owned
	avail := v.Gold
	if v.Mine && v.TopBid != nil {
		avail += *v.TopBid
	}
	for _, x := range []struct {
		label string
		pct   int
	}{{"出最低價", 0}, {"+10%", 10}, {"+25%", 25}} {
		amount := v.MinBid
		if x.pct > 0 {
			amount = max(v.MinBid, v.MinBid*(100+x.pct)/100)
		}
		p.button(fmt.Sprintf("%s（%s）", x.label, money(amount)), ask(amount), btn{emoji: "🔨", row: 0, disabled: !canBid || avail < amount})
	}
	p.button("自訂出價", func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, &ui.Modal{
			Title:  "每週拍賣出價",
			Inputs: []ui.TextInput{{Label: "出價總額（金幣）", Placeholder: fmt.Sprintf("至少 %s", money(v.MinBid)), MaxLength: 12, Required: true}},
			OnSubmit: func(ctx context.Context, mit ui.Interaction, values []string) error {
				n, ok := parseDigits(strings.ReplaceAll(strings.TrimSpace(first(values)), ",", ""))
				if !ok || n < 1 {
					return mit.SendMessage(ctx, ui.Message{Content: ptr("請輸入正整數的金額。"), Ephemeral: true})
				}
				return ask(n)(ctx, mit)
			},
		})
	}, btn{emoji: "✏️", row: 0, disabled: !canBid})
	p.nav("重新整理", "auction", btn{emoji: "🔄", row: 2})
	p.nav("回成就與圖鑑", "progress", btn{emoji: "📚", row: 2})
	p.navBack(2)
	return e, nil
}
