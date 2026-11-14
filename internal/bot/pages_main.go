package bot

// 面板頁面：歡迎（尚未建立農場）、總覽、農田、背包、說明（對應 Python panels.py 的同名區段與 help_embed）。

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/config"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/rules"
	"farmgm/internal/services"
)

// registerPages 頁面名稱 → 讀取與建立函式（Python getattr(self, f"_load_{page}")）。
func (p *Panel) registerPages() {
	p.pages = map[string]pageDef{
		"welcome":         {func(context.Context) (any, error) { return nil, nil }, p.buildWelcome},
		"main":            {p.loadMain, p.buildMain},
		"plots":           {p.loadPlots, p.buildPlots},
		"cellar":          {p.loadCellar, p.buildCellar},
		"lab":             {p.loadLab, p.buildLab},
		"lab_seeds":       {p.loadLabSeeds, p.buildLabSeeds},
		"lab_cross":       {p.loadLab, p.buildLabCross},
		"lab_records":     {p.loadLabRecords, p.buildLabRecords},
		"backpack":        {p.loadBackpack, p.buildBackpack},
		"shop":            {p.loadShop, p.buildShop},
		"prices":          {p.loadPrices, p.buildPrices},
		"genetics":        {p.loadGenetics, p.buildGenetics},
		"sell":            {p.loadSell, p.buildSell},
		"animals":         {p.loadAnimals, p.buildAnimals},
		"pets":            {p.loadPets, p.buildPets},
		"craft":           {p.loadCraft, p.buildCraft},
		"build":           {p.loadBuild, p.buildBuild},
		"defense":         {p.loadDefense, p.buildDefense},
		"casino":          {p.loadCasino, p.buildCasino},
		"steal":           {p.loadSteal, p.buildSteal},
		"steal_farm":      {p.loadStealFarm, p.buildStealFarm},
		"board":           {p.loadBoard, p.buildBoard},
		"rewards":         {p.loadRewards, p.buildRewards},
		"quests":          {p.loadQuests, p.buildQuests},
		"competition":     {p.loadCompetition, p.buildCompetition},
		"history":         {p.loadHistory, p.buildHistory},
		"group":           {p.loadGroup, p.buildGroup},
		"cave":            {p.loadCave, p.buildCave},
		"cave_area":       {p.loadCaveArea, p.buildCaveArea},
		"cave_result":     {p.loadCaveResult, p.buildCaveResult},
		"cave_log":        {p.loadCaveLog, p.buildCaveLog},
		"machines":        {p.loadMachines, p.buildMachines},
		"seedmaker":       {p.loadSeedMaker, p.buildSeedMaker},
		"log":             {p.loadLog, p.buildLog},
		"shop_special":    {p.loadShopSpecial, p.buildShopSpecial},
		"progress":        {p.loadProgress, p.buildProgress},
		"achievements":    {p.loadAchievements, p.buildAchievements},
		"titles":          {p.loadTitles, p.buildTitles},
		"codex":           {p.loadCodex, p.buildCodex},
		"ruins":           {p.loadRuins, p.buildRuins},
		"boss":            {p.loadBoss, p.buildBoss},
		"boss_battle":     {p.loadBossBattle, p.buildBossBattle},
		"boss_donate":     {p.loadBossDonate, p.buildBossDonate},
		"boss_board":      {p.loadBossBoard, p.buildBossBoard},
		"boss_live_board": {p.loadBossLiveBoard, p.buildBossLiveBoard},
		"boss_help":       {p.loadBossHelp, p.buildBossHelp},
		"fishing":         {p.loadFishing, p.buildFishing},
		"fish_game":       {p.loadFishGame, p.buildFishGame},
		"fish_basket":     {p.loadFishBasket, p.buildFishBasket},
		"fish_detail":     {p.loadFishDetail, p.buildFishDetail},
		"fish_codex":      {p.loadFishCodex, p.buildFishCodex},
		"fish_rank":       {p.loadFishRank, p.buildFishRank},
		"fish_shop":       {p.loadFishShop, p.buildFishShop},
		"help":            {p.loadHelp, p.buildHelp},
	}
}

// ======================================================================
// 歡迎（尚未建立農場）
// ======================================================================

