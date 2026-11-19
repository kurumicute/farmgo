package bot

// 物品用途查詢（Go 版新增，2026-11-18）：背包頁選一個物品，列出賣價、怎麼做出來、可以做成什麼、
// 菇房養分、世界 Boss 補給。資料都是程式內建的規則（配方、培育器、菇房養分、世界 Boss 預設設定），不查資料庫。

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/mushroom"
	wb "farmgm/internal/game/worldboss"
	"farmgm/internal/services"
)

// usageItem 可以查詢用途的物品。
type usageItem struct {
	key, code, name, emoji string
	crop                   bool
	price                  int
}

// usageItems 背包中可以查詢的物品：農產品（同一種作物只列一次）、動物產品與加工品、道具、素材。
func usageItems(bp services.Backpack) []usageItem {
	var out []usageItem
	seen := map[string]bool{}
	add := func(u usageItem) {
		if !seen[u.key] {
			seen[u.key] = true
			out = append(out, u)
		}
	}
	for _, x := range bp.Produce {
		price := 0 // 單價以普通品質、沒有突變的為準（特殊品質的單價各不相同）
		if x.Quality == "normal" && x.Mutation == "none" {
			price = x.Price
		}
		if i := slices.IndexFunc(out, func(u usageItem) bool { return u.key == "c|"+x.Code }); i >= 0 {
			out[i].price = max(out[i].price, price)
			continue
		}
		add(usageItem{"c|" + x.Code, x.Code, x.Name, cropEmoji(x.Code), true, price})
	}
	for _, x := range bp.Goods {
		add(usageItem{"i|" + x.Code, x.Code, x.Name, "🥚", false, x.Price})
	}
	for _, x := range bp.Consumables {
		add(usageItem{"i|" + x.Code, x.Code, x.Name, lookupOr(ItemEmoji, x.Code, "🎁"), false, 0})
	}
	for _, x := range bp.Materials {
		add(usageItem{"i|" + x.Code, x.Code, x.Name, lookupOr(MaterialEmoji, x.Code, "🪨"), false, x.Price})
	}
	return out
}

// itemUsageLines 一個物品的用途說明。
func itemUsageLines(u usageItem) []string {
	var lines []string
	if u.price > 0 {
		lines = append(lines, fmt.Sprintf("💰 賣價 %s 金／個", money(u.price)))
	}
	inputs := func(order []string, m map[string]int) string {
		parts := make([]string, len(order))
		for i, c := range order {
			parts[i] = fmt.Sprintf("%s ×%d", services.ItemName(c, nil), m[c])
		}
		return strings.Join(parts, "＋")
	}
	var uses []string
	for _, code := range balance.RecipeOrder {
		rc := balance.Recipes[code]
		building := balance.Buildings[rc.Building].Name
		if rc.Output == u.code {
			lines = append(lines, fmt.Sprintf("🏭 做法：在%s用 %s", building, inputs(rc.InputOrder, rc.Inputs)))
		}
		if n := rc.Inputs[u.code]; n > 0 {
			uses = append(uses, fmt.Sprintf("%s（%s，每份 ×%d）", rc.Name, building, n))
		}
	}
	incubators := make([]string, 0, len(exploration.IncubatorRecipes))
	for code := range exploration.IncubatorRecipes {
		incubators = append(incubators, code)
	}
	sort.Strings(incubators)
	for _, code := range incubators {
		rc := exploration.IncubatorRecipes[code]
		if n := rc.Inputs[u.code]; n > 0 {
			uses = append(uses, fmt.Sprintf("%s（稀有作物培育器，每份 ×%d）", rc.Name, n))
		}
	}
	if len(uses) > 0 {
		lines = append(lines, "🔧 可以做成："+strings.Join(uses, "、"))
	}
	if u.crop {
		cfg := mushroom.Current()
		for _, code := range mushroom.NutrientOrder {
			if slices.Contains(cfg.NutrientCrops[code], u.code) {
				n := mushroom.Nutrients[code]
				lines = append(lines, fmt.Sprintf("🍄 種在農田時提供地下菇房「%s」（長出%s，稀有是%s）", n.Name,
					mushroom.SpeciesName(n.Common), mushroom.SpeciesName(n.Rare)))
			}
		}
	}
	if m, ok := exploration.Materials[u.code]; ok && m.Usage != "" {
		lines = append(lines, "📝 "+m.Usage)
	}
	if it, ok := balance.Items[u.code]; ok && it.Desc != "" {
		lines = append(lines, "📝 "+it.Desc)
	}
	s := wb.DefaultSettings()
	supply, points := "", 0
	if it, ok := s.Items[u.code]; ok {
		supply, points = it.Supply, it.Points
	} else if u.crop {
		supply, points = s.CropSupply, wb.PricePoints(u.price, s.GoldPerPoint)
	}
	if info, ok := wb.SupplyInfos[supply]; ok {
		pts := ""
		if points > 0 {
			pts = fmt.Sprintf("，每個約 %d 積分", points)
		}
		lines = append(lines, fmt.Sprintf("🐉 可以捐給世界 Boss：%s %s%s", info.Emoji, info.Name, pts))
	} else {
		lines = append(lines, "🐉 世界 Boss 預設不收")
	}
	if len(lines) == 1 && u.price > 0 {
		lines = append(lines, "📝 沒有其他用途，可以直接賣掉")
	}
	return lines
}

// usageControls 背包頁的「🔍 查詢物品用途」選單與結果欄位。
func (p *Panel) usageControls(e *ui.Embed, bp services.Backpack, row int) {
	items := usageItems(bp)
	if len(items) == 0 {
		p.usageItem = ""
		return
	}
	if i := slices.IndexFunc(items, func(u usageItem) bool { return u.key == p.usageItem }); i >= 0 {
		u := items[i]
		e.AddField(p.f("🔍 %s %s 的用途", u.emoji, u.name), runes(strings.Join(itemUsageLines(u), "\n"), 1000)+
			p.s("\n（世界 Boss 依預設設定，實際以活動設定為準）"), false)
	} else {
		p.usageItem = ""
	}
	options := make([]option, 0, min(len(items), ui.MaxOptions))
	for _, u := range items[:min(len(items), ui.MaxOptions)] {
		options = append(options, option{u.name, u.key, "", u.emoji})
	}
	p.selectMenu(p.s("🔍 查詢物品用途（可以做成什麼、賣價、能不能捐 Boss）…"), options, func(ctx context.Context, it ui.Interaction, key string) error {
		p.usageItem = key
		p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
		return nil
	}, row, p.usageItem)
}
