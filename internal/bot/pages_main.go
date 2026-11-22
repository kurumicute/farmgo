package bot

// 面板頁面：歡迎（尚未建立農場）、總覽、農田、背包、說明（對應 Python panels.py 的同名區段與 help_embed）。

import (
	"context"
	"farmgm/i18n"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/config"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/beehive"
	"farmgm/internal/game/expansion"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/questboard"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/weather"
	"farmgm/internal/services"
)

// registerPages 頁面名稱 → 讀取與建立函式（Python getattr(self, f"_load_{page}")）。
func (p *Panel) registerPages() {
	p.pages = map[string]pageDef{
		"welcome":         {func(context.Context) (any, error) { return nil, nil }, p.buildWelcome},
		"main":            {p.loadMain, p.buildMain},
		"plots":           {p.loadPlots, p.buildPlots},
		"cellar":          {p.loadCellar, p.buildCellar},
		"orders":          {p.loadOrders, p.buildOrders},
		"beehive":         {p.loadBeehive, p.buildBeehive},
		"fair":            {p.loadFair, p.buildFair},
		"treasure":        {p.loadTreasure, p.buildTreasure},
		"lab":             {p.loadLab, p.buildLab},
		"lab_seeds":       {p.loadLabSeeds, p.buildLabSeeds},
		"lab_cross":       {p.loadLab, p.buildLabCross},
		"lab_clone":       {p.loadLab, p.buildLabClone},
		"lab_discard":     {p.loadLab, p.buildLabDiscard},
		"decor":           {p.loadDecor, p.buildDecor},
		"lab_records":     {p.loadLabRecords, p.buildLabRecords},
		"lab_help":        {func(context.Context) (any, error) { return nil, nil }, p.buildLabHelp},
		"backpack":        {p.loadBackpack, p.buildBackpack},
		"auto_seller":     {p.loadAutoSeller, p.buildAutoSeller},
		"boosts":          {p.loadBoosts, p.buildBoosts},
		"shop":            {p.loadShop, p.buildShop},
		"prices":          {p.loadPrices, p.buildPrices},
		"genetics":        {p.loadGenetics, p.buildGenetics},
		"sell":            {p.loadSell, p.buildSell},
		"animals":         {p.loadAnimals, p.buildAnimals},
		"pets":            {p.loadPets, p.buildPets},
		"craft":           {p.loadCraft, p.buildCraft},
		"build":           {p.loadBuild, p.buildBuild},
		"craft_prices":    {p.loadCraftPrices, p.buildCraftPrices},
		"defense":         {p.loadDefense, p.buildDefense},
		"casino":          {p.loadCasino, p.buildCasino},
		"steal":           {p.loadSteal, p.buildSteal},
		"aid":             {p.loadAid, p.buildAid},
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
		"badges":          {p.loadBadges, p.buildBadges},
		"reputation":      {p.loadReputation, p.buildReputation},
		"looks":           {p.loadLooks, p.buildLooks},
		"auction":         {p.loadAuction, p.buildAuction},
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
	e := &ui.Embed{Title: p.s("🌾 歡迎來到農場！"), Color: Green}
	if p.groupID == nil {
		e.Description = p.s("你還沒有農場。請到 Discord 伺服器中輸入 /farm 建立（農場會屬於那個伺服器）。")
		return e, nil
	}
	e.Description = p.f("你還沒有農場。按下面的按鈕，就會在「%s」建立你的農場。\n"+
		"開始時有 %s 金幣、%d 格農田。\n"+
		"每個 Discord 帳號只有一座農場；之後可以轉移到其他伺服器（每 7 天一次）。", p.groupName, money(balance.InitialGold), balance.InitialPlots)
	create := func(ctx context.Context, it ui.Interaction, name string) {
		_ = it.DeferUpdateIfPending(ctx)
		w := p.who
		var created services.FarmStatus
		err := p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
			var err error
			if created, err = Call(ctx, p.cmds.Runner, "start_external_farm", func(ctx context.Context) (services.FarmStatus, error) {
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
			p.Page = "plots" // 2026-11-16 新玩家直接到農田頁：種植選單就在下面（總覽資訊太多，第一次看不知道從哪開始）
			return nil
		})
		if err != nil {
			logUnexpected(err, "建立農場失敗", "user", w.UserID)
			p.showError(ctx, it, cleanMessage(p.msg(err)))
			return
		}
		// 2026-11-16：直接到農田頁後畫面上沒有農場名稱，建立成功的提示寫出名稱（自訂名稱時玩家才知道有沒有設定成功）
		p.refresh(ctx, it, p.f("🎉 農場建立完成：「%s」！👇 從下方選單選一種作物，會種滿所有空地（種子不夠會自動買）。",
			escapeMD(p.s(created.Name))), nil, p.s("重繪"), time.Time{})
	}
	p.button(p.s("建立農場"), func(ctx context.Context, it ui.Interaction) error {
		create(ctx, it, "")
		return nil
	}, btn{emoji: "🌱", style: ui.Success})
	p.button(p.s("自訂名稱建立"), func(ctx context.Context, it ui.Interaction) error {
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
	todo    services.TodoSummary // 菇房、實驗室、魚簍（2026-11-18 待辦區補齊）
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
	if d.todo, err = svc.TodoSummary(ctx, pid); err != nil {
		return nil, err
	}
	if d.home != nil { // 所屬群組昨天的日報（同一天會快取）
		if d.news, err = svc.DailyNews(ctx, d.home.ID, time.Time{}); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// farmNextStep 農田的下一步（純文字，放在 footer；footer 不支援粗體與 Discord 時間戳）：
// 可收成、待澆水、乾枯、空地與下一批成熟時間。plotsPage=true 時空地提示指向下方的種植選單。
func (p *Panel) farmNextStep(plots []services.PlotInfo, plotsPage bool) string {
	var parts []string
	if n := countState(plots, rules.PlotReady); n > 0 {
		parts = append(parts, p.t("ui.next.ready", "n", n))
	}
	if n := countState(plots, rules.PlotWaitingWater); n > 0 {
		parts = append(parts, p.t("ui.next.waiting", "n", n))
	}
	if n := countState(plots, rules.PlotWithered); n > 0 {
		parts = append(parts, p.t("ui.next.withered", "n", n))
	}
	if n := countState(plots, rules.PlotEmpty); n > 0 {
		if plotsPage {
			parts = append(parts, p.t("ui.next.empty_plots_page", "n", n))
		} else {
			parts = append(parts, p.t("ui.next.empty", "n", n))
		}
	}
	var next *time.Time
	for _, pl := range plots {
		if pl.State == rules.PlotGrowing && pl.ReadyAt != nil && (next == nil || pl.ReadyAt.Before(*next)) {
			next = pl.ReadyAt
		}
	}
	if next != nil {
		if left := int(next.Sub(p.cmds.now()).Seconds()); left > 0 {
			parts = append(parts, p.t("ui.next.next_batch", "time", services.DurationLang(p.lang, left, true)))
		}
	}
	if len(parts) == 0 {
		return p.t("ui.next.all_good")
	}
	return strings.Join(parts, p.t("ui.next.sep"))
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
		title = p.s("〔") + *s.Title + p.s("〕")
	}
	header := p.t("ui.main.header", "name", s.PlayerName, "title", title, "level", s.Level, "gold", money(s.Gold), "plots", len(s.Plots))
	// 2026-11-22 使用者反映手機上總覽太長：預設精簡版（不列每塊土地、狀態合併成一個欄位、日報只留前 2 行、活動不顯示說明），
	// 「更多功能」選單可以切換成完整版
	full := p.cmds.mainFullOf(p.ownerID)
	desc := strings.SplitN(header, "\n", 2)[0]
	if full {
		desc = header + "\n" + strings.Join(plotLines(p.lang, s.Plots, true), "\n")
	}
	e := &ui.Embed{Title: fmt.Sprintf("🌾 ✦ %s ✦", p.s(s.Name)), Color: Green, Description: desc}
	if p.AvatarURL != "" {
		e.Thumbnail = p.AvatarURL
	}
	p.attachImage(e, p.farmImage(d.status)) // 圖片在交易外產生
	storage := fmt.Sprintf("%d/%d\n%s", s.WarehouseUsed, s.WarehouseCapacity, bar(s.WarehouseUsed, s.WarehouseCapacity, 8))
	home := p.t("ui.main.not_set")
	if d.home != nil {
		home = p.s(d.home.Name)
	}
	stats := summaryCounts(p.lang, s.Plots)
	if stats == "" {
		stats = "—"
	}
	a := d.animals
	animal := p.t("ui.main.no_animals")
	if a.Used != 0 {
		animal = p.t("ui.main.animal_status", "used", a.Used, "cap", a.Capacity, "ready", a.Counts["ready"], "hungry", a.Counts["hungry"])
		if a.NextReadyAt != nil {
			animal += "\n⏰ " + ts(a.NextReadyAt)
		}
	}
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
		craft = append(craft, p.t("ui.main.craft_ready", "n", done))
	}
	for _, o := range busy[:min(3, len(busy))] {
		craft = append(craft, fmt.Sprintf("⚙️ %s %s", p.s(o.RecipeName), ts(&o.ReadyAt)))
	}
	craftText := strings.Join(craft, "\n")
	if craftText == "" {
		craftText = p.t("ui.main.idle")
	}
	if full {
		e.AddField(p.t("ui.main.storage"), storage, true)
		e.AddField(p.t("ui.main.home_server"), home, true)
		e.AddField(p.t("ui.main.plot_stats"), strings.ReplaceAll(stats, "　", "\n"), true)
		e.AddField(p.t("ui.main.animals"), animal, true)
		e.AddField(p.t("ui.main.crafting"), craftText, true)
	} else {
		// 精簡版：一個欄位、每項一行（手機上並排欄位會一個一個往下疊）
		crafts := craftText
		if len(craft) > 0 {
			crafts = strings.Join(craft[:min(2, len(craft))], "　")
		}
		status := []string{
			p.t("ui.main.storage") + fmt.Sprintf(" **%d/%d**", s.WarehouseUsed, s.WarehouseCapacity) + "　" + p.t("ui.main.home_server") + " " + home,
			"🌱 " + stats,
			p.t("ui.main.animals") + " " + strings.ReplaceAll(animal, "\n", "　"),
			p.t("ui.main.crafting") + " " + crafts,
		}
		e.AddField(p.s("📋 農場狀態"), strings.Join(status, "\n"), false)
	}
	var events []string
	for _, ev := range d.events {
		name, _ := ev.View["name"].(string)
		effects, _ := ev.View["effects_text"].(string)
		desc, _ := ev.View["description"].(string)
		line := "🔥 **" + name + "**"
		if ev.CropName != nil && *ev.CropName != "" {
			line += p.t("ui.main.event_only", "crop", *ev.CropName)
		}
		line += p.t("ui.main.event_effect") + effects
		if desc != "" && full {
			line += p.s("\n　📝 ") + desc
		}
		events = append(events, line)
	}
	e.AddField(p.t("ui.main.events"), linesOr(events, p.t("ui.main.no_events"), 1024), false)
	var hints []string
	if ready > 0 {
		hints = append(hints, p.t("ui.main.hint_ready", "n", ready))
	}
	if waiting > 0 {
		hints = append(hints, p.t("ui.main.hint_waiting", "n", waiting))
	}
	if len(growing) > 0 {
		next := slices.MinFunc(growing, func(a, b time.Time) int { return a.Compare(b) })
		hints = append(hints, p.t("ui.main.hint_next")+ts(&next))
	}
	if countState(s.Plots, rules.PlotEmpty) == len(s.Plots) {
		hints = append(hints, p.t("ui.main.hint_all_empty"))
	}
	more, goes := todoHints(p.lang, d)
	hints = append(hints, more...)
	todo := strings.Join(hints, "\n")
	if todo == "" {
		todo = p.t("ui.main.all_good")
	}
	// 待辦放在最後一個欄位，緊貼下方按鈕（玩家遊玩時視線停在按鈕區）
	if d.news != nil && !d.news.Empty {
		news := d.news.Text
		if lines := strings.Split(news, "\n"); !full && len(lines) > 2 {
			news = strings.Join(lines[:2], "\n") + "\n" + p.s("…完整日報：「更多功能」選「總覽：完整版」")
		}
		e.AddField(p.t("ui.main.gazette", "server", p.s(d.home.Name)), news, false)
	}
	e.AddField(p.t("ui.main.todo"), todo, false)
	e.Footer = p.farmNextStep(s.Plots, false) // 大圖排在所有欄位之後，緊貼按鈕的只有 footer：放待辦摘要
	// 每日天氣（Go 版新增）：放在最底部（footer 第一行），每天台灣時間 0 點換
	footer := p.weatherFooter(time.Now())
	if len(d.todo.BoardBuffs) > 0 { // 委託增益（2026-11-20）：和天氣放在一起
		var buffs []string
		for _, tier := range d.todo.BoardBuffs {
			buffs = append(buffs, questboard.TierEmoji[tier]+" "+p.s(questboard.Buffs[tier].Text))
		}
		footer += "\n" + p.f("📜 委託增益（完成後 %d 小時）：%s", questboard.BuffHours, strings.Join(buffs, p.s("、")))
	}
	e.Footer = strings.TrimSuffix(footer+"\n"+e.Footer, "\n")
	if g := guideHint(p.lang, p.marks); g != "" { // 新手引導（2026-11-18）：footer 最後一行，緊貼按鈕
		e.Footer += "\n" + g
	}

	p.button(p.t("ui.btn.water_all"), p.waterAll, btn{emoji: "💧", style: ui.Primary, row: 0, disabled: waiting == 0})
	p.button(p.t("ui.btn.harvest_all"), p.harvestAll, btn{emoji: "🧺", style: ui.Success, row: 0, disabled: ready == 0})
	p.button(p.t("ui.btn.refresh"), func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "main", "")
		return nil
	}, btn{emoji: "🔄", row: 0})
	p.button(p.t("ui.btn.share"), p.share, btn{emoji: "📢", row: 0, disabled: p.groupID == nil})
	p.button(p.t("ui.btn.help"), func(ctx context.Context, it ui.Interaction) error {
		p.helpTopic = "" // 從總覽進來先看主題清單
		p.goTo(ctx, it, "help", "")
		return nil
	}, btn{emoji: "❓", row: 0})
	// 2026-11-16 總覽只留兩列按鈕：第 1 列操作、第 2 列最常用的入口；其他功能收進「更多功能」選單
	// （原本 21 顆按鈕排滿 5 列，再加入口就沒有空位）。顏色規則：綠色＝主要操作、藍色＝次要操作，入口一律灰色，紅色只用在確認破壞性操作。
	p.nav(p.t("ui.btn.plots"), "plots", btn{emoji: "🌱", row: 1})
	p.nav(p.t("ui.btn.inventory"), "backpack", btn{emoji: "🎒", row: 1})
	p.nav(p.t("ui.btn.shop"), "shop", btn{emoji: "🏪", row: 1})
	p.nav(p.t("ui.btn.sell"), "sell", btn{emoji: "💰", row: 1})
	p.nav(p.t("ui.btn.animals"), "animals", btn{emoji: "🐔", row: 1})
	p.moreMenu(2)
	p.recentButtons(3)
	// 最後一列緊貼待辦欄位（2026-11-18）：一鍵操作＋待辦的「前往」按鈕（一列最多 5 顆）
	animalsReady, craftsReady := d.animals.Counts["ready"], 0
	for _, o := range d.orders {
		if o.State == "ready" {
			craftsReady++
		}
	}
	slots := 5
	if ready > 0 {
		p.button(p.t("ui.btn.harvest_replant"), p.harvestReplant, btn{emoji: "🔁", style: ui.Success, row: 4})
		slots--
	}
	if kinds := btoi(ready > 0) + btoi(animalsReady > 0) + btoi(craftsReady > 0); kinds >= 2 {
		p.button(p.t("ui.btn.collect_everything"), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, func(ctx context.Context) (string, error) {
				return p.collectEverything(ctx, ready > 0, animalsReady > 0, craftsReady > 0)
			}, actOpts{})
			return nil
		}, btn{emoji: "🧺", style: ui.Success, row: 4})
		slots--
	}
	for _, g := range goes[:min(len(goes), slots)] {
		p.nav(g.label, g.page, btn{emoji: g.emoji, row: 4})
	}
	return e, nil
}

