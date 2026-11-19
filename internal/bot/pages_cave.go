package bot

// 面板頁面：山洞冒險（總覽、區域、探索結果、探索紀錄）、機器、操作紀錄（對應 Python panels.py 的「山洞冒險」
// 「機器」「紀錄」區段：_build_cave、_build_cave_area、_build_cave_result、_build_cave_log、_build_machines、_build_log）。

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
	"farmgm/internal/services"
)

// ======================================================================
// 山洞冒險
// ======================================================================

func areaStatus(a services.AreaView, trip *services.TripView) string {
	if !a.Unlocked {
		if a.NeedsRuins && !a.RuinsOpen {
			return fmt.Sprintf("🔒 所屬群組修復遺跡後開放（Lv.%d 起）", a.UnlockLevel)
		}
		return fmt.Sprintf("🔒 農場 Lv.%d 解鎖", a.UnlockLevel)
	}
	if a.NeedsRuins { // 額外的探索：不受一般探險影響，只看自己的冷卻
		if a.Ready {
			return "✅ 可以探索（不佔一般探險，立即拿到獎勵）"
		}
		return "⏳ " + ts(a.NextAt) + "可再探索"
	}
	if a.Exploring {
		if trip.Finished {
			return "📦 已經回來了，等你領取獎勵"
		}
		return fmt.Sprintf("🧭 探險中，%s回來", tsv(trip.EndsAt))
	}
	if trip != nil {
		return fmt.Sprintf("⏸️ 正在%s探險，回來後才能出發", trip.AreaName)
	}
	return "✅ 可以出發"
}

func tripText(trip *services.TripView) string {
	if trip == nil {
		return "🏕️ 目前沒有在探險，選一個區域出發吧！"
	}
	if trip.Finished {
		return fmt.Sprintf("📦 %s **%s**探險回來了！按「領取探險獎勵」把物品放進背包。", trip.Emoji, trip.AreaName)
	}
	return fmt.Sprintf("🧭 正在 %s **%s**探險，%s回來（%s）", trip.Emoji, trip.AreaName, tsv(trip.EndsAt), tsv(trip.EndsAt, "f"))
}

func (p *Panel) claimButton(trip *services.TripView, row int) {
	if trip == nil || !trip.Finished {
		return
	}
	p.button(p.s("領取探險獎勵"), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.claimExploration, actOpts{page: "cave_result"})
		return nil
	}, btn{emoji: "📦", row: row, style: ui.Success})
}

func (p *Panel) areaImage(code string) []byte {
	return p.image(p.s("探險區域"), func() ([]byte, error) { return farmimage.ExplorePNG(code) })
}

type caveData struct {
	ov    services.ExplorationOverview
	area  services.AreaView // 區域頁
	image []byte
}

