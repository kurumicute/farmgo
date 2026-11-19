package bot

// 面板頁面：種子實驗室（Go 版新增，2026-11-13；從「🌱 農田」頁進入）。
// 實驗室頁面不產生圖片（雜交、選種子的點擊很頻繁，只用文字與 Embed，避免卡頓）。

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/hourglass"
	"farmgm/internal/game/seedlab"
	"farmgm/internal/services"
)

// Teal 種子實驗室的顏色。
const Teal = 0x1ABC9C

// labState 實驗室頁面的選擇（面板狀態）。
type labState struct {
	seed     int64 // 種子庫頁選取的種子
	mother   int64 // 雜交頁的母本、父本
	father   int64
	resin    bool
	herb     bool
	catalyst string // 基因代碼；"" = 不使用
	// 育苗頁：選取的種子、排程次數（0 = 1 次）、是否加藥草
	cloneSeed    int64
	cloneBatches int
	cloneHerb    bool
	// 種子超過 25 顆（Discord 選單上限）時分頁：種子庫頁、雜交頁的母本與父本選單各自的頁碼
	seedPage, motherPage, fatherPage int
	// 種子庫頁的作物篩選（"" / cropFilterAll = 全部）與篩選選單的頁碼（2026-11-17）
	filter     string
	filterPage int
	// 雜交頁母本、父本選單的作物篩選（"" / cropFilterAll = 全部、cropFilterPick = 正在選作物；2026-11-17）
	motherCrop, fatherCrop string
}

// pageOf 第 page 頁（每頁 ui.MaxOptions 項）的內容、修正後的頁碼與總頁數。
func pageOf[T any](items []T, page int) ([]T, int, int) {
	pages := pageCount(len(items), ui.MaxOptions)
	page = max(0, min(page, pages-1))
	return items[min(len(items), page*ui.MaxOptions):min(len(items), (page+1)*ui.MaxOptions)], page, pages
}

// 選單內翻頁的選項值。
const (
	pagePrev = "__page_prev"
	pageNext = "__page_next"
)

// pagedOptions 選單內翻頁：超過 25 項時每頁 23 項，第一項「上一頁」、最後一項「下一頁」（翻頁不用另外佔一列按鈕，
// 也一定緊貼在對應的選單上）。回傳這一頁的選項、修正後的頁碼與總頁數。
func pagedOptions(all []option, page int) ([]option, int, int) {
	if len(all) <= ui.MaxOptions {
		return all, 0, 1
	}
	size := ui.MaxOptions - 2
	pages := pageCount(len(all), size)
	page = max(0, min(page, pages-1))
	prev, next := (page-1+pages)%pages+1, (page+1)%pages+1
	out := []option{{fmt.Sprintf("上一頁：翻到第 %d 頁（目前第 %d/%d 頁）", prev, page+1, pages), pagePrev, "選這項只會翻頁", "◀️"}}
	out = append(out, all[page*size:min(len(all), (page+1)*size)]...)
	return append(out, option{fmt.Sprintf("下一頁：翻到第 %d 頁（目前第 %d/%d 頁）", next, page+1, pages), pageNext, "選這項只會翻頁", "▶️"}), page, pages
}

// turnPage 選到選單內的翻頁選項時更新頁碼（循環）並回傳 true。
func turnPage(value string, page *int, pages int) bool {
	switch value {
	case pagePrev:
		*page = ((*page-1)%pages + pages) % pages
	case pageNext:
		*page = (*page + 1) % pages
	default:
		return false
	}
	return true
}

// pageSlice 每頁 size 項時第 page 頁的內容、修正後的頁碼與總頁數（文字清單分頁用，例如紀錄、價目表）。
func pageSlice[T any](items []T, page, size int) ([]T, int, int) {
	pages := pageCount(len(items), size)
	page = max(0, min(page, pages-1))
	return items[min(len(items), page*size):min(len(items), (page+1)*size)], page, pages
}

// navFirstPage 前往有分頁的頁面，並從第 1 頁開始（從別頁進入時用；頁內翻頁用 pageButtons）。
func (p *Panel) navFirstPage(label, page string, field *int, o btn) {
	p.button(label, func(ctx context.Context, it ui.Interaction) error {
		*field = 0
		p.goTo(ctx, it, page, "")
		return nil
	}, o)
}

// pageButtons 上一頁／下一頁按鈕（循環翻頁）；只有一頁時不顯示。
func (p *Panel) pageButtons(label string, page *int, pages, row int, target string) {
	if pages <= 1 {
		return
	}
	move := func(delta int) ui.Handler {
		return func(ctx context.Context, it ui.Interaction) error {
			*page = ((*page+delta)%pages + pages) % pages
			p.goTo(ctx, it, target, "")
			return nil
		}
	}
	p.button(p.f("%s上一頁（%d/%d）", label, *page+1, pages), move(-1), btn{emoji: "◀️", row: row})
	p.button(label+p.s("下一頁"), move(1), btn{emoji: "▶️", row: row})
}

func (p *Panel) loadLab(ctx context.Context) (any, error) {
	return p.svc().SeedLab(ctx, p.playerID)
}

// labIntro 實驗室介紹（也放在未解鎖畫面）。
func labIntro() string {
	return fmt.Sprintf("🧪 把一般種子**鑑定**成有基因的特殊種子，再用兩顆種子**雜交**培育出更好的基因或全新的雜交作物；"+
		"**突變研究**能讓種子帶上突變並一直傳承下去。基因有 4 種（%s），每種 1–5 級。",
		strings.Join(func() []string {
			out := make([]string, len(seedlab.GeneOrder))
			for i, g := range seedlab.GeneOrder {
				out[i] = seedlab.GeneEmoji[g] + seedlab.GeneNames[g]
			}
			return out
		}(), "、"))
}

func geneEffects() string {
	c := seedlab.Current()
	return fmt.Sprintf("⏱️ 生長：每級生長時間 -%d%%　🧺 產量：每級收成數量 +%d%%（無條件進位）　⭐ 品質：每級品質加成 +%s%%　🧬 突變：每級突變機率 +%s%%（1 級沒有加成）",
		c.GrowthPctPerLevel, c.YieldPctPerLevel, bpPct(c.QualityBPPerLevel), bpPct(c.MutationBPPerLevel))
}

// bpPct 萬分比 → 百分比文字（例如 50 → 0.5）。
func bpPct(bp int) string { return strconv.FormatFloat(float64(bp)/100, 'f', -1, 64) }

// labCodexButtons 實驗室頁的圖鑑按鈕：特殊種子與菇類（開啟圖鑑頁的對應分類）。
// labSeedSources 特殊種子與雜交種子的取得方式（2026-11-14 玩家詢問後加上）。
func (p *Panel) labSeedSources() []string {
	return []string{
		p.s("🔬 **鑑定**：背包的一般種子 → 特殊種子（商店不賣的稀有種子，例如琥珀瓜、月光花，基因比較高）"),
		p.s("🧬 **雜交／改良**：兩顆特殊種子培育出一顆新的（只消耗父本，母本放回種子庫）；不同品種配對成功會得到雜交作物"),
		p.s("🌱 **育苗**：付金幣複製同基因的種子，每次 1–3 顆（加藥草機率更高，不含突變），可以一次排程好幾次，用來種滿農田"),
		p.f("🌾 **收成取回**：種下特殊種子，收成時有 %s%% 機率取回 1 顆同基因的種子", bpPct(seedlab.Current().InheritBP)),
		p.f("🎁 **朋友分享**：同伺服器的玩家每天最多送 %d 顆，沒有實驗室也能收", seedlab.Current().ShareSendPerDay),
		p.s("🍈 稀有的一般種子（琥珀瓜、月光花）來自⛰️ 山洞探險、🎁 每週特賣與🧬 稀有作物培育器，拿到後可以鑑定；雜交作物的種子只能靠雜交、收成取回或朋友分享。"),
	}
}