// guideHint 新手引導的下一步（footer 是純文字，不能用粗體）：第一個還沒領獎的步驟；全部領完後不顯示。
func guideHint(lang string, m *services.ProgressMarks) string {
	if m == nil || len(m.GuideStates) == 0 {
		return ""
	}
	byCode := achievements.ByCode()
	for i, code := range achievements.GuideOrder {
		state, ok := m.GuideStates[code]
		if !ok || state == "claimed" {
			continue
		}
		name := i18n.Name(lang, "achievement."+code, byCode[code].Name)
		if state == "ready" {
			return i18n.T(lang, "ui.guide.ready", "i", i+1, "n", len(achievements.GuideOrder), "name", name)
		}
		return i18n.T(lang, "ui.guide.next", "i", i+1, "n", len(achievements.GuideOrder), "name", name,
			"hint", i18n.Name(lang, "guide."+code, achievements.GuideHints[code]))
	}
	return ""
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// harvestReplant 一鍵收成，並在收成的土地重種同一種作物（2026-11-18）。
func (p *Panel) harvestReplant(ctx context.Context, it ui.Interaction) error {
	p.act(ctx, it, p.call("harvest_and_replant", func(ctx context.Context) (services.ActionResult, error) {
		return p.svc().HarvestAndReplant(ctx, p.playerID)
	}), actOpts{})
	return nil
}

// collectEverything 一鍵收齊（2026-11-18）：依序收成農田、收集動物產品、領取加工品（各自一筆交易）。
// 某一項失敗（例如倉庫滿了）時，已完成的項目保留，失敗原因列在結果裡。
func (p *Panel) collectEverything(ctx context.Context, plots, animals, crafts bool) (string, error) {
	var parts []string
	run := func(label string, fn func(ctx context.Context) (string, error)) error {
		msg, err := fn(ctx)
		if ge, ok := gameerr.As(err); ok {
			parts = append(parts, p.f("⚠️ %s：%s", label, ge.Error()))
			return nil
		}
		if err != nil {
			return err
		}
		parts = append(parts, msg)
		return nil
	}
	steps := []struct {
		on    bool
		label string
		fn    func(ctx context.Context) (string, error)
	}{
		{plots, p.s("收成"), p.call("harvest_all", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().HarvestAll(ctx, p.playerID)
		})},
		{animals, p.s("收集動物產品"), p.collectAll},
		{crafts, p.s("領取加工品"), p.collectCrafts},
	}
	for _, s := range steps {
		if !s.on {
			continue
		}
		if err := run(s.label, s.fn); err != nil {
			return "", err
		}
	}
	return strings.Join(parts, "\n"), nil
}