func (p *Panel) buildWelcome(_ context.Context, _ any) (*ui.Embed, error) {
	e := &ui.Embed{Title: "🌾 歡迎來到農場！", Color: Green}
	if p.groupID == nil {
		e.Description = "你還沒有農場。請到 Discord 伺服器中輸入 /farm 建立（農場會屬於那個伺服器）。"
		return e, nil
	}
	e.Description = fmt.Sprintf("你還沒有農場。按下面的按鈕，就會在「%s」建立你的農場。\n"+
		"開始時有 %d 金幣、%d 格農田。\n"+
		"每個 Discord 帳號只有一座農場；之後可以轉移到其他伺服器（每 7 天一次）。", p.groupName, balance.InitialGold, balance.InitialPlots)
	create := func(ctx context.Context, it ui.Interaction, name string) {
		_ = it.DeferUpdateIfPending(ctx)
		w := p.who
		err := p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
			if _, err := Call(ctx, p.cmds.Runner, "start_external_farm", func(ctx context.Context) (services.FarmStatus, error) {
				return p.cmds.Service.StartExternalFarm(ctx, Platform, w.UserID, w.UserName, w.GuildID, w.GuildName, name)
			}); err != nil {
				return err
			}
			ec, err := Call(ctx, p.cmds.Runner, "external_context", func(ctx context.Context) (services.ExternalContext, error) {
				return p.cmds.Service.ExternalContext(ctx, Platform, w.UserID, w.guildPtr(), w.GuildName)
			})
			if err != nil {
				return err
			}
			if ec.PlayerID != nil {
				p.playerID = *ec.PlayerID
			}
			p.Page = "main"
			return nil
		})
		if err != nil {
			logUnexpected(err, "建立農場失敗", "user", w.UserID)
			p.showError(ctx, it, cleanMessage(PlayerMessage(err)))
			return
		}
		p.refresh(ctx, it, "🎉 農場建立完成！先到「🏪 商店」買種子，或在「🌱 農田」直接選作物種下。", nil, "重繪", time.Time{})
	}
	p.button("建立農場", func(ctx context.Context, it ui.Interaction) error {
		create(ctx, it, "")
		return nil
	}, btn{emoji: "🌱", style: ui.Success})
	p.button("自訂名稱建立", func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, nameModal(create))
	}, btn{emoji: "✏️"})
	return e, nil
}

// ======================================================================
// 總覽
// ======================================================================

type mainData struct {
	status  services.FarmStatus
	animals services.AnimalStatus
	orders  []services.OrderView
	home    *services.HomeGroup
	events  []services.ActiveEventView
	news    *services.DailyNews
}

