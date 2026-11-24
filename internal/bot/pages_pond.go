package bot

// 面板頁面：魚塘（Go 版新增，2026-11-24，規則見 game/pond）。從釣魚中心、魚簍進入，不佔農田。

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/pond"
	"farmgm/internal/game/weather"
	"farmgm/internal/services"
)

// pondPageSize 魚塘清單與賣魚選單每頁幾隻（Discord 選單最多 25 個選項）。
const pondPageSize = 15

func (p *Panel) loadPond(ctx context.Context) (any, error) {
	return p.svc().Pond(ctx, p.playerID)
}

// pondRule 這個等級的數值說明。
func (p *Panel) pondRule(level int) string {
	return p.f("容量 **%d** 隻（史詩魚最多 %d 隻）　每 %d 小時成長 **%s%%**（佔重量區間，晴天）　池養極品 **×%s**",
		pond.Capacity(level), pond.EpicLimit(level), pond.PeriodHours, num(pond.GrowPctOf(level)), num(float64(pond.PremiumPct(level))/100))
}

func (p *Panel) pondFishLine(f services.PondFishView) string {
	head := fmt.Sprintf("%s %s %s", f.Emoji, p.s(f.Name), f.RarityEmoji)
	if f.Bred {
		head += " 🐣"
	}
	switch {
	case f.Premium:
		return p.f("✨ %s **%s** · 池養極品 **%s** 金", head, f.WeightText, money(f.Price))
	case f.FullAt != nil:
		return p.f("%s **%s**（長滿了）· %s 變成極品 **%s** 金", head, f.WeightText, tsv(*f.FullAt), money(f.PremiumPrice))
	}
	return p.f("%s %s／%s %s %d%% · 現在 %s 金 · 長滿 %s 金（約 %d 個週期）", head, f.WeightText, f.MaxText,
		fbar(float64(f.Progress), 100), f.Progress, money(f.Price), money(f.PremiumPrice), f.PeriodsLeft)
}