// todoGo 待辦區的「前往」按鈕（收蜂蜜 2026-11-22 移到農田頁）。
type todoGo struct {
	label, page, emoji string
}

// todoHints 農田以外的待辦（2026-11-18 補齊）：動物、加工、魚簍、菇房、種子實驗室、探險（2026-11-19）；
// 有事要做的項目另外給「前往」按鈕（最多 5 顆，依列出的順序）。
func todoHints(lang string, d mainData) (hints []string, goes []todoGo) {
	t := func(key string, args ...any) string { return i18n.T(lang, "ui.todo."+key, args...) }
	add := func(g todoGo) {
		if len(goes) < 5 {
			goes = append(goes, g)
		}
	}
	a := d.animals
	if n := a.Counts["ready"]; n > 0 {
		hints = append(hints, t("animals_ready", "n", n))
	}
	if n := a.Counts["hungry"]; n > 0 {
		hints = append(hints, t("animals_hungry", "n", n))
	}
	if a.Counts["ready"]+a.Counts["hungry"] > 0 {
		add(todoGo{label: t("go_animals"), page: "animals", emoji: "🐔"})
	}
	ready := 0
	for _, o := range d.orders {
		if o.State == "ready" {
			ready++
		}
	}
	if ready > 0 {
		hints = append(hints, t("crafts_ready", "n", ready))
		add(todoGo{label: t("go_crafts"), page: "craft", emoji: "🏭"})
	}
	td := d.todo
	if td.BasketCapacity > 0 && td.BasketCount >= td.BasketCapacity {
		hints = append(hints, t("basket_full", "n", td.BasketCount, "cap", td.BasketCapacity))
		add(todoGo{label: t("go_basket"), page: "fish_basket", emoji: "🎣"})
	} else if td.BasketCapacity > 0 && td.BasketCount*5 >= td.BasketCapacity*4 {
		hints = append(hints, t("basket_almost", "n", td.BasketCount, "cap", td.BasketCapacity))
		add(todoGo{label: t("go_basket"), page: "fish_basket", emoji: "🎣"})
	}
	if td.CellarReady > 0 {
		hints = append(hints, t("cellar_ready", "n", td.CellarReady))
		add(todoGo{label: t("go_cellar"), page: "cellar", emoji: "🍄"})
	} else if td.CellarNextAt != nil {
		hints = append(hints, t("cellar_next")+ts(td.CellarNextAt))
	}
	if td.LabReady > 0 {
		hints = append(hints, t("lab_ready", "n", td.LabReady))
		add(todoGo{label: t("go_lab"), page: "lab", emoji: "🧪"})
	} else if td.LabNextAt != nil {
		hints = append(hints, t("lab_next")+ts(td.LabNextAt))
	} else if td.LabBuilt && td.LabFreeSlots > 0 {
		hints = append(hints, t("lab_free", "n", td.LabFreeSlots))
	}
	if td.TripBack {
		hints = append(hints, t("trip_back", "area", td.TripArea))
		add(todoGo{label: t("go_trip"), page: "cave", emoji: "⛰️"})
	} else if td.TripReturnAt != nil {
		hints = append(hints, t("trip_out", "area", td.TripArea, "time", ts(td.TripReturnAt)))
	}
	// 蜂箱、NPC 訂單與藏寶圖（2026-11-20）
	// 2026-11-22 使用者要求：「收蜂蜜」按鈕移到農田頁，總覽只留提示
	if h := d.status.Beehive; h != nil && h.Honey > 0 {
		if h.Honey >= h.Capacity {
			hints = append(hints, t("honey_full", "n", h.Honey))
		} else {
			hints = append(hints, t("honey_some", "n", h.Honey, "cap", h.Capacity))
		}
	}
	if td.TreasureDone {
		hints = append(hints, t("treasure_done"))
		add(todoGo{label: t("go_treasure"), page: "treasure", emoji: "🗺️"})
	} else if td.TreasureReturnAt != nil {
		hints = append(hints, t("treasure_out", "time", ts(td.TreasureReturnAt)))
	}
	if td.OrdersOpen > 0 {
		hints = append(hints, t("orders_open", "n", td.OrdersOpen, "time", ts(td.OrdersNextExpire)))
		add(todoGo{label: t("go_orders"), page: "orders", emoji: "🧑‍🌾"})
	}
	if td.RainbowClaimable { // 限時：放在最後、貼近按鈕
		hints = append(hints, t("rainbow"))
		add(todoGo{label: t("go_treasure"), page: "treasure", emoji: "🌈"})
	}
	return hints, goes
}