func (p *Panel) loadMain(ctx context.Context) (any, error) {
	svc, pid := p.svc(), p.playerID
	var d mainData
	var err error
	if d.status, err = svc.GetFarmStatus(ctx, pid); err != nil {
		return nil, err
	}
	if d.home, err = svc.GetHomeGroup(ctx, pid); err != nil {
		return nil, err
	}
	if d.animals, err = svc.AnimalStatus(ctx, pid); err != nil {
		return nil, err
	}
	if d.orders, err = svc.CraftOrders(ctx, pid, false, 20); err != nil {
		return nil, err
	}
	if d.events, err = svc.ActiveEvents(ctx); err != nil {
		return nil, err
	}
	if d.home != nil { // 所屬群組昨天的日報（同一天會快取）
		if d.news, err = svc.DailyNews(ctx, d.home.ID, time.Time{}); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func countState(plots []services.PlotInfo, s rules.PlotState) int {
	n := 0
	for _, p := range plots {
		if p.State == s {
			n++
		}
	}
	return n
}

func (p *Panel) buildMain(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(mainData)
	s := d.status
	var growing []time.Time
	for _, pl := range s.Plots {
		if pl.State == rules.PlotGrowing && pl.ReadyAt != nil {
			growing = append(growing, *pl.ReadyAt)
		}
	}
	ready := countState(s.Plots, rules.PlotReady)
	waiting := countState(s.Plots, rules.PlotWaitingWater)
	title := ""
	if deref(s.Title) != "" {
		title = "〔" + *s.Title + "〕"
	}
	header := fmt.Sprintf("👨‍🌾 **%s**%s　⭐ **Lv %d**　💰 **%s** 金\n━━━━━━━━ 🌱 **農田（%d 格）** ━━━━━━━━",
		s.PlayerName, title, s.Level, money(s.Gold), len(s.Plots))
	lines := make([]string, len(s.Plots))
	for i, pl := range s.Plots {
		lines[i] = plotLine(pl, true)
	}
	e := &ui.Embed{Title: fmt.Sprintf("🌾 ✦ %s ✦", s.Name), Color: Green, Description: header + "\n" + strings.Join(lines, "\n")}
	if p.AvatarURL != "" {
		e.Thumbnail = p.AvatarURL
	}
	p.attachImage(e, p.farmImage(d.status)) // 圖片在交易外產生
	e.AddField("📦 倉庫", fmt.Sprintf("%d/%d\n%s", s.WarehouseUsed, s.WarehouseCapacity, bar(s.WarehouseUsed, s.WarehouseCapacity, 8)), true)
	home := "未設定"
	if d.home != nil {
		home = d.home.Name
	}
	e.AddField("🏠 所屬群組", home, true)
	stats := strings.ReplaceAll(summaryCounts(s.Plots), "　", "\n")
	if stats == "" {
		stats = "—"
	}
	e.AddField("📊 農田統計", stats, true)
	a := d.animals
	animal := "還沒有動物"
	if a.Used != 0 {
		animal = fmt.Sprintf("🐾 %d/%d 隻\n✅ 可收集 %d｜🍽️ 待餵 %d", a.Used, a.Capacity, a.Counts["ready"], a.Counts["hungry"])
		if a.NextReadyAt != nil {
			animal += "\n⏰ " + ts(a.NextReadyAt)
		}
	}
	e.AddField("🐔 動物", animal, true)
	done := 0
	var busy []services.OrderView
	for _, o := range d.orders {
		switch o.State {
		case "ready":
			done++
		case "in_progress":
			busy = append(busy, o)
		}
	}
	sort.SliceStable(busy, func(i, j int) bool { return busy[i].ReadyAt.Before(busy[j].ReadyAt) })
	var craft []string
	if done > 0 {
		craft = append(craft, fmt.Sprintf("✅ %d 張可領取", done))
	}
	for _, o := range busy[:min(3, len(busy))] {
		craft = append(craft, fmt.Sprintf("⚙️ %s %s", o.RecipeName, ts(&o.ReadyAt)))
	}
	craftText := strings.Join(craft, "\n")
	if craftText == "" {
		craftText = "💤 閒置"
	}
	e.AddField("🏭 加工", craftText, true)
	var events []string
	for _, ev := range d.events {
		name, _ := ev.View["name"].(string)
		effects, _ := ev.View["effects_text"].(string)
		desc, _ := ev.View["description"].(string)
		line := "🔥 **" + name + "**"
		if ev.CropName != nil && *ev.CropName != "" {
			line += "（限" + *ev.CropName + "）"
		}
		line += "\n　✨ 效果：" + effects
		if desc != "" {
			line += "\n　📝 " + desc
		}
		events = append(events, line)
	}
	e.AddField("🎉 現在進行中的活動", linesOr(events, "目前沒有活動，平常心種田吧 🌾", 1024), false)
	var hints []string
	if ready > 0 {
		hints = append(hints, fmt.Sprintf("✅ **%d 格可以收成！**", ready))
	}
	if waiting > 0 {
		hints = append(hints, fmt.Sprintf("💧 %d 格等待澆水", waiting))
	}
	if len(growing) > 0 {
		next := slices.MinFunc(growing, func(a, b time.Time) int { return a.Compare(b) })
		hints = append(hints, "⏰ 下一批成熟 "+ts(&next))
	}
	if countState(s.Plots, rules.PlotEmpty) == len(s.Plots) {
		hints = append(hints, "🌱 農田都空著，到「🌱 農田」選作物種下吧！")
	}
	todo := strings.Join(hints, "\n")
	if todo == "" {
		todo = "一切就緒 👍"
	}
	e.AddField("📋 待辦", todo, false)
	if d.news != nil && !d.news.Empty {
		e.AddField(fmt.Sprintf("📰 昨日農場日報（%s）", d.home.Name), d.news.Text, false)
	}
	e.Footer = "面板只有你看得到；15 分鐘後逾時，可再輸入 /farm 開啟。"

	p.button("全部澆水", p.waterAll, btn{emoji: "💧", style: ui.Primary, row: 0, disabled: waiting == 0})
	p.button("全部收成", p.harvestAll, btn{emoji: "🧺", style: ui.Success, row: 0, disabled: ready == 0})
	p.button("重新整理", func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "main", "")
		return nil
	}, btn{emoji: "🔄", row: 0})
	p.button("分享到頻道", p.share, btn{emoji: "📢", row: 0, disabled: p.groupID == nil})
	p.nav("農田", "plots", btn{emoji: "🌱", row: 1})
	p.nav("背包", "backpack", btn{emoji: "🎒", row: 1})
	p.nav("商店", "shop", btn{emoji: "🏪", row: 1})
	p.nav("販售", "sell", btn{emoji: "💰", row: 1})
	p.nav("成就圖鑑", "progress", btn{emoji: "🏅", row: 1, style: ui.Primary})
	p.nav("動物", "animals", btn{emoji: "🐔", row: 2})
	p.nav("加工", "craft", btn{emoji: "🏭", row: 2})
	p.nav("防守", "defense", btn{emoji: "🛡️", row: 2})
	p.nav("偷菜", "steal", btn{emoji: "🥷", row: 2})
	p.nav("排行榜", "board", btn{emoji: "🏆", row: 3})
	p.nav("任務競賽", "quests", btn{emoji: "📜", row: 3})
	p.nav("群組", "group", btn{emoji: "👥", row: 3})
	p.navFirstPage("紀錄", "log", &p.logPage, btn{emoji: "🧾", row: 3})
	p.nav("世界Boss", "boss", btn{emoji: "🐉", row: 3, style: ui.Danger})
	p.navFirstPage("作物價格", "prices", &p.pricePage, btn{emoji: "📈", row: 4})
	p.nav("賭場", "casino", btn{emoji: "🎰", row: 4, style: ui.Primary})
	p.nav("山洞", "cave", btn{emoji: "⛰️", row: 4, style: ui.Primary})
	p.nav("釣魚", "fishing", btn{emoji: "🎣", row: 4, style: ui.Primary})
	p.nav("說明", "help", btn{emoji: "❓", row: 4})
	return e, nil
}

