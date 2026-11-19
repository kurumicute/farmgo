package bot

// 加工品售價表（Go 版新增，2026-11-19 使用者要求）：加工頁的「📋 售價表」按鈕，依設施列出所有加工品的
// 原料、時間、售價，以及和原料直接賣掉相比的損益（沒建造的設施也列出，方便規劃要先蓋哪一座）。
// 2026-11-19：加工品售價每 3 天抽一次（總成本 -50%～+100%），售價後面附上浮動範圍與下次換價時間。

import (
	"context"
	"fmt"
	"strings"

	"farmgm/internal/bot/ui"
	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/craftprice"
	"farmgm/internal/services"
)

func (p *Panel) loadCraftPrices(ctx context.Context) (any, error) {
	return p.svc().Recipes(ctx, &p.playerID)
}

// craftPriceLine 售價表的一行：成品、售價、損益，下一行是原料與時間。
func craftPriceLine(r services.RecipeView) string {
	price := "—"
	if r.SellPrice != nil && *r.SellPrice != 0 {
		price = money(*r.SellPrice*r.OutputQuantity) + " 金"
	}
	gain := ""
	if r.SellPrice != nil && *r.SellPrice != 0 && r.InputValue != 0 {
		diff := *r.SellPrice*r.OutputQuantity - r.InputValue
		mark := "📈"
		if diff < 0 {
			mark = "📉"
		}
		gain = fmt.Sprintf("　%s %+d%%", mark, diff*100/r.InputValue)
	}
	if r.PriceHigh > r.PriceLow {
		gain += fmt.Sprintf("　（%s～%s）", money(r.PriceLow*r.OutputQuantity), money(r.PriceHigh*r.OutputQuantity))
	}
	out := ""
	if r.OutputQuantity > 1 {
		out = fmt.Sprintf(" ×%d", r.OutputQuantity)
	}
	inputs := make([]string, len(r.InputOrder))
	for i, name := range r.InputOrder {
		inputs[i] = fmt.Sprintf("%s ×%d", name, r.Inputs[name])
	}
	return fmt.Sprintf("**%s**%s　🏷️ **%s**%s\n　📥 %s　⏱️ %s", r.OutputName, out, price, gain,
		strings.Join(inputs, "＋"), services.LongDuration(r.Seconds))
}

func (p *Panel) buildCraftPrices(_ context.Context, data any) (*ui.Embed, error) {
	recipes := data.([]services.RecipeView)
	byBuilding := map[string][]string{}
	for _, r := range recipes {
		byBuilding[r.BuildingCode] = append(byBuilding[r.BuildingCode], craftPriceLine(r))
	}
	var parts []any
	for _, code := range balance.BuildingOrder {
		lines := byBuilding[code]
		if len(lines) == 0 {
			continue
		}
		title := "🏭 " + p.s(balance.Buildings[code].Name)
		if i := indexRecipeBuilding(recipes, code); i >= 0 && !recipes[i].Built {
			title += p.s("（🔒 未建造）")
		}
		parts = append(parts, Section{Title: title, Lines: lines})
	}
	parts = append(parts, p.s("📈／📉 是和「原料直接賣掉」相比的損益（普通品質、目前季節的價格）；售價是一份成品的價格。"))
	for _, r := range recipes {
		if !r.PriceUntil.IsZero() {
			parts = append(parts, p.f("🎲 加工品售價每 %d 天抽一次，範圍是總成本 -%d%%～+%d%%（括號內）；下次換價：%s（台灣時間）。",
				craftprice.PeriodDays, craftprice.DownPct, craftprice.UpPct, r.PriceUntil.In(clock.TaipeiTZ).Format("01-02 15:04")))
			break
		}
	}
	e := &ui.Embed{Title: p.s("📋 ✦ 加工品售價表 ✦"), Color: Blue, Description: runes(sections(parts...), 4000)}
	p.nav(p.s("回加工"), "craft", btn{emoji: "↩️", row: 0})
	p.navBack(0)
	return e, nil
}

func indexRecipeBuilding(recipes []services.RecipeView, code string) int {
	for i, r := range recipes {
		if r.BuildingCode == code {
			return i
		}
	}
	return -1
}