// recentButtons 「更多功能」裡最近用過的入口（最多 3 個）放在最下面一列，常用的功能不用每次都打開選單。
// 依 Discord 使用者記在 Bot 記憶體中（重新輸入 /farm 也保留，Bot 重新啟動後清空）。
func (p *Panel) recentButtons(row int) {
	for _, page := range p.recentPages {
		i := slices.IndexFunc(moreEntries, func(m struct{ page, label, desc, emoji string }) bool { return m.page == page })
		if i < 0 {
			continue
		}
		m := moreEntries[i]
		p.button(p.t("ui.more."+m.page), func(ctx context.Context, it ui.Interaction) error {
			p.openMore(ctx, it, m.page)
			return nil
		}, btn{emoji: m.emoji, row: row})
	}
}

// openMore 開啟「更多功能」的入口並記為最近使用（作物價格、紀錄從第 1 頁開始）。
func (p *Panel) openMore(ctx context.Context, it ui.Interaction, page string) {
	switch page {
	case "prices":
		p.pricePage = 0
	case "log":
		p.logPage = 0
	}
	p.recentPages = append([]string{page}, slices.DeleteFunc(slices.Clone(p.recentPages), func(x string) bool { return x == page })...)
	p.recentPages = p.recentPages[:min(len(p.recentPages), 3)]
	p.cmds.setRecentPages(p.ownerID, p.recentPages)
	p.goTo(ctx, it, page, "")
}

// moreEntries 總覽「更多功能」選單的入口（依類型排列：生產 → 冒險 → 社交 → 紀錄）。
// Discord 下拉選單沒有分隔線，分組標在說明的開頭（【生產】等；2026-11-18）；最近用過的入口另外放在最下面一列。
var moreEntries = []struct{ page, label, desc, emoji string }{
	{"craft", "加工", "【生產】磨坊、烘焙坊、果醬工坊、乳品工坊", "🏭"},
	{"lab", "種子實驗室", "【生產】鑑定、雜交、改良特殊種子", "🧪"}, // 2026-11-19 使用者要求：總覽也能直接進種子實驗室（農田頁的按鈕保留）
	{"defense", "防守", "【生產】稻草人、警戒犬、巡查券", "🛡️"},
	{"prices", "作物價格", "【生產】今天的市場價格", "📈"},
	{"orders", "NPC 訂單", "【生產】客人的限時訂單，報酬比市價高", "🧑‍🌾"}, // 2026-11-20
	{"cave", "山洞", "【冒險】探險收集素材、機器", "⛰️"},
	{"treasure", "藏寶圖", "【冒險】集碎片，每週到不同地點挖寶", "🗺️"}, // 2026-11-20
	{"fishing", "釣魚", "【冒險】釣魚中心、魚簍、釣具商店", "🎣"},
	{"casino", "賭場", "【冒險】用作物比大小", "🎰"},
	{"boss", "世界Boss", "【冒險】伺服器共同活動、捐獻物資", "🐉"},
	{"steal", "偷菜", "【社交】偷同伺服器玩家成熟的作物", "🥷"},
	{"aid", "送禮互助", "【社交】送朋友作物、幫忙拔草，雙方都有獎勵", "🤝"},
	{"fair", "品評會", "【社交】每週作品比賽：評審評分＋人氣投票", "🎀"}, // 2026-11-20
	{"quests", "任務競賽", "【社交】群組任務與競賽", "📜"},
	{"group", "群組", "【社交】所屬群組、轉移農場", "👥"},
	{"board", "排行榜", "【紀錄】個人、伺服器、群組排行", "🏆"},
	{"progress", "成就圖鑑", "【紀錄】成就、稱號、簽到、圖鑑", "🏅"},
	{"log", "紀錄", "【紀錄】最近的操作紀錄", "🧾"},
}

// moreMenu 「更多功能」選單：選了就換頁（作物價格、紀錄從第 1 頁開始）。
func (p *Panel) moreMenu(row int) {
	options := make([]option, 0, len(moreEntries)+1)
	for _, m := range moreEntries {
		options = append(options, option{p.t("ui.more." + m.page), m.page, p.t("ui.more." + m.page + "_desc"), m.emoji})
	}
	// 2026-11-19 語言切換：放在選單最後一項（總覽的按鈕列已滿）
	options = append(options, option{p.t("ui.lang.option"), langToggle, p.t("ui.lang.option_desc"), "🌐"})
	// 2026-11-22 總覽精簡／完整切換（手機上完整版太長）
	if p.cmds.mainFullOf(p.ownerID) {
		options = append(options, option{p.s("總覽：精簡版"), mainModeToggle, p.s("不列每塊土地、狀態合成一欄（適合手機）"), "📱"})
	} else {
		options = append(options, option{p.s("總覽：完整版"), mainModeToggle, p.s("列出每塊土地、完整日報與活動說明"), "🖥️"})
	}
	p.selectMenu(p.t("ui.more.placeholder"), options, func(ctx context.Context, it ui.Interaction, page string) error {
		if page == langToggle {
			return p.toggleLanguage(ctx, it)
		}
		if page == mainModeToggle {
			p.cmds.setMainFull(p.ownerID, !p.cmds.mainFullOf(p.ownerID))
			p.goTo(ctx, it, "main", "")
			return nil
		}
		if !slices.ContainsFunc(moreEntries, func(m struct{ page, label, desc, emoji string }) bool { return m.page == page }) {
			return gameerr.NotFoundf("找不到這個頁面。")
		}
		p.openMore(ctx, it, page)
		return nil
	}, row, "")
}

// langToggle 「更多功能」選單裡切換語言的選項值。
const langToggle = "__language__"

// mainModeToggle 「更多功能」選單裡切換總覽精簡／完整版的選項值（2026-11-22）。
const mainModeToggle = "__main_mode__"

// toggleLanguage 中英文互換，記在玩家設定（players.language），之後開面板都用這個語言。
func (p *Panel) toggleLanguage(ctx context.Context, it ui.Interaction) error {
	next := i18n.English
	if p.lang == i18n.English {
		next = i18n.Default
	}
	p.lang = next
	p.act(ctx, it, p.call("set_language", func(ctx context.Context) (services.ActionResult, error) {
		return p.svc().SetLanguage(ctx, p.playerID, next)
	}), actOpts{noProgress: true, prefix: " "})
	return nil
}

// shareCooldown 公開分享（農場、魚卡）的冷卻時間，避免連按洗版。
const shareCooldown = 30 * time.Second

// shareWait 還在冷卻時重繪面板提示並回傳 true；否則記下這次分享的時間並回傳 false。
func (p *Panel) shareWait(ctx context.Context, it ui.Interaction) bool {
	if left := shareCooldown - time.Since(p.lastShareAt); !p.lastShareAt.IsZero() && left > 0 {
		p.refresh(ctx, it, p.f("⚠️ 剛剛才分享過，請 %d 秒後再分享。", int(left.Seconds())+1), nil, p.s("重繪"), time.Time{})
		return true
	}
	p.lastShareAt = time.Now()
	return false
}