func (p *Panel) labCodexButtons(row int) {
	for _, c := range []struct{ key, label, emoji string }{{"lab", p.s("特殊種子圖鑑"), "📖"}, {"mushroom", p.s("菇類圖鑑"), "🍄"}} {
		key := c.key
		p.button(c.label, func(ctx context.Context, it ui.Interaction) error {
			p.codexCategory = key
			p.goTo(ctx, it, "codex", "")
			return nil
		}, btn{emoji: c.emoji, row: row})
	}
}

func labMaterialLine(v services.SeedLabView, codes ...string) string {
	var parts []string
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%s%s %d", lookupOr(MaterialEmoji, code, ""), lookupOr(v.MaterialNames, code, code), v.Materials[code]))
	}
	return strings.Join(parts, "　")
}

func seedLine(s services.LabSeedView) string {
	line := fmt.Sprintf("%s **%s** 第 %d 代　%s", cropEmoji(s.CropCode), s.CropName, s.Generation, s.Genes.Stars())
	if s.MutationName != "" {
		line += "　✨" + s.MutationName
	}
	if s.Busy {
		line += "　⏳培育中"
	}
	return line
}

func seedOptionLabel(s services.LabSeedView) string {
	return fmt.Sprintf("%s 第 %d 代", s.CropName, s.Generation) // 同名種子以說明欄的基因星等區分，不顯示資料庫編號
}

func seedOptionDesc(s services.LabSeedView) string {
	desc := s.Genes.Stars()
	if s.MutationName != "" {
		desc += "　✨" + s.MutationName
	}
	return desc
}

func findSeed(v services.SeedLabView, id int64) *services.LabSeedView {
	for i := range v.Seeds {
		if v.Seeds[i].ID == id {
			return &v.Seeds[i]
		}
	}
	return nil
}

func (p *Panel) labNav(row int) {
	p.nav(p.s("回實驗室"), "lab", btn{emoji: "🧪", row: row})
}

// ---------- 實驗室主頁 ----------

