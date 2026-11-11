package bot

// 子面板：世界 Boss（對應 Python world_boss_panel.py 的 WorldBossPages）。
//
//   - 介紹區（boss、boss_board、boss_help）附固定圖（world_boss.png）；區域內換頁沿用訊息上的附件（比對雜湊）。
//   - 戰鬥區（boss_battle、boss_donate、boss_live_board）不附圖片，只有數值面板。
//   - 捐獻流程：選類別 → 選物品 → 選數量 → 確認。確認時帶畫面產生的 request_id，重複點擊同一個確認按鈕只會處理一次
//     （面板先清除待確認狀態，服務層再以唯一限制擋住）；取消不會扣任何物品。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/gameerr"
	wb "farmgm/internal/game/worldboss"
	"farmgm/internal/services"
)

// 配色取自 dragon.png：魔龍的紫色、火焰的橘紅色、皇冠的金色。
const (
	BossPurple = 0x5B2A86
	BossFlame  = 0xE8590C
	BossGold   = 0xF1C40F
	BossGrey   = 0x99AAB5
)

var (
	bossStateEmoji = map[string]string{"scheduled": "⏳", "active": "🔥", "paused": "⏸️", "ended": "🏁", "defeated": "💀"}
	claimLabels    = map[string]string{
		"pending": "⏳ 活動結束後可領取", "ready": "🎁 可以領取！", "claimed": "✅ 已領取", "none": "— 沒有參與，無獎勵",
		"below_min": "— 積分未達參與門檻，無獎勵",
	}
	bossQuantityPresets = []int{1, 5, 10}
)

// bossDonation 一筆待確認的捐獻。
type bossDonation struct {
	item      services.BossDonationOption
	quantity  int
	requestID string
}

// bossPending 待確認的捐獻（Python boss_pending）：單一物品，或漁獲勾選多種時的 batch。
type bossPending struct {
	single *bossDonation
	batch  []bossDonation
}

// pyFloat Python str(float)：整數值也帶「.0」（例如 round(x, 1) 的 100.0）。
func pyFloat(x float64) string {
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	return s
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func pointsText(o services.BossDonationOption) string {
	if o.PointsMin == o.PointsMax {
		return fmt.Sprintf("%d 分", o.PointsMin)
	}
	return fmt.Sprintf("%d–%d 分", o.PointsMin, o.PointsMax)
}

func bossColor(state string) int {
	switch state {
	case "defeated":
		return BossGold
	case "ended":
		return BossGrey
	case "active":
		return BossFlame
	}
	return BossPurple
}

func medalOr(rank int, fallback string) string {
	if m, ok := Medals[rank]; ok {
		return m
	}
	return fallback
}

func (p *Panel) bossView(ctx context.Context) (*services.BossView, error) {
	return p.svc().BossCurrent(ctx, p.groupID, p.playerID, p.bossSelected)
}

func (p *Panel) bossImage() []byte { return p.image("世界 Boss", farmimage.WorldBossPNG) }

func (p *Panel) bossReset() {
	p.bossCategory, p.bossOptions, p.bossItem, p.bossPending = "", nil, nil, nil
}

// bossGoto 換到世界 Boss 的頁面（回介紹區、進入戰鬥區前清除捐獻流程）。
func (p *Panel) bossGoto(page string, reset bool) ui.Handler {
	return func(ctx context.Context, it ui.Interaction) error {
		if reset {
			p.bossReset()
		}
		p.goTo(ctx, it, page, "")
		return nil
	}
}

func (p *Panel) bossClaimButton(v *services.BossView, row int) {
	eventID := v.EventID
	p.button("領取獎勵", func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("boss_claim", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().BossClaim(ctx, p.groupID, p.playerID, eventID)
		}), actOpts{})
		return nil
	}, btn{emoji: "🎁", style: ui.Primary, row: row, disabled: bossMe(v).ClaimState != "ready"})
}

func bossMe(v *services.BossView) services.BossMyStatus {
	if v.Me == nil {
		return services.BossMyStatus{ClaimState: "none"}
	}
	return *v.Me
}