// share 把農田圖片公開分享到頻道（其他人看得到）。
func (p *Panel) share(ctx context.Context, it ui.Interaction) error {
	if p.shareWait(ctx, it) {
		return nil
	}
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
		p.lastShareAt = time.Time{} // 沒有分享出去，不算冷卻
		_, _ = it.Followup(ctx, ui.Message{Content: ptr("❌ " + p.msg(err)), Ephemeral: true})
		return nil
	}
	lines := plotLines(p.lang, status.Plots, false) // 公開訊息：不顯示「請清除」這類給主人看的提示
	desc := strings.Join(lines, "\n")
	if r := []rune(desc); len(r) > 4000 {
		desc = string(r[:4000])
	}
	e := &ui.Embed{Title: p.f("🌾 %s（Lv %d）", p.s(status.Name), status.Level), Color: Green, Description: desc,
		AuthorName: status.PlayerName}
	e.AddField(p.s("💰 金幣"), money(status.Gold), true)
	msg := ui.Message{Embed: e}
	if len(png) > 0 {
		e.Image = "attachment://" + FarmImage
		msg.Files = []ui.File{{Name: FarmImage, Data: png}}
	}
	notice := p.s("📢 已分享到這個頻道！")
	if _, err := it.Followup(ctx, msg); err != nil {
		slog.Warn("分享農場失敗", "type", fmt.Sprintf("%T", err))
		p.lastShareAt = time.Time{}
		notice = p.s("⚠️ 訊息沒有送出（Bot 可能沒有這個頻道的發言權限）。")
	}
	p.refresh(ctx, it, notice, nil, p.s("重繪"), time.Time{})
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
	labSeeds []services.LabSeedView // 現在可以種的特殊種子（沒有空地時不查）
	grow     map[string]int         // 作物 → 種下澆水後實際的生長秒數（含所有加速；沒有空地時不查，2026-11-19）
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
	if countState(d.status.Plots, rules.PlotEmpty) > 0 {
		if d.labSeeds, err = svc.PlantableLabSeeds(ctx, pid); err != nil {
			return nil, err
		}
		if d.grow, err = svc.GrowTimes(ctx, pid); err != nil {
			return nil, err
		}
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
		name := p.s(x.Name)
		if p.lang != i18n.Default {
			name = p.t("ui.plots.seed_name", "crop", services.CropName(p.lang, x.Code, p.s(x.Name)))
		}
		seedParts = append(seedParts, p.t("ui.plots.seed_line", "emoji", cropEmoji(x.Code), "name", name, "n", x.Quantity))
	}
	seedText := strings.Join(seedParts, p.s("　"))
	if seedText == "" {
		seedText = p.t("ui.plots.no_seeds")
	}
	e := &ui.Embed{Title: p.t("ui.plots.title", "n", len(s.Plots)), Color: Green, Description: sections(
		p.t("ui.plots.gold_seeds", "gold", money(s.Gold), "seeds", seedText),
		Section{Title: p.t("ui.plots.section"), Lines: plotLines(p.lang, s.Plots, true)},
		"📊 "+summaryCounts(p.lang, s.Plots),
	), Footer: p.farmNextStep(s.Plots, true)} // 下一步放 footer：大圖排在欄位之後，footer 才是緊貼按鈕的位置
	p.attachImage(e, p.farmImage(d.status)) // 圖片在交易外產生
	if empty := countState(s.Plots, rules.PlotEmpty); empty > 0 {
		var options []option
		for _, c := range d.crops {
			if !(c.ShopBuyable || seeds[c.Code] > 0) || !c.InSeason { // 不在季節的不列出
				continue
			}
			cname := services.CropName(p.lang, c.Code, p.s(c.Name))
			label := p.t("ui.plots.crop_option", "crop", cname, "price", money(c.SeedPrice), "have", seeds[c.Code])
			if !c.ShopBuyable {
				label = p.t("ui.plots.crop_option_rare", "crop", cname, "have", seeds[c.Code])
			}
			// 2026-11-19 顯示種下澆水後實際要長多久（限時活動、溫室、蚯蚓、灑水器等加速都算進去），不再顯示作物原本的時間
			grow, ok := d.grow[c.Code]
			if !ok {
				grow = c.GrowSeconds
			}
			timeText := services.DurationLang(p.lang, grow, true)
			if grow < c.GrowSeconds {
				timeText += p.t("ui.plots.boosted")
			}
			options = append(options, option{label, c.Code,
				p.t("ui.plots.crop_desc", "time", timeText, "qty", c.HarvestQuantity, "crop", cname, "price", money(c.SellPrice)),
				cropEmoji(c.Code)})
		}
		p.selectMenu(p.t("ui.plots.plant_placeholder", "n", empty), options, func(ctx context.Context, it ui.Interaction, code string) error {
			p.act(ctx, it, p.call("plant_all", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().PlantAll(ctx, p.playerID, code)
			}), actOpts{noProgress: true})
			return nil
		}, 0, "")
	}
	p.button(p.t("ui.btn.water_all"), p.waterAll, btn{emoji: "💧", style: ui.Primary, row: 1, disabled: countState(s.Plots, rules.PlotWaitingWater) == 0})
	p.button(p.t("ui.btn.harvest_all"), p.harvestAll, btn{emoji: "🧺", style: ui.Success, row: 1, disabled: countState(s.Plots, rules.PlotReady) == 0})
	p.button(p.t("ui.btn.clear_withered"), p.clearAll, btn{emoji: "🥀", row: 1, disabled: countState(s.Plots, rules.PlotWithered) == 0})
	price, ok := expansion.LandPrice(len(s.Plots))
	confirming := p.confirm == "upgrade"
	label := p.t("ui.plots.expand", "price", money(price))
	style := ui.Secondary
	switch {
	case !ok:
		label = p.t("ui.plots.at_max")
	case confirming:
		label, style = p.t("ui.plots.confirm_expand"), ui.Danger
	}
	p.button(label, func(ctx context.Context, it ui.Interaction) error {
		if p.confirm != "upgrade" {
			p.confirm = "upgrade"
			p.refresh(ctx, it, p.t("ui.plots.confirm_expand_note", "price", money(price), "n", balance.UpgradePlotsAdded),
				nil, p.s("重繪"), time.Time{})
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
		// 2026-11-15 起可以複選：勾選要施肥的農田（最多持有的化肥數量），選完直接施肥
		growing = growing[:min(len(growing), ui.MaxOptions)]
		multi := &ui.Select{Placeholder: p.t("ui.plots.fertilize_placeholder", "have", fert, "pct", balance.FertilizerPct),
			Row: 2, MinValues: 1, MaxValues: min(len(growing), fert),
			OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
				plots := make([]int, 0, len(values))
				for _, v := range values {
					if n, ok := parseDigits(v); ok {
						plots = append(plots, n)
					}
				}
				p.act(ctx, it, p.call("use_fertilizer", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().UseFertilizerPlots(ctx, p.playerID, plots)
				}), actOpts{})
				return nil
			}}
		for _, pl := range growing {
			desc := p.t("ui.plots.fertilize_desc", "pct", balance.FertilizerPct)
			if pl.RemainingSeconds > 0 {
				desc += p.t("ui.plots.fertilize_left") + services.DurationLang(p.lang, pl.RemainingSeconds, false)
			}
			multi.Options = append(multi.Options, ui.SelectOption{Label: cut(fmt.Sprintf("#%d %s", pl.PlotNumber,
				services.CropName(p.lang, deref(pl.CropCode), p.s(deref(pl.CropName)))), 100),
				Value: fmt.Sprint(pl.PlotNumber), Emoji: "🧪", Description: cut(desc, 100)})
		}
		p.add(multi)
	}
	if worms := lineQty(bp.Consumables, balance.ItemEarthworm); worms > 0 {
		// 第 3 列讓給特殊種子選單（2026-11-15）
		p.button(p.t("ui.plots.earthworm", "n", worms), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("use_earthworm", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().UseEarthworm(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🪱", row: 1})
	}
	if empty := countState(s.Plots, rules.PlotEmpty); empty > 0 && len(d.labSeeds) > 0 {
		// 種子實驗室的特殊種子（2026-11-15）：勾選要種的種子，每顆依序種到一格空地；沒有可種的特殊種子時不顯示
		// 2026-11-16 超過 25 顆時選單內翻頁（第一項「上一頁」、最後一項「下一頁」），以前只列前 25 顆，後面的種子種不到
		// 2026-11-17 同作物排在一起；預設直接列出全部種子可以勾選，選單第一項「🔎 篩選作物」切換成作物清單（同一列），
		// 選了作物只列那種作物的種子（原本一定要先選作物，不能直接種，使用者要求改回）
		codes, _, _ := seedCrops(d.labSeeds)
		picking := len(codes) > 1 && p.plotSeedCrop == cropFilterPick
		seeds := sortSeedsByCrop(d.labSeeds)
		if !picking {
			if seeds = filterSeeds(seeds, p.plotSeedCrop); len(seeds) == 0 {
				p.plotSeedCrop, seeds = "", sortSeedsByCrop(d.labSeeds)
			}
		}
		if picking {
			opts, page, pages := pagedOptions(cropFilterOptions(d.labSeeds, p.t("ui.plots.lab_filter_all")), p.plotSeedPage)
			p.plotSeedPage = page
			p.selectMenu(p.t("ui.plots.lab_filter_placeholder", "n", len(d.labSeeds), "empty", empty), opts,
				func(ctx context.Context, it ui.Interaction, value string) error {
					if !turnPage(value, &p.plotSeedPage, pages) {
						p.plotSeedCrop, p.plotSeedPage = strings.TrimPrefix(value, cropFilterPrefix), 0
					}
					p.goTo(ctx, it, "plots", "")
					return nil
				}, 3, "")
		} else {
			all := make([]option, 0, len(seeds)+1)
			if len(codes) > 1 {
				filterLabel := p.t("ui.plots.lab_filter")
				if name := cropFilterName(d.labSeeds, p.plotSeedCrop); name != "" {
					filterLabel = p.t("ui.plots.lab_filter_change", "crop", name)
				}
				all = append(all, option{filterLabel, cropFilterPrefix, p.t("ui.plots.lab_filter_desc"), "🔎"})
			}
			for _, sv := range seeds {
				// 2026-11-19 和一般作物一樣顯示種下澆水後實際的生長時間（含加速與生長基因），放在最前面才不會被 100 字截掉
				desc := seedOptionDesc(sv)
				if sv.GrowSeconds > 0 {
					desc = "⏱️ " + services.DurationLang(p.lang, sv.GrowSeconds, true) + p.s("　") + desc
				}
				all = append(all, option{seedOptionLabel(sv), fmt.Sprint(sv.ID), desc, cropEmoji(sv.CropCode)})
			}
			opts, page, pages := pagedOptions(all, p.plotSeedPage)
			p.plotSeedPage = page
			holder := p.t("ui.plots.lab_placeholder", "empty", empty)
			if pages > 1 {
				holder = p.t("ui.plots.lab_placeholder_paged", "empty", empty, "page", page+1, "pages", pages)
			}
			multi := &ui.Select{Placeholder: holder, Row: 3, MinValues: 1, MaxValues: min(len(opts), empty),
				OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
					ids := make([]int64, 0, len(values))
					turn := ""
					for _, v := range values {
						if v == pagePrev || v == pageNext || v == cropFilterPrefix {
							turn = v
						} else if id, ok := parseDigits(v); ok {
							ids = append(ids, int64(id))
						}
					}
					if len(ids) == 0 {
						if turn == cropFilterPrefix {
							p.plotSeedCrop, p.plotSeedPage = cropFilterPick, 0
							p.goTo(ctx, it, "plots", "")
						} else if turnPage(turn, &p.plotSeedPage, pages) {
							p.goTo(ctx, it, "plots", "")
						}
						return nil
					}
					// 同時勾了種子和翻頁選項時只種種子（翻頁選項忽略）
					p.act(ctx, it, p.call("plant_lab_seeds", func(ctx context.Context) (services.ActionResult, error) {
						return p.svc().PlantLabSeeds(ctx, p.playerID, ids)
					}), actOpts{})
					return nil
				}}
			for _, o := range opts {
				multi.Options = append(multi.Options, ui.SelectOption{Label: cut(o.label, 100), Value: o.value,
					Emoji: o.emoji, Description: cut(o.desc, 100)})
			}
			p.add(multi)
		}
	}
	// 2026-11-22 使用者要求：蜂箱入口與「收蜂蜜」從總覽移到農田頁。
	// 元件最多 5 列，蚯蚓、化肥選單、特殊種子選單、種子製造機都出現時會放不下，所以依重要程度加入：
	// 收蜂蜜（第 1 列放不下時自動換列）→ 菇房、實驗室、蜂箱、擺設、回總覽 → 種子製造（還有空位才放，山洞頁也進得去）。
	// 連收蜂蜜都放不下時，蜂箱按鈕直接改成收蜂蜜（收完蜂蜜歸零，下次就會變回蜂箱入口）。
	hive := s.Beehive
	hiveOpen := s.Level >= beehive.UnlockLevel || hive != nil
	honey := hive != nil && hive.Honey > 0
	collectHoney := func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("collect_honey", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().CollectHoney(ctx, p.playerID)
		}), actOpts{})
		return nil
	}
	honeyLabel := ""
	if honey {
		honeyLabel = p.f("收蜂蜜（%d 罐）", hive.Honey)
	}
	navs := 3 + btoi(hiveOpen) // 菇房、實驗室、擺設、蜂箱；回總覽另外算
	honeyAlone := honey && p.freeButtonSlots() >= navs+2
	if honeyAlone {
		p.button(honeyLabel, collectHoney, btn{emoji: "🍯", style: ui.Success, row: 1})
	}
	p.nav(p.t("ui.btn.cellar"), "cellar", btn{emoji: "🍄", row: 4, style: ui.Primary})
	p.nav(p.t("ui.btn.lab"), "lab", btn{emoji: "🧪", row: 4, style: ui.Primary})
	switch {
	case honey && !honeyAlone:
		p.button(honeyLabel, collectHoney, btn{emoji: "🍯", style: ui.Success, row: 4})
	case hiveOpen:
		p.nav(p.t("ui.more.beehive"), "beehive", btn{emoji: "🐝", row: 4, style: ui.Primary})
	}
	p.nav(p.t("ui.btn.decor"), "decor", btn{emoji: "🎨", row: 4})
	p.navBack(4)
	if bp.SeedMaker && p.freeButtonSlots() > 0 {
		p.navFirstPage(p.t("ui.btn.seed_maker"), "seedmaker", &p.seedMakerPage, btn{emoji: "🌱", row: 4, style: ui.Primary})
	}
	return e, nil
}