func (p *Panel) loadCave(ctx context.Context) (any, error) {
	ov, err := p.svc().ExplorationOverview(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	d := caveData{ov: ov}
	if ov.Trip != nil { // 探險中顯示所在區域
		d.image = p.areaImage(ov.Trip.Area)
	}
	return d, nil
}

func (p *Panel) buildCave(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(caveData)
	trip := d.ov.Trip
	e := &ui.Embed{Title: p.s("⛰️ ✦ 山洞冒險 ✦"), Color: Gold, Description: sections(
		p.f("👨‍🌾 **%s**　⭐ 農場 **Lv %d**", d.ov.PlayerName, d.ov.Level),
		p.s("探險帶回素材、稀有種子與假幣（放素材背包），素材可以在「🔧 機器」做自動化機器。\n")+
			p.s("一次只能去一個區域，回來領取獎勵後才能再出發；🏛️ 古代遺跡由群組一起修復，修好後每天可額外探索一次。"),
	)}
	for _, a := range d.ov.Areas {
		when := p.s("探險 ") + p.dur(true, a.DurationSeconds)
		if a.NeedsRuins {
			when = p.s("每天一次、立即拿到獎勵")
		}
		e.AddField(p.f("%s %s（Lv.%d）", a.Emoji, p.s(a.Name), a.UnlockLevel),
			p.f("%s　🕒 %s\n🪨 主要素材：%s", areaStatus(a, trip), when, strings.Join(a.Common[:min(3, len(a.Common))], p.s("、"))), false)
	}
	e.AddField(p.s("🧭 目前探險"), tripText(trip), false) // 目前狀態放在最後一個欄位，貼近出發／領取按鈕
	// 2026-11-16 可以出發的區域按一下就出發（原本要先進區域頁再按「出發探險」）；其他狀態（探險中、未解鎖…）打開區域頁。
	// 區域的詳細資訊改從「ℹ️ 查看區域詳情」選單進入。連點保護與區域頁相同：重繪後舊按鈕失效，後端也限制一次一趟。
	var details []option
	for _, a := range d.ov.Areas {
		code := a.Code
		details = append(details, option{p.s(a.Name), code, areaStatusShort(a, trip), a.Emoji})
		if a.Ready {
			target := "cave"
			if a.NeedsRuins {
				target = "cave_result"
			}
			p.button(p.s("出發：")+p.s(a.Name), func(ctx context.Context, it ui.Interaction) error {
				p.caveArea = code
				p.act(ctx, it, func(ctx context.Context) (string, error) { return p.explore(ctx, code) }, actOpts{page: target})
				return nil
			}, btn{emoji: "🧭", row: 0, style: ui.Success})
			continue
		}
		style := ui.Secondary
		if a.Exploring {
			style = ui.Primary
		}
		p.button(p.s(a.Name), func(ctx context.Context, it ui.Interaction) error {
			p.caveArea = code
			p.goTo(ctx, it, "cave_area", "")
			return nil
		}, btn{emoji: a.Emoji, row: 0, style: style})
	}
	p.selectMenu(p.s("ℹ️ 查看區域詳情（素材、稀有種子、等級解鎖）…"), details, func(ctx context.Context, it ui.Interaction, code string) error {
		p.caveArea = code
		p.goTo(ctx, it, "cave_area", "")
		return nil
	}, 2, "")
	p.claimButton(trip, 1)
	p.nav(p.s("探索紀錄"), "cave_log", btn{emoji: "📖", row: 1})
	p.nav(p.s("機器"), "machines", btn{emoji: "🔧", row: 1})
	p.nav(p.s("修復遺跡"), "ruins", btn{emoji: "🏛️", row: 1})
	p.navBack(1)
	p.attachImage(e, d.image)
	return e, nil
}

func (p *Panel) loadCaveArea(ctx context.Context) (any, error) {
	ov, err := p.svc().ExplorationOverview(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	code := p.caveArea
	if code == "" {
		code = exploration.AreaOrder[0]
	}
	i := slices.IndexFunc(ov.Areas, func(a services.AreaView) bool { return a.Code == code })
	if i < 0 {
		return nil, gameerr.NotFoundf("找不到這個探索區域。")
	}
	return caveData{ov: ov, area: ov.Areas[i], image: p.areaImage(code)}, nil
}

func (p *Panel) buildCaveArea(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(caveData)
	a, level, trip := d.area, d.ov.Level, d.ov.Trip
	notes := make([]string, len(a.LevelNotes))
	for i, n := range a.LevelNotes {
		mark := "🔒"
		if level >= n.Level {
			mark = "✅"
		}
		notes[i] = fmt.Sprintf("%s Lv.%d %s", mark, n.Level, n.Text)
	}
	kind := p.f("普通探索（Lv.%d 起高級探索）", a.AdvancedLevel)
	if a.Advanced {
		kind = p.s("🌟 高級探索")
	}
	orDash := func(list []string, empty string) string {
		if s := strings.Join(list, p.s("、")); s != "" {
			return s
		}
		return empty
	}
	var events []string
	for _, ev := range a.Events {
		lock := ""
		if level < ev.MinLevel {
			lock = p.f("（Lv.%d）", ev.MinLevel)
		}
		events = append(events, p.s(ev.Name)+lock)
	}
	events = append(events, p.s("🪙 假幣"))
	color := Grey
	if a.Ready {
		color = Green
	}
	e := &ui.Embed{Title: fmt.Sprintf("%s ✦ %s ✦", a.Emoji, p.s(a.Name)), Color: color, Description: sections(
		p.s(a.Description),
		p.f("🔑 解鎖：農場 **Lv.%d**（你是 Lv %d）\n🕒 探險時間：%s　🎲 每次 %d 份獎勵　%s\n📌 狀態：%s",
			a.UnlockLevel, level, p.dur(true, a.DurationSeconds), a.Rolls, kind, areaStatus(a, trip)),
		Section{Title: p.s("🪨 主要素材"), Lines: []string{strings.Join(a.Common, p.s("、"))}},
		Section{Title: p.s("💎 稀有素材"), Lines: []string{orDash(a.Rare, "—")}},
		Section{Title: p.s("🌰 稀有種子"), Lines: []string{orDash(a.Seeds, p.s("這裡沒有稀有種子"))}},
		Section{Title: p.s("🎁 特殊發現"), Lines: events},
		Section{Title: p.s("⭐ 等級解鎖"), Lines: notes},
	)}
	if a.Exploring && trip != nil && trip.Finished {
		p.claimButton(trip, 0)
	} else {
		var label string
		switch {
		case !a.Unlocked:
			label = p.s("尚未解鎖")
		case a.NeedsRuins && a.Ready:
			label = p.s("探索遺跡")
		case a.NeedsRuins:
			label = p.s("冷卻中")
		case a.Exploring:
			label = p.s("探險中")
		case trip != nil:
			label = p.s("正在其他區域探險")
		default:
			label = p.s("出發探險")
		}
		style := ui.Secondary
		if a.Ready {
			style = ui.Success
		}
		code, target := a.Code, "cave"
		if a.NeedsRuins {
			target = "cave_result"
		}
		// 同一面板連點：Python 以 self.exploring 忽略後續點擊；Go 的回呼依序執行，第一次完成後畫面已重繪、
		// 舊按鈕的 custom_id 失效，第二次點擊不會再執行（後端另有一次一趟的保護）。
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, func(ctx context.Context) (string, error) { return p.explore(ctx, code) }, actOpts{page: target})
			return nil
		}, btn{emoji: "🧭", row: 0, disabled: !a.Ready, style: style})
	}
	p.nav(p.s("回山洞"), "cave", btn{emoji: "⛰️", row: 0})
	p.navBack(0)
	p.attachImage(e, d.image)
	return e, nil
}