func bossHeader(v *services.BossView) string {
	var when string
	switch v.State {
	case "scheduled":
		when = fmt.Sprintf("⏳ 開始時間：%s（%s）", tsv(v.StartsAt, "f"), tsv(v.StartsAt))
	case "ended", "defeated":
		when = "🏁 結束時間：" + ts(v.EndedAt, "f")
	default:
		when = fmt.Sprintf("⏰ 剩餘時間：%s（%s 結束）", tsv(v.EndsAt), tsv(v.EndsAt, "f"))
	}
	return fmt.Sprintf("### %s\n%s　狀態：%s **%s**\n❤️ HP **%s** / %s（%s%%）\n`%s`\n%s", v.Name, v.ScopeLabel, bossStateEmoji[v.State],
		v.StateLabel, money(v.HP), money(v.MaxHP), pyFloat(v.HPPct), wb.Bar(float64(v.HP), float64(v.MaxHP), 20), when)
}

func supplyLines(v *services.BossView) string {
	lines := make([]string, len(v.Supplies))
	for i, s := range v.Supplies {
		lines[i] = fmt.Sprintf("%s **%s**（%s）\n`%s` %s / %s（%s%%）", s.Emoji, s.Name, s.Effect,
			wb.Bar(float64(min(s.Progress, s.Target)), float64(s.Target), 12), money(s.Progress), money(s.Target), pyFloat(s.RatioPct))
	}
	return strings.Join(lines, "\n")
}

// bossMineExtra 參與門檻與稱號（介紹區與戰鬥區的個人欄共用）。
func bossMineExtra(v *services.BossView) string {
	me, extra := bossMe(v), ""
	need := 0
	if v.RewardsText != nil {
		need = v.RewardsText.MinPoints
	}
	if need != 0 && (me.ClaimState == "pending" || me.ClaimState == "none" || me.ClaimState == "below_min") {
		extra += fmt.Sprintf("\n🎯 參與門檻：積分達 **%s** 才有獎勵", money(need))
	}
	if me.Title != nil && *me.Title != "" {
		extra += fmt.Sprintf("\n🎖️ 稱號：**%s**", *me.Title)
	}
	return extra
}

func bossRank(me services.BossMyStatus) string {
	if me.Rank != nil && *me.Rank != 0 {
		return fmt.Sprintf("第 %d 名 / %d 人", *me.Rank, me.Participants)
	}
	return "尚未上榜"
}

// ---------- 介紹區（附圖） ----------

type bossData struct{ v *services.BossView }

func (p *Panel) loadBoss(ctx context.Context) (any, error) {
	v, err := p.bossView(ctx)
	if err != nil {
		return nil, err
	}
	p.setBossEvent(v)
	return bossData{v}, nil
}

func (p *Panel) setBossEvent(v *services.BossView) {
	p.bossEventID = nil
	if v != nil {
		id := v.EventID
		p.bossEventID = &id
	}
}

