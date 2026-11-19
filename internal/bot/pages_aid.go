package bot

// 送禮與互助（Go 版新增，2026-11-17）：選一位同群組的玩家，送他一份作物或幫他拔草（2026-11-19 由幫忙澆水改成），雙方都拿少量金幣。

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/aid"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/services"
)

func (p *Panel) loadAid(ctx context.Context) (any, error) {
	if p.groupID == nil {
		return nil, gameerr.Validationf("送禮與互助需要在 Discord 伺服器中使用（只能幫同一伺服器的成員）。")
	}
	return p.svc().AidOverview(ctx, *p.groupID, p.playerID)
}

func (p *Panel) buildAid(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.AidView)
	c := v.Config
	var target *services.AidTargetView
	for i := range v.Targets {
		if p.aidTarget != nil && v.Targets[i].PlayerID == *p.aidTarget {
			target = &v.Targets[i]
		}
	}
	if target == nil {
		p.aidTarget = nil
	}
	rules := []string{
		p.f("🎁 **送禮**：送同伺服器的玩家一份作物（普通品質、沒有突變，最多 %d 個、價值 %s 金以內），雙方各得 **%d** 金",
			c.GiftMaxQuantity, money(c.GiftMaxValue), c.GiftRewardGold),
		p.f("🌿 **幫忙拔草**：對方所有生長中作物的剩餘生長時間 **-%d%%**，雙方各得 **%d** 金；每人每天只能被拔草 **%d** 次",
			aid.WeedGrowPct, c.WeedRewardGold, aid.WeedReceivePerDay),
		p.f("🔒 雙方都要 **Lv.%d** 以上、農場建立滿 **%d** 天；同一個人同一種互助每天只能一次", c.MinLevel, c.MinFarmDays),
	}
	today := []string{
		p.f("🎁 送出 %d／%d 份　📥 收到 %d／%d 份", v.GiftSent, c.GiftSendPerDay, v.GiftReceived, c.GiftReceivePerDay),
		p.f("🌿 幫忙拔草 %d／%d 位　🙏 被拔草 %d／%d 次", v.WeedSent, c.WeedSendPerDay, v.WeedReceived, aid.WeedReceivePerDay),
	}
	parts := []any{p.s("用正面互動代替偷菜：每天可以送朋友一份作物、幫忙拔草，雙方都有獎勵。"),
		Section{Title: p.s("規則"), Lines: rules}, Section{Title: p.s("今天（台灣時間 0 點重新計算）"), Lines: today}}
	if v.Blocked != "" {
		parts = append(parts, "⚠️ "+v.Blocked+p.s("。"))
	}
	if target != nil {
		lines := []string{p.f("👤 **%s**（%s）Lv.%d　🌱 %d 格生長中", escapeMD(p.s(target.Name)), escapeMD(target.FarmName), target.Level, target.Growing)}
		if target.Blocked != "" {
			lines = append(lines, "⚠️ "+target.Blocked)
		}
		if target.GiftedToday {
			lines = append(lines, p.s("✅ 今天已經送過禮物給他"))
		}
		if target.WeededToday {
			lines = append(lines, p.s("✅ 今天已經幫他拔過草"))
		} else if target.WeedTaken {
			lines = append(lines, p.s("🌿 今天已經有人幫他拔過草了"))
		}
		parts = append(parts, Section{Title: p.s("選擇的對象"), Lines: lines})
	} else {
		parts = append(parts, p.s("👇 先用第一個選單選一位玩家。"))
	}
	e := &ui.Embed{Title: p.f("🤝 ✦ 送禮與互助｜%s ✦", p.groupName), Color: Green, Description: sections(parts...)}
	e.Footer = p.s("獎勵不算進「淨賺金幣」排行。")

	// 第 1 列：選對象（超過 25 人時選單內翻頁）
	var people []option
	for _, t := range v.Targets {
		desc, emoji := p.f("Lv.%d｜🌱 %d 格生長中", t.Level, t.Growing), "👤"
		var done []string
		if t.GiftedToday {
			done = append(done, p.s("已送禮"))
		}
		if t.WeededToday {
			done = append(done, p.s("已幫忙拔草"))
		} else if t.WeedTaken {
			done = append(done, p.s("已被拔草"))
		}
		switch {
		case t.Blocked != "":
			desc, emoji = p.f("Lv.%d｜還不能互助", t.Level), "🔒"
		case len(done) > 0:
			desc += p.s("｜今天") + strings.Join(done, p.s("、"))
		case t.Growing > 0:
			emoji = "🌿"
		}
		people = append(people, option{p.s(t.Name), strconv.FormatInt(t.PlayerID, 10), desc, emoji})
	}
	if len(people) == 0 {
		e.Description += p.s("\n\n這個伺服器還沒有其他玩家。")
	} else {
		shown, page, pages := pagedOptions(people, p.aidTargetPage)
		p.aidTargetPage = page
		selected := ""
		if target != nil {
			selected = strconv.FormatInt(target.PlayerID, 10)
		}
		p.selectMenu(p.s("👥 選擇要送禮或幫忙的玩家…"), shown, func(ctx context.Context, it ui.Interaction, value string) error {
			if turnPage(value, &p.aidTargetPage, pages) {
				p.goTo(ctx, it, "aid", "")
				return nil
			}
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return gameerr.Validationf("找不到這個項目。")
			}
			p.aidTarget, p.aidItemPage = &id, 0
			p.goTo(ctx, it, "aid", "")
			return nil
		}, 0, selected)
	}

	if target != nil {
		victim, name := target.PlayerID, p.s(target.Name)
		// 第 2 列：送禮（每種作物一個選項，送一份的上限數量）
		var gifts []option
		for _, it := range v.Items {
			gifts = append(gifts, option{p.f("送 %s ×%d", p.s(it.Name), it.Limit), fmt.Sprintf("%s|%d", it.Code, it.Limit),
				p.f("持有 %d 個｜單價 %d 金", it.Quantity, it.UnitPrice), cropEmoji(it.Code)})
		}
		canGift := v.Blocked == "" && target.Blocked == "" && !target.GiftedToday && v.GiftSent < c.GiftSendPerDay
		if canGift && len(gifts) > 0 {
			shown, page, pages := pagedOptions(gifts, p.aidItemPage)
			p.aidItemPage = page
			p.selectMenu(p.f("🎁 選擇要送給 %s 的作物…", name), shown, func(ctx context.Context, it ui.Interaction, value string) error {
				if turnPage(value, &p.aidItemPage, pages) {
					p.goTo(ctx, it, "aid", "")
					return nil
				}
				code, qtyText, _ := strings.Cut(value, "|")
				qty, err := strconv.Atoi(qtyText)
				if err != nil {
					return gameerr.Validationf("找不到這個項目。")
				}
				p.act(ctx, it, p.call("send_gift", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().SendGift(ctx, *p.groupID, p.playerID, victim, code, qty)
				}), actOpts{})
				return nil
			}, 1, "")
		} else if canGift {
			e.Description += p.s("\n\n🎁 倉庫裡沒有可以當禮物的作物（只能送普通品質、沒有突變的作物）。")
		}
		// 第 3 列：幫忙拔草
		canWeed := v.Blocked == "" && target.Blocked == "" && !target.WeedTaken && target.Growing > 0 && v.WeedSent < c.WeedSendPerDay
		p.button(p.f("幫 %s 拔草（%d 格）", name, target.Growing), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("help_weed", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().HelpWeed(ctx, *p.groupID, p.playerID, victim)
			}), actOpts{})
			return nil
		}, btn{emoji: "🌿", style: ui.Primary, row: 2, disabled: !canWeed})
	}
	p.nav(p.s("重新整理"), "aid", btn{emoji: "🔄", row: 3})
	p.nav(p.s("偷菜"), "steal", btn{emoji: "🥷", row: 3})
	p.navBack(3)
	return e, nil
}
