package bot

// 子面板：釣魚（對應 Python fishing_panel.py 的 FishingPages）：釣魚中心、釣魚過程、魚簍、魚的詳細資料、圖鑑、
// 排行榜與釣具商店。
//
//   - 不使用圖片，全部以 Emoji、文字與數值呈現。
//   - 釣魚過程的狀態存在資料庫（fishing_sessions），面板只負責顯示；Bot 重啟後重新開啟面板即可繼續。
//   - 等魚上鉤、拉竿反應與拉扯操作的時間到時，面板會自動更新（scheduleFishTimer）；伺服器端另外依時間判定逾時。
//     自動更新經 Registry.RunFor 執行，與玩家的點擊依序處理。
//   - 拉扯操作附上畫面上的回合數，重複點擊不會重複執行；分享沿用「分享農場」的方式（在目前頻道發送公開訊息）。

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/seasons"
	"farmgm/internal/numfmt"
	fishrepo "farmgm/internal/repositories/fishing"
	"farmgm/internal/services"
)

const (
	Sea        = 0x3498DB
	basketPage = 25
	// timerSlack 上鉤時間到後多等一點再更新
	timerSlack = 400 * time.Millisecond
	// deadlineSlack 期限（拉竿、拉扯操作）到期時，伺服器另有網路延遲寬限，要等寬限過後再更新才會看到結果
	deadlineSlack = time.Duration((fishing.ReactionGraceSeconds + 0.5) * float64(time.Second))
)

var (
	rarityColor = map[string]int{"common": 0xBDC3C7, "rare": 0x3498DB, "epic": 0x9B59B6, "legendary": 0xF1C40F}
	celebrate   = map[string]string{
		"common": "🎣 收穫不錯！", "rare": "✨ 釣到稀有魚了！", "epic": "🔥 太厲害了，是史詩魚！", "legendary": "🎉 恭喜獲得傳說魚！",
	}
	fightButtons = []struct {
		action, emoji string
		style         ui.Style
	}{{"reel", "🎣", ui.Primary}, {"release", "↩️", ui.Secondary}, {"wait", "⏳", ui.Secondary}, {"left", "⬅️", ui.Secondary}, {"right", "➡️", ui.Secondary}}
)

// fishDue 自動更新的時間點與額外等待（Python fish_due）。
type fishDue struct {
	at    time.Time
	slack time.Duration
}

func rarityTag(r string) string { return fishing.RarityEmoji[r] + " " + fishing.RarityNames[r] }

// num 拉扯數值（Python 狀態都是整數，Go 以 float64 保存）：整數不帶小數點。
func num(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

// ---------- 共用 ----------

// fishCall 執行釣魚操作，結果直接顯示在釣魚畫面，不再重複查詢資料庫。
func (p *Panel) fishCall(ctx context.Context, it ui.Interaction, name string, work func(ctx context.Context) (services.FishingView, error)) {
	var view services.FishingView
	var err error
	_ = p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
		view, err = Call(ctx, p.cmds.Runner, name, work)
		return nil
	})
	if err != nil {
		logUnexpected(err, "釣魚操作失敗", "player", p.playerID)
		p.showError(ctx, it, cleanMessage(PlayerMessage(err)))
		return
	}
	notice := ""
	if view.Stale {
		notice = "⚠️ 剛才那一下已經處理過了，請依最新畫面操作。"
	}
	p.fishCache = &view
	p.Page, p.picker, p.confirm = "fish_game", nil, ""
	p.refresh(ctx, it, notice, nil, "重繪", time.Time{})
}

// scheduleFishTimer 釣魚畫面在上鉤、反應期限或拉扯逾時的時間點自動更新（同一面板只保留一個計時器）。
// 每次畫面送出後呼叫（Python refresh 中的 _schedule_fish_timer）；不在釣魚畫面時只取消舊的計時器。
func (p *Panel) scheduleFishTimer(it ui.Interaction) {
	p.cancelFishTimer()
	if p.Page != "fish_game" || p.fishDue == nil || !p.fishTimersEnabled {
		return
	}
	due := *p.fishDue
	p.fishDue = nil
	delay := time.Until(due.at) + due.slack
	if delay <= 0 && due.at.Equal(p.fishLastDue) {
		return // 同一個期限已經自動更新過一次：不再重複（玩家可以手動操作）
	}
	p.fishLastDue = due.at
	view := p.View
	p.fishTimer = time.AfterFunc(max(0, delay), func() {
		p.cmds.Registry.RunFor(context.Background(), it, view, func(ctx context.Context) error {
			if p.Page == "fish_game" && !view.Stopped() {
				p.refresh(ctx, it, "", nil, "", time.Time{}) // 自動更新不算玩家操作的延遲
			}
			return nil
		})
	})
}

func (p *Panel) cancelFishTimer() {
	if p.fishTimer != nil {
		p.fishTimer.Stop()
		p.fishTimer = nil
	}
}

// ======================================================================
// 釣魚中心
// ======================================================================

type fishingData struct {
	o    services.FishingOverview
	shop services.FishingShop
}

