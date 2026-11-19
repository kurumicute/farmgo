package services

// 自動販售機的販售項目（Go 版新增，2026-11-15）：玩家可以勾選自動販售機要賣哪些東西（每種作物、雜交作物、自動釣魚機的魚）。
// 設定記在 farm_tools.settings 的 sell_only（自動販售機那一列）；沒有這個設定時全部都賣（和以前一樣）。
// 沒勾選的作物收成時放進倉庫（倉庫滿了就不能收成），沒勾選魚時自動釣魚機的魚放進魚簍。

import (
	"context"
	"slices"
	"strings"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/seedlab"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/items"
)

const (
	autoSellOnlyKey = "sell_only"
	// AutoSellHybrid 販售項目「雜交作物」（全部雜交作物一起設定，避免選單洩漏還沒發現的配方）。
	AutoSellHybrid = "hybrid"
	// AutoSellFish 販售項目「自動釣魚機的魚」。
	AutoSellFish = "fish"
)

// AutoSellOption 販售項目選單的一項。
type AutoSellOption struct {
	Code, Name string
	Selected   bool
}

// AutoSellerView 自動販售機的販售項目設定。
type AutoSellerView struct {
	Owned, Enabled bool
	All            bool // 沒有限制（全部都賣）
	Options        []AutoSellOption
}

// sellOnly 自動販售機的販售項目；nil = 沒有限制（全部都賣）。
func sellOnly(tool *models.FarmTool) []string {
	raw, ok := tool.Settings[autoSellOnlyKey].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// autoSellKey 作物在販售項目裡的代碼（雜交作物共用 AutoSellHybrid）。
func autoSellKey(cropCode string) string {
	if seedlab.IsHybrid(cropCode) {
		return AutoSellHybrid
	}
	return cropCode
}

// autoSells 自動販售機啟用且販售項目包含 key（作物代碼、AutoSellHybrid 或 AutoSellFish）。
func (s *GameService) autoSells(ctx context.Context, t *db.Tx, farmID int64, key string) (bool, error) {
	enabled, err := s.toolEnabled(ctx, t, farmID, balance.ToolAutoSeller)
	if err != nil || !enabled {
		return false, err
	}
	tool, err := items.GetTool(ctx, t, farmID, balance.ToolAutoSeller)
	if err != nil || tool == nil {
		return false, err
	}
	only := sellOnly(tool)
	return only == nil || slices.Contains(only, key), nil
}

// autoSellChoices 販售項目選單：一般作物（依作物順序）、雜交作物、魚。
func autoSellChoices(all []models.CropType) []AutoSellOption {
	var out []AutoSellOption
	for _, c := range all {
		if c.Active && !seedlab.IsHybrid(c.Code) {
			out = append(out, AutoSellOption{Code: c.Code, Name: c.Name})
		}
	}
	return append(out, AutoSellOption{Code: AutoSellHybrid, Name: "雜交作物（全部）"},
		AutoSellOption{Code: AutoSellFish, Name: "自動釣魚機的魚"})
}

// AutoSeller 自動販售機的販售項目設定（沒有自動販售機時 Owned 為 false）。
func (s *GameService) AutoSeller(ctx context.Context, playerID int64) (AutoSellerView, error) {
	return inTx(ctx, s, func(t *db.Tx) (AutoSellerView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return AutoSellerView{}, err
		}
		tool, err := items.GetTool(ctx, t, farm.ID, balance.ToolAutoSeller)
		if err != nil || tool == nil {
			return AutoSellerView{}, err
		}
		all, err := crops.ListAll(ctx, t, false)
		if err != nil {
			return AutoSellerView{}, err
		}
		only := sellOnly(tool)
		v := AutoSellerView{Owned: true, Enabled: tool.Enabled, All: only == nil, Options: autoSellChoices(all)}
		for i := range v.Options {
			v.Options[i].Selected = only == nil || slices.Contains(only, v.Options[i].Code)
		}
		return v, nil
	})
}

// SetAutoSellerItems 設定自動販售機要賣哪些東西；勾選全部時改回沒有限制（之後新增的作物也會賣）。
func (s *GameService) SetAutoSellerItems(ctx context.Context, playerID int64, codes []string) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, err := items.GetTool(ctx, t, farm.ID, balance.ToolAutoSeller)
		if err != nil {
			return ActionResult{}, err
		}
		if tool == nil {
			return ActionResult{}, gameerr.NotFoundf("你還沒有自動販售機。")
		}
		all, err := crops.ListAll(ctx, t, false)
		if err != nil {
			return ActionResult{}, err
		}
		choices := autoSellChoices(all)
		var picked []string
		var names []string
		for _, c := range choices {
			if slices.Contains(codes, c.Code) {
				picked, names = append(picked, c.Code), append(names, c.Name)
			}
		}
		if len(picked) == 0 {
			return ActionResult{}, gameerr.Validationf("請至少勾選一項；不想自動販售時可以到背包停用自動販售機。")
		}
		// 改販售項目會影響自動釣魚的結算方式：先結算到現在
		st, err := s.settleAutoFish(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		settings := map[string]any{}
		for k, v := range tool.Settings {
			settings[k] = v
		}
		msg := txt(ctx, "🏧 自動販售機改為全部都賣（之後新增的作物也會賣）。")
		if len(picked) == len(choices) {
			delete(settings, autoSellOnlyKey)
		} else {
			list := make([]any, len(picked))
			for i, c := range picked {
				list[i] = c
			}
			settings[autoSellOnlyKey] = list
			msg = txf(ctx, "🏧 自動販售機只賣：%s。沒勾選的作物收成後放進倉庫。", strings.Join(names, txt(ctx, "、")))
		}
		if len(settings) == 0 {
			settings = nil
		}
		if _, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("settings", settings)); err != nil {
			return ActionResult{}, err
		}
		if n := st.Note(); n != "" {
			msg += "\n" + n
		}
		return result(msg, farm.Gold, 0, "", 0), nil
	})
}
