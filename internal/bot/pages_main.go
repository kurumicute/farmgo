package bot

// 面板頁面：歡迎（尚未建立農場）、總覽、農田、背包、說明（對應 Python panels.py 的同名區段與 help_embed）。

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/config"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
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
		"lab_clone":       {p.loadLab, p.buildLabClone},
		"decor":           {p.loadDecor, p.buildDecor},
		"lab_records":     {p.loadLabRecords, p.buildLabRecords},
		"lab_help":        {func(context.Context) (any, error) { return nil, nil }, p.buildLabHelp},
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
			p.showError(ctx, it, cleanMessage(PlayerMessage(err)))
			return
		}
		// 2026-11-16：直接到農田頁後畫面上沒有農場名稱，建立成功的提示寫出名稱（自訂名稱時玩家才知道有沒有設定成功）
		p.refresh(ctx, it, fmt.Sprintf("🎉 農場建立完成：「%s」！👇 從下方選單選一種作物，會種滿所有空地（種子不夠會自動買）。",
			escapeMD(created.Name)), nil, "重繪", time.Time{})
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

// farmNextStep 農田的下一步（純文字，放在 footer；footer 不支援粗體與 Discord 時間戳）：
// 可收成、待澆水、乾枯、空地與下一批成熟時間。plotsPage=true 時空地提示指向下方的種植選單。
func (p *Panel) farmNextStep(plots []services.PlotInfo, plotsPage bool) string {
	var parts []string
	if n := countState(plots, rules.PlotReady); n > 0 {
		parts = append(parts, fmt.Sprintf("✅ %d 格可收成", n))
	}
	if n := countState(plots, rules.PlotWaitingWater); n > 0 {
		parts = append(parts, fmt.Sprintf("💧 %d 格待澆水", n))
	}
	if n := countState(plots, rules.PlotWithered); n > 0 {
		parts = append(parts, fmt.Sprintf("🥀 %d 格乾枯", n))
	}
	if n := countState(plots, rules.PlotEmpty); n > 0 {
		if plotsPage {
			parts = append(parts, fmt.Sprintf("⬜ %d 格空地，從下方選單選作物", n))
		} else {
			parts = append(parts, fmt.Sprintf("⬜ %d 格空地", n))
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
			parts = append(parts, "⏰ 下一批約 "+services.LongDuration(left)+"後成熟")
		}
	}
	if len(parts) == 0 {
		return "👍 一切就緒"
	}
	return strings.Join(parts, "｜")
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
	// 待辦放在最後一個欄位，緊貼下方按鈕（玩家遊玩時視線停在按鈕區）
	if d.news != nil && !d.news.Empty {
		e.AddField(fmt.Sprintf("📰 昨日農場日報（%s）", d.home.Name), d.news.Text, false)
	}
	e.AddField("📋 待辦", todo, false)
	e.Footer = p.farmNextStep(s.Plots, false) // 大圖排在所有欄位之後，緊貼按鈕的只有 footer：放待辦摘要

	p.button("全部澆水", p.waterAll, btn{emoji: "💧", style: ui.Primary, row: 0, disabled: waiting == 0})
	p.button("全部收成", p.harvestAll, btn{emoji: "🧺", style: ui.Success, row: 0, disabled: ready == 0})
	p.button("重新整理", func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "main", "")
		return nil
	}, btn{emoji: "🔄", row: 0})
	p.button("分享到頻道", p.share, btn{emoji: "📢", row: 0, disabled: p.groupID == nil})
	p.button("說明", func(ctx context.Context, it ui.Interaction) error {
		p.helpTopic = "" // 從總覽進來先看主題清單
		p.goTo(ctx, it, "help", "")
		return nil
	}, btn{emoji: "❓", row: 0})
	// 2026-11-16 總覽只留兩列按鈕：第 1 列操作、第 2 列最常用的入口；其他功能收進「更多功能」選單
	// （原本 21 顆按鈕排滿 5 列，再加入口就沒有空位）。顏色規則：綠色＝主要操作、藍色＝次要操作，入口一律灰色，紅色只用在確認破壞性操作。
	p.nav("農田", "plots", btn{emoji: "🌱", row: 1})
	p.nav("背包", "backpack", btn{emoji: "🎒", row: 1})
	p.nav("商店", "shop", btn{emoji: "🏪", row: 1})
	p.nav("販售", "sell", btn{emoji: "💰", row: 1})
	p.nav("動物", "animals", btn{emoji: "🐔", row: 1})
	p.moreMenu(2)
	p.recentButtons(3)
	return e, nil
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
		p.button(m.label, func(ctx context.Context, it ui.Interaction) error {
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
var moreEntries = []struct{ page, label, desc, emoji string }{
	{"craft", "加工", "磨坊、烘焙坊、果醬工坊", "🏭"},
	{"steal", "偷菜", "偷同伺服器玩家成熟的作物", "🥷"},
	{"defense", "防守", "稻草人、警戒犬、巡查券", "🛡️"},
	{"cave", "山洞", "探險收集素材、機器", "⛰️"},
	{"fishing", "釣魚", "釣魚中心、魚簍、釣具商店", "🎣"},
	{"casino", "賭場", "用作物比大小", "🎰"},
	{"boss", "世界Boss", "伺服器共同活動、捐獻物資", "🐉"},
	{"board", "排行榜", "個人、伺服器、群組排行", "🏆"},
	{"quests", "任務競賽", "群組任務與競賽", "📜"},
	{"group", "群組", "所屬群組、轉移農場", "👥"},
	{"progress", "成就圖鑑", "成就、稱號、簽到、圖鑑", "🏅"},
	{"prices", "作物價格", "今天的市場價格", "📈"},
	{"log", "紀錄", "最近的操作紀錄", "🧾"},
}

// moreMenu 「更多功能」選單：選了就換頁（作物價格、紀錄從第 1 頁開始）。
func (p *Panel) moreMenu(row int) {
	options := make([]option, len(moreEntries))
	for i, m := range moreEntries {
		options[i] = option{m.label, m.page, m.desc, m.emoji}
	}
	p.selectMenu("🧭 更多功能…", options, func(ctx context.Context, it ui.Interaction, page string) error {
		if !slices.ContainsFunc(moreEntries, func(m struct{ page, label, desc, emoji string }) bool { return m.page == page }) {
			return gameerr.NotFoundf("找不到這個頁面。")
		}
		p.openMore(ctx, it, page)
		return nil
	}, row, "")
}

// shareCooldown 公開分享（農場、魚卡）的冷卻時間，避免連按洗版。
const shareCooldown = 30 * time.Second

// shareWait 還在冷卻時重繪面板提示並回傳 true；否則記下這次分享的時間並回傳 false。
func (p *Panel) shareWait(ctx context.Context, it ui.Interaction) bool {
	if left := shareCooldown - time.Since(p.lastShareAt); !p.lastShareAt.IsZero() && left > 0 {
		p.refresh(ctx, it, fmt.Sprintf("⚠️ 剛剛才分享過，請 %d 秒後再分享。", int(left.Seconds())+1), nil, "重繪", time.Time{})
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
		_, _ = it.Followup(ctx, ui.Message{Content: ptr("❌ " + PlayerMessage(err)), Ephemeral: true})
		return nil
	}
	lines := make([]string, len(status.Plots))
	for i, pl := range status.Plots {
		lines[i] = plotLine(pl, false) // 公開訊息：不顯示「請清除」這類給主人看的提示
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
	notice := "📢 已分享到這個頻道！"
	if _, err := it.Followup(ctx, msg); err != nil {
		slog.Warn("分享農場失敗", "type", fmt.Sprintf("%T", err))
		p.lastShareAt = time.Time{}
		notice = "⚠️ 訊息沒有送出（Bot 可能沒有這個頻道的發言權限）。"
	}
	p.refresh(ctx, it, notice, nil, "重繪", time.Time{})
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
	), Footer: p.farmNextStep(s.Plots, true)} // 下一步放 footer：大圖排在欄位之後，footer 才是緊貼按鈕的位置
	p.attachImage(e, p.farmImage(d.status)) // 圖片在交易外產生
	if empty := countState(s.Plots, rules.PlotEmpty); empty > 0 {
		var options []option
		for _, c := range d.crops {
			if !(c.ShopBuyable || seeds[c.Code] > 0) || !c.InSeason { // 不在季節的不列出
				continue
			}
			label := fmt.Sprintf("%s（種子 %s 金，持有 %d）", c.Name, money(c.SeedPrice), seeds[c.Code])
			if !c.ShopBuyable {
				label = fmt.Sprintf("%s（稀有種子，持有 %d）", c.Name, seeds[c.Code])
			}
			options = append(options, option{label, c.Code,
				fmt.Sprintf("⏱️ %s｜🧺 收成 ×%d %s｜🏷️ %s 金／個", services.LongDuration(c.GrowSeconds), c.HarvestQuantity, c.Name, money(c.SellPrice)),
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
		// 2026-11-15 起可以複選：勾選要施肥的農田（最多持有的化肥數量），選完直接施肥
		growing = growing[:min(len(growing), ui.MaxOptions)]
		multi := &ui.Select{Placeholder: fmt.Sprintf("🧪 勾選要施化肥的農田（持有 %d，剩餘時間 -%d%%，可複選）…", fert, balance.FertilizerPct),
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
			desc := fmt.Sprintf("剩餘時間 -%d%%", balance.FertilizerPct)
			if pl.RemainingSeconds > 0 {
				desc += "，目前還要 " + rules.FormatDuration(pl.RemainingSeconds)
			}
			multi.Options = append(multi.Options, ui.SelectOption{Label: cut(fmt.Sprintf("#%d %s", pl.PlotNumber, deref(pl.CropName)), 100),
				Value: fmt.Sprint(pl.PlotNumber), Emoji: "🧪", Description: cut(desc, 100)})
		}
		p.add(multi)
	}
	if worms := lineQty(bp.Consumables, balance.ItemEarthworm); worms > 0 {
		// 第 3 列讓給特殊種子選單（2026-11-15）
		p.button(fmt.Sprintf("使用蚯蚓（持有 %d）", worms), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("use_earthworm", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().UseEarthworm(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🪱", row: 1})
	}
	if empty := countState(s.Plots, rules.PlotEmpty); empty > 0 && len(d.labSeeds) > 0 {
		// 種子實驗室的特殊種子（2026-11-15）：勾選要種的種子，每顆依序種到一格空地；沒有可種的特殊種子時不顯示
		// 2026-11-16 超過 25 顆時選單內翻頁（第一項「上一頁」、最後一項「下一頁」），以前只列前 25 顆，後面的種子種不到
		// 2026-11-17 種子有好幾種作物時先選作物（同一列），再勾選這種作物的種子；同作物排在一起
		codes, _, _ := seedCrops(d.labSeeds)
		seeds := filterSeeds(sortSeedsByCrop(d.labSeeds), p.plotSeedCrop)
		if len(seeds) == 0 {
			p.plotSeedCrop, seeds = "", sortSeedsByCrop(d.labSeeds)
		}
		if len(codes) > 1 && p.plotSeedCrop == "" {
			opts, page, pages := pagedOptions(cropFilterOptions(d.labSeeds, "所有作物的特殊種子一起列出"), p.plotSeedPage)
			p.plotSeedPage = page
			p.selectMenu(fmt.Sprintf("🧬 種特殊種子：先選作物（共 %d 顆，空地 %d 格）…", len(d.labSeeds), empty), opts,
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
				all = append(all, option{"🔎 改選其他作物", cropFilterPrefix, "選這項回到作物清單", "🔙"})
			}
			for _, sv := range seeds {
				all = append(all, option{seedOptionLabel(sv), fmt.Sprint(sv.ID), seedOptionDesc(sv), cropEmoji(sv.CropCode)})
			}
			opts, page, pages := pagedOptions(all, p.plotSeedPage)
			p.plotSeedPage = page
			holder := fmt.Sprintf("🧬 勾選特殊種子，每顆種到 1 格空地（空地 %d 格，可複選）…", empty)
			if pages > 1 {
				holder = fmt.Sprintf("🧬 勾選特殊種子種到空地（空地 %d 格，第 %d/%d 頁，選單頭尾可翻頁）…", empty, page+1, pages)
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
							p.plotSeedCrop, p.plotSeedPage = "", 0
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
	p.nav("地下菇房", "cellar", btn{emoji: "🍄", row: 4, style: ui.Primary})
	p.nav("種子實驗室", "lab", btn{emoji: "🧪", row: 4, style: ui.Primary})
	p.nav("庭院擺設", "decor", btn{emoji: "🎨", row: 4})
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
		produce = append(produce, fmt.Sprintf("%s %s%s　×**%d**　（%s 金／個）", cropEmoji(x.Code), x.Name,
			services.GeneticsLabel(x.Quality, x.Mutation), x.Quantity, money(x.Price)))
	}
	for _, x := range bp.Goods {
		goods = append(goods, fmt.Sprintf("🥚 %s　×**%d**　（%s 金／個）", x.Name, x.Quantity, money(x.Price)))
	}
	for _, x := range bp.Consumables {
		consumables = append(consumables, fmt.Sprintf("%s %s　×**%d**", lookupOr(ItemEmoji, x.Code, "🎁"), x.Name, x.Quantity))
	}
	for _, x := range bp.Prizes {
		prizes = append(prizes, fmt.Sprintf("🏆 %s　×**%d**　（價值 ×%d，%s 金／個）", x.Name, x.Quantity, x.Multiplier, money(x.UnitPrice)))
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
	// 三種容量集中在第一行：倉庫（農產品、動物產品、道具…）、素材背包（素材與種子）、魚簍
	e := &ui.Embed{Title: "🎒 ✦ 背包 ✦", Color: Blue, Description: sections(
		fmt.Sprintf("📦 倉庫 **%d/%d**　⛰️ 素材背包 **%d/%d**（含種子）　🎣 魚簍 **%d/%d**\n%s",
			bp.Used, bp.Capacity, bp.MaterialUsed, bp.MaterialCapacity, len(basket.Fish), basket.Capacity, bar(bp.Used, bp.Capacity, 16)),
		Section{Title: "🌰 種子", Lines: seeds},
		Section{Title: "🧺 農產品", Lines: produce},
		Section{Title: "🥚 動物產品與加工品", Lines: goods},
		Section{Title: "🧪 道具", Lines: consumables},
		Section{Title: "🏆 賭場獎品", Lines: prizes},
		Section{Title: "⛰️ 素材", Lines: materials},
		Section{Title: "🔧 工具（按下方按鈕切換開關）", Lines: tools},
		Section{Title: "✨ 生效中的效果", Lines: effects},
		Section{Title: fmt.Sprintf("🎣 魚簍（全部價值 %s 金）", money(basket.TotalValue)), Lines: fish},
	)}
	// 工具開關：按鈕顯示「目前狀態」，開＝綠色 ✅、關＝灰色 ⏸️，按下切換
	for _, t := range bp.Tools {
		if !t.Owned {
			continue
		}
		label, emoji, style := t.Name+"：關", "⏸️", ui.Secondary
		if t.Enabled {
			label, emoji, style = t.Name+"：開", "✅", ui.Success
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("set_tool_enabled", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetToolEnabled(ctx, p.playerID, t.Code, !t.Enabled)
			}), actOpts{})
			return nil
		}, btn{emoji: emoji, style: style, row: 0})
	}
	p.nav("前往販售", "sell", btn{emoji: "💰", row: 1})
	p.nav("商店", "shop", btn{emoji: "🏪", row: 1})
	p.nav("魚簍", "fish_basket", btn{emoji: "🎣", row: 1})
	if coins := lineQty(bp.Materials, exploration.FakeCoin); coins > 0 {
		p.button(fmt.Sprintf("兌換假幣（持有 %d）", coins), func(ctx context.Context, it ui.Interaction) error {
			p.picker = &Picker{Key: "fake_coin", Label: fmt.Sprintf("💱 兌換假幣（1 個 = %s 金）", money(exploration.FakeCoinRate())),
				Verb: "換", Max: coins, Presets: []int{1, 5, 10},
				Run: p.callQty("exchange_fake_coins", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().ExchangeFakeCoins(ctx, p.playerID, q)
				})}
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "💱", row: 1, style: ui.Success})
		if p.picker != nil && p.picker.Key == "fake_coin" {
			p.picker.Max = coins
			e.AddField("💱 兌換假幣", fmt.Sprintf("持有 **%d** 個，1 個 = **%s** 金\n👇 選擇兌換數量", coins, money(exploration.FakeCoinRate())), false)
			p.quantityButtons(2)
		}
	}
	if sv := d.seller; sv.Owned {
		// 自動販售機的販售項目（2026-11-15）：勾選要自動賣的東西，沒勾的作物收成後放進倉庫、魚放進魚簍
		var picked []string
		for _, o := range sv.Options {
			if o.Selected {
				picked = append(picked, o.Name)
			}
		}
		text := "全部都賣"
		if !sv.All {
			text = "只賣 " + strings.Join(picked, "、")
		}
		if !sv.Enabled {
			text += "（目前停用）"
		}
		e.AddField("🏧 自動販售機販售項目", runes(text, 1000)+"\n👇 用下方選單勾選要自動賣的東西", false)
		opts := sv.Options[:min(len(sv.Options), ui.MaxOptions)]
		multi := &ui.Select{Placeholder: "🏧 勾選自動販售機要賣的東西（可複選）…", Row: 3, MinValues: 1, MaxValues: len(opts),
			OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
				p.act(ctx, it, p.call("set_auto_seller_items", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().SetAutoSellerItems(ctx, p.playerID, values)
				}), actOpts{})
				return nil
			}}
		for _, o := range opts {
			emoji := cropEmoji(o.Code)
			switch o.Code {
			case services.AutoSellHybrid:
				emoji = "🧬"
			case services.AutoSellFish:
				emoji = "🐟"
			}
			multi.Options = append(multi.Options, ui.SelectOption{Label: o.Name, Value: o.Code, Emoji: emoji, Default: o.Selected})
		}
		p.add(multi)
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

// buildHelp 說明頁：用下拉選單選主題，一次只顯示一個主題（不再一次列出整面文字）；沒選主題時顯示主題清單與改名。
func (p *Panel) buildHelp(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(helpData)
	full := HelpEmbed()
	e := &ui.Embed{Title: full.Title, Color: full.Color, Description: full.Description}
	topic := -1
	if i, err := strconv.Atoi(p.helpTopic); err == nil && i >= 0 && i < len(full.Fields) {
		topic = i
	}
	cost := balance.RenameFarmCost
	if topic >= 0 {
		f := full.Fields[topic]
		e.AddField(f.Name, f.Value, false) // 選取的主題放在最下方，貼近選單
	} else {
		names := make([]string, len(full.Fields))
		for i, f := range full.Fields {
			names[i] = f.Name
		}
		e.AddField("🏡 農場名稱", fmt.Sprintf("目前：**%s**　改名每次 %s 金幣（你有 %s 金幣）", d.name, money(cost), money(d.gold)), false)
		e.AddField("📚 主題", strings.Join(names, "　")+"\n👇 從下方選單選一個主題", false)
	}
	options := make([]option, len(full.Fields))
	for i, f := range full.Fields {
		options[i] = option{f.Name, strconv.Itoa(i), "", ""}
	}
	p.selectMenu("📚 選擇說明主題…", options, func(ctx context.Context, it ui.Interaction, value string) error {
		p.helpTopic = value
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, 1, p.helpTopic)
	if topic >= 0 {
		p.button("主題清單", func(ctx context.Context, it ui.Interaction) error {
			p.helpTopic = ""
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "📚", row: 0})
	}
	p.button(fmt.Sprintf("更改農場名稱（%s 金幣）", money(cost)), func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, renameModal(d.name, func(ctx context.Context, mit ui.Interaction, name string) {
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
	maxLevel := pets.MaxLevel()
	e := &ui.Embed{Title: "❓ 怎麼玩", Color: Blue, Description: "輸入 **/farm** 打開農場面板，所有操作都用按鈕和選單完成，面板只有你看得到；15 分鐘沒有操作會逾時，再輸入 /farm 就能重新打開。"}
	e.AddField("🌱 種田", fmt.Sprintf("• 「🌱 農田」選作物 → 種滿空地（種子不夠會自動買）\n"+
		"• 「💧 全部澆水」→ 等成熟 →「🧺 全部收成」→「💰 販售」\n"+
		"• 成熟後 16 小時沒收成會乾枯；農田最多 %d 格\n"+
		"• 加速：化肥剩餘時間 -%d%%、蚯蚓生長時間 ×%v、溫室（商店）每級再縮短；自動澆水器種下就澆水",
		balance.MaxPlots, balance.FertilizerPct, balance.EarthwormGrowMultiplier), false)
	e.AddField("🐔 養殖與加工", fmt.Sprintf("• 「🐔 動物」先建造畜舍 → 買動物 → 餵食 → 等生產後收集\n"+
		"• 「🏭 加工」用磨坊、烘焙坊、果醬工坊把作物做成更值錢的商品\n"+
		"• 「🐔 動物」→「🐾 寵物」領養 %s 金：貓咪增加收成數量、狗狗多擋一次偷菜\n"+
		"• 寵物加成 Lv0 是 %d%%，每升一級 +%d%%，最高 Lv%d（%d%%）",
		money(pets.BuyPrice()), pets.BasePct(), pets.PctPerLevel(), maxLevel, pets.BonusPct(&maxLevel)), false)
	e.AddField("🥷 偷菜與防守", fmt.Sprintf("• 「🥷 偷菜」選一位同伺服器玩家 → 打開他的農田 → 選要偷哪一格\n"+
		"• 每天每位玩家最多偷 %d 格；每人每天最多被 %d 人偷；每次最多偷 %d 個（小麥 1 個）\n"+
		"• 商店的「傳說扒手手套」不受每日限制\n"+
		"• 「🛡️ 防守」買稻草人、警戒犬、防盜圍欄擋小偷；巡查券可以查是誰偷的",
		balance.StealPlotsPerVictimPerDay, balance.StealThievesPerVictimPerDay, balance.StealMaxPerAction), false)
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