func (p *Panel) buildBoss(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(bossData).v
	if v == nil {
		e := &ui.Embed{Title: "🐉 世界 Boss", Color: BossGrey, Description: "目前沒有世界 Boss 活動。\n活動由管理員在監控網站建立或排定，出現後這裡會顯示 Boss 與補給進度。"}
		p.attachImage(e, p.bossImage())
		p.button("活動說明", p.bossGoto("boss_help", false), btn{emoji: "📖", row: 0})
		p.button("更新狀態", p.bossGoto("boss", true), btn{emoji: "🔄", row: 0})
		p.navBack(0)
		return e, nil
	}
	me := bossMe(v)
	e := &ui.Embed{Title: "🐉 世界 Boss｜" + v.Name, Color: bossColor(v.State), Description: bossHeader(v)}
	p.attachImage(e, p.bossImage())
	enter := fmt.Sprintf("按「⚔️ 進入%s」查看補給、戰況與個人貢獻。", v.Name)
	if v.State == "active" {
		enter = fmt.Sprintf("按「⚔️ 進入%s」前往戰鬥區：查看補給與戰況，捐獻物資支援前線。", v.Name)
	}
	e.AddField("🗺️ 前往戰場", enter, false)
	mine := fmt.Sprintf("🏅 貢獻積分 **%s**　📊 %s\n🎁 獎勵：%s", money(me.Points), bossRank(me), claimLabels[me.ClaimState])
	e.AddField("🙋 我的參與", mine+bossMineExtra(v), false)
	e.Footer = "你是農場後勤：捐獻農作物、漁獲與素材支援前線，Boss 戰鬥由系統自動進行。"

	p.button("進入"+v.Name, p.bossGoto("boss_battle", true), btn{emoji: "⚔️", style: ui.Success, row: 0})
	p.button("上一場排行榜", p.bossGoto("boss_board", false), btn{emoji: "🏆", row: 0})
	p.button("活動說明", p.bossGoto("boss_help", false), btn{emoji: "📖", row: 0})
	p.bossClaimButton(v, 0)
	p.button("更新狀態", p.bossGoto("boss", true), btn{emoji: "🔄", row: 0})

	var claimable []option
	for _, c := range v.Claimable {
		state := "已結束"
		if c.Defeated {
			state = "已擊敗"
		}
		claimable = append(claimable, option{c.Name, fmt.Sprint(c.EventID), state, "🎁"})
	}
	p.selectMenu("🎁 領取之前活動的獎勵…", claimable, func(ctx context.Context, it ui.Interaction, value string) error {
		eventID, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return gameerr.Validationf("找不到這個項目。")
		}
		p.act(ctx, it, p.call("boss_claim", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().BossClaim(ctx, p.groupID, p.playerID, eventID)
		}), actOpts{})
		return nil
	}, 1, "")

	choices := v.Choices
	others := len(choices) > 0
	for _, c := range choices {
		if c.EventID == v.EventID {
			others = false
		}
	}
	if len(choices) > 1 || others {
		var options []option
		for _, c := range choices {
			options = append(options, option{c.Name, fmt.Sprint(c.EventID), c.ScopeLabel + "｜" + c.StateLabel, "🐉"})
		}
		p.selectMenu("🔀 切換世界 Boss…", options, func(ctx context.Context, it ui.Interaction, value string) error {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return gameerr.Validationf("找不到這個項目。")
			}
			p.bossSelected = &id
			p.bossReset()
			p.goTo(ctx, it, "boss", "")
			return nil
		}, 2, fmt.Sprint(v.EventID))
	}
	p.navBack(4)
	return e, nil
}

// ---------- 戰鬥區（不附圖：數值面板） ----------

func (p *Panel) loadBossActive(ctx context.Context) (*services.BossView, error) {
	v, err := p.bossView(ctx)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, gameerr.Validationf("目前沒有世界 Boss 活動。")
	}
	p.setBossEvent(v)
	return v, nil
}

func (p *Panel) loadBossBattle(ctx context.Context) (any, error) { return p.loadBossActive(ctx) }

func (p *Panel) buildBossBattle(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(*services.BossView)
	me := bossMe(v)
	e := &ui.Embed{Title: "⚔️ 戰鬥區｜" + v.Name, Color: bossColor(v.State), Description: bossHeader(v)}
	scope := "群組"
	if v.GroupID == nil {
		scope = "全服"
	}
	e.AddField(fmt.Sprintf("🚚 %s後勤補給進度", scope), supplyLines(v), false)
	e.AddField("⚔️ 前線戰況", fmt.Sprintf("攻擊倍率 ×%s\n出勤率 %s%%\n每小時傷害 %s", pyFloat(v.AttackMult), pyFloat(v.UptimePct), money(v.DamagePerHour)), true)
	e.AddField(fmt.Sprintf("👥 %s參與", scope), fmt.Sprintf("參與人數 %d\n捐獻總數 %s\n總積分 %s", v.Participants, money(v.TotalQuantity), money(v.TotalPoints)), true)
	mine := fmt.Sprintf("👤 **%s**\n🏅 貢獻積分 **%s**\n📦 捐獻總數 %s\n📊 個人排名 %s\n🎁 獎勵：%s", me.PlayerName, money(me.Points),
		money(me.Quantity), bossRank(me), claimLabels[me.ClaimState])
	e.AddField("🙋 個人貢獻", mine+bossMineExtra(v), false)
	e.Footer = "捐獻會立即造成傷害並推進補給；前線依補給自動作戰。"

	p.button("捐獻物資", p.bossGoto("boss_donate", true), btn{emoji: "📦", style: ui.Success, row: 0, disabled: v.State != "active"})
	p.bossClaimButton(v, 0)
	p.button("更新狀態", p.bossGoto("boss_battle", true), btn{emoji: "🔄", row: 0})
	p.button("回介紹區", p.bossGoto("boss", true), btn{emoji: "🐉", row: 0})
	p.button("目前排行", p.bossGoto("boss_live_board", false), btn{emoji: "🏆", row: 1, style: ui.Primary})
	p.navBack(1)
	return e, nil
}