func (p *Panel) loadFishing(ctx context.Context) (any, error) {
	o, err := p.svc().FishingOverview(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	shop, err := p.svc().FishingShop(ctx, p.playerID)
	return fishingData{o, shop}, err
}

func gearText(g *services.GearView) string {
	if g == nil {
		return "❌ 尚未裝備"
	}
	return g.Emoji + " " + g.Name
}

func (p *Panel) buildFishing(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(fishingData)
	o := d.o
	bait := "❌ 尚未裝備"
	if o.Bait != nil {
		bait = fmt.Sprintf("%s **%d** 個", o.Bait.Name, o.BaitQuantity)
	}
	st := o.Stats
	rate := "—"
	if st.Sessions != 0 {
		rate = fmt.Sprintf("%d%%", st.Landed*100/st.Sessions)
	}
	chances := make([]string, len(fishing.Rarities))
	for i, r := range fishing.Rarities {
		chances[i] = fmt.Sprintf("%s%.1f%%", fishing.RarityEmoji[r], o.Chances[r])
	}
	var status string
	switch {
	case o.Active:
		status = "🎣 釣魚中！按「繼續釣魚」回到釣竿旁"
	case o.Rod == nil || o.Line == nil || o.Bait == nil:
		status = "🛒 先到「釣具商店」買魚竿、魚線和魚餌"
	case o.BaitQuantity <= 0:
		status = "🪱 魚餌用完了，到「釣具商店」補充"
	case o.Basket >= o.BasketCapacity:
		status = "🧺 魚簍滿了，先賣掉一些魚"
	default:
		status = "準備釣魚"
	}
	e := &ui.Embed{Title: "🎣 釣魚中心", Color: Sea, Description: fmt.Sprintf(
		"%s（部分魚只在特定季節或時段出現，見魚類圖鑑）\n%s\n🎋 魚竿：%s\n🪢 魚線：%s\n🪱 魚餌：%s\n🧺 魚簍：**%d / %d**\n%s\n"+
			"🌊 目前狀態：**%s**\n\n🎲 這次魚餌的出現機率：%s\n✅ 基礎成功率 **%d%%**（稀有度只影響拉扯難度）\n"+
			"📊 你的紀錄：釣魚 %d 次｜成功 %d（%s）｜🟡 傳說 %d",
		seasons.NowText(time.Now()), Line, gearText(o.Rod), gearText(o.Line), bait, o.Basket, o.BasketCapacity, Line, status,
		strings.Join(chances, "　"), o.BaseSuccessPct, st.Sessions, st.Landed, rate, st.Legendary),
		Footer: "魚上鉤後要在時限內按「拉竿」，拉扯時看 💬 提示：魚衝刺就放線或等待，竄向一邊就壓同方向，別一直收線。"}
	if o.Active {
		p.button("繼續釣魚", func(ctx context.Context, it ui.Interaction) error {
			p.fishCache = nil
			p.goTo(ctx, it, "fish_game", "")
			return nil
		}, btn{emoji: "🎣", style: ui.Success, row: 0})
	} else {
		p.button("開始釣魚", func(ctx context.Context, it ui.Interaction) error {
			p.fishCall(ctx, it, "start_fishing", func(ctx context.Context) (services.FishingView, error) {
				return p.svc().StartFishing(ctx, p.playerID)
			})
			return nil
		}, btn{emoji: "🎣", style: ui.Success, row: 0})
	}
	p.nav("我的魚簍", "fish_basket", btn{emoji: "🎒", row: 0})
	p.nav("魚類圖鑑", "fish_codex", btn{emoji: "📖", row: 0})
	p.nav("釣具商店", "fish_shop", btn{emoji: "🛒", row: 1})
	p.nav("釣魚排行榜", "fish_rank", btn{emoji: "🏆", row: 1})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 1})
	p.equipSelect(d.shop.Items, 2)
	return e, nil
}

func (p *Panel) equipSelect(items []services.GearView, row int) {
	var options []option
	for _, i := range items {
		if !i.Owned || i.Equipped {
			continue
		}
		label := fmt.Sprintf("裝備%s：%s", i.KindName, i.Name)
		if i.Kind == "bait" {
			label += fmt.Sprintf("（%d 個）", i.Quantity)
		}
		options = append(options, option{label, i.Code, i.EffectsText, i.Emoji})
	}
	p.selectMenu("🔧 更換裝備…", options, func(ctx context.Context, it ui.Interaction, code string) error {
		p.act(ctx, it, p.call("equip_fishing_gear", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().EquipFishingGear(ctx, p.playerID, code)
		}), actOpts{})
		return nil
	}, row, "")
}

// ======================================================================
// 釣魚過程
// ======================================================================

func (p *Panel) loadFishGame(ctx context.Context) (any, error) {
	if p.fishCache != nil {
		v := *p.fishCache
		p.fishCache = nil
		return v, nil
	}
	v, err := p.svc().FishingState(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, gameerr.Validationf("還沒有開始釣魚，請到「🎣 釣魚中心」按「開始釣魚」。")
	}
	return *v, nil
}

func (p *Panel) buildFishGame(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.FishingView)
	p.fishDue = nil
	switch v.Phase {
	case "check":
		return p.buildCheck(v), nil
	case "check_failed":
		return p.buildCheckFailed(v), nil
	case "waiting":
		return p.buildWaiting(v), nil
	case "bite":
		return p.buildBite(v), nil
	case "fighting":
		return p.buildFight(v), nil
	}
	return p.buildFishResult(v), nil
}

