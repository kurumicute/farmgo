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
	"farmgm/internal/game/seedlab"
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

// heldAfter 購買後重新讀取持有數量，讓玩家確定真的買到了（Picker.After：在重新讀取商店頁的同一筆交易中執行，
// 背包與農場狀態與頁面共用讀取快取）。讀取失敗時省略（購買已經完成）。
func (p *Panel) heldAfter(key string) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		held, err := p.readHeld(ctx, key)
		if err != nil {
			logUnexpected(err, "購買後讀取持有數量失敗", "player", p.playerID)
			return ""
		}
		return "\n" + held
	}
}

func (p *Panel) readHeld(ctx context.Context, key string) (string, error) {
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

// buyRun 購買（Python buy_seed／buy_item）；持有數量由 Picker.After（heldAfter）附上。
func (p *Panel) buyRun(name string, buy func(ctx context.Context, q int) (services.ActionResult, error)) func(ctx context.Context, q int) (string, error) {
	return func(ctx context.Context, q int) (string, error) {
		r, err := Call(ctx, p.cmds.Runner, name, func(ctx context.Context) (services.ActionResult, error) { return buy(ctx, q) })
		return r.Message, err
	}
}

// 商店分類（農場商店的三個分類以外，另外兩個是其他頁面）。
const (
	shopTabFish    = "fish"
	shopTabSpecial = "special"
)

// shopTabs 2026-11-16 商店分類放在同一排（原本「種子／道具／工具」與「農場商店／釣具商店／特賣」上下兩排，分不出大小分類）：
// 目前所在的分類亮藍色並停用。農場商店、釣具商店、特賣與升級三個頁面共用。
func (p *Panel) shopTabs(row int, current string) {
	for _, x := range []struct{ key, label, emoji string }{
		{shopTabSeeds, "種子", "🌰"}, {shopTabItems, "道具", "🧪"}, {shopTabTools, "工具與倉庫", "💎"},
		{shopTabFish, "釣具商店", "🎣"}, {shopTabSpecial, "特賣與升級", "🎁"},
	} {
		style := ui.Secondary
		if current == x.key {
			style = ui.Primary
		}
		p.button(x.label, func(ctx context.Context, it ui.Interaction) error {
			p.picker = nil // 換分類時取消選取的商品
			switch x.key {
			case shopTabFish:
				p.goTo(ctx, it, "fish_shop", "")
			case shopTabSpecial:
				p.goTo(ctx, it, "shop_special", "")
			default:
				p.shopTab, p.shopPage = x.key, 0
				p.goTo(ctx, it, "shop", "")
			}
			return nil
		}, btn{emoji: x.emoji, row: row, style: style, disabled: current == x.key})
	}
}

// shortOf 金幣不夠買 1 個時的說明（夠買時回傳 ""）。
func shortOf(gold, price int) string {
	if gold >= price {
		return ""
	}
	return fmt.Sprintf("金幣不足（需 %s，你有 %s）", money(price), money(gold))
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
		line := fmt.Sprintf("%s%s **%s種子**　💰 **%s** 金　🎒 持有 **%d**\n　⏱️ %s　🧺 收成 ×%d %s%s　🏷️ 售價 %s 金／個",
			mark("seed:"+c.Code), cropEmoji(c.Code), c.Name, money(c.SeedPrice), held["seed:"+c.Code],
			services.LongDuration(c.GrowSeconds), c.HarvestQuantity, cropEmoji(c.Code), c.Name, money(c.SellPrice))
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
		it := balance.ItemOf(code)
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
			consumables = append(consumables, fmt.Sprintf("%s%s **%s**　💰 **%s** 金　🎒 持有 **%d**\n　📝 %s", mark("item:"+code), emoji,
				it.Name, money(it.Price), have, it.Desc))
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
	// 2026-11-15 起商店分成「種子／道具／工具與倉庫」三個分類，種子多時再分頁（使用者覺得全部擠在一頁太多）
	tab := p.shopTab
	if tab != shopTabItems && tab != shopTabTools {
		tab = shopTabSeeds
	}
	head := fmt.Sprintf("💰 你的金幣：**%s**", money(d.gold)) // 分頁按鈕本身就說明了釣具、特賣在哪裡，不再每頁重複
	var parts []any
	seedPages := 1
	switch tab {
	case shopTabSeeds:
		var shown []string
		shown, p.shopPage, seedPages = pageSlice(seeds, p.shopPage, shopSeedsPerPage)
		title := "🌰 種子"
		if seedPages > 1 {
			title = fmt.Sprintf("🌰 種子（第 %d/%d 頁）", p.shopPage+1, seedPages)
		}
		parts = []any{seasons.NowText(time.Now()) + "：🌰 商店只賣當季可以種的種子，換季後會換一批。",
			Section{Title: title, Lines: shown, Sep: "\n\n"}, Section{Title: "🎉 今天的活動", Lines: events, Sep: "\n\n"}, head}
	case shopTabItems:
		parts = []any{Section{Title: "🧪 道具", Lines: consumables, Sep: "\n\n"}, head}
	default:
		parts = []any{Section{Title: "📦 倉庫", Lines: []string{warehouse}},
			Section{Title: "💎 工具與奢侈品", Lines: tools, Sep: "\n\n"}, head}
	}
	// 金幣放在最後，貼近購買按鈕
	e := &ui.Embed{Title: "🏪 ✦ 農場商店 ✦", Color: Gold, Description: sections(parts...)}
	// makePicker 依目前資料建立選取的商品（2026-11-16：保留選取的商品每次重繪都重建，金幣不足、可買數量才會跟著購買後的金幣更新）
	makePicker := func(key string) (*Picker, error) {
		var pk *Picker
		kind, code, _ := strings.Cut(key, ":")
		switch kind {
		case "special":
			price := 0
			if canExpand {
				price = expandPrice
			}
			pk = &Picker{Key: key, Label: "📦 倉庫擴大卷", PriceText: money(price) + " 金", Confirm: true, Max: 1, Short: shortOf(d.gold, price),
				Run: p.callQty("expand_warehouse", func(ctx context.Context, _ int) (services.ActionResult, error) {
					return p.svc().ExpandWarehouse(ctx, p.playerID)
				})}
		case "seed":
			i := slices.IndexFunc(d.crops, func(c services.CropInfo) bool { return c.Code == code })
			if i < 0 {
				return nil, gameerr.NotFoundf("找不到這個商品。")
			}
			crop := d.crops[i]
			pk = &Picker{Key: key, Label: fmt.Sprintf("%s %s種子", cropEmoji(code), crop.Name),
				PriceText: fmt.Sprintf("每顆 %s 金", money(crop.SeedPrice)), Verb: "買", Max: tradeMax(d.gold, crop.SeedPrice), Short: shortOf(d.gold, crop.SeedPrice),
				Presets: []int{1, 5, 10}, Keep: true, NoProgress: true,
				After: p.heldAfter(key),
				Run: p.buyRun("buy_seeds", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().BuySeeds(ctx, p.playerID, code, q)
				})}
		default:
			if _, ok := balance.Items[code]; !ok {
				return nil, gameerr.NotFoundf("找不到這個商品。")
			}
			item := balance.ItemOf(code)
			label := lookupOr(ItemEmoji, code, "🎁") + " " + item.Name
			if item.Kind == "tool" { // 永久工具與奢侈品價格高：需要再按一次確認才購買
				pk = &Picker{Key: key, Label: label, PriceText: money(item.Price) + " 金", Confirm: true, Max: 1, Short: shortOf(d.gold, item.Price),
					Run: p.callQty("buy_item", func(ctx context.Context, _ int) (services.ActionResult, error) {
						return p.svc().BuyItem(ctx, p.playerID, code, 1)
					})}
			} else {
				pk = &Picker{Key: key, Label: label, PriceText: fmt.Sprintf("每個 %s 金", money(item.Price)), Verb: "買",
					Max: tradeMax(d.gold, item.Price), Short: shortOf(d.gold, item.Price), Presets: []int{1, 5, 10}, Keep: true, NoProgress: true,
					After: p.heldAfter(key),
					Run: p.buyRun("buy_item", func(ctx context.Context, q int) (services.ActionResult, error) {
						return p.svc().BuyItem(ctx, p.playerID, code, q)
					})}
			}
		}
		return pk, nil
	}
	pick := func(ctx context.Context, it ui.Interaction, key string) error {
		pk, err := makePicker(key)
		if err != nil {
			return err
		}
		p.picker = pk
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}
	if p.picker != nil && p.picker.Keep {
		p.picker, _ = makePicker(p.picker.Key) // 商品已經下架時取消選取
	}
	if p.picker != nil {
		hint := "👇 按下方的數量按鈕購買"
		if p.picker.Confirm {
			hint = "👇 按「確認購買」完成購買"
		}
		e.AddField("🛒 已選擇", fmt.Sprintf("**%s**（%s）\n%s", p.picker.Label, p.picker.PriceText, hint), false)
	} else {
		e.Footer = "👇 先從選單挑一樣商品，再選數量"
	}

	var options []option
	switch tab {
	case shopTabSeeds: // 選單只列這一頁的種子
		crops, _, _ := pageSlice(d.crops, p.shopPage, shopSeedsPerPage)
		for _, c := range crops {
			options = append(options, option{c.Name + "種子", "seed:" + c.Code, fmt.Sprintf("🌰 種子｜💰 %s 金", money(c.SeedPrice)), cropEmoji(c.Code)})
		}
	case shopTabItems:
		for _, code := range balance.ItemOrder {
			if it := balance.ItemOf(code); it.Kind != "tool" {
				options = append(options, option{it.Name, "item:" + code, fmt.Sprintf("🧪 道具｜💰 %s 金", money(it.Price)), ItemEmoji[code]})
			}
		}
	default:
		if canExpand && expandPrice != 0 {
			options = append(options, option{"倉庫擴大卷", "special:warehouse",
				fmt.Sprintf("📦 +%d 格｜💰 %s 金", balance.WarehouseExpansionStep, money(expandPrice)), "📦"})
		}
		for _, code := range balance.ItemOrder {
			if it := balance.ItemOf(code); it.Kind == "tool" && held["item:"+code] == 0 {
				options = append(options, option{it.Name, "item:" + code, fmt.Sprintf("💎 工具｜💰 %s 金", money(it.Price)), ItemEmoji[code]})
			}
		}
	}
	p.selectMenu("🛒 選擇要買的商品…", options, pick, 0, selected)
	p.quantityButtons(1)
	if tab == shopTabSeeds {
		p.pageButtons("種子", &p.shopPage, seedPages, 2, "shop")
	}
	p.shopTabs(3, tab)
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
	// 作物變多後一頁放不下（Embed 4000 字會截掉後面的高價作物）：依單價由高到低排序並分頁（2026-11-14）
	list = slices.Clone(list)
	sort.SliceStable(list, func(i, j int) bool { return list[i].SellPrice > list[j].SellPrice })
	shown, page, pages := pageSlice(list, p.pricePage, pricesPerPage)
	p.pricePage = page
	var blocks []string
	for _, c := range shown {
		season := ""
		if c.SeasonText != "" {
			season = "　" + c.SeasonText
		}
		if c.SeasonSellPct > 100 { // 非當季：賣價較高（Go 版新增）
			season += fmt.Sprintf("　📈 非當季售價 +%d%%", c.SeasonSellPct-100)
		}
		blocks = append(blocks, fmt.Sprintf("%s **%s**　%s%s\n　⏱️ %s　🧺 收成 ×%d %s%s\n　🏷️ 一般：%s\n　🌟 黃金作物：%s\n　💎 珍稀變種「%s」：%s",
			cropEmoji(c.Code), c.Name, seedPriceText(c.CropInfo), season, services.LongDuration(c.GrowSeconds), c.HarvestQuantity,
			cropEmoji(c.Code), c.Name, row(c.Prices, "none"), row(c.Prices, "golden"), c.VariantName, row(c.Prices, "variant")))
	}
	title := "作物（依單價由高到低）"
	if pages > 1 {
		title = fmt.Sprintf("作物（依單價由高到低，第 %d/%d 頁）", page+1, pages)
	}
	e := &ui.Embed{Title: "📈 ✦ 市場價格 ✦", Color: Gold, Description: sections(
		fmt.Sprintf("每個農產品的單價依 **品質** 與 **突變** 而不同（金幣／個）。\n%s：當季作物照原價，非當季的作物售價 +%d%%（物以稀為貴）。",
			seasons.NowText(time.Now()), seasons.OffSeasonSellPct-100),
		Section{Title: title, Lines: blocks, Sep: "\n\n"},
		"🍀 巨型作物：收成數量變多，單價同一般｜⚡ 快速生長：只縮短時間，單價同一般",
	)}
	p.nav("品質與突變", "genetics", btn{emoji: "🧬", row: 0})
	p.navBack(0)
	p.pageButtons("", &p.pricePage, pages, 1, "prices")
	return e, nil
}

