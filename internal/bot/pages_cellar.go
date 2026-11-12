package bot

// 面板頁面：地下菇房（Go 版新增，2026-11-12；從「🌱 農田」頁進入）。

import (
	"context"
	"fmt"
	"strings"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/services"
)

// Brown 地下菇房的顏色。
const Brown = 0x8B5A2B

func (p *Panel) loadCellar(ctx context.Context) (any, error) {
	return p.svc().MushroomCellar(ctx, p.playerID)
}

// cellarIntro 菇房介紹（也放在說明頁）。
func cellarIntro(cropNames map[string][]string) string {
	groups := map[string]string{"grain": "小麥、玉米", "veggie": "胡蘿蔔、番茄、南瓜", "fruit": "草莓、葡萄、西瓜等"}
	if cropNames != nil { // 管理網站可調整養分對應的作物：依目前設定列出
		for _, code := range mushroom.NutrientOrder {
			switch names := cropNames[code]; {
			case len(names) == 0 && code == mushroom.DefaultNutrient:
				groups[code] = "沒有作物時"
			case len(names) == 0:
				groups[code] = "目前沒有作物"
			case len(names) > 4:
				groups[code] = strings.Join(names[:3], "、") + "等"
			default:
				groups[code] = strings.Join(names, "、")
			}
		}
	}
	guarantee := "投滿保證一半以上是稀有菇"
	if !mushroom.Current().FullGuarantee {
		guarantee = "投越多越容易長出稀有菇"
	}
	return fmt.Sprintf("🍄 地下菇房的養分由上方農田所種下的作物決定：🌾 穀物（%s）長香菇／靈芝，"+
		"🥕 蔬菜（%s）長洋菇／松露，🍓 水果（%s）長金針菇／發光菇。\n"+
		"%d 個菇床不用種植、不用澆水，約 %s長好一批（每床 %d 朵），收成後自動開始下一批。"+
		"投入🪵木屑（每次探險都能帶回，古老森林最多）提高稀有菇機率，每床每批最多 %d 個，%s。",
		groups["grain"], groups["veggie"], groups["fruit"],
		mushroom.BedCount, mushroom.GrowHoursText(), mushroom.Yield(), mushroom.MaxSawdust, guarantee)
}

func (p *Panel) buildCellar(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.MushroomCellarView)
	e := &ui.Embed{Title: "🍄 ✦ 地下菇房 ✦", Color: Brown}
	if !v.Unlocked {
		e.Description = fmt.Sprintf("🔒 地下菇房在農場 **Lv.%d** 開放（目前 Lv.%d），升級後不用建造就能直接使用。\n\n%s",
			v.UnlockLevel, v.Level, cellarIntro(v.CropNames))
		p.nav("回農田", "plots", btn{emoji: "🌱", row: 0})
		p.navBack(0)
		return e, nil
	}
	var nutrients []string
	for _, n := range v.Nutrients {
		nutrients = append(nutrients, fmt.Sprintf("%s **%d%%**（%d 格）→ %s／✨%s", n.Name, n.Pct, n.Plots, n.CommonName, n.RareName))
	}
	if v.Planted == 0 {
		nutrients = []string{"上方農田沒有作物，只有基本的穀物養分 → 香菇／✨靈芝"}
	}
	var beds []string
	for _, b := range v.Beds {
		state := "⏳ " + tsv(b.ReadyAt)
		if b.Ready {
			state = "✅ **可以收成**"
		}
		beds = append(beds, fmt.Sprintf("🪵 #%d　%s　%s　木屑 %d/%d（%s）", b.No, bar(int(b.Progress*100), 100, 6), state,
			b.Sawdust, mushroom.MaxSawdust, b.RareText))
	}
	var prices []string
	for _, code := range mushroom.SpeciesOrder {
		s := mushroom.SpeciesMap[code]
		mark := ""
		if s.Rare {
			mark = "✨"
		}
		prices = append(prices, fmt.Sprintf("%s%s %d", mark, s.Name, balance.Goods[code].SellPrice))
	}
	e.Description = sections(
		cellarIntro(v.CropNames),
		Section{Title: "上方農田的養分", Lines: nutrients},
		Section{Title: "菇床", Lines: beds},
		fmt.Sprintf("🪵 持有木屑 **%d** 個　💰 售價：%s", v.Sawdust, strings.Join(prices, "、")),
	)
	e.Footer = "實際長出的菇在收成時依當下的作物擲骰；菇可以直接賣，或在烘焙坊做成料理。"
	p.attachImage(e, p.image("地下菇房", func() ([]byte, error) {
		return farmimage.CellarPNG(farmimage.CellarBedsFromView(v))
	}))

	free := 0
	for _, b := range v.Beds {
		free += mushroom.MaxSawdust - b.Sawdust
	}
	p.button(fmt.Sprintf("收成（%d 床）", v.ReadyCount), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("harvest_mushrooms", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().HarvestMushrooms(ctx, p.playerID)
		}), actOpts{})
		return nil
	}, btn{emoji: "🧺", style: ui.Success, row: 0, disabled: v.ReadyCount == 0})
	p.button("木屑全部補滿", func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("add_sawdust", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().AddSawdust(ctx, p.playerID, 0, 0)
		}), actOpts{noProgress: true})
		return nil
	}, btn{emoji: "🪵", style: ui.Primary, row: 0, disabled: v.Sawdust == 0 || free == 0})
	p.button("重新整理", func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "cellar", "")
		return nil
	}, btn{emoji: "🔄", row: 0})

	var options []option
	for _, b := range v.Beds {
		for n := 1; n <= min(v.Sawdust, mushroom.MaxSawdust-b.Sawdust); n++ {
			options = append(options, option{fmt.Sprintf("菇床 #%d 投入 %d 個木屑", b.No, n), fmt.Sprintf("%d:%d", b.No, n),
				"投入後：" + mushroom.RareChanceText(b.Sawdust+n), "🪵"})
		}
	}
	p.selectMenu(fmt.Sprintf("🪵 投入木屑（持有 %d）…", v.Sawdust), options, func(ctx context.Context, it ui.Interaction, value string) error {
		var bed, n int
		if _, err := fmt.Sscanf(value, "%d:%d", &bed, &n); err != nil {
			return nil
		}
		p.act(ctx, it, p.call("add_sawdust", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().AddSawdust(ctx, p.playerID, bed, n)
		}), actOpts{noProgress: true})
		return nil
	}, 1, "")
	p.nav("回農田", "plots", btn{emoji: "🌱", row: 2})
	p.navBack(2)
	return e, nil
}