func (p *Panel) buildLab(_ context.Context, data any) (*ui.Embed, error) {
	v := data.(services.SeedLabView)
	e := &ui.Embed{Title: p.s("🧪 ✦ 種子實驗室 ✦"), Color: Teal}
	if !v.Unlocked || !v.Built {
		if !v.Unlocked {
			e.Description = sections(p.f("🔒 種子實驗室在農場 **Lv.%d** 開放（目前 Lv.%d）。", v.UnlockLevel, v.Level), labIntro(),
				Section{Title: p.s("特殊種子怎麼來"), Lines: p.labSeedSources()})
		} else {
			mark := "✅"
			if v.Gold < v.BuildGold {
				mark = "❌"
			}
			e.Description = sections(labIntro(), Section{Title: p.s("建造需要"),
				Lines: []string{p.f("%s 💰 金幣 %s／%s", mark, money(v.Gold), money(v.BuildGold))}},
				Section{Title: p.s("特殊種子怎麼來"), Lines: p.labSeedSources()})
			confirming := p.confirm == "lab_build"
			label, style := p.s("建造實驗室"), ui.Success
			if confirming {
				label, style = p.s("確認建造"), ui.Danger
			}
			p.button(label, func(ctx context.Context, it ui.Interaction) error {
				if p.confirm != "lab_build" {
					p.confirm = "lab_build"
					p.refresh(ctx, it, p.f("⚠️ 建造會花費 %s 金幣，再按一次「確認建造」。", money(v.BuildGold)), nil, p.s("重繪"), time.Time{})
					return nil
				}
				p.act(ctx, it, p.call("build_seed_lab", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().BuildSeedLab(ctx, p.playerID)
				}), actOpts{})
				return nil
			}, btn{emoji: "🏗️", style: style, row: 0, disabled: v.Gold < v.BuildGold})
		}
		if len(v.Seeds) > 0 { // 沒有實驗室也能收朋友分享的種子並種下
			e.Description += p.f("\n\n🎁 種子庫裡有 **%d** 顆朋友分享的特殊種子，沒有實驗室也能種下。", len(v.Seeds))
			p.nav(p.f("種子庫（%d）", len(v.Seeds)), "lab_seeds", btn{emoji: "🌰", style: ui.Primary, row: 1})
		}
		p.labCodexButtons(1)
		p.nav(p.s("回農田"), "plots", btn{emoji: "🌱", row: 2})
		p.navBack(2)
		return e, nil
	}

	levelLine := p.f("🔬 研究等級 **Lv.%d**　經驗 %d", v.LabLevel, v.Exp)
	if v.NextExp > 0 {
		levelLine += p.f("／%d（%s）", v.NextExp, bar(v.Exp, v.NextExp, 8))
	} else {
		levelLine += p.s("（已滿級）")
	}
	levelLine += p.f("　研究槽 %d 個", v.Slots)
	var slots []string
	for _, j := range v.Jobs {
		state := "⏳ " + tsv(j.ReadyAt)
		if j.Ready {
			state = p.s("✅ **可以領取**")
		}
		var what string
		switch {
		case j.Kind == "cross" && j.Father != nil && j.Unknown:
			what = fmt.Sprintf("🧬 %s × %s → %s", p.s(j.Mother.CropName), p.s(j.Father.CropName), j.ResultName)
		case j.Kind == "cross" && j.Father != nil:
			what = p.f("🧬 %s × %s → %s（成功率 %s%%）", p.s(j.Mother.CropName), p.s(j.Father.CropName), j.ResultName, fmtPct(j.SuccessPct))
		case j.Kind == "clone":
			what = p.f("🌱 育苗 ×%d 次：%s 第 %d 代（%s）", max(1, j.Batches), p.s(j.Mother.CropName), j.Mother.Generation, j.Mother.Genes.Stars())
		default:
			what = p.f("☢️ 突變研究：%s（成功率 %s%%）", p.s(j.Mother.CropName), fmtPct(j.SuccessPct))
		}
		slots = append(slots, p.f("#%d　%s　%s\n　%s", j.Slot, bar(int(j.Progress*100), 100, 6), state, what))
	}
	for i := len(v.Jobs); i < v.Slots; i++ {
		slots = append(slots, p.f("#%d　空的", i+1))
	}
	stored, busy := 0, 0
	for _, s := range v.Seeds {
		if s.Busy {
			busy++
		} else {
			stored++
		}
	}
	share := p.f("🎁 今天已分享 %d／%d 顆", v.SentToday, seedlab.Current().ShareSendPerDay)
	if len(v.ReceivedFrom) > 0 {
		share += p.s("　今天收到：") + strings.Join(v.ReceivedFrom, p.s("、"))
	}
	// 研究槽（哪些可以領取、還要多久）放在最後，貼近「領取」按鈕
	e.Description = sections(
		levelLine,
		p.f("🌰 種子庫 %d／%d 顆（培育中 %d）　%s", len(v.Seeds), v.Capacity, busy, share),
		"🧰 "+labMaterialLine(v, seedlab.MatResin, seedlab.MatHerb, seedlab.MatCrystal, seedlab.MatAncientCore),
		Section{Title: p.s("研究槽"), Lines: slots},
	)
	e.Footer = p.s("研究在背景計時，完成後回來按「領取」。看不懂術語？按「📖 實驗室說明」。")

	p.button(p.f("領取（%d）", v.ReadyCount), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("collect_lab", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().CollectLab(ctx, p.playerID)
		}), actOpts{})
		return nil
	}, btn{emoji: "🧺", style: ui.Success, row: 0, disabled: v.ReadyCount == 0})
	p.button(p.s("重新整理"), func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, "lab", "")
		return nil
	}, btn{emoji: "🔄", row: 0})
	// 研究槽擴充（2026-11-15）：研究 Lv.3 後才能買，按兩次確認
	if v.NextSlotPrice > 0 {
		label, style := p.f("研究槽擴充（%s 金）", money(v.NextSlotPrice)), ui.Secondary
		if p.confirm == "lab_slot" {
			label, style = p.s("確認擴充研究槽"), ui.Danger
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			if p.confirm != "lab_slot" {
				p.confirm = "lab_slot"
				p.refresh(ctx, it, p.f("⚠️ 研究槽擴充到 %d 個需要 %s 金幣，再按一次「確認擴充研究槽」。", v.Slots+1, money(v.NextSlotPrice)),
					nil, p.s("重繪"), time.Time{})
				return nil
			}
			p.act(ctx, it, p.call("buy_lab_slot", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuyLabSlot(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🔬", row: 0, style: style, disabled: v.Gold < v.NextSlotPrice})
	}
	p.hourglassSelect(v, 4)
	p.nav(p.f("種子庫（%d）", stored), "lab_seeds", btn{emoji: "🌰", style: ui.Primary, row: 1})
	p.nav(p.s("雜交培育"), "lab_cross", btn{emoji: "🧬", style: ui.Primary, row: 1, disabled: stored < 2 || v.FreeSlots == 0})
	p.navFirstPage(p.s("培育紀錄"), "lab_records", &p.labRecordPage, btn{emoji: "📜", row: 1})
	p.labCodexButtons(1)

	// 2026-11-19 批量鑑定：選了作物後跳出數量對話框，預填這次最多能鑑定的顆數（種子數、種子庫空位、金幣、單次上限取最小）
	var options []option
	room := v.Capacity - len(v.Seeds)
	limits := map[string]int{}
	for _, o := range v.Identify {
		kind := p.s("一般作物：基因 1–2 級")
		if o.Rare {
			kind = p.s("稀有作物：基因 2–3 級")
		}
		most := min(o.Have, room, services.IdentifyMax)
		if o.Fee > 0 {
			most = min(most, v.Gold/o.Fee)
		}
		limits[o.CropCode] = most
		options = append(options, option{p.f("%s（持有 %d，鑑定費 %s 金／顆）", p.s(o.Name), o.Have, money(o.Fee)), o.CropCode,
			p.f("%s｜這次最多 %d 顆", kind, most), cropEmoji(o.CropCode)})
	}
	if room > 0 {
		p.selectMenu(p.s("🔬 鑑定一般種子（背包的種子 → 特殊種子，可以一次鑑定多顆）…"), options, func(ctx context.Context, it ui.Interaction, code string) error {
			most := limits[code]
			if most <= 1 { // 只能鑑定 1 顆（或金幣不足）時直接鑑定，錯誤原因由服務層說明
				p.act(ctx, it, p.call("identify_seed", func(ctx context.Context) (services.ActionResult, error) {
					return p.svc().IdentifySeed(ctx, p.playerID, code)
				}), actOpts{noProgress: true})
				return nil
			}
			return it.SendModal(ctx, &ui.Modal{
				Title: p.s("鑑定種子"),
				Inputs: []ui.TextInput{{Label: p.f("鑑定幾顆？（1–%d）", most), Default: strconv.Itoa(most),
					MaxLength: 3, Required: true}},
				OnSubmit: func(ctx context.Context, mit ui.Interaction, values []string) error {
					n, ok := parseDigits(strings.TrimSpace(first(values)))
					if !ok || n < 1 || n > most {
						return mit.SendMessage(ctx, ui.Message{Content: ptr(p.f("請輸入 1–%d 的整數。", most)), Ephemeral: true})
					}
					p.act(ctx, mit, p.call("identify_seed", func(ctx context.Context) (services.ActionResult, error) {
						return p.svc().IdentifySeeds(ctx, p.playerID, code, n)
					}), actOpts{noProgress: true})
					return nil
				},
			})
		}, 2, "")
	}
	p.nav(p.s("實驗室說明"), "lab_help", btn{emoji: "📖", row: 3})
	p.nav(p.s("回農田"), "plots", btn{emoji: "🌱", row: 3})
	p.navBack(3)
	return e, nil
}

// buildLabHelp 實驗室說明：術語、基因效果、遺傳機率與特殊種子的取得方式集中在這裡，其他頁面只留操作需要的一句話。
func (p *Panel) buildLabHelp(_ context.Context, _ any) (*ui.Embed, error) {
	c := seedlab.Current()
	two, three := seedlab.CloneChanceBP(false)
	e := &ui.Embed{Title: p.s("📖 ✦ 實驗室說明 ✦"), Color: Teal, Description: runes(sections(
		labIntro(),
		Section{Title: p.s("名詞"), Lines: []string{
			p.s("♀ **母本**：培育時保留的那顆種子，完成後放回種子庫"),
			p.s("♂ **父本**：培育時會被用掉的那顆種子"),
			p.s("🧬 **雜交**：兩種不同作物配對，成功會得到全新的雜交作物"),
			p.s("🔧 **改良**：同一種作物配對，一定成功，用來提升基因"),
			p.s("🔢 **世代**：培育出的新種子 = 兩顆種子中較大的世代 + 1"),
			p.s("☢️ **突變**：讓種子帶上夜光、豐收等特性，之後培育會一直傳下去"),
		}},
		Section{Title: p.s("實驗室限定突變（只能從突變研究取得）"), Lines: labMutationEffects()},
		Section{Title: p.s("基因效果（1 級沒有加成）"), Lines: strings.Split(strings.ReplaceAll(geneEffects(), "　", "\n"), "\n")},
		Section{Title: p.s("培育時基因怎麼決定"), Lines: []string{
			p.f("每個基因有 %s%% 機率直接拿兩顆中**較高**的那個，其餘母本、父本各一半機率", bpPct(c.DominantBP)),
			p.f("之後再有 %s%% 機率升 1 級、%s%% 機率降 1 級", bpPct(c.GeneUpBP), bpPct(c.GeneDownBP)),
		}},
		Section{Title: p.s("育苗"), Lines: []string{
			p.f("付金幣複製同基因、同世代的種子，每次 1–3 顆（2 顆以上 %s%%、3 顆 %s%%；加藥草更高），不含突變", bpPct(two), bpPct(three)),
		}},
		Section{Title: p.s("特殊種子怎麼來"), Lines: p.labSeedSources()},
	), 4000)}
	p.labNav(0)
	p.navBack(0)
	return e, nil
}

func fmtPct(pct float64) string { return strconv.FormatFloat(pct, 'f', -1, 64) }

// hourglassSelect 有加速沙漏、而且有還在計時的研究槽時，顯示「使用加速沙漏」選單（2026-11-15）。
func (p *Panel) hourglassSelect(v services.SeedLabView, row int) {
	have := v.Materials[balance.ItemHourglass]
	if have == 0 {
		return
	}
	cfg := hourglass.Current()
	var options []option
	for _, j := range v.Jobs {
		left := j.ReadyAt.Sub(v.Now)
		if j.Ready || !hourglass.Usable(left) {
			continue
		}
		what := p.s("☢️ 突變研究")
		switch j.Kind {
		case "cross":
			what = p.s("🧬 雜交")
		case "clone":
			what = p.s("🌱 育苗")
		}
		options = append(options, option{p.f("研究槽 #%d %s %s", j.Slot, what, p.s(j.Mother.CropName)), fmt.Sprintf("lab:%d", j.Slot),
			p.f("剩餘 %s → %s", p.dur(true, int(left.Seconds())), p.dur(true, int(hourglass.Cut(left).Seconds()))), "⏳"})
	}
	p.selectMenu(p.f("⏳ 使用加速沙漏（持有 %d，剩餘時間 -%d%%）…", have, cfg.CutPct), options,
		func(ctx context.Context, it ui.Interaction, key string) error {
			p.act(ctx, it, p.call("use_hourglass", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().UseHourglass(ctx, p.playerID, key)
			}), actOpts{noProgress: true})
			return nil
		}, row, "")
}

// ---------- 種子庫 ----------

type labSeedsData struct {
	lab     services.SeedLabView
	targets []services.LabShareTargetView
}

func (p *Panel) loadLabSeeds(ctx context.Context) (any, error) {
	v, err := p.svc().SeedLab(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	d := labSeedsData{lab: v}
	if p.groupID != nil {
		if s := findSeed(v, p.lab.seed); s != nil && !s.Busy {
			if d.targets, err = p.svc().LabShareTargets(ctx, *p.groupID, p.playerID); err != nil {
				return nil, err
			}
		}
	}
	return d, nil
}

func (p *Panel) buildLabSeeds(ctx context.Context, data any) (*ui.Embed, error) {
	d := data.(labSeedsData)
	v := d.lab
	if !v.Built && len(v.Seeds) == 0 {
		return p.buildLab(ctx, v)
	}
	cfg := seedlab.Current()
	e := &ui.Embed{Title: p.s("🌰 ✦ 種子庫 ✦"), Color: Teal}
	// 同作物排在一起，可依作物篩選（2026-11-17）；篩選的作物已經沒有種子時回到全部
	seeds := filterSeeds(sortSeedsByCrop(v.Seeds), p.lab.filter)
	if len(seeds) == 0 {
		p.lab.filter, seeds = "", sortSeedsByCrop(v.Seeds)
	}
	// 種子超過 25 顆時分頁：清單與選單都只顯示這一頁
	pageSeeds, page, pages := pageOf(seeds, p.lab.seedPage)
	p.lab.seedPage = page
	var lines []string
	for _, s := range pageSeeds {
		lines = append(lines, seedLine(s))
	}
	sel := findSeed(v, p.lab.seed)
	if sel != nil && sel.Busy {
		sel = nil
	}
	listTitle := p.f("特殊種子 %d／%d", len(v.Seeds), v.Capacity)
	if name := cropFilterName(v.Seeds, p.lab.filter); name != "" {
		listTitle += p.f("｜只看 %s %d 顆", name, len(seeds))
	}
	if pages > 1 {
		listTitle += p.f("（第 %d/%d 頁）", page+1, pages)
	}
	parts := []any{Section{Title: listTitle, Lines: lines}}
	if len(lines) == 0 {
		parts = []any{p.s("種子庫是空的。到實驗室把背包裡的一般種子拿去鑑定吧！")}
	}
	if sel != nil {
		detail := []string{fmt.Sprintf("%s %s", cropEmoji(sel.CropCode), sel.Label()), sel.Genes.Text()}
		if eff := labMutationEffect(sel.Mutation); eff != "" {
			detail = append(detail, p.f("✨ %s：%s", sel.MutationName, eff))
		}
		if sel.Hybrid {
			detail = append(detail, p.s("🌟 雜交作物：商店買不到，只能自己培育或由朋友分享。"))
		}
		switch {
		case sel.OffSeason != "":
			detail = append(detail, "⚠️ "+sel.OffSeason+p.s("。可以先留在種子庫、拿去雜交或分享給朋友。"))
		case len(v.EmptyPlots) == 0:
			detail = append(detail, p.s("⚠️ 農田目前沒有空地：收成後空出來的土地就能種下這顆種子。"))
		}
		if v.Built {
			detail = append(detail, p.f("🌱 育苗：%s 金、約 %s，得到 1–3 顆同基因的種子",
				money(sel.CloneFee), p.dur(true, int(seedlab.CloneDuration(1).Seconds()))))
			detail = append(detail, p.f("☢️ 突變研究：成功率 %s%%（加古代核心 %s%%），💎水晶 ×%d、約 %s",
				bpPct(seedlab.MutateChanceBP(sel.Genes.Mutation, false)), bpPct(seedlab.MutateChanceBP(sel.Genes.Mutation, true)),
				cfg.MutateCrystal, p.dur(true, int(seedlab.MutateDuration().Seconds()))))
		}
		parts = append(parts, Section{Title: p.s("選取的種子"), Lines: detail})
	}
	e.Description = runes(sections(parts...), 4000)
	e.Footer = p.f("收成特殊種子有 %s%% 機率取回 1 顆。每天最多分享 %d 顆給朋友。",
		bpPct(cfg.InheritBP), cfg.ShareSendPerDay)

	var options []option
	for _, s := range pageSeeds {
		if s.Busy {
			continue
		}
		options = append(options, option{seedOptionLabel(s), fmt.Sprint(s.ID), seedOptionDesc(s), cropEmoji(s.CropCode)})
	}
	selected := ""
	if sel != nil {
		selected = fmt.Sprint(sel.ID)
	}
	placeholder := p.s("🌰 選擇種子…")
	if pages > 1 {
		placeholder = p.f("🌰 選擇種子（第 %d/%d 頁）…", page+1, pages)
	}
	p.selectMenu(placeholder, options, func(ctx context.Context, it ui.Interaction, value string) error {
		id, _ := strconv.ParseInt(value, 10, 64)
		p.lab.seed = id
		p.goTo(ctx, it, "lab_seeds", "")
		return nil
	}, 0, selected)
	if codes, _, _ := seedCrops(v.Seeds); sel == nil && len(codes) > 1 {
		// 作物篩選（2026-11-17）：選了種子之後這一列給「種下」選單，按「取消選取」回來改篩選
		opts, fpage, fpages := pagedOptions(cropFilterOptions(v.Seeds, p.s("種子庫顯示所有種子")), p.lab.filterPage)
		p.lab.filterPage = fpage
		holder := p.s("🔎 篩選作物：全部")
		if name := cropFilterName(v.Seeds, p.lab.filter); name != "" {
			holder = p.s("🔎 篩選作物：只看 ") + name
		}
		p.selectMenu(holder+p.s("（選擇要看的作物）…"), opts, func(ctx context.Context, it ui.Interaction, value string) error {
			if !turnPage(value, &p.lab.filterPage, fpages) {
				p.lab.filter, p.lab.seedPage = strings.TrimPrefix(value, cropFilterPrefix), 0
			}
			p.goTo(ctx, it, "lab_seeds", "")
			return nil
		}, 1, "")
	}
	if sel != nil {
		p.button(p.s("取消選取／篩選"), func(ctx context.Context, it ui.Interaction) error {
			p.lab.seed = 0
			p.goTo(ctx, it, "lab_seeds", "")
			return nil
		}, btn{emoji: "🔎", row: 4})
	}
	if sel != nil {
		seedID := sel.ID
		var plots []option
		for _, n := range v.EmptyPlots {
			plots = append(plots, option{p.f("種到第 %d 格", n), fmt.Sprint(n), p.s(sel.CropName), "🌱"})
		}
		// 不能種的時候留一個停用的選單說明原因（以前沒有空地時選單會整個消失，玩家以為不能種）
		blocked := ""
		switch {
		case sel.OffSeason != "":
			blocked = p.s("🌱 現在不是") + p.s(sel.CropName) + p.s("的季節，不能種下")
		case len(plots) == 0:
			blocked = p.s("🌱 農田沒有空地，收成後才能種下這顆種子")
		}
		if blocked != "" {
			if s := p.selectMenu(blocked, []option{{p.s("目前不能種下"), "none", "", "🌱"}},
				func(context.Context, ui.Interaction, string) error { return nil }, 1, ""); s != nil {
				s.Disabled = true
			}
			plots = nil
		}
		p.selectMenu(p.f("🌱 種下這顆種子（空地 %d 格）…", len(v.EmptyPlots)), plots, func(ctx context.Context, it ui.Interaction, value string) error {
			n, _ := strconv.Atoi(value)
			p.lab.seed = 0
			p.act(ctx, it, p.call("plant_lab_seed", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().PlantLabSeed(ctx, p.playerID, seedID, n)
			}), actOpts{})
			return nil
		}, 1, "")
		if p.groupID != nil {
			groupID := *p.groupID
			var targets []option
			for _, tg := range d.targets {
				o := option{p.f("分享給 %s", tg.PlayerName), fmt.Sprint(tg.PlayerID), tg.FarmName, "🎁"}
				if tg.Full { // 2026-11-19：今天收滿了先標出來，不用選了才知道送不出去
					o.desc, o.emoji = p.f("%s｜今天已收滿 %d 位玩家的分享", tg.FarmName, cfg.ShareReceiveSenders), "🚫"
				}
				targets = append(targets, o)
			}
			left := max(0, cfg.ShareSendPerDay-v.SentToday)
			if left > 0 {
				p.selectMenu(p.f("🎁 分享給同伺服器的玩家（今天還能送 %d 顆）…", left), targets,
					func(ctx context.Context, it ui.Interaction, value string) error {
						target, _ := strconv.ParseInt(value, 10, 64)
						p.lab.seed = 0
						p.act(ctx, it, p.call("share_lab_seed", func(ctx context.Context) (services.ActionResult, error) {
							return p.svc().ShareLabSeed(ctx, groupID, p.playerID, seedID, target)
						}), actOpts{noProgress: true})
						return nil
					}, 2, "")
			}
		}
		free := v.Built && v.FreeSlots > 0
		crystals := v.Materials[seedlab.MatCrystal] >= cfg.MutateCrystal
		p.button(p.s("突變研究"), func(ctx context.Context, it ui.Interaction) error {
			p.lab.seed = 0
			p.act(ctx, it, p.call("start_mutation", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().StartMutation(ctx, p.playerID, seedID, false)
			}), actOpts{page: "lab", noProgress: true})
			return nil
		}, btn{emoji: "☢️", style: ui.Primary, row: 3, disabled: !free || !crystals})
		p.button(p.s("突變研究＋古代核心"), func(ctx context.Context, it ui.Interaction) error {
			p.lab.seed = 0
			p.act(ctx, it, p.call("start_mutation", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().StartMutation(ctx, p.playerID, seedID, true)
			}), actOpts{page: "lab", noProgress: true})
			return nil
		}, btn{emoji: "🌳", row: 3, disabled: !free || !crystals || v.Materials[seedlab.MatAncientCore] == 0})
		p.button(p.s("育苗"), func(ctx context.Context, it ui.Interaction) error {
			p.lab.cloneSeed, p.lab.cloneBatches, p.lab.cloneHerb = seedID, 1, false
			p.goTo(ctx, it, "lab_clone", "")
			return nil
		}, btn{emoji: "🌱", style: ui.Success, row: 3, disabled: !free})
	}
	p.pageButtons("", &p.lab.seedPage, pages, 3, "lab_seeds")
	p.labNav(4)
	p.nav(p.s("回農田"), "plots", btn{emoji: "🌱", row: 4})
	// 種子庫擴充（2026-11-15）：按兩次確認
	if v.Built && v.NextLibraryPrice > 0 {
		step := seedlab.Current().LibraryStep
		label, style := p.f("種子庫 +%d 格（%s 金）", step, money(v.NextLibraryPrice)), ui.Secondary
		if p.confirm == "seed_library" {
			label, style = p.s("確認擴充種子庫"), ui.Danger
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			if p.confirm != "seed_library" {
				p.confirm = "seed_library"
				p.refresh(ctx, it, p.f("⚠️ 種子庫擴充到 %d 格需要 %s 金幣，再按一次「確認擴充種子庫」。", v.Capacity+step, money(v.NextLibraryPrice)),
					nil, p.s("重繪"), time.Time{})
				return nil
			}
			p.act(ctx, it, p.call("expand_seed_library", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().ExpandSeedLibrary(ctx, p.playerID)
			}), actOpts{})
			return nil
		}, btn{emoji: "🌰", row: 4, style: style, disabled: v.Gold < v.NextLibraryPrice})
	}
	return e, nil
}