// share 把農田圖片公開分享到頻道（其他人看得到）。
func (p *Panel) share(ctx context.Context, it ui.Interaction) error {
	_ = it.DeferUpdateIfPending(ctx)
	var status services.FarmStatus
	var png []byte
	err := p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
		var err error
		status, err = Call(ctx, p.cmds.Runner, "get_farm_status", func(ctx context.Context) (services.FarmStatus, error) {
			return p.svc().GetFarmStatus(ctx, p.playerID)
		})
		if err == nil {
			png = p.farmImage(status)
		}
		return err
	})
	if err != nil {
		_, _ = it.Followup(ctx, ui.Message{Content: ptr("❌ " + PlayerMessage(err)), Ephemeral: true})
		return nil
	}
	lines := make([]string, len(status.Plots))
	for i, pl := range status.Plots {
		lines[i] = plotLine(pl, true)
	}
	desc := strings.Join(lines, "\n")
	if r := []rune(desc); len(r) > 4000 {
		desc = string(r[:4000])
	}
	e := &ui.Embed{Title: fmt.Sprintf("🌾 %s（Lv %d）", status.Name, status.Level), Color: Green, Description: desc,
		AuthorName: status.PlayerName}
	e.AddField("💰 金幣", money(status.Gold), true)
	msg := ui.Message{Embed: e}
	if len(png) > 0 {
		e.Image = "attachment://" + FarmImage
		msg.Files = []ui.File{{Name: FarmImage, Data: png}}
	}
	if _, err := it.Followup(ctx, msg); err != nil {
		slog.Warn("分享農場失敗", "type", fmt.Sprintf("%T", err))
	}
	return nil
}

// ---------- 一鍵操作（服務層在同一筆交易中整批完成） ----------

func (p *Panel) waterAll(ctx context.Context, it ui.Interaction) error {
	p.act(ctx, it, p.call("water_all", func(ctx context.Context) (services.ActionResult, error) {
		return p.svc().WaterAll(ctx, p.playerID)
	}), actOpts{noProgress: true})
	return nil
}

func (p *Panel) harvestAll(ctx context.Context, it ui.Interaction) error {
	p.act(ctx, it, p.call("harvest_all", func(ctx context.Context) (services.ActionResult, error) {
		return p.svc().HarvestAll(ctx, p.playerID)
	}), actOpts{})
	return nil
}

func (p *Panel) clearAll(ctx context.Context, it ui.Interaction) error {
	p.act(ctx, it, p.call("clear_all_withered", func(ctx context.Context) (services.ActionResult, error) {
		return p.svc().ClearAllWithered(ctx, p.playerID)
	}), actOpts{noProgress: true})
	return nil
}

// ======================================================================
// 農田
// ======================================================================

type plotsData struct {
	status   services.FarmStatus
	backpack services.Backpack
	crops    []services.CropInfo
}

func (p *Panel) loadPlots(ctx context.Context) (any, error) {
	svc, pid := p.svc(), p.playerID
	var d plotsData
	var err error
	if d.status, err = svc.GetFarmStatus(ctx, pid); err != nil {
		return nil, err
	}
	if d.backpack, err = svc.GetBackpack(ctx, pid); err != nil {
		return nil, err
	}
	d.crops, err = svc.GetMarketPrices(ctx)
	return d, err
}

func lineQty(lines []services.BackpackLine, code string) int {
	for _, l := range lines {
		if l.Code == code {
			return l.Quantity
		}
	}
	return 0
}