// ======================================================================
// 背包
// ======================================================================

type backpackData struct {
	backpack services.Backpack
	fish     services.FishBasket
	seller   services.AutoSellerView // 自動販售機的販售項目（沒有自動販售機時 Owned 為 false）
}

func (p *Panel) loadBackpack(ctx context.Context) (any, error) {
	bp, err := p.svc().GetBackpack(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	fish, err := p.svc().FishBasket(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	d := backpackData{backpack: bp, fish: fish}
	for _, t := range bp.Tools {
		if t.Code == balance.ToolAutoSeller && t.Owned {
			d.seller, err = p.svc().AutoSeller(ctx, p.playerID)
		}
	}
	return d, err
}

func (p *Panel) buildBackpack(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(backpackData)
	bp, basket := d.backpack, d.fish
	var tools, effects, seeds, produce, goods, consumables, prizes, materials, fish []string
	for _, t := range bp.Tools {
		switch {
		case !t.Owned:
			tools = append(tools, p.f("🔒 %s（未購買）", p.s(t.Name)))
		case t.Enabled:
			tools = append(tools, p.f("✅ %s（啟用中）", p.s(t.Name)))
		default:
			tools = append(tools, p.f("⏸️ %s（已停用）", p.s(t.Name)))
		}
	}
	for _, x := range bp.Effects {
		if x.Status == "active" {
			effects = append(effects, p.f("🪱 %s：%s結束", p.s(x.Name), ts(x.ExpiresAt)))
		}
	}
	for _, x := range bp.Seeds {
		line := p.f("%s %s　×**%d**", cropEmoji(x.Code), p.s(x.Name), x.Quantity)
		seeds = append(seeds, line)
	}
	for _, x := range bp.Produce {
		produce = append(produce, p.f("%s %s%s　×**%d**　（%s 金／個）", cropEmoji(x.Code), p.s(x.Name),
			services.GeneticsLabel(x.Quality, x.Mutation), x.Quantity, money(x.Price)))
	}
	for _, x := range bp.Goods {
		goods = append(goods, p.f("🥚 %s　×**%d**　（%s 金／個）", p.s(x.Name), x.Quantity, money(x.Price)))
	}
	for _, x := range bp.Consumables {
		consumables = append(consumables, p.f("%s %s　×**%d**", lookupOr(ItemEmoji, x.Code, "🎁"), p.s(x.Name), x.Quantity))
	}
	for _, x := range bp.Prizes {
		prizes = append(prizes, p.f("🏆 %s　×**%d**　（價值 ×%d，%s 金／個）", p.s(x.Name), x.Quantity, x.Multiplier, money(x.UnitPrice)))
	}
	for _, x := range bp.Materials {
		materials = append(materials, p.f("%s %s　×**%d**　（%s）", lookupOr(MaterialEmoji, x.Code, "🪨"), p.s(x.Name), x.Quantity, p.s(x.Description)))
	}
	for _, f := range basket.Fish[:min(8, len(basket.Fish))] {
		fish = append(fish, fishLine(f, 0))
	}
	if len(basket.Fish) > 8 {
		fish = append(fish, p.f("…還有 %d 隻，到「🎣 魚簍」查看", len(basket.Fish)-8))
	}
	// 三種容量集中在第一行：倉庫（農產品、動物產品…）、素材背包（素材、種子與消耗型道具）、魚簍
	e := &ui.Embed{Title: p.s("🎒 ✦ 背包 ✦"), Color: Blue, Description: sections(
		p.f("📦 倉庫 **%d/%d**　⛰️ 素材背包 **%d/%d**（含種子、道具）　🎣 魚簍 **%d/%d**\n%s",
			bp.Used, bp.Capacity, bp.MaterialUsed, bp.MaterialCapacity, len(basket.Fish), basket.Capacity, bar(bp.Used, bp.Capacity, 16)),
		Section{Title: p.s("🌰 種子"), Lines: seeds},
		Section{Title: p.s("🧺 農產品"), Lines: produce},
		Section{Title: p.s("🥚 動物產品與加工品"), Lines: goods},
		Section{Title: p.s("🧪 道具"), Lines: consumables},
		Section{Title: p.s("🏆 賭場獎品"), Lines: prizes},
		Section{Title: p.s("⛰️ 素材"), Lines: materials},
		Section{Title: p.s("🔧 工具（按下方按鈕切換開關）"), Lines: tools},
		Section{Title: p.s("✨ 生效中的效果"), Lines: effects},
		Section{Title: p.f("🎣 魚簍（全部價值 %s 金）", money(basket.TotalValue)), Lines: fish},
	)}
	// 工具開關：按鈕顯示「目前狀態」，開＝綠色 ✅、關＝灰色 ⏸️，按下切換
	for _, t := range bp.Tools {
		if !t.Owned {
			continue
		}
		label, emoji, style := p.s(t.Name)+p.s("：關"), "⏸️", ui.Secondary
		if t.Enabled {
			label, emoji, style = p.s(t.Name)+p.s("：開"), "✅", ui.Success
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("set_tool_enabled", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetToolEnabled(ctx, p.playerID, t.Code, !t.Enabled)
			}), actOpts{})
			return nil
		}, btn{emoji: emoji, style: style, row: 0})
	}
	p.nav(p.s("前往販售"), "sell", btn{emoji: "💰", row: 1})
	p.nav(p.s("商店"), "shop", btn{emoji: "🏪", row: 1})
	p.nav(p.s("魚簍"), "fish_basket", btn{emoji: "🎣", row: 1})
	p.nav(p.s("增益道具"), "boosts", btn{emoji: "✨", row: 1, style: ui.Primary})
	if coins := lineQty(bp.Materials, exploration.FakeCoin); coins > 0 {
		p.button(p.f("兌換假幣（持有 %d）", coins), func(ctx context.Context, it ui.Interaction) error {
			p.picker = &Picker{Key: "fake_coin", Label: p.f("💱 兌換假幣（1 個 = %s 金）", money(exploration.FakeCoinRate())),
				Verb: p.s("換"), Max: coins, Presets: []int{1, 5, 10},
				Run: p.callQty("exchange_fake_coins", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().ExchangeFakeCoins(ctx, p.playerID, q)
				})}
			p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
			return nil
		}, btn{emoji: "💱", row: 1, style: ui.Success})
		if p.picker != nil && p.picker.Key == "fake_coin" {
			p.picker.Max = coins
			e.AddField(p.s("💱 兌換假幣"), p.f("持有 **%d** 個，1 個 = **%s** 金\n👇 選擇兌換數量", coins, money(exploration.FakeCoinRate())), false)
			p.quantityButtons(2)
		}
	}
	if p.picker == nil || p.picker.Key != "fake_coin" { // 第 3 列：兌換假幣的數量按鈕和用途查詢選單共用，兌換時先收起選單
		p.usageControls(e, bp, 2)
	}
	if sv := d.seller; sv.Owned {
		// 自動販售機（2026-11-22 販售項目與保留條件移到獨立的設定頁，背包頁只留摘要）
		e.AddField(p.s("🏧 自動販售機"), p.autoSellerSummary(sv), false)
		p.nav(p.s("自動販售機設定"), "auto_seller", btn{emoji: "🏧", row: 3})
	}
	p.navBack(4)
	return e, nil
}