// ---------- 育苗（2026-11-15） ----------

func (p *Panel) buildLabClone(ctx context.Context, data any) (*ui.Embed, error) {
	v := data.(services.SeedLabView)
	st := &p.lab
	sel := findSeed(v, st.cloneSeed)
	if !v.Built || sel == nil || sel.Busy {
		st.cloneSeed = 0
		return p.buildLabSeeds(ctx, labSeedsData{lab: v})
	}
	cfg := seedlab.Current()
	batches := max(1, min(st.cloneBatches, cfg.CloneMaxBatches))
	st.cloneBatches = batches
	herbNeed := cfg.CloneHerb * batches
	if st.cloneHerb && v.Materials[seedlab.MatHerb] < herbNeed {
		st.cloneHerb = false
	}
	fee := sel.CloneFee
	total := fee * batches
	two, three := seedlab.CloneChanceBP(false)
	htwo, hthree := seedlab.CloneChanceBP(true)
	room := v.Capacity - len(v.Seeds) // 每次至少 1 顆要放得下（還沒領取的育苗在服務端另外算）
	e := &ui.Embed{Title: p.s("🌱 ✦ 育苗 ✦"), Color: Teal}
	chance := func(two, three int) string {
		return p.f("1 顆 %s%%、2 顆 %s%%、3 顆 %s%%", bpPct(10_000-two), bpPct(two-three), bpPct(three))
	}
	herbLine := p.f("🌿 不加藥草：%s", chance(two, three))
	herbLine2 := p.f("🌿 加藥草（每次 ×%d）：%s", cfg.CloneHerb, chance(htwo, hthree))
	plan := []string{
		p.f("排程 **%d** 次：%s 金幣%s，約 %s 後一起領取", batches, money(total),
			map[bool]string{true: p.f("＋🌿藥草 ×%d", herbNeed), false: ""}[st.cloneHerb],
			p.dur(true, int(seedlab.CloneDuration(batches).Seconds()))),
		p.f("預計得到 **%d–%d** 顆（%s）", batches, batches*3, map[bool]string{true: p.s("加藥草"), false: p.s("不加藥草")}[st.cloneHerb]),
	}
	if v.Gold < total {
		plan = append(plan, p.f("❌ 金幣不足（目前 %s）", money(v.Gold)))
	}
	if room < batches {
		plan = append(plan, p.f("❌ 種子庫空位不夠：排程 %d 次至少要 %d 個空位（目前 %d）", batches, batches, max(0, room)))
	}
	e.Description = sections(
		Section{Title: p.s("要育苗的種子"), Lines: []string{seedLine(*sel), sel.Genes.Text()}},
		Section{Title: p.s("每次的結果"), Lines: []string{p.f("💰 每次 %s 金幣、約 %s", money(fee),
			p.dur(true, int(seedlab.CloneDuration(1).Seconds()))), herbLine, herbLine2}},
		Section{Title: p.s("這次排程"), Lines: plan},
		"🧰 "+labMaterialLine(v, seedlab.MatHerb)+p.f("　💰 金幣 %s", money(v.Gold)),
	)
	e.Footer = p.s("育苗的種子基因、世代和原本的一樣，但不會複製突變。完成前原本的種子不能種或雜交，完成後放回種子庫。")

	var counts []option
	for n := 1; n <= cfg.CloneMaxBatches; n++ {
		counts = append(counts, option{p.f("排程 %d 次", n), fmt.Sprint(n),
			p.f("%s 金幣、約 %s、得到 %d–%d 顆", money(fee*n), p.dur(true, int(seedlab.CloneDuration(n).Seconds())), n, n*3), "🗓️"})
	}
	p.selectMenu(p.s("🗓️ 選擇育苗次數…"), counts, func(ctx context.Context, it ui.Interaction, value string) error {
		st.cloneBatches, _ = strconv.Atoi(value)
		p.goTo(ctx, it, "lab_clone", "")
		return nil
	}, 0, fmt.Sprint(batches))
	herbLabel, herbStyle := p.f("加藥草（需要 %d，持有 %d）", herbNeed, v.Materials[seedlab.MatHerb]), ui.Secondary
	if st.cloneHerb {
		herbLabel, herbStyle = "✓ "+herbLabel, ui.Success
	}
	p.button(herbLabel, func(ctx context.Context, it ui.Interaction) error {
		st.cloneHerb = !st.cloneHerb
		p.goTo(ctx, it, "lab_clone", "")
		return nil
	}, btn{emoji: "🌿", style: herbStyle, row: 1, disabled: !st.cloneHerb && v.Materials[seedlab.MatHerb] < herbNeed})
	seedID, herb := sel.ID, st.cloneHerb
	p.button(p.f("開始育苗（%s 金）", money(total)), func(ctx context.Context, it ui.Interaction) error {
		st.cloneSeed, st.seed = 0, 0
		p.act(ctx, it, p.call("start_clone", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().StartClone(ctx, p.playerID, seedID, batches, herb)
		}), actOpts{page: "lab", noProgress: true})
		return nil
	}, btn{emoji: "🌱", style: ui.Success, row: 1, disabled: v.FreeSlots == 0 || v.Gold < total || room < batches})
	p.nav(p.s("回種子庫"), "lab_seeds", btn{emoji: "🌰", row: 2})
	p.labNav(2)
	return e, nil
}