// buildCheck 人機確認：題目只寫中文名稱，按鈕只有 emoji、順序隨機，答案只在伺服器端比對。
func (p *Panel) buildCheck(v services.FishingView) *ui.Embed {
	note := ""
	if v.Note != "" {
		note = v.Note + "\n\n"
	}
	e := &ui.Embed{Title: "🤖 釣魚人機確認", Color: Gold, Description: fmt.Sprintf(
		"%s請按下 **%s**\n\n%s\n⏰ %s前作答，答對就會直接拋竿。\n⚠️ 答錯會暫停釣魚一段時間（不會扣金幣、魚或魚餌）。", note, v.Target, Line, ts(v.ExpiresAt))}
	for _, o := range v.Options {
		code := o.Code
		p.button("", func(ctx context.Context, it ui.Interaction) error {
			p.fishCall(ctx, it, "answer_fishing_check", func(ctx context.Context) (services.FishingView, error) {
				return p.svc().AnswerFishingCheck(ctx, p.playerID, code)
			})
			return nil
		}, btn{emoji: o.Emoji, row: 0})
	}
	p.nav("釣魚中心", "fishing", btn{emoji: "↩️", row: 1})
	return e
}

func (p *Panel) buildCheckFailed(v services.FishingView) *ui.Embed {
	e := &ui.Embed{Title: "🚫 人機確認沒有通過", Color: Red, Description: fmt.Sprintf(
		"釣魚暫停 **%d** 分鐘，%s可以再拋竿。\n\n%s\n金幣、魚和魚餌都沒有被扣。農場的其他功能照常可以使用。", v.Minutes, ts(v.LockedUntil), Line)}
	p.nav("釣魚中心", "fishing", btn{emoji: "↩️", row: 0})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 0})
	return e
}

func (p *Panel) giveUpButton(v services.FishingView, row int) {
	id := v.ID
	p.button("收竿放棄", func(ctx context.Context, it ui.Interaction) error {
		p.fishCall(ctx, it, "give_up_fishing", func(ctx context.Context) (services.FishingView, error) {
			return p.svc().GiveUpFishing(ctx, p.playerID, id)
		})
		return nil
	}, btn{emoji: "🛑", row: row, style: ui.Danger})
}

func (p *Panel) buildWaiting(v services.FishingView) *ui.Embed {
	p.fishDue = &fishDue{v.BiteAt, timerSlack}
	e := &ui.Embed{Title: "🎣 你已經拋出魚竿！", Color: Sea, Description: fmt.Sprintf(
		"🌊 水面很平靜……\n\n🐟 好像有東西靠近了！\n\n%s\n🎋 %s　🪢 %s　🪱 %s\n👀 魚上鉤時這個畫面會自動更新，「🎣 拉竿」按鈕亮起就快按！",
		Line, pyStr(v.Rod), pyStr(v.Line), pyStr(v.Bait))}
	p.button("拉竿", p.noop, btn{emoji: "🎣", row: 0, disabled: true})
	p.giveUpButton(v, 0)
	return e
}

// pyStr Python f"{x}"：None 顯示為 "None"。
func pyStr(s *string) string {
	if s == nil {
		return "None"
	}
	return *s
}

func (p *Panel) buildBite(v services.FishingView) *ui.Embed {
	p.fishDue = &fishDue{v.ReactDeadline, deadlineSlack}
	id := v.ID
	e := &ui.Embed{Title: "‼️ 魚上鉤了！", Color: Red, Description: fmt.Sprintf("⚡ **快點拉竿！**\n\n⏰ %s之前按下「🎣 拉竿」\n\n%s", tsv(v.ReactDeadline), Line)}
	p.button("拉竿", func(ctx context.Context, it ui.Interaction) error {
		p.fishCall(ctx, it, "hook_fish", func(ctx context.Context) (services.FishingView, error) {
			return p.svc().HookFish(ctx, p.playerID, id)
		})
		return nil
	}, btn{emoji: "🎣", row: 0, style: ui.Success})
	p.giveUpButton(v, 0)
	return e
}

