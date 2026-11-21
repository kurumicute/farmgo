package bot

// 面板頁面：蜂箱（Go 版新增，2026-11-20；從總覽的「🧭 更多功能」進入）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/beehive"
	"farmgm/internal/services"
)

// Honey 蜂箱頁的顏色。
const Honey = 0xF5B700

type beehiveData struct {
	view   services.BeehiveView
	status services.FarmStatus // 農田圖（蜂箱畫在農田上）
}

func (p *Panel) loadBeehive(ctx context.Context) (any, error) {
	v, err := p.svc().Beehive(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	st, err := p.svc().GetFarmStatus(ctx, p.playerID)
	return beehiveData{v, st}, err
}

// hiveEffects 某個等級的效果說明（產蜜不分等級，寫在 honeyRule）。
func (p *Panel) hiveEffects(level int) string {
	return p.f("🌸 周圍 8 格種下的作物突變 +%s%%　🐝 叮小偷 %s%%", bpPct(beehive.MutationBP[level]), bpPct(beehive.StingBP[level]))
}

// honeyRule 產蜜規則（2026-11-21 改成固定，不分等級、天氣）。
func (p *Panel) honeyRule() string {
	return p.f("🍯 每 %d 小時產 %d 罐蜂蜜，最多存 %d 罐（每罐賣 %s 金幣）", beehive.IntervalSeconds/3600, beehive.HoneyPerInterval,
		beehive.Capacity, money(balance.Goods[beehive.Honey].SellPrice))
}

func (p *Panel) buildBeehive(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(beehiveData)
	v := d.view
	e := &ui.Embed{Title: p.s("🐝 ✦ 蜂箱 ✦"), Color: Honey}
	if !v.Unlocked {
		e.Description = p.f("🔒 蜂箱在農場 **Lv.%d** 開放（目前 Lv.%d）。\n\n蜂箱放在一格空地上，那一格不能種植，但周圍的作物比較容易突變，還會產蜂蜜、幫忙叮跑小偷。\n%s\n%s",
			beehive.UnlockLevel, v.FarmLevel, p.hiveEffects(1), p.honeyRule())
		p.navBack(0)
		return e, nil
	}
	h := v.Hive
	if !h.Placed() {
		e.Description = sections(
			p.f("選一格空地放蜂箱（**%s** 金幣）。那一格不能種植，但周圍的作物比較容易突變，還會產蜂蜜、幫忙叮跑小偷。", money(beehive.Prices[1])),
			Section{Title: p.s("Lv.1 效果"), Lines: []string{p.hiveEffects(1)}},
			p.honeyRule(),
		)
	} else {
		var neighbors []string
		for _, n := range v.Neighbors {
			neighbors = append(neighbors, fmt.Sprintf("#%d", n))
		}
		honey := p.f("🍯 蜂蜜 **%d／%d** 罐　每 %d 小時產 %d 罐", h.Honey, v.Capacity, beehive.IntervalSeconds/3600, beehive.HoneyPerInterval)
		if v.NextAt != nil {
			honey += p.f("，下一批 %s", tsv(*v.NextAt))
		} else {
			honey += p.s("，**已經滿了**")
		}
		parts := []any{
			p.f("蜂箱在第 **%d** 格，Lv.**%d**／%d", h.Plot, h.Level, beehive.MaxLevel),
			Section{Title: p.s("效果"), Lines: []string{p.hiveEffects(h.Level), p.s("授粉的格子：") + strings.Join(neighbors, " ")}},
		}
		if h.Level < beehive.MaxLevel {
			parts = append(parts, Section{Title: p.f("升到 Lv.%d（%s 金幣）", h.Level+1, money(beehive.Prices[h.Level+1])),
				Lines: []string{p.hiveEffects(h.Level + 1)}})
		}
		parts = append(parts, honey) // 蜂蜜存量放最後，貼近按鈕
		e.Description = sections(parts...)
	}
	e.Footer = p.f("💰 持有 %s 金幣。蜂蜜可以直接賣，或做成蜂蜜蛋糕（烘焙坊）、蜂蜜牛奶（乳品工坊）。", money(v.Gold))
	p.attachImage(e, p.farmImage(d.status))

	if h.Placed() {
		p.button(p.f("收蜂蜜（%d 罐）", h.Honey), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("collect_honey", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().CollectHoney(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🍯", style: ui.Success, row: 0, disabled: h.Honey == 0})
		if h.Level < beehive.MaxLevel {
			price := beehive.Prices[h.Level+1]
			confirming := p.confirm == "beehive_upgrade"
			label, style := p.f("升級（%s 金幣）", money(price)), ui.Primary
			if confirming {
				label, style = p.f("確定花 %s 金幣升級？", money(price)), ui.Danger
			}
			p.button(label, func(ctx context.Context, it ui.Interaction) error {
				if p.confirm != "beehive_upgrade" { // 同擴建：第一次點擊只換成確認按鈕（goTo 會清掉 confirm）
					p.confirm = "beehive_upgrade"
					p.refresh(ctx, it, p.f("再按一次確定花 %s 金幣把蜂箱升到 Lv.%d。", money(price), h.Level+1), nil, p.s("重繪"), time.Time{})
					return nil
				}
				p.act(ctx, it, p.call("upgrade_beehive", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().UpgradeBeehive(ctx, p.playerID)
				}), actOpts{})
				return nil
			}, btn{emoji: "⬆️", style: style, row: 0, disabled: v.Gold < price})
		}
		// 拆除（2026-11-20 使用者要求）：按兩次確認；不退費，蜂蜜先收進倉庫
		label := p.s("拆除蜂箱")
		if p.confirm == "beehive_remove" {
			label = p.s("確定拆除？")
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			if p.confirm != "beehive_remove" {
				p.confirm = "beehive_remove"
				p.refresh(ctx, it, p.f("⚠️ 拆除後第 %d 格變回空地，蜂箱等級歸零、不退費（之後要重新花 %s 金幣放置）；蜂箱裡的蜂蜜會先收進倉庫。再按一次「確定拆除？」。",
					h.Plot, money(beehive.Prices[1])), nil, p.s("重繪"), time.Time{})
				return nil
			}
			p.act(ctx, it, p.call("remove_beehive", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().RemoveBeehive(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🪓", style: ui.Danger, row: 0})
	}
	p.button(p.s("重新整理"), func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "beehive", "")
		return nil
	}, btn{emoji: "🔄", row: 0})

	fee, verb := beehive.Prices[1], p.s("放在")
	if h.Placed() {
		fee, verb = beehive.MoveFee, p.s("搬到")
	}
	var options []option
	for _, n := range v.EmptyPlots {
		options = append(options, option{p.f("%s第 %d 格", verb, n), fmt.Sprint(n), p.f("%s 金幣", money(fee)), "🐝"})
	}
	holder := p.f("🐝 選一格空地放蜂箱（%s 金幣）…", money(fee))
	if h.Placed() {
		holder = p.f("🐝 把蜂箱搬到別格（%s 金幣，蜂蜜保留）…", money(fee))
	}
	p.selectMenu(holder, options, func(ctx context.Context, it ui.Interaction, value string) error {
		n, ok := parseDigits(value)
		if !ok {
			return nil
		}
		p.act(ctx, it, p.call("place_beehive", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().PlaceBeehive(ctx, p.playerID, n)
		}), actOpts{})
		return nil
	}, 1, "")
	p.nav(p.s("回農田"), "plots", btn{emoji: "🌱", row: 2})
	p.navBack(2)
	return e, nil
}