// ---------- 雜交培育 ----------

func (p *Panel) buildLabCross(ctx context.Context, data any) (*ui.Embed, error) {
	v := data.(services.SeedLabView)
	if !v.Built {
		return p.buildLab(ctx, v)
	}
	st := &p.lab
	mother := findSeed(v, st.mother)
	if mother != nil && mother.Busy {
		mother, st.mother = nil, 0
	}
	father := findSeed(v, st.father)
	// 配方隱藏（2026-11-14）：任兩顆種子都能試驗配對，不再只列出相容的種子
	if father == nil || father.Busy || mother == nil || father.ID == mother.ID {
		father, st.father = nil, 0
	}
	if st.resin && v.Materials[seedlab.MatResin] == 0 {
		st.resin = false
	}
	if st.herb && v.Materials[seedlab.MatHerb] == 0 {
		st.herb = false
	}
	if st.catalyst != "" && v.Materials[seedlab.MatCrystal] == 0 {
		st.catalyst = ""
	}
	name := func(code string) string { return lookupOr(v.CropNames, code, code) }
	e := &ui.Embed{Title: p.s("🧬 ✦ 雜交培育 ✦"), Color: Teal}
	var preview []string
	if mother != nil {
		preview = append(preview, p.s("♀ 母本：")+seedLine(*mother))
	}
	if father != nil {
		preview = append(preview, p.s("♂ 父本：")+seedLine(*father))
	}
	if mother != nil && father != nil {
		plan := seedlab.CrossPlan{Mother: mother.CropCode, Father: father.CropCode, Resin: st.resin, Herb: st.herb, Catalyst: st.catalyst}
		code, bp, _ := plan.Result()
		switch {
		case mother.CropCode == father.CropCode:
			preview = append(preview, p.f("➡️ 改良基因（同品種）：**%s**　成功率 100%%　約 %s", name(code),
				p.dur(true, int(plan.Duration().Seconds()))))
		case services.CrossKnown(mother.CropCode, father.CropCode, v.KnownHybrids):
			preview = append(preview, p.f("➡️ 雜交：**%s**　成功率 %s%%　約 %s", name(code), fmtPct(float64(bp)/100),
				p.dur(true, int(plan.Duration().Seconds()))),
				p.f("失敗時得到%s種子，材料退回。", p.s(mother.CropName)))
		default:
			preview = append(preview, p.s("➡️ 試驗雜交：**❔ 結果未知**（成功率、時間培育後才知道）"),
				p.f("不能雜交或失敗時得到%s種子，材料退回。", p.s(mother.CropName)))
		}
	} else {
		preview = append(preview, p.s("先選母本，再選父本。不同品種的組合要自己試驗，成功雜交出新作物就會記進配方表。"))
	}
	var mats []string
	if st.resin {
		mats = append(mats, p.f("🍯 樹脂（成功率 +%s%%）", bpPct(seedlab.Current().ResinSuccessBP)))
	}
	if st.herb {
		mats = append(mats, p.f("🌿 藥草（時間 -%d%%）", seedlab.Current().HerbTimePct))
	}
	if st.catalyst != "" {
		mats = append(mats, p.f("💎 水晶催化%s基因（升級機率 +%s%%）", seedlab.GeneNames[st.catalyst], bpPct(seedlab.Current().CatalystUpBP)))
	}
	if len(mats) == 0 {
		mats = []string{p.s("沒有使用（材料都是選用的）")}
	}
	// 配方表只列出已發現的配方（取得過該雜交作物的種子），其餘只顯示數量
	var table []string
	hybrids := seedlab.CurrentHybrids()
	for _, h := range hybrids {
		if v.KnownHybrids[h.Code] {
			table = append(table, p.f("%s × %s → **%s**（%s%%、%s）", name(h.A), name(h.B), name(h.Code), bpPct(h.SuccessBP),
				p.dur(true, h.Minutes*60)))
		}
	}
	found := len(table)
	if hidden := len(hybrids) - found; hidden > 0 {
		table = append(table, p.f("❔ 還有 **%d** 種配方沒有發現：把兩種**不同的一般作物或稀有作物**配對試試看！", hidden))
	}
	tableTitle := p.f("雜交配方（已發現 %d/%d）", found, len(hybrids))
	e.Description = runes(sections(
		Section{Title: p.s("這次培育"), Lines: preview},
		Section{Title: p.s("培育材料"), Lines: mats},
		"🧰 "+labMaterialLine(v, seedlab.MatResin, seedlab.MatHerb, seedlab.MatCrystal),
		Section{Title: tableTitle, Lines: table},
	), 4000)
	e.Footer = p.f("♂ 父本會被用掉，♀ 母本保留。研究槽空 %d 個。基因怎麼遺傳見「📖 實驗室說明」。", v.FreeSlots)

	var mothers []services.LabSeedView
	for _, s := range v.Seeds {
		if !s.Busy {
			mothers = append(mothers, s)
		}
	}
	selected := ""
	if mother != nil {
		selected = fmt.Sprint(mother.ID)
	}
	// 種子超過 25 顆時分頁：翻頁放在選單裡（第一項「上一頁」、最後一項「下一頁」），不再另外佔用按鈕列
	mPage, mPages := p.seedSelect(p.s("♀ 選擇母本"), mothers, &st.motherCrop, &st.motherPage, 0, selected,
		func(s services.LabSeedView) option {
			return option{seedOptionLabel(s), fmt.Sprint(s.ID), seedOptionDesc(s), cropEmoji(s.CropCode)}
		}, func(id int64) { st.mother = id }, "lab_cross")
	if mPages > 1 && mother != nil {
		// 2026-11-16 選好母本後選單只顯示已選的種子、看不到頁碼，玩家不知道怎麼改選其他頁的種子
		e.Description += p.f("\n\n🔄 母本選單共 %d 頁（目前第 %d 頁）：要改選其他頁的種子，打開母本選單，選最上面的「上一頁」或最下面的「下一頁」。",
			mPages, mPage+1)
	}
	if mother != nil {
		var fathers []services.LabSeedView
		for _, s := range v.Seeds {
			if !s.Busy && s.ID != mother.ID {
				fathers = append(fathers, s)
			}
		}
		fsel := ""
		if father != nil {
			fsel = fmt.Sprint(father.ID)
		}
		if len(fathers) == 0 {
			e.Description += p.s("\n\n⚠️ 種子庫沒有其他可以配對的種子。")
		}
		p.seedSelect(p.s("♂ 選擇父本"), fathers, &st.fatherCrop, &st.fatherPage, 1, fsel,
			func(s services.LabSeedView) option {
				result := p.s("改良") + name(mother.CropCode)
				if s.CropCode != mother.CropCode {
					result = p.s("❔ 試驗雜交")
					if services.CrossKnown(mother.CropCode, s.CropCode, v.KnownHybrids) {
						result = name(seedlab.FindHybrid(mother.CropCode, s.CropCode).Code)
					}
				}
				return option{seedOptionLabel(s), fmt.Sprint(s.ID), "→ " + result + p.s("　") + seedOptionDesc(s), cropEmoji(s.CropCode)}
			}, func(id int64) { st.father = id }, "lab_cross")
	}
	if v.Materials[seedlab.MatCrystal] > 0 {
		cat := []option{{p.s("不使用水晶催化劑"), "none", "", "⬜"}}
		for _, g := range seedlab.GeneOrder {
			cat = append(cat, option{p.f("水晶催化%s基因", seedlab.GeneNames[g]), g,
				p.f("這個基因升級機率 +%s%%（消耗 1 個水晶）", bpPct(seedlab.Current().CatalystUpBP)), seedlab.GeneEmoji[g]})
		}
		csel := st.catalyst
		if csel == "" {
			csel = "none"
		}
		p.selectMenu(p.s("💎 水晶催化劑…"), cat, func(ctx context.Context, it ui.Interaction, value string) error {
			st.catalyst = value
			if value == "none" {
				st.catalyst = ""
			}
			p.goTo(ctx, it, "lab_cross", "")
			return nil
		}, 2, csel)
	}
	toggle := func(label, emoji string, on *bool, have int) {
		style := ui.Secondary
		if *on {
			style, label = ui.Success, "✓ "+label
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			*on = !*on
			p.goTo(ctx, it, "lab_cross", "")
			return nil
		}, btn{emoji: emoji, style: style, row: 3, disabled: have == 0 && !*on})
	}
	toggle(p.f("樹脂（%d）", v.Materials[seedlab.MatResin]), "🍯", &st.resin, v.Materials[seedlab.MatResin])
	toggle(p.f("藥草（%d）", v.Materials[seedlab.MatHerb]), "🌿", &st.herb, v.Materials[seedlab.MatHerb])
	p.button(p.s("開始培育"), func(ctx context.Context, it ui.Interaction) error {
		m, f, resin, herb, cat := st.mother, st.father, st.resin, st.herb, st.catalyst
		p.lab = labState{}
		p.act(ctx, it, p.call("start_cross", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().StartCross(ctx, p.playerID, m, f, resin, herb, cat)
		}), actOpts{page: "lab", noProgress: true})
		return nil
	}, btn{emoji: "🧬", style: ui.Success, row: 3, disabled: mother == nil || father == nil || v.FreeSlots == 0})
	p.labNav(4)
	return e, nil
}