func (p *Panel) buildFight(v services.FishingView) *ui.Embed {
	if v.ActionDeadline != nil {
		p.fishDue = &fishDue{*v.ActionDeadline, deadlineSlack}
	}
	tensionWarn, farWarn := "", ""
	if v.Tension >= v.MaxTension*0.75 {
		tensionWarn = " ⚠️"
	}
	if v.Distance >= float64(v.EscapeDistance)*0.8 {
		farWarn = " ⚠️"
	}
	hint := fmt.Sprintf("🎣 收線張力 +%d", v.NextReelTension)
	if v.ReelStreak != 0 {
		hint = fmt.Sprintf("🔥 連續收線 ×%s，下一次收線張力 **+%d**", num(v.ReelStreak), v.NextReelTension)
	}
	hints := []string{hint}
	if v.Need != 0 {
		hints = append([]string{fmt.Sprintf("🎯 還要成功拉 **%d** 下（%s%s）", v.PullsLeft, strings.Repeat("🟢", v.Progress),
			strings.Repeat("⚪", max(0, v.PullsLeft)))}, hints...)
	}
	prompt := fmt.Sprintf("💬 **%s**", v.IntentText)
	if v.Dodge != "" {
		prompt = fmt.Sprintf("💬 **%s**\n💬 %s", v.DodgeText, v.IntentText)
	}
	last := ""
	if v.LastText != nil && *v.LastText != "" {
		last = fmt.Sprintf("📝 %s\n", *v.LastText)
	}
	e := &ui.Embed{Title: "🎣 正在與魚搏鬥！", Color: rarityColor[v.Rarity], Description: fmt.Sprintf(
		"🐟 對手：**%s**級的魚　🔁 第 %d 回合\n%s\n"+
			"🐟 魚的體力：**%s / %s**\n%s\n\n"+
			"📏 魚的距離：**%s / %d**%s\n%s\n\n"+
			"🪢 魚線張力：**%s / %s**%s\n%s\n%s\n%s\n%s\n%s⏰ %s內要操作，不然魚會跑掉",
		rarityTag(v.Rarity), v.Turn+1, Line,
		num(v.Stamina), num(v.MaxStamina), fbar(v.Stamina, v.MaxStamina),
		num(v.Distance), v.EscapeDistance, farWarn, fbar(v.Distance, float64(v.EscapeDistance)),
		num(v.Tension), num(v.MaxTension), tensionWarn, fbar(v.Tension, v.MaxTension), Line,
		prompt, strings.Join(hints, "\n"), last, ts(v.ActionDeadline)),
		Footer: "收線成功算 1 下，連續收線張力越來越高｜等待：張力下降｜放線：張力大降、魚游遠｜魚竄向一邊：一定要壓同方向（算 1 下），按其他鍵都會被扯。"}
	id, turn := v.ID, v.Turn
	for _, b := range fightButtons {
		action := b.action
		p.button(fishing.ActionNames[action], func(ctx context.Context, it ui.Interaction) error {
			p.fishCall(ctx, it, "fishing_action", func(ctx context.Context) (services.FishingView, error) {
				return p.svc().FishingAction(ctx, p.playerID, id, action, turn)
			})
			return nil
		}, btn{emoji: b.emoji, row: 0, style: b.style})
	}
	p.giveUpButton(v, 1)
	return e
}

