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
	Keep           map[string][]string // 保留條件（代碼 → 條件），AutoSellKeepAll 是所有作物的
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
		v := AutoSellerView{Owned: true, Enabled: tool.Enabled, All: only == nil, Options: autoSellChoices(all), Keep: keepRules(tool)}
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

// ---------- 保留條件（2026-11-22 使用者要求） ----------
//
// 販售項目有勾的作物，收成時符合保留條件（任一條件）就放進倉庫，不自動賣出。例如小麥只保留可以加工的，其他品質、突變的都賣掉。
// 設定記在 farm_tools.settings 的 keep：{"作物代碼或 hybrid": ["craftable", "q:rare", "m:golden", …]}；
// 代碼 AutoSellKeepAll（"*"）是「所有作物」的預設條件，作物有自己的條件時用自己的。

const (
	autoSellKeepKey = "keep"
	// AutoSellKeepAll 保留條件「所有作物」（作物沒有自己的條件時套用）。
	AutoSellKeepAll = "*"
	// KeepCraftable 保留條件「可以加工的」：普通品質，且突變在 CraftMutations 裡。
	KeepCraftable = "craftable"
	// KeepNone 選單的「不保留」：有勾選販售的全部賣出（作物自己的設定會蓋過「所有作物」的條件）。
	KeepNone = "none"
	// KeepInherit 選單的「跟著所有作物」：刪除這種作物自己的條件。
	KeepInherit = "inherit"
)

// KeepChoices 保留條件選單：可以加工的、各品質（不含普通）、各突變（不含無）。
func KeepChoices() []AutoSellOption {
	out := []AutoSellOption{{Code: KeepCraftable, Name: "可以加工的（普通品質）"}}
	for _, q := range balance.QualityOrder[1:] {
		out = append(out, AutoSellOption{Code: "q:" + q, Name: balance.Qualities[q] + "品質"})
	}
	for _, m := range balance.MutationOrder[1:] {
		out = append(out, AutoSellOption{Code: "m:" + m, Name: balance.Mutations[m]})
	}
	return out
}

// KeepName 保留條件的名稱（未知的代碼原樣回傳）。
func KeepName(code string) string {
	for _, c := range KeepChoices() {
		if c.Code == code {
			return c.Name
		}
	}
	return code
}

// keepRules 讀取保留條件；沒有設定時是 nil。
func keepRules(tool *models.FarmTool) map[string][]string {
	raw, ok := tool.Settings[autoSellKeepKey].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for k, v := range raw {
		out[k] = []string{} // 空清單：這種作物不保留（不套用「所有作物」的條件）
		list, _ := v.([]any)
		for _, x := range list {
			if s, ok := x.(string); ok {
				out[k] = append(out[k], s)
			}
		}
	}
	return out
}

// keepFor 這種作物套用的保留條件（自己的，沒有時用「所有作物」的）。
func keepFor(keep map[string][]string, key string) []string {
	if conds, ok := keep[key]; ok {
		return conds
	}
	return keep[AutoSellKeepAll]
}

// keepMatches 這批收成（品質、突變）是否符合任一保留條件。
func keepMatches(conds []string, quality, mutation string) bool {
	if quality == "" {
		quality = "normal"
	}
	if mutation == "" {
		mutation = "none"
	}
	for _, c := range conds {
		switch {
		case c == KeepCraftable:
			if quality == "normal" && slices.Contains(CraftMutations, mutation) {
				return true
			}
		case c == "q:"+quality, c == "m:"+mutation:
			return true
		}
	}
	return false
}

// autoSellsLot 這批收成要不要自動賣出：販售項目有勾，且不符合保留條件。
func (s *GameService) autoSellsLot(ctx context.Context, t *db.Tx, farmID int64, key, quality, mutation string) (bool, error) {
	sells, err := s.autoSells(ctx, t, farmID, key)
	if err != nil || !sells {
		return false, err
	}
	tool, err := items.GetTool(ctx, t, farmID, balance.ToolAutoSeller)
	if err != nil || tool == nil {
		return false, err
	}
	return !keepMatches(keepFor(keepRules(tool), key), quality, mutation), nil
}

// SetAutoSellerKeep 設定一種作物（或 AutoSellKeepAll）的保留條件。
// conds 含 KeepInherit 或為空：刪除這種作物自己的條件（改用「所有作物」的）；含 KeepNone：不保留。
func (s *GameService) SetAutoSellerKeep(ctx context.Context, playerID int64, key string, conds []string) (ActionResult, error) {
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
		name := txt(ctx, "所有作物")
		if key != AutoSellKeepAll {
			all, err := crops.ListAll(ctx, t, false)
			if err != nil {
				return ActionResult{}, err
			}
			found := false
			for _, c := range autoSellChoices(all) {
				if c.Code == key && c.Code != AutoSellFish {
					found, name = true, txt(ctx, c.Name)
				}
			}
			if !found {
				return ActionResult{}, gameerr.Validationf("沒有這個作物。")
			}
		}
		var picked []any
		var shown []string
		for _, c := range KeepChoices() {
			if slices.Contains(conds, c.Code) {
				picked = append(picked, c.Code)
				shown = append(shown, txt(ctx, c.Name))
			}
		}
		settings := map[string]any{}
		for k, v := range tool.Settings {
			settings[k] = v
		}
		keep := map[string]any{}
		if old, ok := settings[autoSellKeepKey].(map[string]any); ok {
			for k, v := range old {
				keep[k] = v
			}
		}
		msg := txf(ctx, "🏧 %s不設保留條件：有勾選販售的都自動賣出。", name)
		if key != AutoSellKeepAll {
			msg = txf(ctx, "🏧 %s改用「所有作物」的保留條件。", name)
		}
		inherit := len(conds) == 0 || slices.Contains(conds, KeepInherit) || (key == AutoSellKeepAll && slices.Contains(conds, KeepNone))
		switch {
		case inherit:
			delete(keep, key)
		case slices.Contains(conds, KeepNone) || len(picked) == 0:
			keep[key] = []any{}
			msg = txf(ctx, "🏧 %s不保留：有勾選販售的都自動賣出。", name)
		default:
			keep[key] = picked
			msg = txf(ctx, "🏧 %s收成時保留：%s，其他的自動賣出。", name, strings.Join(shown, txt(ctx, "、")))
		}
		if len(keep) == 0 {
			delete(settings, autoSellKeepKey)
		} else {
			settings[autoSellKeepKey] = keep
		}
		if len(settings) == 0 {
			settings = nil
		}
		if _, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("settings", settings)); err != nil {
			return ActionResult{}, err
		}
		return result(msg, farm.Gold, 0, "", 0), nil
	})
}