// ======================================================================
// 說明
// ======================================================================

type helpData struct {
	name       string
	gold       int
	renameCost int
}

func (p *Panel) loadHelp(ctx context.Context) (any, error) {
	st, err := p.svc().GetFarmStatus(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	cost, err := p.svc().RenameCost(ctx, p.playerID)
	return helpData{p.s(st.Name), st.Gold, cost}, err
}

// buildHelp 說明頁：用下拉選單選主題，一次只顯示一個主題（不再一次列出整面文字）；沒選主題時顯示主題清單與改名。
func (p *Panel) buildHelp(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(helpData)
	full := HelpEmbed()
	e := &ui.Embed{Title: full.Title, Color: full.Color, Description: p.s(full.Description)}
	topic := -1
	if i, err := strconv.Atoi(p.helpTopic); err == nil && i >= 0 && i < len(full.Fields) {
		topic = i
	}
	cost := d.renameCost
	if topic >= 0 {
		f := full.Fields[topic]
		e.AddField(p.s(f.Name), f.Value, false) // 選取的主題放在最下方，貼近選單
	} else {
		names := make([]string, len(full.Fields))
		for i, f := range full.Fields {
			names[i] = p.s(f.Name)
		}
		e.AddField(p.s("🏡 農場名稱"), p.f("目前：**%s**　這次改名 %s（你有 %s 金幣）\n改名費逐次加價，%d 天沒改名會降一級",
			d.name, renameCostText(cost), money(d.gold), expansion.Current().RenameDecayDays), false)
		e.AddField(p.s("📚 主題"), strings.Join(names, p.s("　"))+p.s("\n👇 從下方選單選一個主題"), false)
	}
	options := make([]option, len(full.Fields))
	for i, f := range full.Fields {
		options[i] = option{p.s(f.Name), strconv.Itoa(i), "", ""}
	}
	p.selectMenu(p.s("📚 選擇說明主題…"), options, func(ctx context.Context, it ui.Interaction, value string) error {
		p.helpTopic = value
		p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
		return nil
	}, 1, p.helpTopic)
	if topic >= 0 {
		p.button(p.s("主題清單"), func(ctx context.Context, it ui.Interaction) error {
			p.helpTopic = ""
			p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
			return nil
		}, btn{emoji: "📚", row: 0})
	}
	p.button(p.f("更改農場名稱（%s）", renameCostText(cost)), func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, renameModal(d.name, cost, func(ctx context.Context, mit ui.Interaction, name string) {
			p.act(ctx, mit, p.call("rename_farm", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().RenameFarm(ctx, p.playerID, name)
			}), actOpts{})
		}))
	}, btn{emoji: "✏️", row: 0, disabled: d.gold < cost})
	p.navBack(0)
	return e, nil
}