// fbar Python _bar（寬度 10）：數值為 float。
func fbar(value, total float64) string {
	filled := 0
	if total > 0 {
		filled = min(10, max(0, numfmt.Round(10*value/total)))
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", 10-filled)
}

func (p *Panel) buildFishResult(v services.FishingView) *ui.Embed {
	again := func(ctx context.Context, it ui.Interaction) error {
		p.fishCall(ctx, it, "start_fishing", func(ctx context.Context) (services.FishingView, error) {
			return p.svc().StartFishing(ctx, p.playerID)
		})
		return nil
	}
	var e *ui.Embed
	if fish := v.Fish; v.Status == "landed" && fish != nil {
		record := ""
		if v.Record != nil && v.Record.Species {
			record = "🏆 個人紀錄：你釣過最重的" + fish.Name + "！\n"
			if v.Record.Overall {
				record = "🏆 個人紀錄：你釣過最重的魚！\n"
			}
		}
		e = &ui.Embed{Title: fmt.Sprintf("🎉 釣到了 %s %s！", fish.Emoji, fish.Name), Color: rarityColor[fish.Rarity], Description: fmt.Sprintf(
			"%s\n🐟 魚類：**%s**\n%s 稀有度：**%s**\n⚖️ 重量：**%s**\n💰 價值：**%d** 金幣\n%s%s\n%s\n🧺 已放進魚簍",
			Line, fish.Name, fish.RarityEmoji, fish.RarityName, fish.WeightText, fish.Price, record, Line, celebrate[fish.Rarity])}
		id := fish.ID
		p.fishSelected = &id
		p.button("再釣一次", again, btn{emoji: "🎣", style: ui.Success, row: 0})
		p.button("分享到群組", func(ctx context.Context, it ui.Interaction) error {
			p.shareFish(ctx, it, id, false)
			return nil
		}, btn{emoji: "📢", row: 0, disabled: p.groupID == nil})
	} else {
		e = &ui.Embed{Title: "😢 " + v.StatusName, Color: Grey, Description: fmt.Sprintf(
			"%s\n\n%s\n這次的對手是 **%s** 級的魚。\n💡 拉扯時看 💬 提示：魚衝刺時先放線或等待，竄向一邊就壓同方向，平靜、累了的時候收線；別連續收線，張力會越來越高。",
			deref(v.LastText), Line, rarityTag(v.Rarity))}
		p.button("再釣一次", again, btn{emoji: "🎣", style: ui.Success, row: 0})
	}
	p.nav("我的魚簍", "fish_basket", btn{emoji: "🎒", row: 0})
	p.nav("釣魚中心", "fishing", btn{emoji: "↩️", row: 0})
	e.AddField("🎒 剩餘資源", suppliesText(v), false)
	return e
}

// suppliesText 結算畫面：剩餘魚餌與魚簍容量；用完或滿了就提醒，不用等到拋竿才知道。
func suppliesText(v services.FishingView) string {
	lines := []string{"🪱 魚餌：❌ 尚未裝備"}
	if v.BaitLeft != nil {
		lines[0] = fmt.Sprintf("🪱 魚餌：%s 剩 **%d** 個", v.BaitLeft.Name, v.BaitLeft.Quantity)
	}
	lines = append(lines, fmt.Sprintf("🧺 魚簍：**%d / %d** 隻（還能放 **%d** 隻）", v.Basket, v.BasketCapacity, max(0, v.BasketCapacity-v.Basket)))
	if v.BaitLeft != nil && v.BaitLeft.Quantity <= 0 {
		lines = append(lines, "⚠️ 魚餌用完了，要先到釣具商店補充。")
	}
	if v.Basket >= v.BasketCapacity {
		lines = append(lines, "⚠️ 魚簍滿了，要先賣掉一些魚才能再釣。")
	}
	return strings.Join(lines, "\n")
}

// ======================================================================
// 魚簍、魚的詳細資料與販售
// ======================================================================

func (p *Panel) loadFishBasket(ctx context.Context) (any, error) {
	return p.svc().FishBasket(ctx, p.playerID)
}

func (p *Panel) sellFish(ids []int64) func(ctx context.Context) (string, error) {
	return p.call("sell_fish", func(ctx context.Context) (services.ActionResult, error) {
		return p.svc().SellFish(ctx, p.playerID, ids)
	})
}

func (p *Panel) buildFishBasket(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.FishBasket)
	fish := d.Fish
	pages := pageCount(len(fish), basketPage)
	p.fishPage = min(p.fishPage, pages-1)
	shown := fish[min(len(fish), p.fishPage*basketPage):min(len(fish), (p.fishPage+1)*basketPage)]
	lines := make([]string, len(shown))
	for i, f := range shown {
		lines[i] = fishLine(f, p.fishPage*basketPage+i+1)
	}
	list := "魚簍空空的，去釣魚吧！🎣"
	if len(lines) > 0 {
		list = strings.Join(lines, "\n")
	}
	e := &ui.Embed{Title: "🎒 我的魚簍", Color: Sea, Description: runes(fmt.Sprintf("🧺 **%d / %d** 隻　💰 全部價值 **%s** 金　💳 金幣 **%s**\n%s\n%s",
		len(fish), d.Capacity, money(d.TotalValue), money(d.Gold), Line, list), 4000)}
	if pages > 1 {
		e.Footer = fmt.Sprintf("第 %d / %d 頁　🏆 = 該魚種的個人最重紀錄", p.fishPage+1, pages)
	} else {
		e.Footer = "🏆 = 該魚種的個人最重紀錄。選一隻魚可以單獨出售或分享到群組。"
	}
	var options []option
	for _, f := range shown {
		options = append(options, option{f.Name + " " + f.WeightText, fmt.Sprint(f.ID), fmt.Sprintf("%s｜💰 %d 金", f.RarityName, f.Price), f.RarityEmoji})
	}
	p.selectMenu("🐟 選一隻魚查看、出售或分享…", options, func(ctx context.Context, it ui.Interaction, value string) error {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return gameerr.Validationf("找不到這個項目。")
		}
		p.fishSelected = &id
		p.goTo(ctx, it, "fish_detail", "")
		return nil
	}, 0, "")
	if len(shown) > 0 {
		multi := &ui.Select{Placeholder: "💰 勾選多隻魚一起出售…", Row: 1, MinValues: 1, MaxValues: len(shown),
			OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
				var ids []int64
				for _, v := range values {
					id, err := strconv.ParseInt(v, 10, 64)
					if err != nil {
						return gameerr.Validationf("找不到這個項目。")
					}
					ids = append(ids, id)
				}
				p.fishSellIDs, p.confirm = ids, ""
				p.refresh(ctx, it, "👇 確認要賣的魚，再按「確認出售」。", nil, "重繪", time.Time{})
				return nil
			}}
		for _, f := range shown {
			multi.Options = append(multi.Options, ui.SelectOption{Label: runes(f.Name+" "+f.WeightText, 100), Value: fmt.Sprint(f.ID),
				Description: runes(fmt.Sprintf("💰 %d 金", f.Price), 100), Emoji: f.RarityEmoji})
		}
		p.View.Add(multi)
	}
	if len(p.fishSellIDs) > 0 {
		var chosen []services.BasketFish
		total := 0
		for _, f := range fish {
			if slices.Contains(p.fishSellIDs, f.ID) {
				chosen = append(chosen, f)
				total += f.Price
			}
		}
		chosenLines := make([]string, len(chosen))
		for i, f := range chosen {
			chosenLines[i] = fishLine(f, 0)
		}
		e.AddField("⚠️ 確認出售", runes(strings.Join(chosenLines, "\n"), 900)+fmt.Sprintf("\n共 **%d** 隻，**%s** 金幣", len(chosen), money(total)), false)
		p.button(fmt.Sprintf("確認出售 %d 隻（%s 金）", len(chosen), money(total)), func(ctx context.Context, it ui.Interaction) error {
			ids := p.fishSellIDs
			p.fishSellIDs = nil
			p.act(ctx, it, p.sellFish(ids), actOpts{})
			return nil
		}, btn{emoji: "✅", row: 2, style: ui.Danger})
		p.button("取消", func(ctx context.Context, it ui.Interaction) error {
			p.fishSellIDs = nil
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "✖️", row: 2})
	}
	confirming := p.confirm == "fish_sell_all"
	label, style := "全部出售", ui.Success
	if confirming {
		label, style = "確認全部出售", ui.Danger
	}
	p.button(label, func(ctx context.Context, it ui.Interaction) error {
		if p.confirm != "fish_sell_all" {
			p.confirm = "fish_sell_all"
			p.refresh(ctx, it, fmt.Sprintf("⚠️ 確定要把魚簍裡全部 %d 隻魚賣掉嗎（共 %s 金幣）？再按一次「確認全部出售」。", len(fish), money(d.TotalValue)),
				nil, "重繪", time.Time{})
			return nil
		}
		p.act(ctx, it, p.sellFish(nil), actOpts{})
		return nil
	}, btn{emoji: "💰", row: 3, disabled: len(fish) == 0, style: style})
	p.button("展示最珍貴的魚", func(ctx context.Context, it ui.Interaction) error {
		p.shareFish(ctx, it, fish[0].ID, true)
		return nil
	}, btn{emoji: "🏆", row: 3, disabled: len(fish) == 0 || p.groupID == nil})
	if pages > 1 {
		turn := func(step int) ui.Handler {
			return func(ctx context.Context, it ui.Interaction) error {
				p.fishPage = ((p.fishPage+step)%pages + pages) % pages
				p.fishSellIDs = nil
				p.refresh(ctx, it, "", nil, "重繪", time.Time{})
				return nil
			}
		}
		p.button("上一頁", turn(-1), btn{emoji: "◀️", row: 3})
		p.button("下一頁", turn(1), btn{emoji: "▶️", row: 3})
	}
	p.nav("釣魚中心", "fishing", btn{emoji: "🎣", row: 4})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 4})
	return e, nil
}

