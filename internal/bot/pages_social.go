package bot

// 面板頁面：偷菜、排行榜與排名獎勵、任務與競賽、群組（對應 Python panels.py 的同名區段：_build_steal、
// _build_steal_farm、_build_board、_build_rewards、_build_quests、_build_competition、_build_history、_build_group，
// 以及 steal_footer）。排名獎勵與競賽三頁原本是 CMD 文字（leaderboard rewards、competition status／leaderboard／
// history），改寫成本檔的 Go 函式，輸出與 Python CMD 相同。

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/services"
)

// boardState 排行榜條件（Python self.board）。
type boardState struct{ scope, kind, period string }

var (
	scopeOrder  = []string{"global", "group", "groups"}
	kindOrder   = []string{"money", "sales", "goods"}
	periodOrder = []string{"all", "day", "week", "month"}
)

// tsv ts 的值版本。
func tsv(t time.Time, style ...string) string { return ts(&t, style...) }

// pageCount Python max(1, -(-n // size))。
func pageCount(n, size int) int { return max(1, (n+size-1)/size) }

// ======================================================================
// 偷菜
// ======================================================================

// stealFooter 偷菜頁底部的規則說明；有傳說扒手手套時不受每日土地與人數上限。
func stealFooter(glove bool) string {
	per := fmt.Sprintf("每株作物每人只能偷一次，每次最多 %d 個（小麥 1 個）；", balance.StealMaxPerAction)
	if glove {
		return per + "🧤 傳說扒手手套：不限人數與次數；對方可能有防守設施。"
	}
	return per + fmt.Sprintf("每天只能偷同一位玩家 %d 格田、每人每天最多被 %d 人偷；對方可能有防守設施。",
		balance.StealPlotsPerVictimPerDay, balance.StealThievesPerVictimPerDay)
}

func (p *Panel) requireGuildForSteal() error {
	if p.groupID == nil {
		return gameerr.Validationf("偷菜需要在 Discord 伺服器中使用（只能偷同一伺服器成員的作物）。")
	}
	return nil
}

type stealData struct {
	targets []services.StealTarget
	players []services.StealPlayer
	glove   bool
}

func (p *Panel) loadSteal(ctx context.Context) (any, error) {
	if err := p.requireGuildForSteal(); err != nil {
		return nil, err
	}
	svc := p.svc()
	var d stealData
	var err error
	if d.targets, err = svc.StealTargets(ctx, *p.groupID, p.playerID); err != nil {
		return nil, err
	}
	if d.players, err = svc.StealPlayers(ctx, *p.groupID, p.playerID); err != nil {
		return nil, err
	}
	d.glove, err = svc.ThiefGloveEnabled(ctx, p.playerID)
	return d, err
}

// stealAction value 為「格號|數量」。
func (p *Panel) stealAction(victim int64, value string) func(ctx context.Context) (string, error) {
	var plot, qty int
	_, perr := fmt.Sscanf(value, "%d|%d", &plot, &qty)
	return func(ctx context.Context) (string, error) {
		if perr != nil {
			return "", gameerr.Validationf("找不到這個項目。")
		}
		r, err := Call(ctx, p.cmds.Runner, "steal_crop", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().StealCrop(ctx, *p.groupID, p.playerID, victim, plot, qty)
		})
		return r.Message, err
	}
}