// ---------- 捐獻 ----------

type bossDonateData struct {
	v       *services.BossView
	options []services.BossDonationOption
	loaded  bool // 有讀取選項（Python options 不是 None）
}

func (p *Panel) loadBossDonate(ctx context.Context) (any, error) {
	v, err := p.loadBossActive(ctx)
	if err != nil {
		return nil, err
	}
	d := bossDonateData{v: v}
	if p.bossCategory != "" && v.State == "active" {
		if d.options, err = p.svc().BossDonationOptions(ctx, p.groupID, p.playerID, v.EventID, p.bossCategory); err != nil {
			return nil, err
		}
		d.loaded = true
	}
	return d, nil
}

func (p *Panel) buildBossDonate(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(bossDonateData)
	v, options := d.v, d.options
	p.bossOptions = options
	supplyNames := map[string]string{}
	for _, s := range v.Supplies {
		supplyNames[s.Code] = s.Emoji + " " + s.Name
	}
	limits := v.Settings.Limits
	lines := []string{fmt.Sprintf("❤️ HP %s / %s　`%s`", money(v.HP), money(v.MaxHP), wb.Bar(float64(v.HP), float64(v.MaxHP), 12)), supplyLines(v), ""}
	switch {
	case v.State != "active":
		lines = append(lines, fmt.Sprintf("⚠️ 活動%s，現在不能捐獻。", v.StateLabel))
	case p.bossCategory == "":
		lines = append(lines, "① 先選擇要捐獻的類別。")
	case len(options) == 0:
		lines = append(lines, "背包裡沒有這個類別可以捐獻的物品。")
	case p.bossCategory == "fish" && p.bossPending == nil:
		lines = append(lines, fmt.Sprintf("② 勾選要捐的魚種，可以一次勾選多種：勾選的魚種會**全部捐出**（每種單次最多 %d 隻，每種每場最多 %d 隻）。",
			limits.PerActionMax, limits.PerItemMax))
	case p.bossItem == nil && p.bossPending == nil:
		lines = append(lines, "② 選擇要捐獻的物品（顯示持有數量、每個積分與對應的補給）。")
	case p.bossPending == nil:
		lines = append(lines, fmt.Sprintf("③ 選擇要捐獻的數量（單次最多 %d 個，每種物品每場最多 %d 個）。", limits.PerActionMax, limits.PerItemMax))
	}
	if o := p.bossItem; o != nil {
		lines = append(lines, fmt.Sprintf("\n已選：**%s**　持有 %d　每個 %s　→ %s　本場還可捐 %d", o.Name, o.Held, pointsText(*o), supplyNames[o.Supply], o.Remaining))
	}
	if pd := p.bossPending; pd != nil && pd.single == nil {
		low, high, total := 0, 0, 0
		items := make([]string, len(pd.batch))
		for i, x := range pd.batch {
			low += x.item.PointsMin * x.quantity
			high += x.item.PointsMax * x.quantity
			total += x.quantity
			items[i] = fmt.Sprintf("・%s ×%d", x.item.Name, x.quantity)
		}
		est := money(low)
		if low != high {
			est = fmt.Sprintf("約 %s–%s", money(low), money(high))
		}
		lines = append(lines, fmt.Sprintf("\n### 確認捐獻以下 %d 種漁獲（共 %s 隻）？\n%s\n預計獲得 %s 積分。確認後才會扣除；按「取消」不會扣除任何物品。",
			len(pd.batch), money(total), strings.Join(items, "\n"), est))
	} else if pd != nil {
		o, q := pd.single.item, pd.single.quantity
		est := money(o.PointsMin * q)
		if o.PointsMin != o.PointsMax {
			est = fmt.Sprintf("約 %s–%s", money(o.PointsMin*q), money(o.PointsMax*q))
		}
		lines = append(lines, fmt.Sprintf("\n### 確認捐獻 %s ×%d？\n預計獲得 %s 積分。確認後才會扣除物品；按「取消」不會扣除任何物品。", o.Name, q, est))
	}
	e := &ui.Embed{Title: "📦 捐獻物資｜" + v.Name, Color: BossPurple, Description: runes(strings.Join(lines, "\n"), 4096),
		Footer: "作物與魚依售價換算積分；同一種物品捐越多不會無限加成，補給超過目標的部分效果有上限。"}

	if v.State == "active" {
		cats := make([]option, len(wb.Categories))
		for i, code := range wb.Categories {
			info := wb.CategoryInfos[code]
			cats[i] = option{info.Name, code, "", info.Emoji}
		}
		p.selectMenu("① 捐獻類別…", cats, func(ctx context.Context, it ui.Interaction, value string) error {
			p.bossReset()
			p.bossCategory = value
			p.goTo(ctx, it, "boss_donate", "")
			return nil
		}, 0, p.bossCategory)
		if len(options) > 0 && p.bossCategory == "fish" {
			p.bossFishMulti(options, limits.PerActionMax, supplyNames)
		} else if len(options) > 0 {
			selected := ""
			var items []option
			for i, o := range options {
				if p.bossItem != nil && selected == "" && o.Code == p.bossItem.Code && o.Quality == p.bossItem.Quality && o.Mutation == p.bossItem.Mutation {
					selected = fmt.Sprint(i)
				}
				items = append(items, option{o.Name, fmt.Sprint(i), fmt.Sprintf("持有 %d｜每個 %s｜→ %s｜還可捐 %d", o.Held, pointsText(o),
					supplyNames[o.Supply], o.Remaining), ""})
			}
			p.selectMenu("② 選擇物品…", items, func(ctx context.Context, it ui.Interaction, value string) error {
				idx, err := strconv.Atoi(value)
				if err == nil && idx >= 0 && idx < len(p.bossOptions) {
					item := p.bossOptions[idx]
					p.bossItem, p.bossPending = &item, nil
				}
				p.goTo(ctx, it, "boss_donate", "")
				return nil
			}, 1, selected)
		}
		if p.bossItem != nil && p.bossPending == nil {
			p.bossQuantityButtons(*p.bossItem, limits.PerActionMax)
		}
		if p.bossPending != nil {
			p.button("確認捐獻", p.bossConfirm, btn{emoji: "✅", style: ui.Success, row: 3})
			p.button("取消", func(ctx context.Context, it ui.Interaction) error {
				p.bossPending = nil
				p.refresh(ctx, it, "已取消，沒有扣除任何物品。", nil, "重繪", time.Time{})
				return nil
			}, btn{emoji: "❌", style: ui.Danger, row: 3})
		}
	}
	p.button("回戰鬥區", p.bossGoto("boss_battle", true), btn{emoji: "⚔️", row: 4})
	p.navBack(4)
	return e, nil
}

