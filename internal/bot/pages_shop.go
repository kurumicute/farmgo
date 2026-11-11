package bot

// 面板頁面：商店（種子、道具、倉庫擴大卷、工具與奢侈品）、作物價格、品質與突變、販售
// （對應 Python panels.py 的「商店」與「販售」區段：_build_shop、_build_prices、_build_genetics、_build_sell）。

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/seasons"
	"farmgm/internal/numfmt"
	"farmgm/internal/services"
)

// noop 停用中的按鈕（例如目前所在的分頁）；Discord 仍要求回應，只先 defer（Python _noop）。
func (p *Panel) noop(ctx context.Context, it ui.Interaction) error {
	return it.DeferUpdateIfPending(ctx)
}

// runes 取前 n 個字元（Python 字串切片 s[:n]，不加省略符號）。
func runes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ======================================================================
// 商店（種子與道具在同一個選單）
// ======================================================================

type shopData struct {
	crops    []services.CropInfo
	events   []services.ActiveEventView
	gold     int
	capacity int
	used     int
	held     map[string]int
	image    []byte
}

func (p *Panel) loadShop(ctx context.Context) (any, error) {
	svc, pid := p.svc(), p.playerID
	bp, err := svc.GetBackpack(ctx, pid)
	if err != nil {
		return nil, err
	}
	d := shopData{held: map[string]int{}}
	for _, x := range bp.Seeds {
		d.held["seed:"+x.Code] = x.Quantity
	}
	for _, x := range bp.Consumables {
		d.held["item:"+x.Code] = x.Quantity
	}
	for _, t := range bp.Tools {
		if t.Owned {
			d.held["item:"+t.Code] = 1
		}
	}
	status, err := svc.GetFarmStatus(ctx, pid)
	if err != nil {
		return nil, err
	}
	all, err := svc.GetMarketPrices(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.ShopBuyable && c.InSeason { // 稀有作物只能探索或培育取得；只賣當季的種子（Go 版新增）
			d.crops = append(d.crops, c)
		}
	}
	if d.events, err = svc.ActiveEvents(ctx); err != nil {
		return nil, err
	}
	d.gold, d.capacity, d.used = status.Gold, status.WarehouseCapacity, status.WarehouseUsed
	d.image = p.image("商店", farmimage.ShopPNG)
	return d, nil
}