func (p *Panel) buildSteal(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(stealData)
	pages := pageCount(len(d.targets), ui.MaxOptions)
	p.stealPage = min(p.stealPage, pages-1)
	shown := d.targets[min(len(d.targets), p.stealPage*25):min(len(d.targets), (p.stealPage+1)*25)]
	var lines []string
	for _, t := range shown {
		lines = append(lines, fmt.Sprintf("🥷 **%s**　農田 `#%d`　%s\n　🧺 剩 ×%d　👜 這次最多偷 ×%d",
			t.PlayerName, t.PlotNumber, t.CropName, t.RemainingQuantity, t.MaxSteal))
	}
	var list any = "目前沒有可以偷的成熟作物。"
	if len(lines) > 0 {
		list = Section{Title: "可以偷的成熟作物", Lines: lines, Sep: "\n\n"}
	}
	e := &ui.Embed{Title: fmt.Sprintf("🥷 ✦ 偷菜｜%s ✦", p.groupName), Color: Red, Description: sections(
		"👀 用第一個選單選一位玩家，可以打開他的農田畫面，再選要偷哪一格。", list)}
	e.Footer = stealFooter(d.glove)
	if pages > 1 {
		e.Footer += fmt.Sprintf("第 %d/%d 頁", p.stealPage+1, pages)
	}

	// 玩家選單：選一位玩家打開他的農田
	playerPages := pageCount(len(d.players), ui.MaxOptions)
	p.stealPlayerPage = min(p.stealPlayerPage, playerPages-1)
	shownPlayers := d.players[min(len(d.players), p.stealPlayerPage*25):min(len(d.players), (p.stealPlayerPage+1)*25)]
	var players []option
	for _, r := range shownPlayers {
		desc, emoji := fmt.Sprintf("目前沒有可偷的作物｜%s", r.FarmName), "🌱"
		switch {
		case r.LimitReached:
			desc, emoji = "今天已經偷過了｜"+r.FarmName, "✅"
		case r.VictimFull:
			desc, emoji = fmt.Sprintf("今天已被 %d 人偷過｜%s", balance.StealThievesPerVictimPerDay, r.FarmName), "🔒"
		case r.Stealable != 0:
			desc, emoji = fmt.Sprintf("可以偷 %d 格｜%s", r.Stealable, r.FarmName), "🥷"
		}
		players = append(players, option{r.PlayerName, fmt.Sprint(r.PlayerID), desc, emoji})
	}
	p.selectMenu("👀 選擇玩家，查看他的農田…", players, func(ctx context.Context, it ui.Interaction, value string) error {
		var victim int64
		if _, err := fmt.Sscan(value, &victim); err != nil {
			return gameerr.Validationf("找不到這個項目。")
		}
		p.stealVictim = &victim
		p.goTo(ctx, it, "steal_farm", "")
		return nil
	}, 0, "")

	var crops []option
	for _, t := range shown {
		crops = append(crops, option{fmt.Sprintf("%s #%d %s", t.PlayerName, t.PlotNumber, t.CropName),
			fmt.Sprintf("%d|%d|%d", t.PlayerID, t.PlotNumber, t.MaxSteal), fmt.Sprintf("剩 %d，偷 %d 個", t.RemainingQuantity, t.MaxSteal), "🥷"})
	}
	p.selectMenu("🥷 直接選擇要偷的作物…", crops, func(ctx context.Context, it ui.Interaction, value string) error {
		victimText, rest, _ := strings.Cut(value, "|")
		var victim int64
		if _, err := fmt.Sscan(victimText, &victim); err != nil {
			return gameerr.Validationf("找不到這個項目。")
		}
		p.act(ctx, it, p.stealAction(victim, rest), actOpts{})
		return nil
	}, 1, "")
	if pages > 1 {
		move := func(delta int) ui.Handler {
			return func(ctx context.Context, it ui.Interaction) error {
				p.stealPage = ((p.stealPage+delta)%pages + pages) % pages
				p.goTo(ctx, it, "steal", "")
				return nil
			}
		}
		p.button("作物上一頁", move(-1), btn{emoji: "◀️", row: 2})
		p.button("作物下一頁", move(1), btn{emoji: "▶️", row: 2})
	}
	p.nav("重新整理", "steal", btn{emoji: "🔄", row: 2})
	if playerPages > 1 {
		move := func(delta int) ui.Handler {
			return func(ctx context.Context, it ui.Interaction) error {
				p.stealPlayerPage = ((p.stealPlayerPage+delta)%playerPages + playerPages) % playerPages
				p.goTo(ctx, it, "steal", "")
				return nil
			}
		}
		p.button(fmt.Sprintf("玩家上一頁（%d/%d）", p.stealPlayerPage+1, playerPages), move(-1), btn{emoji: "◀️", row: 3})
		p.button("玩家下一頁", move(1), btn{emoji: "▶️", row: 3})
	}
	p.navBack(4)
	return e, nil
}