func (p *Panel) explore(ctx context.Context, code string) (string, error) {
	if exploration.AreaOf(code).NeedsRuins { // 古代遺跡：立即拿到獎勵，直接顯示探索結果
		r, err := Call(ctx, p.cmds.Runner, "explore_ruins", func(ctx context.Context) (services.ExploreReward, error) {
			return p.svc().ExploreRuins(ctx, p.playerID)
		})
		if err != nil {
			return "", err
		}
		p.caveResult = &r
		return p.f("%s 探索了%s！", r.Emoji, p.s(r.AreaName)), nil
	}
	out, err := Call(ctx, p.cmds.Runner, "explore", func(ctx context.Context) (services.ExploreOutcome, error) {
		return p.svc().Explore(ctx, p.playerID, code)
	})
	if err != nil {
		return "", err
	}
	if out.Trip == nil {
		return "", fmt.Errorf("探險沒有回傳行程")
	}
	r := out.Trip
	return p.f("%s 出發前往%s！探險 %s，%s回來後記得領取獎勵。", r.Emoji, p.s(r.AreaName), p.dur(true, r.DurationSeconds), tsv(r.EndsAt)), nil
}

func (p *Panel) claimExploration(ctx context.Context) (string, error) {
	r, err := Call(ctx, p.cmds.Runner, "claim_exploration", func(ctx context.Context) (services.ExploreReward, error) {
		return p.svc().ClaimExploration(ctx, p.playerID)
	})
	if err != nil {
		return "", err
	}
	p.caveResult = &r
	return p.f("%s 從%s探險回來了！", r.Emoji, p.s(r.AreaName)), nil
}