// heldAfter 購買後重新讀取持有數量，讓玩家確定真的買到了。
func (p *Panel) heldAfter(ctx context.Context, key string) (string, error) {
	bp, err := Call(ctx, p.cmds.Runner, "get_backpack", func(ctx context.Context) (services.Backpack, error) {
		return p.svc().GetBackpack(ctx, p.playerID)
	})
	if err != nil {
		return "", err
	}
	kind, code, _ := strings.Cut(key, ":")
	n := lineQty(bp.Consumables, code)
	if kind == "seed" {
		n = lineQty(bp.Seeds, code)
	}
	st, err := Call(ctx, p.cmds.Runner, "get_farm_status", func(ctx context.Context) (services.FarmStatus, error) {
		return p.svc().GetFarmStatus(ctx, p.playerID)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("🎒 現在持有 **%d**　💰 剩餘金幣 **%s**", n, money(st.Gold)), nil
}

// buyRun 購買後附上持有數量（Python buy_seed／buy_item）。
func (p *Panel) buyRun(key, name string, buy func(ctx context.Context, q int) (services.ActionResult, error)) func(ctx context.Context, q int) (string, error) {
	return func(ctx context.Context, q int) (string, error) {
		r, err := Call(ctx, p.cmds.Runner, name, func(ctx context.Context) (services.ActionResult, error) { return buy(ctx, q) })
		if err != nil {
			return "", err
		}
		held, err := p.heldAfter(ctx, key)
		if err != nil {
			return "", err
		}
		return r.Message + "\n" + held, nil
	}
}

// tradeMax 金幣可以買的數量（至少 1，最多 MaxTradeQuantity）。
func tradeMax(gold, price int) int {
	return max(1, min(balance.MaxTradeQuantity, numfmt.FloorDiv(gold, price)))
}

func (p *Panel) buildShop(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(shopData)
	held := d.held
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
	var seeds []string
	for _, c := range d.crops {
		line := fmt.Sprintf("%s%s **%s種子**　💰 **%d** 金　🎒 持有 **%d**\n　⏱️ %s　🧺 收成 ×%d %s%s　🏷️ 售價 %d 金／個",
			mark("seed:"+c.Code), cropEmoji(c.Code), c.Name, c.SeedPrice, held["seed:"+c.Code],
			services.LongDuration(c.GrowSeconds), c.HarvestQuantity, cropEmoji(c.Code), c.Name, c.SellPrice)
		if c.SeasonText != "" {
			when := "現在不能種，種子可以先買"
			if c.InSeason {
				when = "現在可以種"
			}
			line += fmt.Sprintf("\n　%s（%s）", c.SeasonText, when)
		}
		seeds = append(seeds, line)
	}
	var consumables, tools []string
	for _, code := range balance.ItemOrder {
		it := balance.Items[code]
		have := held["item:"+code]
		emoji := lookupOr(ItemEmoji, code, "🎁")
		if it.Kind == "tool" {
			tag := "♾️ 永久工具"
			if slices.Contains(balance.LuxuryTools, code) {
				tag = "💎 奢侈品"
			}
			own := "🔒 未擁有"
			if have != 0 {
				own = "✅ 已擁有"
			}
			tools = append(tools, fmt.Sprintf("%s%s **%s**　💰 **%s** 金　%s　%s\n　📝 %s", mark("item:"+code), emoji, it.Name,
				money(it.Price), tag, own, it.Desc))
		} else {
			consumables = append(consumables, fmt.Sprintf("%s%s **%s**　💰 **%d** 金　🎒 持有 **%d**\n　📝 %s", mark("item:"+code), emoji,
				it.Name, it.Price, have, it.Desc))
		}
	}
	expandPrice, canExpand := balance.WarehouseExpansionPrice(d.capacity)
	ladder := make([]string, len(balance.WarehouseExpansionPrices))
	for i, x := range balance.WarehouseExpansionPrices {
		ladder[i] = money(x)
	}
	priceText := "✅ 已達最大容量"
	if canExpand {
		priceText = fmt.Sprintf("💰 **%s** 金", money(expandPrice))
	}
	warehouse := fmt.Sprintf("%s📦 **倉庫擴大卷**　%s　🎒 倉庫 **%d/%d**\n　📝 立即 +%d 格（最大 %d 格），價格逐次提高：%s",
		mark("special:warehouse"), priceText, d.used, d.capacity, balance.WarehouseExpansionStep, balance.WarehouseMaxCapacity,
		strings.Join(ladder, " → "))
	var events []string
	for _, ev := range d.events {
		name, _ := ev.View["name"].(string)
		effects, _ := ev.View["effects_text"].(string)
		line := "🔥 **" + name + "**"
		if ev.CropName != nil && *ev.CropName != "" {
			line += "（限" + *ev.CropName + "）"
		}
		events = append(events, line+"\n　"+effects)
	}
	e := &ui.Embed{Title: "🏪 ✦ 農場商店 ✦", Color: Gold, Description: sections(
		fmt.Sprintf("💰 你的金幣：**%s**　｜　🎣 釣具在「釣具商店」分頁　｜　🎁 溫室、每週特賣、稱號在「特賣與升級」", money(d.gold)),
		seasons.NowText(time.Now())+"：🌰 商店只賣當季可以種的種子，換季後會換一批。",
		Section{Title: "🌰 種子", Lines: seeds, Sep: "\n\n"},
		Section{Title: "🧪 道具", Lines: consumables, Sep: "\n\n"},
		Section{Title: "📦 倉庫", Lines: []string{warehouse}},
		Section{Title: "💎 工具與奢侈品", Lines: tools, Sep: "\n\n"},
		Section{Title: "🎉 今天的活動", Lines: events, Sep: "\n\n"},
	)}
	if p.picker != nil {
		hint := "👇 按下方的數量按鈕購買"
		if p.picker.Confirm {
			hint = "👇 按「確認購買」完成購買"
		}
		e.AddField("🛒 已選擇", fmt.Sprintf("**%s**（%s）\n%s", p.picker.Label, p.picker.PriceText, hint), false)
	} else {
		e.Footer = "👇 先從選單挑一樣商品，再選數量"
	}

	pick := func(ctx context.Context, it ui.Interaction, key string) error {
		kind, code, _ := strings.Cut(key, ":")
		switch kind {
		case "special":
			price := 0
			if canExpand {
				price = expandPrice
			}
			p.picker = &Picker{Key: key, Label: "📦 倉庫擴大卷", PriceText: money(price) + " 金", Confirm: true, Max: 1,
				Run: p.callQty("expand_warehouse", func(ctx context.Context, _ int) (services.ActionResult, error) {
					return p.svc().ExpandWarehouse(ctx, p.playerID)
				})}
		case "seed":
			i := slices.IndexFunc(d.crops, func(c services.CropInfo) bool { return c.Code == code })
			if i < 0 {
				return gameerr.NotFoundf("找不到這個商品。")
			}
			crop := d.crops[i]
			p.picker = &Picker{Key: key, Label: fmt.Sprintf("%s %s種子", cropEmoji(code), crop.Name),
				PriceText: fmt.Sprintf("每顆 %d 金", crop.SeedPrice), Verb: "買", Max: tradeMax(d.gold, crop.SeedPrice),
				Presets: []int{1, 5, 10}, Keep: true, NoProgress: true,
				Run: p.buyRun(key, "buy_seeds", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().BuySeeds(ctx, p.playerID, code, q)
				})}
		default:
			item, ok := balance.Items[code]
			if !ok {
				return gameerr.NotFoundf("找不到這個商品。")
			}
			label := lookupOr(ItemEmoji, code, "🎁") + " " + item.Name
			if item.Kind == "tool" { // 永久工具與奢侈品價格高：需要再按一次確認才購買
				p.picker = &Picker{Key: key, Label: label, PriceText: money(item.Price) + " 金", Confirm: true, Max: 1,
					Run: p.callQty("buy_item", func(ctx context.Context, _ int) (services.ActionResult, error) {
						return p.svc().BuyItem(ctx, p.playerID, code, 1)
					})}
			} else {
				p.picker = &Picker{Key: key, Label: label, PriceText: fmt.Sprintf("每個 %d 金", item.Price), Verb: "買",
					Max: tradeMax(d.gold, item.Price), Presets: []int{1, 5, 10}, Keep: true, NoProgress: true,
					Run: p.buyRun(key, "buy_item", func(ctx context.Context, q int) (services.ActionResult, error) {
						return p.svc().BuyItem(ctx, p.playerID, code, q)
					})}
			}
		}
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}

	var options []option
	for _, c := range d.crops {
		options = append(options, option{c.Name + "種子", "seed:" + c.Code, fmt.Sprintf("🌰 種子｜💰 %d 金", c.SeedPrice), cropEmoji(c.Code)})
	}
	for _, code := range balance.ItemOrder {
		if it := balance.Items[code]; it.Kind != "tool" {
			options = append(options, option{it.Name, "item:" + code, fmt.Sprintf("🧪 道具｜💰 %d 金", it.Price), ItemEmoji[code]})
		}
	}
	if canExpand && expandPrice != 0 {
		options = append(options, option{"倉庫擴大卷", "special:warehouse",
			fmt.Sprintf("📦 +%d 格｜💰 %s 金", balance.WarehouseExpansionStep, money(expandPrice)), "📦"})
	}
	for _, code := range balance.ItemOrder {
		if it := balance.Items[code]; it.Kind == "tool" && held["item:"+code] == 0 {
			options = append(options, option{it.Name, "item:" + code, fmt.Sprintf("💎 工具｜💰 %s 金", money(it.Price)), ItemEmoji[code]})
		}
	}
	p.selectMenu("🛒 選擇要買的商品…", options, pick, 0, selected)
	p.quantityButtons(1)
	// 商店分成兩個分頁：農場商店（種子、道具、工具）與釣具商店
	p.button("農場商店", p.noop, btn{emoji: "🌾", row: 4, style: ui.Primary, disabled: true})
	p.nav("釣具商店", "fish_shop", btn{emoji: "🎣", row: 4})
	p.nav("特賣與升級", "shop_special", btn{emoji: "🎁", row: 4, style: ui.Success})
	p.navBack(4)
	p.attachImage(e, d.image)
	return e, nil
}

// ======================================================================
// 作物價格、品質與突變
// ======================================================================

func (p *Panel) loadPrices(ctx context.Context) (any, error) {
	return p.svc().CropCatalog(ctx)
}

func (p *Panel) buildPrices(_ context.Context, data any) (*ui.Embed, error) {
	list := data.([]services.CropCatalogEntry)
	row := func(prices map[string]map[string]int, mutation string) string {
		parts := make([]string, len(balance.QualityOrder))
		for i, q := range balance.QualityOrder {
			parts[i] = fmt.Sprintf("%s **%d**", balance.Qualities[q], prices[q][mutation])
		}
		return strings.Join(parts, "｜")
	}
	var blocks []string
	for _, c := range list {
		season := ""
		if c.SeasonText != "" {
			season = "　" + c.SeasonText
		}
		if c.SeasonSellPct > 100 { // 非當季：賣價較高（Go 版新增）
			season += fmt.Sprintf("　📈 非當季售價 +%d%%", c.SeasonSellPct-100)
		}
		blocks = append(blocks, fmt.Sprintf("%s **%s**　種子 %d 金%s\n　⏱️ %s　🧺 收成 ×%d %s%s\n　🏷️ 一般：%s\n　🌟 黃金作物：%s\n　💎 珍稀變種「%s」：%s",
			cropEmoji(c.Code), c.Name, c.SeedPrice, season, services.LongDuration(c.GrowSeconds), c.HarvestQuantity,
			cropEmoji(c.Code), c.Name, row(c.Prices, "none"), row(c.Prices, "golden"), c.VariantName, row(c.Prices, "variant")))
	}
	e := &ui.Embed{Title: "📈 ✦ 市場價格 ✦", Color: Gold, Description: sections(
		fmt.Sprintf("每個農產品的單價依 **品質** 與 **突變** 而不同（金幣／個）。\n%s：當季作物照原價，非當季的作物售價 +%d%%（物以稀為貴）。",
			seasons.NowText(time.Now()), seasons.OffSeasonSellPct-100),
		Section{Title: "作物", Lines: blocks, Sep: "\n\n"},
		"🍀 巨型作物：收成數量變多，單價同一般｜⚡ 快速生長：只縮短時間，單價同一般",
	)}
	p.nav("品質與突變", "genetics", btn{emoji: "🧬", row: 0})
	p.navBack(0)
	return e, nil
}

type geneticsData struct {
	quality   services.QualityTable
	mutations []services.MutationRow
}

func (p *Panel) loadGenetics(ctx context.Context) (any, error) {
	pid := p.playerID
	q, err := p.svc().QualityTable(ctx, &pid)
	if err != nil {
		return nil, err
	}
	m, err := p.svc().MutationTable(ctx)
	return geneticsData{q, m}, err
}

// pct 萬分比 → 百分比文字（Python f"{bp / 100:g}"）。
func pct(bp int) string { return fmt.Sprintf("%.6g", float64(bp)/100) }

func (p *Panel) buildGenetics(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(geneticsData)
	t := d.quality
	quality := make([]string, len(balance.QualityOrder))
	for i, q := range balance.QualityOrder {
		line := fmt.Sprintf("**%s**　機率 %s%%", balance.Qualities[q], pct(t.BaseBP[q]))
		if len(t.EffectiveBP) > 0 {
			line += fmt.Sprintf("（你的實際機率 **%s%%**）", pct(t.EffectiveBP[q]))
		}
		quality[i] = line + "　售價 ×" + pct(t.PricePct[q])
	}
	mutations := make([]string, len(d.mutations))
	for i, m := range d.mutations {
		mutations[i] = fmt.Sprintf("**%s**　機率 %s%%\n　%s", m.Name, pct(m.BP), m.Effect)
	}
	level := "None"
	if t.Level != nil {
		level = fmt.Sprint(*t.Level)
	}
	e := &ui.Embed{Title: "🧬 ✦ 品質與突變 ✦", Color: Gold, Description: sections(
		"品質與突變在 **播種時** 決定，之後不會改變。",
		Section{Title: "⭐ 品質", Lines: quality},
		fmt.Sprintf("農場每升 1 級，有 %s%% 機率從普通升到較高品質（上限 %s%%）。你的農場等級：**%s**",
			pct(t.LevelBonusBP), pct(t.LevelBonusMaxBP), level),
		Section{Title: "🧪 突變（每批最多一種）", Lines: mutations, Sep: "\n\n"},
	)}
	p.nav("價目表", "prices", btn{emoji: "📈", row: 0})
	p.navBack(0)
	return e, nil
}

// ======================================================================
// 販售
// ======================================================================

// sellRow 一項可以賣的東西（Python _sellables 的 dict）。prize 為賭場獎品（以 prizeID 販售）。
type sellRow struct {
	key, code, quality, mutation, name, emoji string
	quantity, price                           int
	prize                                     bool
	prizeID                                   int64
}

func (r sellRow) subtotal() int { return r.quantity * r.price }

func (p *Panel) loadSell(ctx context.Context) (any, error) {
	return p.svc().GetBackpack(ctx, p.playerID)
}

// sellables 背包中可以賣的農產品與商品（不含賭場獎品）。
func sellables(bp services.Backpack) []sellRow {
	var rows []sellRow
	for _, x := range bp.Produce {
		// 珍稀變種以變種代碼販售（服務層會自動判定突變），其他農產品指定品質與突變
		mutation := x.Mutation
		if mutation == "variant" {
			mutation = "none"
		}
		rows = append(rows, sellRow{key: fmt.Sprintf("p|%s|%s|%s", x.Code, x.Quality, x.Mutation), code: x.Code, quality: x.Quality,
			mutation: mutation, name: x.Name + services.GeneticsLabel(x.Quality, x.Mutation), quantity: x.Quantity, price: x.Price,
			emoji: cropEmoji(x.Code)})
	}
	for _, x := range bp.Goods {
		rows = append(rows, sellRow{key: "g|" + x.Code, code: x.Code, quality: "normal", mutation: "none", name: x.Name,
			quantity: x.Quantity, price: x.Price, emoji: "🥚"})
	}
	return rows
}

func prizeRows(bp services.Backpack) []sellRow {
	var rows []sellRow
	for _, x := range bp.Prizes {
		rows = append(rows, sellRow{key: fmt.Sprintf("z|%d", x.ID), prize: true, prizeID: x.ID,
			name: fmt.Sprintf("%s（賭場 ×%d）", x.Name, x.Multiplier), quantity: x.Quantity, price: x.UnitPrice, emoji: "🏆"})
	}
	return rows
}

func (p *Panel) sellCall(ctx context.Context, row sellRow, quantity int) (services.ActionResult, error) {
	if row.prize {
		return Call(ctx, p.cmds.Runner, "sell_prize", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().SellPrize(ctx, p.playerID, row.prizeID, quantity)
		})
	}
	return Call(ctx, p.cmds.Runner, "sell", func(ctx context.Context) (services.ActionResult, error) {
		return p.svc().Sell(ctx, p.playerID, row.code, quantity, row.quality, row.mutation)
	})
}