type stealFarmData struct {
	view services.StealFarmView
}

func (p *Panel) loadStealFarm(ctx context.Context) (any, error) {
	if err := p.requireGuildForSteal(); err != nil {
		return nil, err
	}
	if p.stealVictim == nil {
		return nil, gameerr.Validationf("請先在偷菜頁選擇一位玩家。")
	}
	v, err := p.svc().StealFarmView(ctx, *p.groupID, p.playerID, *p.stealVictim)
	if err != nil {
		return nil, err
	}
	return stealFarmData{v}, nil
}

func (p *Panel) buildStealFarm(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(stealFarmData)
	v := d.view
	stealable := map[int]services.StealTarget{}
	for _, t := range v.Targets {
		stealable[t.PlotNumber] = t
	}
	lines := make([]string, len(v.Plots))
	for i, pl := range v.Plots {
		lines[i] = plotLine(pl, false)
		if t, ok := stealable[pl.PlotNumber]; ok {
			lines[i] += fmt.Sprintf("　🥷 **可偷 ×%d**", t.MaxSteal)
		}
	}
	var status string
	switch {
	case v.LimitReached:
		status = fmt.Sprintf("✅ 你今天已經偷過 %s 的田了（每天只能偷同一位玩家 %d 格），明天再來。", v.PlayerName, balance.StealPlotsPerVictimPerDay)
	case v.VictimFull:
		status = fmt.Sprintf("🔒 %s 今天已經被 %d 個人偷過了（每人每天最多被 %d 人偷），明天再來。", v.PlayerName,
			balance.StealThievesPerVictimPerDay, balance.StealThievesPerVictimPerDay)
	case v.WarehouseFull:
		status = "📦 你的倉庫已滿，先清出空間才能偷菜。"
	case len(v.Targets) > 0 && v.Glove:
		status = fmt.Sprintf("🧤 傳說扒手手套生效中：有 **%d** 格可以偷，每格都能下手，不限次數。", len(v.Targets))
	case len(v.Targets) > 0:
		status = fmt.Sprintf("🥷 有 **%d** 格可以偷，用下方選單選一格；今天只能偷他 %d 格，選好再下手。", len(v.Targets), balance.StealPlotsPerVictimPerDay)
	default:
		status = "🌱 目前沒有可以偷的成熟作物（偷過的作物不能再偷）。"
	}
	e := &ui.Embed{Title: fmt.Sprintf("🥷 ✦ %s 的農田 ✦", v.PlayerName), Color: Red, Description: sections(
		fmt.Sprintf("🏡 **%s**　%s", v.FarmName, summaryCounts(v.Plots)),
		status,
		Section{Title: "農田", Lines: lines},
	), Footer: stealFooter(v.Glove)}
	var options []option
	for _, t := range v.Targets {
		options = append(options, option{fmt.Sprintf("#%d %s", t.PlotNumber, t.CropName), fmt.Sprintf("%d|%d", t.PlotNumber, t.MaxSteal),
			fmt.Sprintf("剩 %d，這次偷 %d 個", t.RemainingQuantity, t.MaxSteal), "🥷"})
	}
	victim := v.PlayerID
	p.selectMenu("🥷 選擇要偷哪一格…", options, func(ctx context.Context, it ui.Interaction, value string) error {
		p.act(ctx, it, p.stealAction(victim, value), actOpts{})
		return nil
	}, 0, "")
	p.nav("重新整理", "steal_farm", btn{emoji: "🔄", row: 1})
	p.nav("換一位玩家", "steal", btn{emoji: "👀", row: 1})
	p.navBack(1)
	p.attachImage(e, p.farmImage(services.FarmStatus{Plots: d.view.Plots, Now: d.view.Now})) // 圖片在交易外產生
	return e, nil
}