func (p *Panel) buildPlots(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(plotsData)
	s, bp := d.status, d.backpack
	seeds := map[string]int{}
	var seedParts []string
	for _, x := range bp.Seeds {
		seeds[x.Code] = x.Quantity
		seedParts = append(seedParts, fmt.Sprintf("%s%s ×**%d**", cropEmoji(x.Code), x.Name, x.Quantity))
	}
	seedText := strings.Join(seedParts, "　")
	if seedText == "" {
		seedText = "沒有種子"
	}
	plotLines := make([]string, len(s.Plots))
	for i, pl := range s.Plots {
		plotLines[i] = plotLine(pl, true)
	}
	e := &ui.Embed{Title: fmt.Sprintf("🌱 ✦ 農田（%d 格）✦", len(s.Plots)), Color: Green, Description: sections(
		fmt.Sprintf("💰 金幣 **%s**\n🌰 %s", money(s.Gold), seedText),
		Section{Title: "農田", Lines: plotLines},
		"📊 "+summaryCounts(s.Plots),
	), Footer: "選作物會種滿所有空地（種子不夠時自動購買）；種下後記得澆水。"}
	p.attachImage(e, p.farmImage(d.status)) // 圖片在交易外產生
	if empty := countState(s.Plots, rules.PlotEmpty); empty > 0 {
		var options []option
		for _, c := range d.crops {
			if !(c.ShopBuyable || seeds[c.Code] > 0) || !c.InSeason { // 不在季節的不列出
				continue
			}
			label := fmt.Sprintf("%s（種子 %d 金，持有 %d）", c.Name, c.SeedPrice, seeds[c.Code])
			if !c.ShopBuyable {
				label = fmt.Sprintf("%s（稀有種子，持有 %d）", c.Name, seeds[c.Code])
			}
			options = append(options, option{label, c.Code,
				fmt.Sprintf("⏱️ %s｜🧺 收成 ×%d %s｜🏷️ %d 金／個", services.LongDuration(c.GrowSeconds), c.HarvestQuantity, c.Name, c.SellPrice),
				cropEmoji(c.Code)})
		}
		p.selectMenu(fmt.Sprintf("🌱 選擇作物，種到 %d 格空地…", empty), options, func(ctx context.Context, it ui.Interaction, code string) error {
			p.act(ctx, it, p.call("plant_all", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().PlantAll(ctx, p.playerID, code)
			}), actOpts{noProgress: true})
			return nil
		}, 0, "")
	}
	p.button("全部澆水", p.waterAll, btn{emoji: "💧", style: ui.Primary, row: 1, disabled: countState(s.Plots, rules.PlotWaitingWater) == 0})
	p.button("全部收成", p.harvestAll, btn{emoji: "🧺", style: ui.Success, row: 1, disabled: countState(s.Plots, rules.PlotReady) == 0})
	p.button("清除乾枯", p.clearAll, btn{emoji: "🥀", row: 1, disabled: countState(s.Plots, rules.PlotWithered) == 0})
	price, ok := balance.UpgradePrice(len(s.Plots))
	confirming := p.confirm == "upgrade"
	label := fmt.Sprintf("擴建（%s 金）", money(price))
	style := ui.Secondary
	switch {
	case !ok:
		label = "已達上限"
	case confirming:
		label, style = "確認擴建", ui.Danger
	}
	p.button(label, func(ctx context.Context, it ui.Interaction) error {
		if p.confirm != "upgrade" {
			p.confirm = "upgrade"
			p.refresh(ctx, it, fmt.Sprintf("⚠️ 擴建需要 %s 金幣（+%d 格），再按一次「確認擴建」。", money(price), balance.UpgradePlotsAdded),
				nil, "重繪", time.Time{})
			return nil
		}
		p.act(ctx, it, p.call("upgrade_farm", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().UpgradeFarm(ctx, p.playerID)
		}), actOpts{})
		return nil
	}, btn{emoji: "⬆️", row: 1, disabled: !ok, style: style})
	var growing []services.PlotInfo
	for _, pl := range s.Plots {
		if pl.State == rules.PlotGrowing && !pl.Fertilized {
			growing = append(growing, pl)
		}
	}
	if fert := lineQty(bp.Consumables, balance.ItemFertilizer); len(growing) > 0 && fert > 0 {
		var options []option
		for _, pl := range growing {
			options = append(options, option{fmt.Sprintf("#%d %s", pl.PlotNumber, deref(pl.CropName)), fmt.Sprint(pl.PlotNumber),
				fmt.Sprintf("剩餘時間 -%d%%", balance.FertilizerPct), "🧪"})
		}
		p.selectMenu(fmt.Sprintf("🧪 施化肥（持有 %d，剩餘時間 -%d%%）…", fert, balance.FertilizerPct), options,
			func(ctx context.Context, it ui.Interaction, value string) error {
				n, _ := parseDigits(value)
				p.act(ctx, it, p.call("use_fertilizer", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().UseFertilizer(ctx, p.playerID, n)
				}), actOpts{})
				return nil
			}, 2, "")
	}
	if worms := lineQty(bp.Consumables, balance.ItemEarthworm); worms > 0 {
		p.button(fmt.Sprintf("使用蚯蚓（持有 %d）", worms), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("use_earthworm", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().UseEarthworm(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🪱", row: 3})
	}
	p.nav("地下菇房", "cellar", btn{emoji: "🍄", row: 4, style: ui.Primary})
	p.nav("種子實驗室", "lab", btn{emoji: "🧪", row: 4, style: ui.Primary})
	if bp.SeedMaker {
		p.navFirstPage("種子製造", "seedmaker", &p.seedMakerPage, btn{emoji: "🌱", row: 4, style: ui.Primary})
	}
	p.navBack(4)
	return e, nil
}

// ======================================================================
// 背包
// ======================================================================

type backpackData struct {
	backpack services.Backpack
	fish     services.FishBasket
}

func (p *Panel) loadBackpack(ctx context.Context) (any, error) {
	bp, err := p.svc().GetBackpack(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	fish, err := p.svc().FishBasket(ctx, p.playerID)
	return backpackData{bp, fish}, err
}

func (p *Panel) buildBackpack(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(backpackData)
	bp, basket := d.backpack, d.fish
	var tools, effects, seeds, produce, goods, consumables, prizes, materials, fish []string
	for _, t := range bp.Tools {
		switch {
		case !t.Owned:
			tools = append(tools, fmt.Sprintf("🔒 %s（未購買）", t.Name))
		case t.Enabled:
			tools = append(tools, fmt.Sprintf("✅ %s（啟用中）", t.Name))
		default:
			tools = append(tools, fmt.Sprintf("⏸️ %s（已停用）", t.Name))
		}
	}
	for _, x := range bp.Effects {
		if x.Status == "active" {
			effects = append(effects, fmt.Sprintf("🪱 %s：%s結束", x.Name, ts(x.ExpiresAt)))
		}
	}
	for _, x := range bp.Seeds {
		line := fmt.Sprintf("%s %s　×**%d**", cropEmoji(x.Code), x.Name, x.Quantity)
		seeds = append(seeds, line)
	}
	for _, x := range bp.Produce {
		produce = append(produce, fmt.Sprintf("%s %s%s　×**%d**　（%d 金／個）", cropEmoji(x.Code), x.Name,
			services.GeneticsLabel(x.Quality, x.Mutation), x.Quantity, x.Price))
	}
	for _, x := range bp.Goods {
		goods = append(goods, fmt.Sprintf("🥚 %s　×**%d**　（%d 金／個）", x.Name, x.Quantity, x.Price))
	}
	for _, x := range bp.Consumables {
		consumables = append(consumables, fmt.Sprintf("%s %s　×**%d**", lookupOr(ItemEmoji, x.Code, "🎁"), x.Name, x.Quantity))
	}
	for _, x := range bp.Prizes {
		prizes = append(prizes, fmt.Sprintf("🏆 %s　×**%d**　（價值 ×%d，%d 金／個）", x.Name, x.Quantity, x.Multiplier, x.UnitPrice))
	}
	for _, x := range bp.Materials {
		materials = append(materials, fmt.Sprintf("%s %s　×**%d**　（%s）", lookupOr(MaterialEmoji, x.Code, "🪨"), x.Name, x.Quantity, x.Description))
	}
	for _, f := range basket.Fish[:min(8, len(basket.Fish))] {
		fish = append(fish, fishLine(f, 0))
	}
	if len(basket.Fish) > 8 {
		fish = append(fish, fmt.Sprintf("…還有 %d 隻，到「🎣 魚簍」查看", len(basket.Fish)-8))
	}
	e := &ui.Embed{Title: "🎒 ✦ 背包 ✦", Color: Blue, Description: sections(
		fmt.Sprintf("📦 倉庫 **%d/%d**\n%s", bp.Used, bp.Capacity, bar(bp.Used, bp.Capacity, 16)),
		Section{Title: "🌰 種子（放素材背包，不佔倉庫）", Lines: seeds},
		Section{Title: "🧺 農產品", Lines: produce},
		Section{Title: "🥚 動物產品與加工品", Lines: goods},
		Section{Title: "🧪 道具", Lines: consumables},
		Section{Title: "🏆 賭場獎品", Lines: prizes},
		Section{Title: fmt.Sprintf("⛰️ 素材背包 %d/%d（不佔倉庫，種子也算在內）", bp.MaterialUsed, bp.MaterialCapacity), Lines: materials},
		Section{Title: "🔧 工具", Lines: tools},
		Section{Title: "✨ 生效中的效果", Lines: effects},
		Section{Title: fmt.Sprintf("🎣 魚簍 %d/%d（全部價值 %s 金）", len(basket.Fish), basket.Capacity, money(basket.TotalValue)), Lines: fish},
	)}
	for _, t := range bp.Tools {
		if !t.Owned {
			continue
		}
		label := "啟用" + t.Name
		if t.Enabled {
			label = "停用" + t.Name
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("set_tool_enabled", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetToolEnabled(ctx, p.playerID, t.Code, !t.Enabled)
			}), actOpts{})
			return nil
		}, btn{emoji: "🔧", row: 0})
	}
	p.nav("前往販售", "sell", btn{emoji: "💰", row: 1})
	p.nav("商店", "shop", btn{emoji: "🏪", row: 1})
	p.nav("魚簍", "fish_basket", btn{emoji: "🎣", row: 1})
	if coins := lineQty(bp.Materials, exploration.FakeCoin); coins > 0 {
		p.button(fmt.Sprintf("兌換假幣（持有 %d）", coins), func(ctx context.Context, it ui.Interaction) error {
			p.picker = &Picker{Key: "fake_coin", Label: fmt.Sprintf("💱 兌換假幣（1 個 = %d 金）", exploration.FakeCoinRate()),
				Verb: "換", Max: coins, Presets: []int{1, 5, 10},
				Run: p.callQty("exchange_fake_coins", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().ExchangeFakeCoins(ctx, p.playerID, q)
				})}
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "💱", row: 1, style: ui.Success})
		if p.picker != nil && p.picker.Key == "fake_coin" {
			p.picker.Max = coins
			e.AddField("💱 兌換假幣", fmt.Sprintf("持有 **%d** 個，1 個 = **%d** 金\n👇 選擇兌換數量", coins, exploration.FakeCoinRate()), false)
			p.quantityButtons(2)
		}
	}
	p.navBack(4)
	return e, nil
}