func (p *Panel) noteSale(ctx context.Context, income int) (string, error) {
	return Call(ctx, p.cmds.Runner, "note_sale", func(ctx context.Context) (string, error) {
		return p.svc().NoteSale(ctx, p.playerID, income)
	})
}

// sellOne 賣出一項（農產品或商品）並檢查「一次販售」紀錄。
func (p *Panel) sellOne(ctx context.Context, row sellRow, quantity int) (string, error) {
	r, err := p.sellCall(ctx, row, quantity)
	if err != nil {
		return "", err
	}
	note, err := p.noteSale(ctx, r.GoldDelta)
	return r.Message + note, err
}

// sellAll 賣出背包中所有農產品與商品（不含賭場獎品）。
func (p *Panel) sellAll(ctx context.Context) (string, error) {
	bp, err := Call(ctx, p.cmds.Runner, "get_backpack", func(ctx context.Context) (services.Backpack, error) {
		return p.svc().GetBackpack(ctx, p.playerID)
	})
	if err != nil {
		return "", err
	}
	rows := sellables(bp)
	if len(rows) == 0 {
		return "", gameerr.Validationf("沒有可以賣的東西。")
	}
	return p.sellRows(ctx, rows)
}

// sellRows 依序賣出多項（每項一筆交易），失敗的項目列在最後；「全部賣出」與勾選販售共用。
// 遊戲規則的錯誤（數量不足等）列為未賣出；其他錯誤直接回傳（已賣出的項目不會還原，與 Python 相同）。
func (p *Panel) sellRows(ctx context.Context, rows []sellRow) (string, error) {
	income, sold, gold, hasGold := 0, 0, 0, false
	var failed []string
	for _, row := range rows {
		r, err := p.sellCall(ctx, row, row.quantity)
		if err != nil {
			if ge, ok := gameerr.As(err); ok {
				failed = append(failed, row.name+"："+ge.Error())
				continue
			}
			return "", err
		}
		income += r.GoldDelta
		sold += row.quantity
		gold, hasGold = r.Gold, true
	}
	text := fmt.Sprintf("賣出 %d 個商品，獲得 %s 金幣。", sold, money(income))
	if hasGold {
		text += fmt.Sprintf("\n💰 金幣 %s → %s", money(gold-income), money(gold))
		note, err := p.noteSale(ctx, income)
		if err != nil {
			return "", err
		}
		text += note
	}
	if len(failed) > 0 {
		text += "\n未賣出：" + strings.Join(failed[:min(3, len(failed))], "；")
	}
	return text, nil
}