// ======================================================================
// 排行榜
// ======================================================================

// boardRow 個人或群組排行的一列（Python 兩者都是 dict，以 "member_count" in r 區分）。
type boardRow struct {
	rank                   int
	playerID               *int64
	name                   string
	score, soldQty, income int
	memberCount            int
	group                  bool
}

type boardData struct {
	rows   []boardRow
	titles map[int64]string
}

func playerRows(rows []leaderboard.Row) []boardRow {
	out := make([]boardRow, len(rows))
	for i, r := range rows {
		id := r.PlayerID
		out[i] = boardRow{rank: r.Rank, playerID: &id, name: r.PlayerName, score: r.Score, soldQty: r.SoldQuantity, income: r.SalesIncome}
	}
	return out
}

func (p *Panel) loadBoard(ctx context.Context) (any, error) {
	b, svc := p.board, p.svc()
	var d boardData
	switch {
	case b.scope == "group":
		if p.groupID == nil {
			return nil, gameerr.Validationf("本伺服器排行需要在 Discord 伺服器中查看。")
		}
		pid := p.playerID
		rows, err := svc.Leaderboard(ctx, b.kind, p.groupID, &pid, 10)
		if err != nil {
			return nil, err
		}
		d.rows = playerRows(rows)
	case b.scope == "groups":
		if _, err := svc.SettleDueRankings(ctx, "system"); err != nil { // 查看時順便結算已到期的排名獎勵
			return nil, err
		}
		rows, err := svc.GroupLeaderboard(ctx, b.kind, b.period, 10)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			d.rows = append(d.rows, boardRow{rank: r.Rank, name: r.GroupName, score: r.Score, soldQty: r.SoldQuantity,
				income: r.SalesIncome, memberCount: r.MemberCount, group: true})
		}
	case b.period == "all":
		rows, err := svc.Leaderboard(ctx, b.kind, nil, nil, 10)
		if err != nil {
			return nil, err
		}
		d.rows = playerRows(rows)
	default:
		rows, err := svc.PeriodLeaderboard(ctx, b.kind, b.period, 10)
		if err != nil {
			return nil, err
		}
		d.rows = playerRows(rows)
	}
	var ids []int64
	for _, r := range d.rows {
		if r.playerID != nil {
			ids = append(ids, *r.playerID)
		}
	}
	titles, err := svc.PlayerTitles(ctx, ids)
	d.titles = titles
	return d, err
}

func (p *Panel) buildBoard(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(boardData)
	b := p.board
	period := PeriodNames[b.period]
	if b.scope == "group" {
		period = "全部期間"
	}
	e := &ui.Embed{Title: fmt.Sprintf("🏆 %s｜%s｜%s", ScopeNames[b.scope], KindNames[b.kind], period), Color: Gold}
	var lines []string
	for _, r := range d.rows {
		medal, ok := Medals[r.rank]
		if !ok {
			medal = fmt.Sprintf("`%2d.`", r.rank)
		}
		name := r.name
		if r.playerID != nil {
			if title := d.titles[*r.playerID]; title != "" {
				name = fmt.Sprintf("%s〔%s〕", name, title)
			}
		}
		var value string
		switch b.kind {
		case "money":
			value = money(r.score) + " 金"
			if b.period != "all" && b.scope != "group" {
				value += "（賣農產品收入）"
			}
		case "sales":
			value = fmt.Sprintf("%s 個（%s 金）", money(r.soldQty), money(r.income))
		default:
			value = fmt.Sprintf("%s 金（%s 個）", money(r.income), money(r.soldQty))
		}
		extra := ""
		if r.group {
			extra = fmt.Sprintf("｜%d 人", r.memberCount)
		}
		lines = append(lines, fmt.Sprintf("%s **%s**　%s%s", medal, name, value, extra))
	}
	e.Description = "目前沒有資料。"
	if len(lines) > 0 {
		e.Description = sections(Section{Lines: lines, Sep: "\n\n"})
	}

	setValue := func(set func(v string)) func(ctx context.Context, it ui.Interaction, value string) error {
		return func(ctx context.Context, it ui.Interaction, value string) error {
			set(value)
			p.goTo(ctx, it, "board", "")
			return nil
		}
	}
	opts := func(order []string, names map[string]string) []option {
		out := make([]option, len(order))
		for i, k := range order {
			out[i] = option{names[k], k, "", ""}
		}
		return out
	}
	p.selectMenu("範圍："+ScopeNames[b.scope], opts(scopeOrder, ScopeNames), setValue(func(v string) { p.board.scope = v }), 0, "")
	p.selectMenu("項目："+KindNames[b.kind], opts(kindOrder, KindNames), setValue(func(v string) { p.board.kind = v }), 1, "")
	if b.scope != "group" {
		p.selectMenu("期間："+PeriodNames[b.period], opts(periodOrder, PeriodNames), setValue(func(v string) { p.board.period = v }), 2, "")
	}
	p.nav("排名獎勵", "rewards", btn{emoji: "🎁", row: 3})
	p.navBack(4)
	return e, nil
}