// bossFishMulti 漁獲：勾選多個魚種，勾選的魚種全部捐出（數量 = 持有、本場剩餘、單次上限的最小值）。
func (p *Panel) bossFishMulti(options []services.BossDonationOption, perAction int, supplyNames map[string]string) {
	type shownOpt struct {
		i int
		o services.BossDonationOption
	}
	var shown []shownOpt
	for i, o := range options[:min(len(options), ui.MaxOptions)] {
		if min(o.Held, o.Remaining, perAction) > 0 {
			shown = append(shown, shownOpt{i, o})
		}
	}
	if len(shown) == 0 {
		return
	}
	chosen := map[string]bool{}
	if p.bossPending != nil {
		for _, x := range p.bossPending.batch {
			chosen[x.item.Code] = true
		}
	}
	multi := &ui.Select{Placeholder: "② 勾選要全部捐出的魚種（可複選）…", Row: 1, MinValues: 1, MaxValues: len(shown),
		OnSelectMany: func(ctx context.Context, it ui.Interaction, values []string) error {
			var batch []bossDonation
			for _, value := range values {
				idx, err := strconv.Atoi(value)
				if err != nil || idx < 0 || idx >= len(p.bossOptions) {
					continue
				}
				o := p.bossOptions[idx]
				if q := min(o.Held, o.Remaining, perAction); q > 0 {
					batch = append(batch, bossDonation{o, q, newRequestID()})
				}
			}
			p.bossItem, p.bossPending = nil, nil
			if len(batch) > 0 {
				p.bossPending = &bossPending{batch: batch}
			}
			p.goTo(ctx, it, "boss_donate", "")
			return nil
		}}
	for _, s := range shown {
		o := s.o
		multi.Options = append(multi.Options, ui.SelectOption{Label: runes(o.Name, 100), Value: fmt.Sprint(s.i), Default: chosen[o.Code],
			Description: runes(fmt.Sprintf("持有 %d｜捐 %d 隻｜每隻 %s｜→ %s", o.Held, min(o.Held, o.Remaining, perAction), pointsText(o), supplyNames[o.Supply]), 100)})
	}
	p.View.Add(multi)
}