// 農場商店的分類（2026-11-15）。
const (
	shopTabSeeds = "seeds"
	shopTabItems = "items"
	shopTabTools = "tools"
)

// shopSeedsPerPage 商店種子每頁幾種。
const shopSeedsPerPage = 6

// pricesPerPage 作物價格每頁幾種（每種約 150 字，8 種約 1,300 字，遠低於 Embed 的 4,000 字）。
const pricesPerPage = 8

// seedPriceText 價目表的種子價格：商店不賣種子的作物不顯示價格，改說明取得方式。
func seedPriceText(c services.CropInfo) string {
	switch {
	case c.ShopBuyable:
		return fmt.Sprintf("種子 %s 金", money(c.SeedPrice))
	case seedlab.IsHybrid(c.Code):
		return "🧬 雜交作物（種子只能在種子實驗室培育）"
	default:
		return "🔒 商店不賣種子（山洞探險、每週特賣、稀有作物培育器）"
	}
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
	level := "—"
	if t.Level != nil {
		level = fmt.Sprint(*t.Level)
	}
	e := &ui.Embed{Title: "🧬 ✦ 品質與突變 ✦", Color: Gold, Description: sections(
		"品質與突變在 **播種時** 決定，之後不會改變。",
		Section{Title: "⭐ 品質", Lines: quality},
		fmt.Sprintf("農場每升 1 級，有 %s%% 機率從普通升到較高品質（上限 %s%%）。你的農場等級：**%s**",
			pct(t.LevelBonusBP), pct(t.LevelBonusMaxBP), level),
		Section{Title: "🧪 突變（每批最多一種）", Lines: mutations, Sep: "\n\n"},
		Section{Title: "🔬 實驗室限定突變（一般種植不會抽到，只能靠種子實驗室的突變研究）", Lines: labMutationEffects()},
	)}
	p.navFirstPage("價目表", "prices", &p.pricePage, btn{emoji: "📈", row: 0})
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
		return fmt.Sprintf("%s%s **%s**　×**%d**\n　🏷️ 單價 %s　💰 小計 **%s**", mark, r.emoji, r.name, r.quantity, money(r.price), money(r.subtotal()))
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
	// 2026-11-16 單選與複選合併成一個選單（原本兩個選單內容相同，選了一個會清掉另一個）：
	// 只選 1 項 → 顯示數量按鈕，可以只賣一部分；選多項 → 每項賣全部，按「確認賣出」才賣。超過 25 項時分頁。
	pickOne := func(row sellRow) {
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
		p.picker = &Picker{Key: row.key, Label: fmt.Sprintf("%s %s（持有 %d，單價 %s）", row.emoji, row.name, row.quantity, money(row.price)),
			Verb: "賣", Max: row.quantity, Presets: presets, Run: run}
	}
	choices, page, pages := pageOf(all, p.sellPage)
	p.sellPage = page
	// 賣的是畫面上顯示的數量，不會多賣之後才進背包的東西
	var chosen []sellRow
	for _, r := range all {
		if slices.Contains(p.sellKeys, r.key) {
			chosen = append(chosen, r)
		}
	}
	if len(choices) > 0 {
		placeholder := "💰 選擇要賣的東西（可複選）…"
		if pages > 1 {
			placeholder = fmt.Sprintf("💰 選擇要賣的東西（可複選，第 %d/%d 頁）…", page+1, pages)
		}
		multi := &ui.Select{Placeholder: placeholder, Row: 0, MinValues: 1, MaxValues: len(choices),
			OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
				p.sellKeys, p.picker, p.confirm = slices.Clone(values), nil, ""
				notice := "👇 確認勾選的項目，再按「確認賣出」。"
				if len(values) == 1 {
					i := slices.IndexFunc(all, func(r sellRow) bool { return r.key == values[0] })
					if i < 0 {
						return gameerr.NotFoundf("找不到這個項目。")
					}
					pickOne(all[i])
					p.sellKeys, notice = nil, ""
				}
				p.refresh(ctx, it, notice, nil, "重繪", time.Time{})
				return nil
			}}
		for _, r := range choices {
			multi.Options = append(multi.Options, ui.SelectOption{Label: cut(fmt.Sprintf("%s ×%d", r.name, r.quantity), 100), Value: r.key,
				Emoji: r.emoji, Description: cut(fmt.Sprintf("🏷️ 單價 %s｜💰 全賣 %s 金", money(r.price), money(r.subtotal())), 100),
				Default: slices.Contains(p.sellKeys, r.key)}) // 單選（數量按鈕）時不預設勾選：改選另一項會直接換成那一項
		}
		p.add(multi)
	}
	p.quantityButtons(1)
	p.pageButtons("", &p.sellPage, pages, 2, "sell")
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