// codeBlock CMD 文字放進程式碼區塊（Python cut(f"```\n{text}\n```", 4096)）。
func codeBlock(text string) string { return cut("```\n"+text+"\n```", 4096) }

func (p *Panel) loadRewards(ctx context.Context) (any, error) { return p.rankingRewardsText(ctx) }

func (p *Panel) buildRewards(_ context.Context, data any) (*ui.Embed, error) {
	p.nav("回排行榜", "board", btn{emoji: "🏆", row: 0})
	p.navBack(0)
	return &ui.Embed{Title: "🎁 排名獎勵", Color: Gold, Description: codeBlock(data.(string))}, nil
}

// rankingRewardsText Python CMD「leaderboard rewards」：先結算已到期的排名獎勵，再列出進行中的規則與自己最近的獎勵。
func (p *Panel) rankingRewardsText(ctx context.Context) (string, error) {
	svc := p.svc()
	if _, err := svc.SettleDueRankings(ctx, "system"); err != nil {
		return "", err
	}
	all, err := svc.ListRankingRules(ctx)
	if err != nil {
		return "", err
	}
	lines := []string{"===== 排名獎勵 ====="}
	n := 0
	for _, r := range all {
		if !r.Enabled {
			continue
		}
		n++
		lines = append(lines, fmt.Sprintf("#%d %s｜%s・%s・%s結算", r.ID, r.Name, r.ScopeLabel, r.MetricLabel, r.PeriodLabel),
			"    "+r.RewardsText)
	}
	if n == 0 {
		lines = append(lines, "目前沒有進行中的排名獎勵。")
	}
	awards, err := svc.PlayerRankingAwards(ctx, p.playerID, 10)
	if err != nil {
		return "", err
	}
	lines = append(lines, "----- 我最近獲得的排名獎勵 -----")
	for _, a := range awards {
		lines = append(lines, fmt.Sprintf("%s（%s）第 %d 名：%s", a.RuleName, a.Period, a.Rank, a.RewardText))
	}
	if len(awards) == 0 {
		lines = append(lines, "（尚未獲得）")
	}
	return strings.Join(lines, "\n"), nil
}

// ======================================================================
// 任務與競賽
// ======================================================================

type questsData struct {
	quests       []services.QuestView
	competitions []services.CompetitionView
}