func (p *Panel) bossQuantityButtons(item services.BossDonationOption, perAction int) {
	maximum := min(item.Held, item.Remaining, perAction)
	if maximum <= 0 {
		return
	}
	choose := func(ctx context.Context, it ui.Interaction, q int) {
		p.bossPending = &bossPending{single: &bossDonation{item, q, newRequestID()}}
		p.goTo(ctx, it, "boss_donate", "")
	}
	for _, q := range bossQuantityPresets {
		if q < maximum {
			p.button(fmt.Sprintf("× %d", q), func(ctx context.Context, it ui.Interaction) error {
				choose(ctx, it, q)
				return nil
			}, btn{style: ui.Primary, row: 2})
		}
	}
	p.button(fmt.Sprintf("最多 × %d", maximum), func(ctx context.Context, it ui.Interaction) error {
		choose(ctx, it, maximum)
		return nil
	}, btn{style: ui.Primary, row: 2})
	p.button("自訂數量…", func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, quantityModal("捐獻數量", "數量", maximum, choose))
	}, btn{emoji: "✏️", row: 2})
}

func (p *Panel) bossDonate(ctx context.Context, eventID int64, x bossDonation) (string, error) {
	r, err := Call(ctx, p.cmds.Runner, "boss_donate", func(ctx context.Context) (services.BossDonateResult, error) {
		return p.svc().BossDonate(ctx, p.groupID, p.playerID, eventID, x.item.Category, x.item.Code, x.quantity, x.requestID,
			x.item.Quality, x.item.Mutation)
	})
	return r.Message, err
}

func (p *Panel) bossConfirm(ctx context.Context, it ui.Interaction) error {
	pending, eventID := p.bossPending, p.bossEventID
	// 先清除待確認狀態：同一面板重複點擊時第二下不會再送出（服務層另以 request_id 防重複）
	p.bossPending = nil
	if pending == nil || eventID == nil {
		p.refresh(ctx, it, "⚠️ 這筆捐獻已經送出或已取消，請依最新畫面操作。", nil, "重繪", time.Time{})
		return nil
	}
	id := *eventID
	p.bossItem = nil
	if pending.single == nil {
		batch := pending.batch
		// 每種魚各自一筆捐獻（各自的 request_id，重送不會重複扣）；某一種失敗不影響已完成的其他種
		p.act(ctx, it, func(ctx context.Context) (string, error) {
			var lines []string
			for _, x := range batch {
				msg, err := p.bossDonate(ctx, id, x)
				if err != nil {
					ge, ok := gameerr.As(err)
					if !ok {
						return "", err
					}
					msg = fmt.Sprintf("❌ %s：%s", x.item.Name, ge.Error())
				}
				lines = append(lines, msg)
			}
			return strings.Join(lines, "\n"), nil
		}, actOpts{page: "boss_donate"})
		return nil
	}
	x := *pending.single
	p.act(ctx, it, func(ctx context.Context) (string, error) { return p.bossDonate(ctx, id, x) }, actOpts{page: "boss_donate"})
	return nil
}