type caveResultData struct {
	r     services.ExploreReward
	image []byte
}

func (p *Panel) loadCaveResult(context.Context) (any, error) {
	if p.caveResult == nil {
		return nil, gameerr.Validationf("目前沒有探索結果，請回冒險介面選擇區域探索。")
	}
	return caveResultData{*p.caveResult, p.areaImage(p.caveResult.Area)}, nil
}

func (p *Panel) buildCaveResult(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(caveResultData)
	r := d.r
	var items, seeds []string
	for _, i := range r.Items {
		emoji := lookupOr(MaterialEmoji, i.Code, cropEmoji(strings.TrimPrefix(i.Code, "seed_")))
		items = append(items, p.f("%s %s　×**%d**　（%s）", emoji, p.s(i.Name), i.Quantity, i.Rarity))
		if q, ok := r.Seeds[p.s(i.Name)]; ok { // Seeds 依掉落順序（Python dict 的順序）
			seeds = append(seeds, fmt.Sprintf("%s ×%d", p.s(i.Name), q))
		}
	}
	if len(items) == 0 {
		items = []string{p.s("這次什麼都沒找到…")}
	}
	seedText := p.s("沒有")
	if len(seeds) > 0 {
		seedText = "🎉 " + strings.Join(seeds, p.s("、"))
	}
	coins := p.s("沒有")
	if r.FakeCoins != 0 {
		coins = p.f("🎉 %d 個", r.FakeCoins)
	}
	events := p.s("沒有")
	if len(r.Events) > 0 {
		events = strings.Join(r.Events, p.s("、"))
	}
	summary := p.f("🌰 稀有種子：%s\n🪙 假幣：%s\n🎁 特殊事件：%s", seedText, coins, events)
	if r.Advanced {
		summary += p.s("\n🌟 高級探索")
	}
	if r.GuardianNote != "" {
		summary += "\n" + r.GuardianNote
	}
	tail := p.s("🧭 獎勵已放進背包，現在可以再出發探險了！")
	if r.Instant {
		tail = p.f("🏛️ 遺跡裡多找到 %d 份", r.ExtraRolls)
		if r.BonusRolls != 0 {
			tail += p.f("，守護者已被打倒再多 %d 份", r.BonusRolls)
		}
		tail += p.f("！獎勵已放進背包，%s可再探索遺跡（一般探險不受影響）。", ts(r.NextAt))
	}
	e := &ui.Embed{Title: p.f("%s ✦ %s探索結果 ✦", r.Emoji, p.s(r.AreaName)), Color: Gold, Description: sections(
		Section{Title: p.s("🎒 獲得物品"), Lines: items}, summary, tail)}
	p.nav(p.s("回山洞"), "cave", btn{emoji: "⛰️", row: 0})
	p.navBack(0)
	p.attachImage(e, d.image)
	return e, nil
}

func (p *Panel) loadCaveLog(ctx context.Context) (any, error) {
	return p.svc().ExplorationLogs(ctx, p.playerID, 10)
}