func (p *Panel) loadFishDetail(ctx context.Context) (any, error) {
	if p.fishSelected == nil {
		return nil, gameerr.Validationf("請先在魚簍選一隻魚。")
	}
	basket, err := p.svc().FishBasket(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(basket.Fish, func(f services.BasketFish) bool { return f.ID == *p.fishSelected })
	if i < 0 {
		return nil, gameerr.Validationf("這隻魚已經不在魚簍裡了（可能已經賣掉）。")
	}
	return basket.Fish[i], nil
}

func (p *Panel) buildFishDetail(_ context.Context, data any) (*ui.Embed, error) {
	f := data.(services.BasketFish)
	record := ""
	if f.Record {
		record = "🏆 這是你釣過最重的" + f.Name + "！\n"
	}
	e := &ui.Embed{Title: f.Emoji + " " + f.Name, Color: rarityColor[f.Rarity], Description: fmt.Sprintf(
		"%s\n%s 稀有度：**%s**\n⚖️ 重量：**%s**\n💰 價值：**%d** 金幣\n🕒 釣到時間：%s\n%s%s",
		Line, f.RarityEmoji, f.RarityName, f.WeightText, f.Price, tsv(f.CaughtAt, "f"), record, Line)}
	id := f.ID
	confirming := p.confirm == "fish_sell_one"
	p.button("分享到群組", func(ctx context.Context, it ui.Interaction) error {
		p.shareFish(ctx, it, id, false)
		return nil
	}, btn{emoji: "📢", row: 0, style: ui.Primary, disabled: p.groupID == nil})
	label, style := fmt.Sprintf("出售（%d 金）", f.Price), ui.Success
	if confirming {
		label, style = "確認出售", ui.Danger
	}
	p.button(label, func(ctx context.Context, it ui.Interaction) error {
		if p.confirm != "fish_sell_one" {
			p.confirm = "fish_sell_one"
			p.refresh(ctx, it, fmt.Sprintf("⚠️ 確定要賣掉這隻%s（%d 金幣）嗎？再按一次「確認出售」。", f.Name, f.Price), nil, "重繪", time.Time{})
			return nil
		}
		p.act(ctx, it, p.sellFish([]int64{id}), actOpts{page: "fish_basket"})
		return nil
	}, btn{emoji: "💰", row: 0, style: style})
	p.nav("回魚簍", "fish_basket", btn{emoji: "↩️", row: 0})
	return e, nil
}

// shareFish 分享（沿用分享農場：在目前頻道發送公開訊息）。
func (p *Panel) shareFish(ctx context.Context, it ui.Interaction, fishID int64, showcase bool) {
	_ = it.DeferUpdateIfPending(ctx)
	var card services.SharedFish
	err := p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
		var err error
		card, err = Call(ctx, p.cmds.Runner, "share_fish", func(ctx context.Context) (services.SharedFish, error) {
			return p.svc().ShareFish(ctx, p.playerID, fishID)
		})
		return err
	})
	if err != nil {
		logUnexpected(err, "分享魚類失敗", "player", p.playerID)
		p.showError(ctx, it, PlayerMessage(err))
		return
	}
	title := fmt.Sprintf("🎣 %s 釣到了一隻%s魚！", card.PlayerName, card.RarityName)
	if showcase {
		title = fmt.Sprintf("🏆 %s 展示最珍貴的收藏！", card.PlayerName)
	}
	record := ""
	if card.Record.Species {
		record = "🏆 個人紀錄：目前最大捕獲重量\n"
	}
	e := &ui.Embed{Title: title, Color: rarityColor[card.Rarity], Description: fmt.Sprintf(
		"%s\n%s 魚類：**%s**\n%s 稀有度：**%s**\n⚖️ 重量：**%s**\n💰 價值：**%d** 金幣\n%s%s\n%s\n👤 捕獲玩家：**%s**",
		Line, card.Emoji, card.Name, card.RarityEmoji, card.RarityName, card.WeightText, card.Price, record, Line, celebrate[card.Rarity], card.PlayerName)}
	notice := "📢 已分享到這個頻道！（分享不會消耗魚）"
	if _, err := it.Followup(ctx, ui.Message{Embed: e}); err != nil {
		slog.Warn("分享魚類失敗", "type", fmt.Sprintf("%T", err))
		notice = "⚠️ 訊息沒有送出（Bot 可能沒有這個頻道的發言權限）。"
	}
	p.refresh(ctx, it, notice, nil, "重繪", time.Time{})
}