// ---------- 排行榜與說明 ----------

type bossBoardData struct {
	board       *services.BossLeaderboard
	titles      map[int64]string
	currentName string
}

func (p *Panel) withTitles(ctx context.Context, b *services.BossLeaderboard) (map[int64]string, error) {
	if b == nil {
		return nil, nil
	}
	ids := make([]int64, len(b.Rows))
	for i, r := range b.Rows {
		ids[i] = r.PlayerID
	}
	return p.svc().PlayerTitles(ctx, ids)
}

func (p *Panel) loadBossBoard(ctx context.Context) (any, error) {
	v, err := p.loadBossActive(ctx)
	if err != nil {
		return nil, err
	}
	b, err := p.svc().BossLastLeaderboard(ctx, p.groupID, p.playerID, v.EventID, wb.LeaderboardSize)
	if err != nil {
		return nil, err
	}
	titles, err := p.withTitles(ctx, b)
	return bossBoardData{b, titles, v.Name}, err
}

func (p *Panel) buildBossBoard(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(bossBoardData)
	var e *ui.Embed
	if d.board == nil {
		e = &ui.Embed{Title: "🏆 上一場貢獻排行", Color: BossGold, Description: fmt.Sprintf(
			"還沒有已結束的%s活動。\n這一場的即時排行請到「⚔️ 戰鬥區」→「🏆 目前排行」。", d.currentName)}
		p.attachImage(e, p.bossImage())
	} else {
		result := "🏁 時間到未擊敗"
		if d.board.Defeated {
			result = "💀 已擊敗"
		}
		e = p.boardEmbed(d.board, d.titles, "🏆 上一場貢獻排行｜"+d.board.Name, fmt.Sprintf("%s　結束於 %s", result, ts(d.board.EndedAt, "f")), true)
	}
	p.button("回介紹區", p.bossGoto("boss", true), btn{emoji: "🐉", row: 0})
	p.navBack(0)
	return e, nil
}

func (p *Panel) loadBossLiveBoard(ctx context.Context) (any, error) {
	v, err := p.loadBossActive(ctx)
	if err != nil {
		return nil, err
	}
	b, err := p.svc().BossLeaderboard(ctx, p.groupID, p.playerID, v.EventID, wb.LeaderboardSize)
	if err != nil {
		return nil, err
	}
	titles, err := p.withTitles(ctx, &b)
	return bossBoardData{board: &b, titles: titles}, err
}

func (p *Panel) buildBossLiveBoard(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(bossBoardData)
	e := p.boardEmbed(d.board, d.titles, "🏆 目前貢獻排行｜"+d.board.Name, "這一場的即時排行（每次開啟或重新整理時更新）", false)
	p.button("重新整理", p.bossGoto("boss_live_board", false), btn{emoji: "🔄", row: 0})
	p.button("回戰鬥區", p.bossGoto("boss_battle", true), btn{emoji: "⚔️", row: 0})
	p.navBack(0)
	return e, nil
}

func (p *Panel) boardEmbed(b *services.BossLeaderboard, titles map[int64]string, title, head string, image bool) *ui.Embed {
	var lines []string
	for _, r := range b.Rows {
		name := r.PlayerName
		if badge := titles[r.PlayerID]; badge != "" {
			name = fmt.Sprintf("%s〔%s〕", name, badge)
		}
		lines = append(lines, fmt.Sprintf("%s **%s**　🏅 %s 分　📦 %s 個", medalOr(r.Rank, fmt.Sprintf("`%2d.`", r.Rank)), name, money(r.Points), money(r.Quantity)))
	}
	body := "還沒有人捐獻，成為第一個支援前線的人吧！"
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
	}
	e := &ui.Embed{Title: title, Color: BossGold, Description: head + "\n\n" + body, Footer: "依貢獻積分排序；同分時先捐獻的玩家在前。"}
	if image {
		p.attachImage(e, p.bossImage())
	}
	mine := "沒有捐獻"
	if me := b.Me; me != nil {
		mine = fmt.Sprintf("第 %d 名 / %d 人　🏅 %s 分　📦 %s 個", me.Rank, b.Participants, money(me.Points), money(me.Quantity))
	}
	e.AddField("🙋 我的名次", mine, false)
	return e
}