func (p *Panel) buildSell(_ context.Context, data any) (*ui.Embed, error) {
	bp := data.(services.Backpack)
	rows := sellables(bp)
	total := 0
	for _, r := range rows {
		total += r.subtotal()
	}
	selected := ""
	if p.picker != nil {
		selected = p.picker.Key
	}
	line := func(r sellRow) string {
		mark := ""
		if r.key == selected {
			mark = "▶ "
		}
		return fmt.Sprintf("%s%s **%s**　×**%d**\n　🏷️ 單價 %d　💰 小計 **%s**", mark, r.emoji, r.name, r.quantity, r.price, money(r.subtotal()))
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, line(r))
	}
	// 賭場獎品另外列出；「全部賣出」不會賣掉獎品（玩家可能想保留展示）
	prizes := prizeRows(bp)
	var prizeLines []string
	for _, r := range prizes {
		prizeLines = append(prizeLines, line(r))
	}
	var list any = "背包裡沒有可以賣的農產品或商品。"
	value := ""
	if len(rows) > 0 {
		list = Section{Title: "可以賣的東西", Lines: lines, Sep: "\n\n"}
		value = fmt.Sprintf("💎 全部價值（依目前單價）：**%s** 金", money(total))
	}
	e := &ui.Embed{Title: "💰 ✦ 販售 ✦", Color: Gold, Description: sections(
		list, value, Section{Title: "🏆 賭場獎品（「全部賣出」不會賣掉，可以留著展示）", Lines: prizeLines, Sep: "\n\n"},
	)}
	if p.picker != nil {
		e.AddField("🛒 已選擇", fmt.Sprintf("**%s**\n👇 按下方的數量按鈕販售", p.picker.Label), false)
	}

	all := append(slices.Clone(rows), prizes...)
	pick := func(ctx context.Context, it ui.Interaction, key string) error {
		i := slices.IndexFunc(all, func(r sellRow) bool { return r.key == key })
		if i < 0 {
			return gameerr.NotFoundf("找不到這個項目。")
		}
		row := all[i]
		half := max(1, row.quantity/2)
		presets := []int{1}
		for _, q := range []int{half, row.quantity} {
			if !slices.Contains(presets, q) {
				presets = append(presets, q)
			}
		}
		sort.Ints(presets)
		run := func(ctx context.Context, q int) (string, error) { return p.sellOne(ctx, row, q) }
		if row.prize {
			run = func(ctx context.Context, q int) (string, error) {
				r, err := p.sellCall(ctx, row, q)
				return r.Message, err
			}
		}
		p.picker = &Picker{Key: key, Label: fmt.Sprintf("%s %s（持有 %d，單價 %d）", row.emoji, row.name, row.quantity, row.price),
			Verb: "賣", Max: row.quantity, Presets: presets, Run: run}
		p.sellKeys = nil
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}
	var options []option
	for _, r := range all {
		options = append(options, option{fmt.Sprintf("%s ×%d", r.name, r.quantity), r.key, fmt.Sprintf("🏷️ 單價 %d", r.price), r.emoji})
	}
	p.selectMenu("💰 選擇要賣的東西…", options, pick, 0, selected)
	p.quantityButtons(1)

	// 勾選多項一起賣出：每項賣全部，確認後才賣；賣的是畫面上顯示的數量，不會多賣之後才進背包的東西
	choices := all[:min(len(all), ui.MaxOptions)] // Discord 選單最多 25 項
	var chosen []sellRow
	for _, r := range choices {
		if slices.Contains(p.sellKeys, r.key) {
			chosen = append(chosen, r)
		}
	}
	if len(choices) > 0 {
		multi := &ui.Select{Placeholder: "☑️ 勾選多項一起賣出（每項賣全部）…", Row: 2, MinValues: 1, MaxValues: len(choices),
			OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
				p.sellKeys, p.picker, p.confirm = slices.Clone(values), nil, ""
				p.refresh(ctx, it, "👇 確認勾選的項目，再按「確認賣出」。", nil, "重繪", time.Time{})
				return nil
			}}
		for _, r := range choices {
			multi.Options = append(multi.Options, ui.SelectOption{Label: cut(fmt.Sprintf("%s ×%d", r.name, r.quantity), 100), Value: r.key,
				Emoji: r.emoji, Description: cut(fmt.Sprintf("💰 全賣 %s 金", money(r.subtotal())), 100),
				Default: slices.Contains(p.sellKeys, r.key)})
		}
		p.View.Add(multi)
	}
	if len(chosen) > 0 {
		chosenTotal := 0
		names := make([]string, len(chosen))
		for i, r := range chosen {
			chosenTotal += r.subtotal()
			names[i] = fmt.Sprintf("%s %s ×%d", r.emoji, r.name, r.quantity)
		}
		e.AddField("☑️ 已勾選", runes(strings.Join(names, "\n"), 900)+fmt.Sprintf("\n共 **%d** 項，約 **%s** 金幣", len(chosen), money(chosenTotal)), false)
		p.button(fmt.Sprintf("確認賣出 %d 項（約 %s 金）", len(chosen), money(chosenTotal)), func(ctx context.Context, it ui.Interaction) error {
			p.sellKeys = nil
			p.act(ctx, it, func(ctx context.Context) (string, error) { return p.sellRows(ctx, chosen) }, actOpts{})
			return nil
		}, btn{emoji: "✅", row: 3, style: ui.Danger})
		p.button("取消勾選", func(ctx context.Context, it ui.Interaction) error {
			p.sellKeys = nil
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 3})
	}

	confirming := p.confirm == "sell_all"
	label, style := "全部賣出", ui.Success
	if confirming {
		label, style = "確認全部賣出", ui.Danger
	}
	p.button(label, func(ctx context.Context, it ui.Interaction) error {
		if p.confirm != "sell_all" {
			p.confirm = "sell_all"
			p.refresh(ctx, it, fmt.Sprintf("⚠️ 確定要把背包裡所有農產品與商品賣掉嗎（約 %s 金幣）？再按一次確認。", money(total)), nil, "重繪", time.Time{})
			return nil
		}
		p.act(ctx, it, p.sellAll, actOpts{})
		return nil
	}, btn{emoji: "💰", row: 3, disabled: len(rows) == 0, style: style})
	p.navBack(4)
	return e, nil
}
