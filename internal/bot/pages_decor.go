package bot

// 面板頁面：庭院擺設（Go 版新增，2026-11-15；從「🌱 農田」頁進入）。購買裝飾、選位置擺放；擺好的裝飾畫在農田圖左右兩側。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/decor"
	"farmgm/internal/game/seasons"
	"farmgm/internal/services"
)

func (p *Panel) loadDecor(ctx context.Context) (any, error) {
	return p.svc().Decorations(ctx, p.playerID)
}

func (p *Panel) buildDecor(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.DecorView)
	byCode := map[string]services.DecorItemView{}
	for _, it := range v.Items {
		byCode[it.Code] = it
	}
	e := &ui.Embed{Title: "🎨 ✦ 庭院擺設 ✦", Color: Green}
	slots := make([]string, decor.Slots)
	for i, code := range v.Placed {
		slots[i] = fmt.Sprintf("%s：（空）", decor.SlotName(i+1))
		if it, ok := byCode[code]; ok && code != "" {
			slots[i] = fmt.Sprintf("%s：%s %s", decor.SlotName(i+1), it.Emoji, it.Name)
		}
	}
	var tiers []any
	for _, tier := range decor.Tiers {
		var names []string
		price := 0
		for _, it := range v.Items {
			if it.Tier != tier {
				continue
			}
			price = it.Price
			mark := ""
			if it.Owned {
				mark = "✅"
			}
			name := mark + it.Emoji + it.Name
			if !it.OnSale && it.Season != "" {
				name += "（" + seasons.SeasonNames[it.Season] + "季限定・已絕版）"
			}
			names = append(names, name)
		}
		if len(names) == 0 {
			continue
		}
		head := fmt.Sprintf("%s **%s**（%s 金）", tier.Emoji, tier.Name, money(price))
		if tier == decor.Auction {
			head = fmt.Sprintf("%s **%s**（每週拍賣得標）", tier.Emoji, tier.Name)
		}
		if tier == decor.Seasonal { // 2026-11-19 本季限定放最上面，標示下架倒數
			head = fmt.Sprintf("%s **本季限定：%s季**（%s 金，%s下架）", seasons.SeasonEmoji[v.Season], seasons.SeasonNames[v.Season],
				money(decor.SeasonalPrice), ts(&v.SeasonEndsAt))
		}
		tiers = append(tiers, head+"："+strings.Join(names, "、"))
	}
	owned := 0
	for _, it := range v.Items {
		if it.Owned {
			owned++
		}
	}
	parts := []any{
		"擺在農田圖左右兩側的庭院，只改外觀、沒有加成。買一次永久擁有，可以隨時換位置或收起來。",
		Section{Title: "庭院位置", Lines: slots},
		fmt.Sprintf("**裝飾目錄**（已擁有 %d／%d）", owned, len(v.Items)),
	}
	parts = append(parts, tiers...)
	e.Description = runes(sections(parts...), 4000)
	e.Footer = fmt.Sprintf("💳 金幣 %s　回「🌱 農田」就能看到擺好的庭院。", money(v.Gold))

	// 購買：選一種還沒有的裝飾 → 確認（本季限定排最前面；超過 25 種時選單內翻頁）
	var buy []option
	for _, tier := range decor.Tiers {
		for _, it := range v.Items {
			if it.Tier == tier && !it.Owned && it.OnSale {
				desc := it.Tier.Emoji + " " + it.Tier.Name
				if it.Season != "" {
					desc = fmt.Sprintf("%s %s季限定，換季就下架", seasons.SeasonEmoji[it.Season], seasons.SeasonNames[it.Season])
				}
				buy = append(buy, option{fmt.Sprintf("%s（%s 金）", it.Name, money(it.Price)), it.Code, desc, it.Emoji})
			}
		}
	}
	shown, page, pages := pagedOptions(buy, p.decorPage)
	p.decorPage = page
	p.selectMenu("🛒 購買裝飾…", shown, func(ctx context.Context, it ui.Interaction, code string) error {
		if turnPage(code, &p.decorPage, pages) {
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}
		p.decorBuy = code
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, 0, p.decorBuy)
	// 擺放：先選位置，再選裝飾（或收起來）
	var places []option
	for i := range decor.Slots {
		desc := "空的"
		if it, ok := byCode[v.Placed[i]]; ok && v.Placed[i] != "" {
			desc = it.Emoji + " " + it.Name
		}
		places = append(places, option{"庭院" + decor.SlotName(i+1), fmt.Sprint(i + 1), desc, "📍"})
	}
	selSlot := ""
	if p.decorSlot > 0 {
		selSlot = fmt.Sprint(p.decorSlot)
	}
	// 2026-11-16 還沒有任何裝飾時不顯示位置選單（以前選了只是重繪，沒有任何反應）
	if owned > 0 {
		p.selectMenu("📍 選擇庭院位置…", places, func(ctx context.Context, it ui.Interaction, value string) error {
			p.decorSlot, _ = strconv.Atoi(value)
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, 1, selSlot)
	} else {
		e.Footer = fmt.Sprintf("💳 金幣 %s　先從下方選單買一個裝飾，才能擺到庭院。", money(v.Gold))
	}
	if p.decorSlot > 0 && owned > 0 {
		slot := p.decorSlot
		put := []option{{"收起來（清空這個位置）", "none", "", "📦"}}
		for _, it := range v.Items {
			if !it.Owned {
				continue
			}
			where := "收起來的"
			if it.Slot > 0 {
				where = "目前在" + decor.SlotName(it.Slot)
			}
			put = append(put, option{it.Name, it.Code, where, it.Emoji})
		}
		putShown, putPage, putPages := pagedOptions(put, p.decorPutPage) // 擁有超過 24 種時選單內翻頁
		p.decorPutPage = putPage
		p.selectMenu(fmt.Sprintf("🎨 擺到%s…", decor.SlotName(slot)), putShown, func(ctx context.Context, it ui.Interaction, code string) error {
			if turnPage(code, &p.decorPutPage, putPages) {
				p.refresh(ctx, it, "", nil, "重繪", time.Time{})
				return nil
			}
			if code == "none" {
				code = ""
			}
			place := p.call("place_decor", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().PlaceDecor(ctx, p.playerID, slot, code)
			})
			p.act(ctx, it, func(ctx context.Context) (string, error) {
				msg, err := place(ctx)
				return msg + "\n🌱 回「🌱 農田」就能看到擺好的庭院。", err
			}, actOpts{noProgress: true})
			return nil
		}, 2, "")
	}
	if it, ok := byCode[p.decorBuy]; ok && !it.Owned {
		code := it.Code
		e.AddField("🛒 確認購買", fmt.Sprintf("%s **%s**（%s %s）\n💰 **%s** 金幣", it.Emoji, it.Name, it.Tier.Emoji, it.Tier.Name, money(it.Price)), false)
		p.button(fmt.Sprintf("確認購買（%s 金）", money(it.Price)), func(ctx context.Context, it ui.Interaction) error {
			p.decorBuy = ""
			p.act(ctx, it, p.call("buy_decor", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuyDecor(ctx, p.playerID, code)
			}), actOpts{})
			return nil
		}, btn{emoji: "✅", row: 3, style: ui.Success, disabled: v.Gold < it.Price})
		p.button("取消", func(ctx context.Context, it ui.Interaction) error {
			p.decorBuy = ""
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 3})
	}
	p.nav("農場外觀", "looks", btn{emoji: "🖼️", row: 4}) // 2026-11-19 主題與寵物造型
	p.nav("回農田", "plots", btn{emoji: "🌱", row: 4})
	p.navBack(4)
	return e, nil
}