func (p *Panel) loadQuests(ctx context.Context) (any, error) {
	svc := p.svc()
	quests, err := svc.PlayerQuests(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	all, err := svc.Competitions(ctx, true)
	if err != nil {
		return nil, err
	}
	var comps []services.CompetitionView
	for _, c := range all {
		if c.State == "active" || c.State == "scheduled" {
			comps = append(comps, c)
		}
	}
	return questsData{quests, comps}, nil
}

func (p *Panel) buildQuests(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(questsData)
	var questLines, compLines []string
	var claimable []services.QuestView
	for _, q := range d.quests {
		mark := "⏳"
		if q.State == "completed" {
			mark = "✅"
			if q.MyContribution > 0 {
				claimable = append(claimable, q)
			}
		}
		questLines = append(questLines, fmt.Sprintf("%s **%s**［%s］\n　%s　%d/%d（%d%%）\n　🙋 我的貢獻 %d　⏰ %s截止\n　🎁 獎勵：%s",
			mark, q.Name, q.GroupName, bar(q.Progress, q.Target, 10), q.Progress, q.Target, q.Percent, q.MyContribution, tsv(q.EndsAt), q.RewardText))
	}
	for _, c := range d.competitions {
		compLines = append(compLines, fmt.Sprintf("⚔️ **%s**（%s）\n　%s　⏰ %s結束", c.Name, c.TypeName, c.StateLabel, tsv(c.EndsAt)))
	}
	var qs, cs any = "📋 目前沒有任務。", "⚔️ 目前沒有進行中的競賽。"
	if len(questLines) > 0 {
		qs = Section{Title: "📋 群組任務", Lines: questLines, Sep: "\n\n"}
	}
	if len(compLines) > 0 {
		cs = Section{Title: "⚔️ 競賽", Lines: compLines, Sep: "\n\n"}
	}
	e := &ui.Embed{Title: "📜 ✦ 群組任務與競賽 ✦", Color: Blue, Description: sections(qs, cs),
		Footer: "只有在農場所屬伺服器中的操作才會計入該群組的任務與競賽。"}

	var claims []option
	for _, q := range claimable {
		claims = append(claims, option{q.Name, fmt.Sprintf("%d|%d", q.GroupID, q.QuestID), q.RewardText, "🎁"})
	}
	p.selectMenu("🎁 領取任務獎勵…", claims, func(ctx context.Context, it ui.Interaction, value string) error {
		var gid, qid int64
		if _, err := fmt.Sscanf(value, "%d|%d", &gid, &qid); err != nil {
			return gameerr.Validationf("找不到這個項目。")
		}
		p.act(ctx, it, p.call("claim_quest", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().ClaimQuest(ctx, gid, p.playerID, qid)
		}), actOpts{})
		return nil
	}, 0, "")
	var comps []option
	for _, c := range d.competitions {
		comps = append(comps, option{c.Name, fmt.Sprint(c.CompetitionID), c.TypeName, "⚔️"})
	}
	p.selectMenu("⚔️ 查看競賽排名…", comps, func(ctx context.Context, it ui.Interaction, value string) error {
		var cid int64
		if _, err := fmt.Sscan(value, &cid); err != nil {
			return gameerr.Validationf("找不到這個項目。")
		}
		p.competitionID = &cid
		p.goTo(ctx, it, "competition", "")
		return nil
	}, 1, "")
	p.nav("已結算的競賽", "history", btn{emoji: "📚", row: 2})
	p.navBack(4)
	return e, nil
}

func (p *Panel) loadCompetition(ctx context.Context) (any, error) {
	if p.competitionID == nil {
		return nil, gameerr.Validationf("請先在任務競賽頁選擇一個競賽。")
	}
	pid := p.playerID
	c, err := p.svc().CompetitionDetail(ctx, *p.competitionID, &pid)
	if err != nil {
		return nil, err
	}
	return competitionStatusText(c) + "\n\n" + competitionBoardText(c), nil
}

func (p *Panel) buildCompetition(_ context.Context, data any) (*ui.Embed, error) {
	p.nav("回任務競賽", "quests", btn{emoji: "📜", row: 0})
	p.navBack(0)
	return &ui.Embed{Title: "⚔️ 競賽", Color: Blue, Description: codeBlock(data.(string))}, nil
}

// localTimePtr Python CMD _local：沒有時間時為「-」。
func localTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return localTime(*t)
}