func (p *Panel) buildPond(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.PondView)
	e := &ui.Embed{Title: p.s("🐟 ✦ 魚塘 ✦"), Color: Sea}
	intro := p.s("把魚簍裡的魚放進魚塘、加飼料，魚會慢慢長大；長到最大重量就是「池養極品」，可以用比最高價還高的價格賣出。同種的魚養在一起還有機會生魚苗。")
	if !v.Unlocked {
		e.Description = p.f("🔒 魚塘在農場 **Lv.%d** 開放（目前 Lv.%d）。\n\n%s", pond.UnlockLevel(), v.FarmLevel, intro)
		p.nav(p.s("回釣魚中心"), "fishing", btn{emoji: "🎣", row: 0})
		p.navBack(0)
		return e, nil
	}
	if v.Level == 0 {
		e.Description = sections(intro,
			Section{Title: p.s("Lv.1 效果"), Lines: []string{p.pondRule(1)}},
			p.s("🚫 傳說魚和要用指定魚餌才釣得到的深海魚不能放。"),
			p.f("💰 建造費 **%s** 金幣（你有 %s 金幣）", money(v.NextPrice), money(v.Gold)))
		confirming := p.confirm == "pond_build"
		label, style := p.f("建造魚塘（%s 金）", money(v.NextPrice)), ui.Success
		if confirming {
			label, style = p.f("確定花 %s 金幣建造？", money(v.NextPrice)), ui.Danger
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			if p.confirm != "pond_build" {
				p.confirm = "pond_build"
				p.refresh(ctx, it, p.f("再按一次確定花 %s 金幣建造魚塘。", money(v.NextPrice)), nil, p.s("重繪"), time.Time{})
				return nil
			}
			p.act(ctx, it, p.call("build_pond", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuildPond(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🏗️", style: style, row: 0, disabled: v.Gold < v.NextPrice})
		p.nav(p.s("回釣魚中心"), "fishing", btn{emoji: "🎣", row: 0})
		p.navBack(0)
		return e, nil
	}

	level := min(v.Level, v.MaxLevel)
	fish := v.Fish
	pages := pageCount(len(fish), pondPageSize)
	p.pondPage = min(p.pondPage, pages-1)
	shown := fish[min(len(fish), p.pondPage*pondPageSize):min(len(fish), (p.pondPage+1)*pondPageSize)]
	var lines []string
	for _, f := range shown {
		lines = append(lines, p.pondFishLine(f))
	}
	listTitle := p.f("🐟 魚塘裡的魚（%d/%d）", len(fish), v.Capacity)
	if pages > 1 {
		listTitle = p.f("🐟 魚塘裡的魚（%d/%d，第 %d/%d 頁）", len(fish), v.Capacity, p.pondPage+1, pages)
	}
	if len(lines) == 0 {
		lines = []string{p.s("魚塘空空的，從下面的選單把魚簍裡的魚放進來吧！")}
	}
	parts := []any{
		p.f("魚塘 Lv.**%d**／%d　🐡 史詩魚 %d/%d", v.Level, v.MaxLevel, v.EpicCount, v.EpicLimit),
		Section{Title: p.s("效果"), Lines: []string{p.pondRule(level)}},
	}
	if v.Level < v.MaxLevel {
		parts = append(parts, Section{Title: p.f("升到 Lv.%d（%s 金幣）", v.Level+1, money(v.NextPrice)),
			Lines: []string{p.pondRule(v.Level + 1)}})
	}
	parts = append(parts, Section{Title: listTitle, Lines: lines})
	// 待辦相關的資訊放最後，貼近按鈕：繁殖、飼料、可以賣的極品
	var bottom []string
	if v.Settled.Born > 0 {
		bottom = append(bottom, p.f("🐣 魚塘裡生了 **%d** 隻魚苗！", v.Settled.Born))
	}
	if len(v.BreedHint) > 0 {
		names := make([]string, len(v.BreedHint))
		for i, n := range v.BreedHint {
			names[i] = p.s(n)
		}
		bottom = append(bottom, p.f("🐣 下次跨過午夜時有機會繁殖：%s（要吃到飼料）", strings.Join(names, p.s("、"))))
	}
	feed := p.f("🌾 飼料 **%d／%d** 份", v.Feed, v.FeedCap)
	switch {
	case len(fish) == 0:
	case v.Feed == 0:
		feed += p.s("，**吃完了**，魚不會長大")
	case v.FeedHours == 0:
		feed += p.s("，**不夠每隻魚吃一次**，沒吃到的魚不會長大")
	default:
		feed += p.f("，還夠 %d 小時", v.FeedHours)
	}
	feed += p.f("（素材背包 %d 份，每份 %s 金）", v.FeedInBag, money(v.FeedPrice))
	bottom = append(bottom, feed)
	if v.Premiums > 0 {
		bottom = append(bottom, p.f("✨ 池養極品 **%d** 隻，共 **%s** 金幣", v.Premiums, money(v.PremiumSum)))
	}
	parts = append(parts, strings.Join(bottom, "\n"))
	e.Description = runes(sections(parts...), 4000)
	today := weather.Today(v.Now)
	e.Footer = p.f("今天%s：成長 ×%s　💰 持有 %s 金幣　🚫 傳說魚、深海魚不能放", p.s(today.Label()),
		num(float64(pond.WeatherPct(v.Now))/100), money(v.Gold))

	// 第 0 列：放魚（魚簍裡可以放的魚）
	free := v.Capacity - len(fish)
	var cands []services.PondCandidate
	for _, c := range v.Candidates {
		if c.Reason == "" {
			cands = append(cands, c)
		}
	}
	if len(cands) > 25 {
		cands = cands[:25]
	}
	if free > 0 && len(cands) > 0 {
		put := &ui.Select{Placeholder: p.f("🐟 從魚簍放魚進魚塘（還有 %d 個位子）…", free), Row: 0, MinValues: 1,
			MaxValues: min(len(cands), free), OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
				ids, err := parseIDs(values)
				if err != nil {
					return err
				}
				p.act(ctx, it, p.call("put_fish_in_pond", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().PutFishInPond(ctx, p.playerID, ids)
				}), actOpts{})
				return nil
			}}
		for _, c := range cands {
			put.Options = append(put.Options, ui.SelectOption{Label: runes(p.s(c.Name)+" "+c.WeightText, 100), Value: fmt.Sprint(c.ID),
				Description: runes(p.f("%s｜現在 %s 金", p.s(c.RarityName), money(c.Price)), 100), Emoji: c.RarityEmoji})
		}
		p.add(put)
	}
	// 第 1 列：選魚賣出（這一頁的魚），按「確認出售」才賣
	if len(shown) > 0 {
		sell := &ui.Select{Placeholder: p.s("💰 選魚塘裡的魚出售…"), Row: 1, MinValues: 1, MaxValues: len(shown),
			OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
				ids, err := parseIDs(values)
				if err != nil {
					return err
				}
				p.pondSellIDs, p.confirm = ids, ""
				p.refresh(ctx, it, p.s("👇 確認要賣的魚，再按「確認出售」。"), nil, p.s("重繪"), time.Time{})
				return nil
			}}
		for _, f := range shown {
			tag := p.f("%s｜現在 %s 金", p.s(f.RarityName), money(f.Price))
			if f.Premium {
				tag = p.f("✨ 池養極品｜%s 金", money(f.Price))
			}
			sell.Options = append(sell.Options, ui.SelectOption{Label: runes(p.s(f.Name)+" "+f.WeightText, 100), Value: fmt.Sprint(f.ID),
				Description: runes(tag, 100), Emoji: f.RarityEmoji, Default: slices.Contains(p.pondSellIDs, f.ID)})
		}
		p.add(sell)
	}
	// 第 2 列：賣掉全部極品、補滿飼料、升級、重新整理
	p.button(p.f("賣掉全部極品（%d 隻，%s 金）", v.Premiums, money(v.PremiumSum)), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("sell_pond_fish", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().SellPondFish(ctx, p.playerID, nil)
		}), actOpts{})
		return nil
	}, btn{emoji: "✨", style: ui.Success, row: 2, disabled: v.Premiums == 0})
	need := v.FeedCap - v.Feed
	buy := max(0, need-v.FeedInBag)
	feedLabel := p.s("補滿飼料")
	if buy > 0 {
		feedLabel = p.f("補滿飼料（買 %d 份 %s 金）", buy, money(buy*v.FeedPrice))
	}
	p.button(feedLabel, func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("fill_pond_feed", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().FillPondFeed(ctx, p.playerID)
		}), actOpts{})
		return nil
	}, btn{emoji: "🌾", style: ui.Primary, row: 2, disabled: need <= 0})
	if v.Level < v.MaxLevel {
		confirming := p.confirm == "pond_upgrade"
		label, style := p.f("升級（%s 金）", money(v.NextPrice)), ui.Secondary
		if confirming {
			label, style = p.f("確定花 %s 金幣升級？", money(v.NextPrice)), ui.Danger
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			if p.confirm != "pond_upgrade" {
				p.confirm = "pond_upgrade"
				p.refresh(ctx, it, p.f("再按一次確定花 %s 金幣把魚塘升到 Lv.%d。", money(v.NextPrice), v.Level+1), nil, p.s("重繪"), time.Time{})
				return nil
			}
			p.act(ctx, it, p.call("upgrade_pond", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().UpgradePond(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "⬆️", style: style, row: 2, disabled: v.Gold < v.NextPrice})
	}
	p.button(p.s("重新整理"), func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "pond", "")
		return nil
	}, btn{emoji: "🔄", row: 2})
	// 第 3 列：確認出售、翻頁
	if len(p.pondSellIDs) > 0 {
		var chosen []services.PondFishView
		total := 0
		for _, f := range fish {
			if slices.Contains(p.pondSellIDs, f.ID) {
				chosen = append(chosen, f)
				total += f.Price
			}
		}
		chosenLines := make([]string, len(chosen))
		for i, f := range chosen {
			chosenLines[i] = p.pondFishLine(f)
		}
		e.AddField(p.s("⚠️ 確認出售"), runes(strings.Join(chosenLines, "\n"), 900)+p.f("\n共 **%d** 隻，**%s** 金幣", len(chosen), money(total)), false)
		p.button(p.f("確認出售 %d 隻（%s 金）", len(chosen), money(total)), func(ctx context.Context, it ui.Interaction) error {
			ids := p.pondSellIDs
			p.pondSellIDs = nil
			p.act(ctx, it, p.call("sell_pond_fish", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SellPondFish(ctx, p.playerID, ids)
			}), actOpts{})
			return nil
		}, btn{emoji: "✅", row: 3, style: ui.Danger})
		p.button(p.s("取消"), func(ctx context.Context, it ui.Interaction) error {
			p.pondSellIDs = nil
			p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 3})
	}
	if pages > 1 {
		turn := func(step int) ui.Handler {
			return func(ctx context.Context, it ui.Interaction) error {
				p.pondPage = ((p.pondPage+step)%pages + pages) % pages
				p.pondSellIDs = nil
				p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
				return nil
			}
		}
		p.button(p.s("上一頁"), turn(-1), btn{emoji: "◀️", row: 3})
		p.button(p.s("下一頁"), turn(1), btn{emoji: "▶️", row: 3})
	}
	// 第 4 列：導覽
	p.nav(p.s("我的魚簍"), "fish_basket", btn{emoji: "🎒", row: 4})
	p.nav(p.s("釣具商店"), "fish_shop", btn{emoji: "🛒", row: 4})
	p.nav(p.s("回釣魚中心"), "fishing", btn{emoji: "🎣", row: 4})
	p.navBack(4)
	return e, nil
}

// parseIDs 選單的值 → 魚的 ID。
func parseIDs(values []string) ([]int64, error) {
	ids := make([]int64, 0, len(values))
	for _, v := range values {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, gameerr.Validationf("找不到這個項目。")
		}
		ids = append(ids, id)
	}
	return ids, nil
}