// ======================================================================
// 說明
// ======================================================================

type helpData struct {
	name string
	gold int
}

func (p *Panel) loadHelp(ctx context.Context) (any, error) {
	st, err := p.svc().GetFarmStatus(ctx, p.playerID)
	return helpData{st.Name, st.Gold}, err
}

func (p *Panel) buildHelp(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(helpData)
	p.navBack(0)
	cost := balance.RenameFarmCost
	p.button(fmt.Sprintf("更改農場名稱（%d 金幣）", cost), func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, renameModal(d.name, func(ctx context.Context, mit ui.Interaction, name string) {
			p.act(ctx, mit, p.call("rename_farm", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().RenameFarm(ctx, p.playerID, name)
			}), actOpts{})
		}))
	}, btn{emoji: "✏️", row: 0, disabled: d.gold < cost})
	e := HelpEmbed()
	e.AddField("🏡 農場名稱", fmt.Sprintf("目前：**%s**\n按「✏️ 更改農場名稱」可以改名，每次 %d 金幣（你有 %s 金幣）。", d.name, cost, money(d.gold)), false)
	return e, nil
}

// HelpEmbed 玩法說明（/help 與說明頁共用）。
func HelpEmbed() *ui.Embed {
	maxLevel := pets.MaxLevel()
	e := &ui.Embed{Title: "❓ 怎麼玩", Color: Blue, Description: "輸入 **/farm** 打開農場面板，所有操作都用按鈕和選單完成，面板只有你看得到。"}
	e.AddField("🌱 種田", fmt.Sprintf("「🌱 農田」選作物會種滿空地（種子不夠會自動買）→「💧 全部澆水」→ 等成熟 →「🧺 全部收成」→「💰 販售」。\n"+
		"農田最多 %d 格。成熟後 16 小時沒收成會乾枯。\n"+
		"化肥讓剩餘時間減少 %d%%；蚯蚓讓正在生長與之後澆水的作物生長時間 ×%v；自動澆水器種下就澆水。溫室每一級讓生長時間再少一些（商店購買）。",
		balance.MaxPlots, balance.FertilizerPct, balance.EarthwormGrowMultiplier), false)
	e.AddField("🐔 養殖與加工", fmt.Sprintf("先建造畜舍再買動物，餵食後等待生產再收集。磨坊、烘焙坊、果醬工坊可以把作物加工成更值錢的商品。\n"+
		"「🐾 寵物」可以領養貓咪（收成數量增加）和狗狗（多擋一次偷菜），領養 %s 金，Lv0 就有 %d%%，每升一級再 +%d%%，"+
		"最高 Lv%d（%d%%）；寵物會出現在農場圖上，不佔畜舍。",
		money(pets.BuyPrice()), pets.BasePct(), pets.PctPerLevel(), maxLevel, pets.BonusPct(&maxLevel)), false)
	e.AddField("🥷 偷菜與防守", fmt.Sprintf("可以偷同一伺服器成員已成熟的作物：在「🥷 偷菜」選一位玩家打開他的農田，再選要偷哪一格；"+
		"每天只能偷同一位玩家 %d 格田，每人每天最多被 %d 個人偷（每次最多偷 %d 個，小麥 1 個）。"+
		"商店的「傳說扒手手套」可以不受每日限制，偷所有人的所有成熟農田。"+
		"稻草人、警戒犬、防盜圍欄能阻止別人偷你的菜，巡查券可以查是誰來偷。",
		balance.StealPlotsPerVictimPerDay, balance.StealThievesPerVictimPerDay, balance.StealMaxPerAction), false)
	e.AddField("🍄 地下菇房", fmt.Sprintf("農場 Lv.%d 開放，從「🌱 農田」→「🍄 地下菇房」進入，不用建造。"+
		"地下菇房的養分由上方農田所種下的作物決定；菇床不用種植、不用澆水，約 %s長好一批。"+
		"投入探險帶回的🪵木屑提高稀有菇機率（投滿 %d 個保證一半以上是稀有菇）。菇可以直接賣，或在烘焙坊做成料理。",
		mushroom.Unlock(), mushroom.GrowHoursText(), mushroom.MaxSawdust), false)
	e.AddField("⛰️ 山洞冒險", caveHelpText(), false)
	e.AddField("🎣 釣魚", "總覽「🎣 釣魚」→ 先到「🛒 釣具商店」買魚竿、魚線、魚餌（商店也有「🎣 釣具商店」分頁）→「開始釣魚」。"+
		"魚上鉤後在時限內按「拉竿」，再依 💬 提示收線／放線／等待，魚竄向一邊要壓同方向；"+
		"每條魚要成功拉幾下（普通 2–4、稀有 3–5、史詩 4–6、傳說 5–7），魚竄向一邊時只有壓同方向才算，"+
		"只按收線一定會斷線。偶爾會有人機確認，答錯只會暫停釣魚。"+
		"稀有度只影響拉扯難度，基礎成功率都一樣；釣到的魚放進魚簍，可以出售或「📢 分享到群組」。", false)
	e.AddField("🐉 世界 Boss", "伺服器的共同活動：總覽「🐉 世界Boss」→「📦 捐獻物資」把農作物、漁獲、素材或加工品捐給前線，"+
		"糧食／醫療／戰鬥／裝備四種補給越充足，前線自動打 Boss 越快。活動結束後依結果、積分與排名「🎁 領取獎勵」。", false)
	e.AddField("🏅 簽到、成就與圖鑑", "每天（台灣時間）第一次打開面板會自動簽到，連續簽到 7 天一輪，第 7 天獎勵最多，中斷一天就重新計算。"+
		"總覽「🏅 成就圖鑑」可以領取成就獎勵（進度包含以前的紀錄）、選擇要顯示的稱號，"+
		"以及查看作物、動物產品、加工品、山洞素材與魚類圖鑑。稱號會顯示在總覽、排行榜與世界 Boss 排行。", false)
	e.AddField("👥 群組", "每個 Discord 伺服器是一個群組。你的農場屬於建立時的伺服器，只有在那裡的操作才計入群組任務與競賽；"+
		"可在「👥 群組」把農場轉移到目前的伺服器（每 7 天一次）。", false)
	e.AddField("🏆 排行與獎勵", "排行榜有個人、本伺服器與群組總排行，可切換金幣、農產品販售、商品總販售與期間；排名獎勵會自動發放。", false)
	e.AddField("🎵 聽音樂", "先進入語音頻道，再用 **/play** 點歌（關鍵字、YouTube／Spotify 連結）。\n"+
		"/nowplaying 顯示控制面板（暫停、跳過、循環、音量）、/queue 看清單；完整說明請用 **/music**。", false)
	e.AddField("💬 官方群組", "問題回報、建議與活動公告都在官方群組："+config.OfficialGuildURL, false)
	return e
}

// caveHelpText 說明頁的山洞冒險（區域等級與時間、假幣匯率依管理網站目前的探險設定）。
func caveHelpText() string {
	var names, hours []string
	for _, code := range exploration.AreaOrder {
		a := exploration.AreaOf(code)
		if a.NeedsRuins {
			continue
		}
		names = append(names, fmt.Sprintf("%s（Lv.%d）", a.Name, a.UnlockLevel))
		if a.DurationSeconds%3600 == 0 {
			hours = append(hours, fmt.Sprintf("%d", a.DurationSeconds/3600))
		} else {
			hours = append(hours, fmt.Sprintf("%.1f", float64(a.DurationSeconds)/3600))
		}
	}
	return fmt.Sprintf("總覽「⛰️ 山洞」探索%s，分別探險 %s 小時，"+
		"一次只能去一個區域，回來後領取獎勵才能再出發；可取得素材、🪵木屑、稀有種子與假幣（背包兌換，1 個 = %d 金）。"+
		"素材可在「🔧 機器」製作種子製造機、自動收割機、自動加工機與稀有作物培育器。",
		strings.Join(names, "、"), strings.Join(hours, "／"), exploration.FakeCoinRate())
}