// competitionStatusText Python CMD「competition status <ID>」（面板一定有玩家）。
func competitionStatusText(c services.CompetitionDetail) string {
	rewards := make([]string, len(c.Rewards))
	for i, g := range c.Rewards {
		rewards[i] = fmt.Sprintf("第 %d 名 %d 金幣／人", i+1, g)
	}
	lines := []string{
		fmt.Sprintf("===== 競賽 #%d「%s」（%s）=====", c.CompetitionID, c.Name, c.StateLabel),
		"類型：" + c.TypeName,
		fmt.Sprintf("計分規則：%s；只計入在主要群組中進行的操作", c.ScoringRule),
		fmt.Sprintf("期間：%s ～ %s", localTime(c.StartsAt), localTime(c.EndsAt)),
		fmt.Sprintf("結算獎勵：%s（發給該群組中以此為主要群組的全體成員）", strings.Join(rewards, "、")),
	}
	if c.Description != "" {
		lines = slices.Insert(lines, 2, "說明："+c.Description)
	}
	if c.CancelReason != nil && *c.CancelReason != "" {
		lines = append(lines, "取消原因："+*c.CancelReason)
	}
	lines = append(lines, fmt.Sprintf("我的貢獻：%d 分", c.MyPoints))
	if c.MyHomeGroupID != nil {
		for _, r := range c.Ranking {
			if r.GroupID == *c.MyHomeGroupID {
				lines = append(lines, fmt.Sprintf("我的主要群組：第 %d 名，%d 分", r.Rank, r.Score))
				break
			}
		}
	}
	if c.MyAward != nil {
		lines = append(lines, fmt.Sprintf("我的獎勵：第 %d 名 %d 金幣（已發放）", c.MyAward.Rank, c.MyAward.Gold))
	}
	return strings.Join(lines, "\n")
}

// competitionBoardText Python CMD「competition leaderboard <ID>」。
func competitionBoardText(c services.CompetitionDetail) string {
	kind := "即時排名"
	if c.Final {
		kind = "最終排名"
	}
	lines := []string{
		fmt.Sprintf("===== 競賽 #%d「%s」%s（%s）=====", c.CompetitionID, c.Name, kind, c.StateLabel),
		"排名  群組              分數   貢獻人數",
	}
	for _, r := range c.Ranking {
		lines = append(lines, fmt.Sprintf("%3d   %-16s %6d   %6d", r.Rank, r.GroupName, r.Score, r.Contributors))
	}
	if len(c.Ranking) == 0 {
		lines = append(lines, "（尚無群組得分）")
	}
	lines = append(lines, "個人貢獻：")
	for i, pp := range c.TopPlayers {
		lines = append(lines, fmt.Sprintf("  %d. %s（%s）：%d 分", i+1, pp.PlayerName, pp.GroupName, pp.Points))
	}
	if len(c.TopPlayers) == 0 {
		lines = append(lines, "  （尚無貢獻）")
	}
	return strings.Join(lines, "\n")
}

func (p *Panel) loadHistory(ctx context.Context) (any, error) {
	pid := p.playerID
	history, err := p.svc().CompetitionHistory(ctx, &pid, 10)
	if err != nil {
		return nil, err
	}
	return competitionHistoryText(history), nil
}

// competitionHistoryText Python CMD「competition history」。
func competitionHistoryText(history []services.CompetitionDetail) string {
	if len(history) == 0 {
		return "目前沒有已結算的競賽。"
	}
	blocks := []string{"===== 競賽歷史（最終排名）====="}
	for _, c := range history {
		var top []string
		for _, r := range c.Ranking[:min(3, len(c.Ranking))] {
			top = append(top, fmt.Sprintf("第 %d 名 %s（%d）", r.Rank, r.GroupName, r.Score))
		}
		topText := strings.Join(top, "、")
		if topText == "" {
			topText = "無人參賽"
		}
		mine := ""
		if c.MyAward != nil {
			mine = fmt.Sprintf("｜我獲得第 %d 名獎勵 %d 金幣", c.MyAward.Rank, c.MyAward.Gold)
		}
		blocks = append(blocks, fmt.Sprintf("#%d %s（結算於 %s）：%s%s", c.CompetitionID, c.Name, localTimePtr(c.SettledAt), topText, mine))
	}
	return strings.Join(blocks, "\n")
}