// ======================================================================
// 圖鑑與排行榜
// ======================================================================

func (p *Panel) loadFishCodex(ctx context.Context) (any, error) {
	return p.svc().FishCodex(ctx, p.playerID)
}

func (p *Panel) buildFishCodex(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.FishCodex)
	var blocks []string
	for _, r := range fishing.Rarities {
		var rows []services.FishCodexEntry
		var prices []fishing.PriceRow
		for _, x := range d.Entries {
			if x.Rarity == r {
				rows = append(rows, x)
				prices = append(prices, fishing.PriceRow{Rarity: x.Rarity, MinPrice: x.MinPrice, MaxPrice: x.MaxPrice})
			}
		}
		if len(rows) == 0 {
			continue
		}
		lines := make([]string, len(rows))
		for i, x := range rows {
			line := fmt.Sprintf("❓ ？？？　💰 %d–%d", x.MinPrice, x.MaxPrice)
			if x.Caught {
				best := 0
				if x.BestWeight != nil {
					best = *x.BestWeight
				}
				line = fmt.Sprintf("%s **%s** ×%d　最重 %s　💰 %d–%d", x.Emoji, x.Name, x.Count, fishing.WeightText(best), x.MinPrice, x.MaxPrice)
			}
			if len(x.BaitNames) > 0 {
				line += "　🔒 需要" + strings.Join(x.BaitNames, "或")
			}
			if x.LimitText != "" {
				line += "　" + x.LimitText
			}
			lines[i] = line
		}
		lo, hi, _ := fishing.PriceSpan(prices, r)
		blocks = append(blocks, fmt.Sprintf("**%s**（%d–%d 金）\n%s", rarityTag(r), lo, hi, strings.Join(lines, "\n")))
	}
	e := &ui.Embed{Title: "📖 魚類圖鑑", Color: Sea, Description: runes(fmt.Sprintf("已發現 **%d / %d** 種\n%s\n%s", d.Discovered, d.Total, Line,
		strings.Join(blocks, "\n\n")), 4000),
		Footer: "釣到一次就會登記；賣掉的魚也算。❓ 是還沒釣過的魚。🔒 要裝備指定的魚餌才釣得到；季節依台灣月份（春 3–5、夏 6–8、秋 9–11、冬 12–2 月），時段是台灣時間。"}
	p.nav("釣魚中心", "fishing", btn{emoji: "🎣", row: 0})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 0})
	return e, nil
}

type fishRankData struct {
	board  fishrepo.Leaderboard
	global bool
}

func (p *Panel) loadFishRank(ctx context.Context) (any, error) {
	scope := p.groupID
	if p.fishRankGlobal {
		scope = nil
	}
	b, err := p.svc().FishingLeaderboard(ctx, scope)
	return fishRankData{b, scope == nil}, err
}

func (p *Panel) buildFishRank(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(fishRankData)
	medal := func(i int) string { return medalOr(i, fmt.Sprintf("`%2d`", i)) }
	catches := make([]string, len(d.board.Catches))
	for i, x := range d.board.Catches {
		catches[i] = fmt.Sprintf("%s **%s**　🐟 %d 隻　🟡 %d", medal(i+1), x.PlayerName, x.Count, x.Legendary)
	}
	best := make([]string, len(d.board.Best))
	for i, x := range d.board.Best {
		best[i] = fmt.Sprintf("%s **%s**　%s%s %s %s　💰 %d", medal(i+1), x.PlayerName, fishing.RarityEmoji[x.Rarity], x.Emoji, x.FishName,
			fishing.WeightText(x.WeightG), x.Price)
	}
	orNone := func(lines []string) string {
		if len(lines) == 0 {
			return "還沒有人釣到魚"
		}
		return strings.Join(lines, "\n")
	}
	scope := "本伺服器"
	if d.global {
		scope = "全部玩家"
	}
	e := &ui.Embed{Title: fmt.Sprintf("🏆 釣魚排行榜（%s）", scope), Color: Gold, Description: runes(
		"**🐟 捕獲數**\n"+orNone(catches)+fmt.Sprintf("\n\n%s\n**💎 最有價值的一隻魚**\n", Line)+orNone(best), 4000)}
	if p.groupID != nil {
		label := "看全部玩家"
		if d.global {
			label = "看本伺服器"
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			p.fishRankGlobal = !p.fishRankGlobal
			p.refresh(ctx, it, "", nil, "重繪", time.Time{})
			return nil
		}, btn{emoji: "🔁", row: 0})
	}
	p.nav("釣魚中心", "fishing", btn{emoji: "🎣", row: 0})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 0})
	return e, nil
}