// ---------- 培育紀錄 ----------

func (p *Panel) loadLabRecords(ctx context.Context) (any, error) {
	return p.svc().LabRecords(ctx, p.playerID, logMaxRecords)
}

// labRecordsPerPage 培育紀錄每頁幾筆（每筆一行，最長約 150 字）。
const labRecordsPerPage = 15

func (p *Panel) buildLabRecords(_ context.Context, data any) (*ui.Embed, error) {
	all := data.([]services.LabRecordView)
	rows, page, pages := pageSlice(all, p.labRecordPage, labRecordsPerPage)
	p.labRecordPage = page
	e := &ui.Embed{Title: p.s("📜 ✦ 培育紀錄 ✦"), Color: Teal}
	var lines []string
	for _, r := range rows {
		lines = append(lines, p.f("%s　%s", tsv(r.CreatedAt, "d"), r.Text))
	}
	if len(lines) == 0 {
		e.Description = p.s("還沒有培育紀錄。")
	} else {
		e.Description = runes(strings.Join(lines, "\n"), 4000)
	}
	e.Footer = p.f("第 %d/%d 頁，共 %d 筆（可翻閱最近 %d 筆）。突變：%s。", page+1, pages, len(all), logMaxRecords, labMutationNames())
	p.labNav(0)
	p.navBack(0)
	p.pageButtons("", &p.labRecordPage, pages, 0, "lab_records")
	return e, nil
}