func (p *Panel) loadBossHelp(ctx context.Context) (any, error) {
	v, err := p.bossView(ctx)
	return bossData{v}, err
}

func (p *Panel) buildBossHelp(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(bossData).v
	s := wb.DefaultSettings()
	if v != nil {
		s = v.Settings
	}
	eff, lim := s.Effects, s.Limits
	supply := make([]string, len(wb.Supplies))
	for i, code := range wb.Supplies {
		x := wb.SupplyInfos[code]
		supply[i] = fmt.Sprintf("%s **%s** → %s：%s", x.Emoji, x.Name, x.Effect, x.Desc)
	}
	how := "1️⃣ 世界 Boss 分成🌏 **全服**（所有伺服器的玩家一起打，例如固定出現的深淵魔龍）與🏠 **伺服器**活動，大家一起當**農場後勤**。\n" +
		"2️⃣ 在介紹區按「⚔️ 進入（Boss 名稱）」到戰鬥區，再按「📦 捐獻物資」，從背包選農作物、漁獲、素材或加工品捐給前線（確認後才扣除）。\n" +
		"3️⃣ 前線依四種補給的達成率**自動作戰**，每小時扣 Boss HP；每次捐獻也會立即造成一次傷害。\n" +
		"4️⃣ 活動結束（擊敗 Boss 或時間到）後，按「🎁 領取獎勵」。每人每場只能領一次。"
	rules := fmt.Sprintf("🌾 作物與 🐟 魚：每 %d 金幣售價換 1 積分（無條件進位）\n"+
		"⚙️ 素材、🍲 加工品與 🧪 特殊材料：依活動指定的固定積分\n"+
		"📦 單次最多 %d 個；每種物品每場最多 %d 個\n"+
		"📈 補給最多算到目標的 %d%%；沒有糧食時出勤率只有 %d%%\n"+
		"⚔️ 戰鬥補給滿額傷害 +%d%%、🛡️ 裝備補給滿額 +%d%%、🩹 醫療補給滿額救回 %d%% 缺勤時間",
		s.GoldPerPoint, lim.PerActionMax, lim.PerItemMax, eff.RatioCapPct, eff.BaseUptimePct, eff.CombatAttackPct, eff.EquipmentAttackPct, eff.MedicalRecoveryPct)
	e := &ui.Embed{Title: "📖 世界 Boss 活動說明", Color: BossPurple, Description: how,
		Footer: "只有捐獻過的玩家可以領獎；所有紀錄存在資料庫，Bot 重啟也不會遺失。"}
	p.attachImage(e, p.bossImage())
	e.AddField("🚚 補給類型與效果", strings.Join(supply, "\n"), false)
	e.AddField("🧮 積分與限制", rules, false)
	if v != nil && v.RewardsText != nil {
		r := v.RewardsText
		ranks := make([]string, len(r.Rank))
		for i, t := range r.Rank {
			ranks[i] = fmt.Sprintf("%s %s", medalOr(i+1, fmt.Sprintf("第 %d 名", i+1)), t)
		}
		reward := ""
		if r.MinPoints != 0 {
			reward = fmt.Sprintf("🎯 參與門檻：個人積分達 %s 才有以下任何獎勵\n", money(r.MinPoints))
		}
		reward += fmt.Sprintf("🏆 擊敗 Boss：%s\n🤝 未擊敗（參與獎）：%s\n⭐ 個人積分達 %s：%s\n%s", r.Victory, r.Participation,
			money(r.BonusPoints), r.Bonus, strings.Join(ranks, "\n"))
		if r.Title != "" {
			reward += fmt.Sprintf("\n🎖️ 擊敗時前 %d 名獲得稱號「%s」", r.TitleTop, r.Title)
		}
		e.AddField("🎁 獎勵制度", runes(reward, 1024), false)
	}
	p.button("回介紹區", p.bossGoto("boss", true), btn{emoji: "🐉", row: 0})
	p.navBack(0)
	return e, nil
}