func (p *Panel) buildCaveLog(_ context.Context, data any) (*ui.Embed, error) {
	logs := data.([]services.ExplorationLogView)
	var lines []string
	for _, x := range logs {
		adv := ""
		if x.Advanced {
			adv = p.s("　🌟高級")
		}
		rewards := make([]string, len(x.Rewards))
		for i, r := range x.Rewards {
			rewards[i] = fmt.Sprintf("%s ×%d", p.s(r.Name), r.Quantity)
		}
		got := strings.Join(rewards, p.s("、"))
		if got == "" {
			got = p.s("一無所獲")
		}
		line := p.f("%s **%s**　%s%s\n　🎒 %s", x.Emoji, p.s(x.AreaName), tsv(x.ExploredAt), adv, got)
		if len(x.Events) > 0 {
			line += p.s("\n　🎁 ") + strings.Join(x.Events, p.s("、"))
		}
		lines = append(lines, line)
	}
	empty := ""
	if len(lines) == 0 {
		empty = p.s("還沒有探索紀錄。")
	}
	e := &ui.Embed{Title: p.s("📖 ✦ 探索紀錄 ✦"), Color: Blue, Description: sections(Section{Title: p.s("最近 10 次探索"), Lines: lines, Sep: "\n\n"}, empty)}
	p.nav(p.s("回山洞"), "cave", btn{emoji: "⛰️", row: 0})
	p.navBack(0)
	return e, nil
}

// ======================================================================
// 機器（素材製作；種子製造機、稀有作物培育器、自動加工機設定）
// ======================================================================

type machinesData struct {
	status    services.MachinesStatus
	incubator []services.IncubatorRecipeView
	recipes   []services.RecipeView
}

func (p *Panel) loadMachines(ctx context.Context) (any, error) {
	svc, pid := p.svc(), p.playerID
	var d machinesData
	var err error
	if d.status, err = svc.MachinesStatus(ctx, pid); err != nil {
		return nil, err
	}
	if d.incubator, err = svc.IncubatorRecipes(ctx); err != nil {
		return nil, err
	}
	d.recipes, err = svc.Recipes(ctx, &pid)
	return d, err
}

// missingText 缺少的素材，依材料清單的順序（Python dict 的順序）。
func missingText(m services.MachineView, sep string) []string {
	var out []string
	for _, mat := range m.Materials {
		if n, ok := m.Missing[mat.Name]; ok {
			out = append(out, fmt.Sprintf("%s%s%d", mat.Name, sep, n))
		}
	}
	return out
}