// labMutationEffect 實驗室限定突變（夜光、豐收、水晶）的效果；其他突變回傳 ""（效果在「品質與突變」頁）。
func labMutationEffect(m string) string {
	switch m {
	case "glow":
		return fmt.Sprintf("售價 ×%s（和黃金作物一樣疊在品質倍率之後）", bpPct(balance.GlowPricePct))
	case "bountiful":
		return fmt.Sprintf("收成數量 +%d%%（無條件捨去，至少 +1）", balance.BountifulBonusPct)
	case "crystal":
		return fmt.Sprintf("收成時額外得到 💎 水晶 %d–%d 個（素材背包要有空位）", balance.CrystalDropMin, balance.CrystalDropMax)
	}
	return ""
}

// labMutationEffects 實驗室限定突變的說明列（2026-11-17 玩家反映沒寫屬性）。
func labMutationEffects() []string {
	var out []string
	for _, m := range seedlab.LabMutations {
		if eff := labMutationEffect(m); eff != "" {
			out = append(out, fmt.Sprintf("**%s**：%s", balance.Mutations[m], eff))
		}
	}
	return out
}

func labMutationNames() string {
	names := make([]string, len(seedlab.LabMutations))
	for i, m := range seedlab.LabMutations {
		names[i] = balance.Mutations[m]
	}
	return strings.Join(names, "、")
}