func (p *Panel) buildHistory(_ context.Context, data any) (*ui.Embed, error) {
	p.nav("回任務競賽", "quests", btn{emoji: "📜", row: 0})
	p.navBack(0)
	return &ui.Embed{Title: "📚 已結算的競賽", Color: Blue, Description: codeBlock(data.(string))}, nil
}

// ======================================================================
// 群組與轉移
// ======================================================================

type groupData struct {
	group    services.GroupInfo
	members  []groups.Member
	transfer services.TransferView
}

func (p *Panel) loadGroup(ctx context.Context) (any, error) {
	if p.groupID == nil {
		return nil, gameerr.Validationf("群組資訊需要在 Discord 伺服器中查看。")
	}
	svc := p.svc()
	var d groupData
	var err error
	if d.group, err = svc.GetGroup(ctx, *p.groupID); err != nil {
		return nil, err
	}
	if d.members, err = svc.GroupMembers(ctx, *p.groupID); err != nil {
		return nil, err
	}
	d.transfer, err = svc.GroupTransferStatus(ctx, p.playerID, p.groupID)
	return d, err
}

func (p *Panel) buildGroup(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(groupData)
	g, t := d.group, d.transfer
	var members []groups.Member
	for _, m := range d.members {
		if m.Farm != nil {
			members = append(members, m)
		}
	}
	sort.SliceStable(members, func(i, j int) bool { return members[i].Farm.Gold > members[j].Farm.Gold })
	var lines []string
	for _, m := range members[:min(20, len(members))] {
		mark := "👤"
		if m.Farm.RankedInGroup {
			mark = "🏠"
		}
		lines = append(lines, fmt.Sprintf("%s **%s**　⭐ Lv %d\n　💰 %s 金　✅ 可收成 %d 格", mark, m.PlayerName, m.Farm.Level, money(m.Farm.Gold), m.Farm.Ready))
	}
	e := &ui.Embed{Title: fmt.Sprintf("👥 ✦ %s ✦", g.Name), Color: Blue, Description: sections(
		fmt.Sprintf("👥 成員 **%d** 人", g.MemberCount),
		Section{Title: "成員（依金幣）", Lines: lines, Sep: "\n\n"},
	)}
	here := t.HomeGroupID != nil && *t.HomeGroupID == *p.groupID
	home := deref(t.HomeGroupName)
	var note string
	switch {
	case here:
		note = "你的農場屬於這個伺服器。"
	case t.CanTransfer:
		note = fmt.Sprintf("你的農場屬於「%s」，可以轉移到這裡。", home)
	default:
		note = fmt.Sprintf("你的農場屬於「%s」；轉移冷卻中，%s可再轉移。", home, ts(t.NextTransferAt))
	}
	e.AddField("🏠 我的所屬群組", note, false)
	e.Footer = "🏠 = 農場屬於此群組（列入群組排行）。轉移後需等 7 天才能再轉移。"

	if !here {
		confirming := p.confirm == "transfer"
		label, style := "把農場轉移到這個伺服器", ui.Primary
		if confirming {
			label, style = "確認轉移", ui.Danger
		}
		gid := *p.groupID
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			if p.confirm != "transfer" {
				p.confirm = "transfer"
				p.refresh(ctx, it, fmt.Sprintf("⚠️ 確定把農場從「%s」轉移到「%s」嗎？金幣、物品、農田都不變；之後的群組排行、任務與競賽改以新群組計算；"+
					"轉移後 %s內不能再轉移。再按一次「確認轉移」。", home, g.Name, services.LongDuration(balance.GroupTransferCooldownSeconds)), nil, "重繪", time.Time{})
				return nil
			}
			p.act(ctx, it, p.call("transfer_home_group", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().TransferHomeGroup(ctx, p.playerID, gid)
			}), actOpts{})
			return nil
		}, btn{emoji: "🔁", row: 0, disabled: !t.CanTransfer, style: style})
	}
	p.navBack(4)
	return e, nil
}