func (p *Panel) buildMachines(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(machinesData)
	st := d.status
	machines := map[string]services.MachineView{}
	var blocks []string
	for _, m := range st.Machines {
		machines[m.Code] = m
		var state string
		switch {
		case m.Owned && m.Code == "seed_maker": // 只在手動使用時運作，沒有啟用／停用
			state = p.s("✅ 已擁有，從「🌱 農田」→「🌱 種子製造」使用")
		case m.Owned:
			state = p.s("✅ 已擁有，已停用")
			if m.Enabled {
				state = p.s("✅ 已擁有，啟用中")
			}
		case !m.Unlocked:
			state = p.f("🔒 農場 Lv.%d 解鎖", m.UnlockLevel)
		case len(m.Missing) > 0:
			state = p.s("🧺 材料不足（還缺 ") + strings.Join(missingText(m, "×"), p.s("、")) + p.s("）")
		default:
			state = p.s("🛠️ 可以製作")
		}
		mats := make([]string, len(m.Materials))
		for i, mat := range m.Materials {
			mark := "❌"
			if mat.Have >= mat.Need {
				mark = "✅"
			}
			mats[i] = fmt.Sprintf("%s%s %d/%d", mark, p.s(mat.Name), mat.Have, mat.Need)
		}
		block := p.f("%s **%s**（Lv.%d）　%s\n　📝 %s", m.Emoji, p.s(m.Name), m.UnlockLevel, state, p.s(m.Description))
		if !m.Owned {
			block += p.s("\n　🧱 ") + strings.Join(mats, p.s("、"))
		}
		if m.Code == "auto_processor" && m.Owned {
			name := p.s("未設定")
			if code, _ := m.Settings["recipe"].(string); code != "" {
				if r, ok := balance.Recipes[code]; ok {
					name = p.s(r.Name)
				}
			}
			block += p.s("\n　⚙️ 加工項目：") + name
		}
		blocks = append(blocks, block)
	}
	e := &ui.Embed{Title: p.s("🔧 ✦ 機器 ✦"), Color: Blue, Description: sections(
		p.f("⭐ 農場 Lv %d　素材從「⛰️ 山洞」探索取得", st.Level),
		Section{Title: p.s("機器"), Lines: blocks, Sep: "\n\n"},
	)}
	if p.picker != nil {
		e.AddField(p.s("🛒 已選擇"), p.f("**%s**\n👇 選擇數量", p.picker.Label), false)
	}

	// 2026-11-16 選單整理：
	//   - 「製作機器」只列現在做得出來的機器（等級不足、缺素材的原因已寫在上方清單，以前選了才進錯誤畫面）
	//   - 稀有作物培育與自動加工設定分成兩個選單；自動加工標出目前的設定，「不自動開單」放第一項（不會被 25 項上限截掉）
	//   - 選好培育項目後，第 1 列改顯示培育數量按鈕（製作選單暫時收起，避免超過 5 列）
	var builds []option
	for _, m := range st.Machines {
		if m.Owned || !m.Unlocked || len(m.Missing) > 0 {
			continue
		}
		builds = append(builds, option{p.s(m.Name), m.Code, p.f("Lv.%d｜材料足夠，可以製作", m.UnlockLevel), m.Emoji})
	}
	if p.picker != nil {
		p.quantityButtons(0)
	} else {
		p.selectMenu(p.s("🛠️ 製作機器（消耗素材）…"), builds, func(ctx context.Context, it ui.Interaction, code string) error {
			p.act(ctx, it, p.call("build_machine", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuildMachine(ctx, p.playerID, code)
			}), actOpts{})
			return nil
		}, 0, "")
	}

	if machines["rare_incubator"].Owned { // 種子製造改到「🌱 種子製造」頁（2026-11-14）
		var incubate []option
		for _, r := range d.incubator {
			inputs := make([]string, len(r.InputOrder))
			for i, n := range r.InputOrder {
				inputs[i] = fmt.Sprintf("%s×%d", n, r.Inputs[n])
			}
			incubate = append(incubate, option{p.s("培育：") + p.s(r.Name), r.Code,
				strings.Join(inputs, p.s("、")) + p.f("｜%s／份", p.dur(true, r.Seconds)), "🧬"})
		}
		selected := ""
		if p.picker != nil {
			selected = strings.TrimPrefix(p.picker.Key, "incubate:")
		}
		p.selectMenu(p.s("🧬 稀有作物培育…"), incubate, func(ctx context.Context, it ui.Interaction, code string) error {
			i := slices.IndexFunc(d.incubator, func(r services.IncubatorRecipeView) bool { return r.Code == code })
			if i < 0 {
				return gameerr.NotFoundf("找不到這個項目。")
			}
			p.picker = &Picker{Key: "incubate:" + code, Label: "🧬 " + p.s(d.incubator[i].Name), Verb: p.s("培育"), Max: exploration.IncubatorMaxQuantity,
				Presets: []int{1, 3, 5},
				Run: p.callQty("start_incubation", func(ctx context.Context, q int) (services.ActionResult, error) {
					return p.svc().StartIncubation(ctx, p.playerID, code, q)
				})}
			p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
			return nil
		}, 1, selected)
	}
	if m := machines["auto_processor"]; m.Owned {
		current, _ := m.Settings["recipe"].(string)
		procs := []option{{p.s("不自動開單"), "none", p.s("只自動領取完成的加工品"), "⏸️"}}
		for _, r := range d.recipes {
			procs = append(procs, option{p.s(r.Name), r.Code, p.s(r.BuildingName), "🏭"})
		}
		selected := current
		if selected == "" {
			selected = "none"
		}
		p.selectMenu(p.s("🏭 自動加工機要做什麼…"), procs, func(ctx context.Context, it ui.Interaction, code string) error {
			if code == "none" {
				code = ""
			}
			p.act(ctx, it, p.call("set_processor_recipe", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetProcessorRecipe(ctx, p.playerID, code)
			}), actOpts{})
			return nil
		}, 2, selected)
	}
	for _, m := range st.Machines {
		if !m.Owned || m.Code == "seed_maker" {
			continue
		}
		verb := p.s("啟用")
		if m.Enabled {
			verb = p.s("停用")
		}
		code, enable := m.Code, !m.Enabled
		p.button(verb+p.s(m.Name), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("set_machine_enabled", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().SetMachineEnabled(ctx, p.playerID, code, enable)
			}), actOpts{})
			return nil
		}, btn{emoji: m.Emoji, row: 3})
	}
	if machines["seed_maker"].Owned {
		p.navFirstPage(p.s("種子製造"), "seedmaker", &p.seedMakerPage, btn{emoji: "🌱", row: 4})
	}
	p.nav(p.s("加工"), "craft", btn{emoji: "🏭", row: 4})
	p.nav(p.s("山洞"), "cave", btn{emoji: "⛰️", row: 4})
	p.navBack(4)
	return e, nil
}