// ---------- 特殊種子依作物篩選（2026-11-17） ----------

// cropFilterPrefix 篩選選項的值：cropFilterPrefix + 作物代碼；cropFilterAll 顯示全部作物。
const (
	cropFilterPrefix = "__crop:"
	cropFilterAll    = "*"
	cropFilterPick   = "?" // 農田頁：正在選要篩選的作物
)

// seedSelect 單選的種子選單，可以在選單內篩選作物（2026-11-17，雜交頁的母本、父本）：
// 種子有兩種以上作物時第一項是「🔎 篩選作物」，選了會把這個選單換成作物清單，選好作物後只列那種作物的種子。
// filter：""／cropFilterAll = 全部、cropFilterPick = 正在選作物、其他為作物代碼。超過 25 項時選單內翻頁。
// 回傳修正後的頁碼與總頁數。
func (p *Panel) seedSelect(holder string, seeds []services.LabSeedView, filter *string, page *int, row int, selected string,
	opt func(services.LabSeedView) option, pick func(id int64), reload string) (int, int) {
	codes, _, _ := seedCrops(seeds)
	if len(codes) <= 1 && *filter == cropFilterPick {
		*filter = ""
	}
	if *filter == cropFilterPick {
		opts, pg, pages := pagedOptions(cropFilterOptions(seeds, "所有作物的種子一起列出"), *page)
		*page = pg
		p.selectMenu(fmt.Sprintf("🔎 %s：先選擇要看的作物（共 %d 顆）…", strings.TrimLeft(holder, "♀♂ "), len(seeds)), opts,
			func(ctx context.Context, it ui.Interaction, value string) error {
				if !turnPage(value, page, pages) {
					*filter, *page = strings.TrimPrefix(value, cropFilterPrefix), 0
				}
				p.goTo(ctx, it, reload, "")
				return nil
			}, row, "")
		return pg, pages
	}
	shown := filterSeeds(sortSeedsByCrop(seeds), *filter)
	if len(shown) == 0 {
		*filter, shown = "", sortSeedsByCrop(seeds)
	}
	var all []option
	if len(codes) > 1 {
		label := "🔎 篩選作物（只看某種作物）"
		if name := cropFilterName(seeds, *filter); name != "" {
			label = "🔎 改選其他作物（目前只看" + name + "）"
		}
		all = append(all, option{label, cropFilterPrefix, "選這項會打開作物清單", "🔎"})
	}
	for _, s := range shown {
		all = append(all, opt(s))
	}
	opts, pg, pages := pagedOptions(all, *page)
	*page = pg
	h := holder
	if name := cropFilterName(seeds, *filter); name != "" {
		h += "（只看" + name + "）"
	}
	if pages > 1 {
		h += fmt.Sprintf("（第 %d/%d 頁）", pg+1, pages)
	}
	p.selectMenu(h+"…", opts, func(ctx context.Context, it ui.Interaction, value string) error {
		switch {
		case value == cropFilterPrefix:
			*filter, *page = cropFilterPick, 0
		case turnPage(value, page, pages):
		default:
			id, _ := strconv.ParseInt(value, 10, 64)
			pick(id)
		}
		p.goTo(ctx, it, reload, "")
		return nil
	}, row, selected)
	return pg, pages
}

// sortSeedsByCrop 同一種作物的種子排在一起（依作物名稱；同作物維持原本順序），不再各種種子穿插。
func sortSeedsByCrop(seeds []services.LabSeedView) []services.LabSeedView {
	out := slices.Clone(seeds)
	slices.SortStableFunc(out, func(a, b services.LabSeedView) int { return strings.Compare(a.CropName, b.CropName) })
	return out
}

// filterSeeds 只留 crop 這種作物的種子（"" 或 cropFilterAll = 全部）。
func filterSeeds(seeds []services.LabSeedView, crop string) []services.LabSeedView {
	if crop == "" || crop == cropFilterAll {
		return seeds
	}
	return slices.DeleteFunc(slices.Clone(seeds), func(s services.LabSeedView) bool { return s.CropCode != crop })
}

// seedCrops 種子裡有哪些作物（依作物名稱）與各自的顆數。
func seedCrops(seeds []services.LabSeedView) (codes []string, names map[string]string, counts map[string]int) {
	names, counts = map[string]string{}, map[string]int{}
	for _, s := range sortSeedsByCrop(seeds) {
		if counts[s.CropCode] == 0 {
			codes = append(codes, s.CropCode)
			names[s.CropCode] = s.CropName
		}
		counts[s.CropCode]++
	}
	return codes, names, counts
}

// cropFilterOptions 篩選選單：第一項「全部作物」，接著每種作物一項（超過 25 項時由呼叫端分頁）。
func cropFilterOptions(seeds []services.LabSeedView, allDesc string) []option {
	codes, names, counts := seedCrops(seeds)
	out := []option{{fmt.Sprintf("全部作物（%d 顆）", len(seeds)), cropFilterPrefix + cropFilterAll, allDesc, "🌈"}}
	for _, c := range codes {
		out = append(out, option{fmt.Sprintf("只看 %s（%d 顆）", names[c], counts[c]), cropFilterPrefix + c, "", cropEmoji(c)})
	}
	return out
}

// cropFilterName 篩選條件的顯示名稱（全部時為 ""）。
func cropFilterName(seeds []services.LabSeedView, crop string) string {
	if crop == "" || crop == cropFilterAll {
		return ""
	}
	for _, s := range seeds {
		if s.CropCode == crop {
			return s.CropName
		}
	}
	return ""
}