// HelpEmbed 玩法說明（/help 與說明頁共用）：每個主題用短條列，寫出完整的進入路徑。
func HelpEmbed() *ui.Embed {
	e := &ui.Embed{Title: "❓ 怎麼玩", Color: Blue, Description: "輸入 **/farm** 打開農場面板，所有操作都用按鈕和選單完成，面板只有你看得到；15 分鐘沒有操作會逾時，再輸入 /farm 就能重新打開。"}
	e.AddField("🌱 種田", fmt.Sprintf("• 「🌱 農田」選作物 → 種滿空地（種子不夠會自動買）\n"+
		"• 「💧 全部澆水」→ 等成熟 →「🧺 全部收成」→「💰 販售」\n"+
		"• 成熟後 16 小時沒收成會乾枯；農田最多 %d 格（第 17 格起是第二塊農田，畫在農田圖下方）\n"+
		"• 加速：化肥剩餘時間 -%d%%、蚯蚓生長時間 ×%v、溫室（商店）每級再縮短；自動澆水器種下就澆水",
		expansion.MaxPlots(), balance.FertilizerPct, balance.EarthwormGrowMultiplier), false)
	e.AddField("🐔 養殖與加工", "• 「🐔 動物」先建造畜舍 → 買動物 → 餵食 → 等生產後收集\n"+
		"• 「🏭 加工」用磨坊、烘焙坊、果醬工坊把作物做成更值錢的商品\n"+
		"• 「🐔 動物」→「🐾 寵物」領養：貓咪多收成、狗狗多擋偷菜、兔子多收菇、小鳥釣到好魚、刺蝟讓品質升級\n"+
		"• 每種寵物的領養價格與每級加成不同，到「🐾 寵物」頁查看", false)
	e.AddField("🥷 偷菜與防守", fmt.Sprintf("• 「🥷 偷菜」選一位同伺服器玩家 → 打開他的農田 → 選要偷哪一格\n"+
		"• 每天每位玩家最多偷 %d 格、最多試 %d 次（被防守設施擋下也算）；每人每天最多被 %d 人偷；每次最多偷 %d 個（小麥 1 個）\n"+
		"• 商店的「傳說扒手手套」不受每日限制\n"+
		"• 「🛡️ 防守」買稻草人、警戒犬、防盜圍欄擋小偷；巡查券可以查是誰偷的",
		balance.StealPlotsPerVictimPerDay, balance.StealAttemptsPerVictimPerDay, balance.StealThievesPerVictimPerDay, balance.StealMaxPerAction), false)
	e.AddField("🍄 地下菇房", fmt.Sprintf("• 農場 Lv.%d 開放：「🌱 農田」→「🍄 地下菇房」，不用建造\n"+
		"• 長出什麼菇由上方農田種的作物決定；不用種、不用澆水，約 %s長好一批\n"+
		"• 投入🪵木屑（山洞帶回）提高稀有菇機率，投滿 %d 個保證一半以上是稀有菇\n"+
		"• 菇可以直接賣，或在烘焙坊做成料理",
		mushroom.Unlock(), mushroom.GrowHoursText(), mushroom.MaxSawdust), false)
	e.AddField("⛰️ 山洞冒險", caveHelpText(), false)
	e.AddField("🎣 釣魚", "• 「🎣 釣魚」→「🛒 釣具商店」買魚竿、魚線、魚餌 →「開始釣魚」\n"+
		"• 魚上鉤後在時限內按「拉竿」，再照最下面的 💬 提示收線／放線／等待\n"+
		"• 魚竄向一邊時要壓**同方向**；一直收線一定會斷線\n"+
		"• 每條魚要成功拉幾下：普通 2–4、稀有 3–5、史詩 4–6、傳說 5–7\n"+
		"• 釣到的魚放進魚簍，可以出售或「📢 分享到頻道」；偶爾有人機確認，答錯只會暫停", false)
	e.AddField("🐉 世界 Boss", "• 伺服器共同活動：「🐉 世界Boss」→「📦 捐獻物資」\n"+
		"• 捐農作物、漁獲、素材或加工品，補充糧食／醫療／戰鬥／裝備四種補給\n"+
		"• 補給越充足，前線打 Boss 越快；結束後「🎁 領取獎勵」", false)
	e.AddField("🏅 簽到、成就與圖鑑", "• 每天（台灣時間）第一次打開面板自動簽到，7 天一輪，第 7 天獎勵最多，中斷就重算\n"+
		"• 「🏅 成就圖鑑」看簽到紀錄、領成就獎勵、選稱號、看各種圖鑑\n"+
		"• 稱號會顯示在總覽、排行榜與世界 Boss 排行", false)
	e.AddField("👥 群組", "• 每個 Discord 伺服器是一個群組；農場屬於建立時的伺服器\n"+
		"• 只有在所屬群組的操作才計入群組任務與競賽\n"+
		"• 「👥 群組」可以把農場轉移到目前的伺服器（每 7 天一次）", false)
	e.AddField("🏆 排行與獎勵", "• 「🏆 排行榜」有個人、本伺服器、群組總排行\n"+
		"• 可以切換金幣、農產品販售、商品總販售與期間；排名獎勵自動發放", false)
	e.AddField("🎵 聽音樂", "• 先進入語音頻道，再用 **/play** 點歌（關鍵字、YouTube／Spotify 連結）\n"+
		"• /nowplaying 控制面板、/queue 看清單；完整說明用 **/music**", false)
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
	return fmt.Sprintf("• 「⛰️ 山洞」探索%s，分別要 %s 小時\n"+
		"• 一次只能去一個區域，回來領取獎勵後才能再出發\n"+
		"• 帶回素材、🪵木屑、稀有種子與假幣（「🎒 背包」兌換，1 個 = %s 金）\n"+
		"• 「⛰️ 山洞」→「🔧 機器」用素材製作種子製造機、自動收割機、自動加工機、稀有作物培育器",
		strings.Join(names, "、"), strings.Join(hours, "／"), money(exploration.FakeCoinRate()))
}

// weatherFooter 今日天氣、後兩天的預報與今天的天氣奇遇（2026-11-20）。
func (p *Panel) weatherFooter(now time.Time) string {
	days := weather.Forecast(now, 3)
	w := days[0]
	lines := []string{p.t("ui.main.weather", "emoji", w.Emoji, "name", weatherName(p.lang, w), "effect", weatherEffect(p.lang, w))}
	sep, list := "｜", "、"
	if p.lang != i18n.Default {
		sep, list = " | ", ", "
	}
	parts := make([]string, 0, len(days)-1)
	for i, k := range days[1:] {
		parts = append(parts, p.t(fmt.Sprintf("weather.forecast_day%d", i+1))+" "+k.Emoji+" "+weatherName(p.lang, k))
	}
	forecast := p.t("weather.forecast", "days", strings.Join(parts, sep))
	if lucks := weather.LucksOn(now, 1); len(lucks) > 0 {
		names := make([]string, len(lucks))
		for i, c := range lucks {
			names[i] = p.t("weather.luck_name_" + c)
		}
		forecast += p.t("weather.forecast_luck", "day", p.t("weather.forecast_day1"), "lucks", strings.Join(names, list))
	}
	lines = append(lines, forecast)
	for _, c := range weather.LucksOn(now, 0) {
		switch c {
		case weather.LuckDrought:
			lines = append(lines, p.t("weather.luck_drought"))
		case weather.LuckDrizzle:
			lines = append(lines, p.t("weather.luck_drizzle", "n", weather.DrizzleExtraYield, "pct", weather.DrizzleRarePct))
		case weather.LuckRainbow:
			lines = append(lines, p.t("weather.luck_rainbow"))
		}
	}
	return strings.Join(lines, "\n")
}

// weatherName 天氣名稱（繁中用天氣表的名稱）。
func weatherName(lang string, w weather.Kind) string {
	return i18n.Name(lang, "weather."+w.Code, w.Name)
}

// weatherEffect 天氣效果說明（繁中沿用 EffectText，數字一樣從天氣表來）。
func weatherEffect(lang string, w weather.Kind) string {
	if lang == i18n.Default {
		return w.EffectText()
	}
	switch {
	case w.GrowReductionPct > 0:
		return i18n.T(lang, "weather.effect_sunny", "pct", w.GrowReductionPct)
	case w.FreeWater > 0:
		return i18n.T(lang, "weather.effect_rainy")
	case w.HarvestLoss > 0:
		return i18n.T(lang, "weather.effect_storm", "n", w.HarvestLoss, "block", weather.StormDefenseBlock)
	}
	return i18n.T(lang, "weather.effect_cloudy")
}
