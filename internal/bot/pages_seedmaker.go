package bot

// 面板頁面：種子製造（Go 版新增，2026-11-14；從「🌱 農田」頁或「🔧 機器」頁進入）。
// 原本只能在「🔧 機器」的「⚙️ 使用機器」選單裡用，而且只列普通品質的作物，玩家常常找不到或選單是空的。

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/services"
)

func (p *Panel) loadSeedMaker(ctx context.Context) (any, error) {
	return p.svc().GetBackpack(ctx, p.playerID)
}

// seedMakerStacks 背包裡能做成種子的作物（依代碼、品質、突變分開）。
func seedMakerStacks(bp services.Backpack) []services.BackpackLine {
	var out []services.BackpackLine
	for _, x := range bp.Produce {
		rare := slices.ContainsFunc(exploration.RareCrops, func(c exploration.RareCrop) bool { return c.Code == x.Code })
		if slices.Contains(exploration.SeedMakerQualities, x.Quality) && slices.Contains(exploration.SeedMakerMutations, x.Mutation) &&
			(exploration.SeedMakerRareCrops || !rare) {
			out = append(out, x)
		}
	}
	return out
}

func seedMakerValue(x services.BackpackLine) string {
	return strings.Join([]string{"seed", x.Code, x.Quality, x.Mutation}, ":")
}

func (p *Panel) buildSeedMaker(_ context.Context, data any) (*ui.Embed, error) {
	bp := data.(services.Backpack)
	m := exploration.Machines["seed_maker"]
	e := &ui.Embed{Title: "🌱 ✦ 種子製造機 ✦", Color: Green}
	if !bp.SeedMaker {
		e.Description = fmt.Sprintf("🔒 還沒有種子製造機。\n\n農場 **Lv.%d** 起可以在「⛰️ 山洞」→「🔧 機器」用探索素材製作，"+
			"之後就能把收成的作物做成種子：1 個作物 = %d 顆種子。", m.UnlockLevel, exploration.SeedMakerRatio)
		p.nav("機器", "machines", btn{emoji: "🔧", row: 0})
		p.nav("回農田", "plots", btn{emoji: "🌱", row: 0})
		p.navBack(0)
		return e, nil
	}
	stacks := seedMakerStacks(bp)
	lines := make([]string, len(stacks))
	for i, x := range stacks {
		lines[i] = fmt.Sprintf("%s %s%s　×**%d**　（%d 金／個）", cropEmoji(x.Code), x.Name,
			services.GeneticsLabel(x.Quality, x.Mutation), x.Quantity, x.Price)
	}
	rule := fmt.Sprintf("1 個作物 = **%d 顆**對應的種子，任何品質、突變都可以，做出來的一律是普通種子。", exploration.SeedMakerRatio)
	if !exploration.SeedMakerRareCrops {
		rule += "\n琥珀瓜、月光花等稀有作物不能做成種子。"
	}
	e.Description = sections(rule, Section{Title: "🧺 可以做成種子的作物", Lines: lines})
	if len(stacks) == 0 {
		e.Description += "\n\n背包裡沒有可以做成種子的作物，先去收成吧。"
	}
	e.Footer = "高品質或突變作物的售價比較高，做成種子前先比較一下。種子放素材背包，不佔倉庫。"
	if p.picker != nil {
		e.AddField("🛒 已選擇", fmt.Sprintf("**%s**\n👇 選擇數量", p.picker.Label), false)
	}

	page, cur, pages := pageOf(stacks, p.seedMakerPage)
	p.seedMakerPage = cur
	options := make([]option, len(page))
	for i, x := range page {
		options[i] = option{fmt.Sprintf("%s%s（持有 %d）", x.Name, services.GeneticsLabel(x.Quality, x.Mutation), x.Quantity),
			seedMakerValue(x), fmt.Sprintf("1 個 → %d 顆種子｜售價 %d 金／個", exploration.SeedMakerRatio, x.Price), cropEmoji(x.Code)}
	}
	selected := ""
	if p.picker != nil {
		selected = p.picker.Key
	}
	p.selectMenu("🌱 選擇要做成種子的作物…", options, func(ctx context.Context, it ui.Interaction, value string) error {
		i := slices.IndexFunc(stacks, func(x services.BackpackLine) bool { return seedMakerValue(x) == value })
		if i < 0 {
			return gameerr.NotFoundf("找不到這個作物。")
		}
		x := stacks[i]
		presets := []int{1, 5, 10}
		if x.Quantity > 10 { // 「全部」：持有數量（不超過一次上限）
			presets = append(presets, min(x.Quantity, exploration.SeedMakerMax))
		}
		p.picker = &Picker{Key: value, Label: fmt.Sprintf("🌱 %s%s（1 個 → %d 顆種子）", x.Name, services.GeneticsLabel(x.Quality, x.Mutation),
			exploration.SeedMakerRatio), Verb: "用", Max: max(1, min(x.Quantity, exploration.SeedMakerMax)), Presets: presets,
			Run: p.callQty("make_seeds", func(ctx context.Context, q int) (services.ActionResult, error) {
				return p.svc().MakeSeeds(ctx, p.playerID, x.Code, q, x.Quality, x.Mutation)
			})}
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, 0, selected)
	p.quantityButtons(1)
	p.pageButtons("作物", &p.seedMakerPage, pages, 2, "seedmaker")
	p.nav("回農田", "plots", btn{emoji: "🌱", row: 3})
	p.nav("機器", "machines", btn{emoji: "🔧", row: 3})
	p.navBack(3)
	return e, nil
}