// ======================================================================
// 釣具商店（商店的第二個分頁）
// ======================================================================

func (p *Panel) loadFishShop(ctx context.Context) (any, error) {
	return p.svc().FishingShop(ctx, p.playerID)
}

func (p *Panel) buildFishShop(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(services.FishingShop)
	selected := ""
	if p.picker != nil {
		selected = p.picker.Key
	}
	blocks := map[string][]string{}
	for _, i := range d.Items {
		if !i.Active && !i.Owned {
			continue
		}
		var state string
		equipped := ""
		if i.Equipped {
			equipped = "　🟢 裝備中"
		}
		switch {
		case !i.Unlocked:
			state = fmt.Sprintf("🔒 農場 Lv.%d 解鎖", i.RequiredLevel)
		case i.Kind == "bait":
			state = fmt.Sprintf("🎒 持有 **%d**", i.Quantity) + equipped
		case i.Owned:
			state = "✅ 已擁有" + equipped
		default:
			state = "🛒 可購買"
		}
		stock := ""
		if i.Stock != nil {
			stock = fmt.Sprintf("　📦 庫存 %d", *i.Stock)
		}
		mark, unit := "", ""
		if "fish:"+i.Code == selected {
			mark = "▶ "
		}
		if i.Kind == "bait" {
			unit = " ／個"
		}
		blocks[i.Kind] = append(blocks[i.Kind], fmt.Sprintf("%s%s **%s**　💰 **%s** 金%s　%s%s\n　📝 %s\n　✨ %s", mark, i.Emoji, i.Name,
			money(i.Price), unit, state, stock, i.Description, i.EffectsText))
	}
	parts := []string{fmt.Sprintf("💰 你的金幣：**%s**　⭐ 農場 Lv **%d**", money(d.Gold), d.Level)}
	for _, kind := range fishing.GearKinds {
		if len(blocks[kind]) > 0 {
			parts = append(parts, fmt.Sprintf("**%s %s**\n", fishing.GearKindEmoji[kind], fishing.GearKindNames[kind])+strings.Join(blocks[kind], "\n\n"))
		}
	}
	e := &ui.Embed{Title: "🎣 ✦ 釣具商店 ✦", Color: Sea, Description: runes(strings.Join(parts, "\n\n"), 4000)}
	if p.picker != nil {
		hint := "👇 按下方的數量按鈕購買"
		if p.picker.Confirm {
			hint = "👇 按「確認購買」完成購買"
		}
		e.AddField("🛒 已選擇", fmt.Sprintf("**%s**（%s）\n%s", p.picker.Label, p.picker.PriceText, hint), false)
	} else {
		e.Footer = "魚竿、魚線、魚餌可以分別裝備；買第一件時會自動裝備。使用農場金幣購買。"
	}
	pick := func(ctx context.Context, it ui.Interaction, key string) error {
		code := strings.TrimPrefix(key, "fish:")
		idx := slices.IndexFunc(d.Items, func(g services.GearView) bool { return g.Code == code })
		if idx < 0 {
			return gameerr.NotFoundf("找不到這個商品。")
		}
		i := d.Items[idx]
		buy := func(ctx context.Context, q int) (string, error) {
			r, err := Call(ctx, p.cmds.Runner, "buy_fishing_gear", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuyFishingGear(ctx, p.playerID, code, q)
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s\n💰 剩餘金幣 **%s**", r.Message, money(r.Gold)), nil
		}
		label := i.Emoji + " " + i.Name
		if i.Kind == "bait" {
			p.picker = &Picker{Key: key, Label: label, PriceText: fmt.Sprintf("每個 %d 金", i.Price), Verb: "買",
				Max: max(1, min(fishing.BaitMaxBuy, d.Gold/max(1, i.Price))), Presets: []int{1, 10, 50}, Keep: true, Run: buy}
		} else {
			p.picker = &Picker{Key: key, Label: label, PriceText: money(i.Price) + " 金", Confirm: true, Max: 1, Run: buy}
		}
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}
	var options []option
	for _, i := range d.Items {
		if i.Active && i.Unlocked && (i.Kind == "bait" || !i.Owned) && (i.Stock == nil || *i.Stock > 0) {
			options = append(options, option{i.Name, "fish:" + i.Code, fmt.Sprintf("%s｜💰 %s 金｜%s", i.KindName, money(i.Price), i.EffectsText), i.Emoji})
		}
	}
	p.selectMenu("🛒 選擇要買的釣具…", options, pick, 0, selected)
	p.quantityButtons(1)
	p.equipSelect(d.Items, 2)
	p.nav("農場商店", "shop", btn{emoji: "🌾", row: 4})
	p.button("釣具商店", p.noop, btn{emoji: "🎣", row: 4, style: ui.Primary, disabled: true})
	p.nav("特賣與升級", "shop_special", btn{emoji: "🎁", row: 4})
	p.nav("釣魚中心", "fishing", btn{emoji: "🎣", row: 4})
	p.nav("回總覽", "main", btn{emoji: "🏠", row: 4})
	return e, nil
}