// ======================================================================
// 操作紀錄
// ======================================================================

func (p *Panel) loadLog(ctx context.Context) (any, error) {
	return p.svc().GetRecentTransactions(ctx, p.playerID, logMaxRecords)
}

// 操作紀錄與培育紀錄：一次讀最近的 logMaxRecords 筆，每頁 logPerPage 筆（2026-11-14 從只顯示 15 筆改成分頁）。
const (
	logMaxRecords = 100
	logPerPage    = 10
)

// signedComma Python f"{n:+,}"。
func signedComma(n int) string {
	if n >= 0 {
		return "+" + numfmt.Comma(n)
	}
	return numfmt.Comma(n)
}

func (p *Panel) buildLog(_ context.Context, data any) (*ui.Embed, error) {
	all := data.([]services.TransactionInfo)
	txs, page, pages := pageSlice(all, p.logPage, logPerPage)
	p.logPage = page
	var lines []string
	for _, t := range txs {
		delta := ""
		if t.GoldDelta != 0 {
			delta = "`" + signedComma(t.GoldDelta) + "`"
		}
		label, ok := rules.EventTypeLabels[rules.EventType(t.EventType)]
		if !ok {
			label = t.EventType
		}
		lines = append(lines, p.f("🕒 %s　**%s** %s\n　%s", tsv(t.CreatedAt), label, delta, cut(p.s(t.Description), 120)))
	}
	p.navBack(0)
	p.pageButtons("", &p.logPage, pages, 0, "log")
	e := &ui.Embed{Title: p.s("🧾 ✦ 最近的操作紀錄 ✦"), Color: Grey, Description: p.s("目前沒有紀錄。")}
	if len(lines) > 0 {
		e.Description = sections(Section{Lines: lines, Sep: "\n\n"})
		e.Footer = p.f("第 %d/%d 頁，共 %d 筆（可翻閱最近 %d 筆）", page+1, pages, len(all), logMaxRecords)
	}
	return e, nil
}

// areaStatusShort 選單說明用的區域狀態：選單說明不會轉換 Discord 時間戳，所以不含時間戳。
func areaStatusShort(a services.AreaView, trip *services.TripView) string {
	switch {
	case !a.Unlocked:
		return strings.TrimPrefix(areaStatus(a, trip), "🔒 ")
	case a.NeedsRuins && !a.Ready:
		return "冷卻中，之後可再探索"
	case a.Exploring && trip != nil && trip.Finished:
		return "已經回來了，等你領取獎勵"
	case a.Exploring:
		return "探險中"
	default:
		return strings.TrimPrefix(areaStatus(a, trip), "✅ ")
	}
}
