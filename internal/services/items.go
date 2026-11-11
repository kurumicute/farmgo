package services

// 紀錄、道具、工具（對應 Python GameService 的紀錄與道具段落）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/transactions"
)

// ---------- 紀錄 ----------

// GetRecentTransactions limit 預設 10（1–100）。
func (s *GameService) GetRecentTransactions(ctx context.Context, playerID int64, limit int) ([]TransactionInfo, error) {
	limit = max(1, min(limit, 100))
	return inTx(ctx, s, func(t *db.Tx) ([]TransactionInfo, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		list, err := transactions.RecentForFarm(ctx, t, farm.ID, limit)
		if err != nil {
			return nil, err
		}
		out := make([]TransactionInfo, len(list))
		for i, x := range list {
			out[i] = TransactionInfo{x.CreatedAt, x.EventType, x.ItemCode, x.Quantity, x.GoldDelta, x.Description}
		}
		return out, nil
	})
}

// ---------- 道具 ----------

// FindItem 依代碼或中文名稱找道具代碼；找不到時回傳 ""（可能是作物）。
func FindItem(key string) string {
	key = strings.TrimSpace(key)
	for _, code := range balance.ItemOrder {
		it := balance.Items[code]
		if strings.ToLower(key) == code || key == it.Name || contains(it.Aliases, key) {
			return code
		}
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Buy 商店購買入口：道具或種子。
func (s *GameService) Buy(ctx context.Context, playerID int64, key string, quantity int) (ActionResult, error) {
	if code := FindItem(key); code != "" {
		return s.BuyItem(ctx, playerID, code, quantity)
	}
	return s.BuySeeds(ctx, playerID, key, quantity)
}

// BuyItem quantity 預設 1。
func (s *GameService) BuyItem(ctx context.Context, playerID int64, itemCode string, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	it, ok := balance.Items[itemCode]
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種道具。", itemCode)
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		if it.Kind == "tool" {
			if quantity != 1 {
				return ActionResult{}, gameerr.Validationf("%s是永久工具，一次只能購買 1 個。", it.Name)
			}
			// 農場已鎖定，加上唯一限制，重複指令或並行請求都不會重複扣款
			tool, err := items.GetTool(ctx, t, farm.ID, itemCode)
			if err != nil {
				return ActionResult{}, err
			}
			if tool != nil {
				return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經永久持有%s，不需要再購買。", it.Name)
			}
		}
		cost := it.Price * quantity
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：需要 %d，目前只有 %d。", cost, farm.Gold)
		}
		if it.Kind == "tool" {
			if _, err := items.AddTool(ctx, t, farm.ID, itemCode, now); err != nil {
				return ActionResult{}, err
			}
		} else {
			if err := s.ensureSpace(ctx, t, farm, quantity); err != nil {
				return ActionResult{}, err
			}
			if err := inventory.Add(ctx, t, farm.ID, itemCode, quantity, "normal", "none"); err != nil {
				return ActionResult{}, err
			}
		}
		farm.Gold -= cost
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("購買 %d 個%s，金幣 -%d", quantity, it.Name, cost),
			rec{ItemCode: itemCode, Quantity: quantity, GoldDelta: -cost}); err != nil {
			return ActionResult{}, err
		}
		// 剛買下自動播種機時，立即把空地種滿
		auto := ""
		if itemCode == balance.ToolAutoPlanter {
			if auto, err = s.autoPlant(ctx, t, farm); err != nil {
				return ActionResult{}, err
			}
		}
		return result(fmt.Sprintf("已購買 %d 個%s，花費 %d 金幣。%s", quantity, it.Name, cost, auto), farm.Gold, -cost, itemCode, quantity), nil
	})
}

func (s *GameService) UseFertilizer(ctx context.Context, playerID int64, plotNumber int) (ActionResult, error) {
	name := balance.Items[balance.ItemFertilizer].Name
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plot, now, state, err := s.plotForAction(ctx, t, farm, plotNumber)
		if err != nil {
			return ActionResult{}, err
		}
		if state != rules.PlotGrowing {
			return ActionResult{}, gameerr.Validationf("%s只能用在生長中的土地（第 %d 格目前：%s）。", name, plotNumber, rules.PlotStateLabels[state])
		}
		if plot.Fertilized {
			return ActionResult{}, gameerr.Validationf("第 %d 格這次種植已經施過%s了。", plotNumber, name)
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, balance.ItemFertilizer, 1, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "沒有%s，請先購買（buy fertilizer 1）。", name)
		}
		newReady := now.Add(mulDuration(plot.ReadyAt.Sub(now), balance.FertilizerTimeFactor))
		if !newReady.After(*plot.WateredAt) {
			newReady = plot.WateredAt.Add(time.Microsecond)
		}
		if _, err := t.Exec(ctx, "UPDATE farm_plots SET ready_at = $2, fertilized = true, updated_at = now() WHERE id = $1",
			plot.ID, newReady); err != nil {
			return ActionResult{}, err
		}
		plot.ReadyAt, plot.Fertilized = &newReady, true
		crop, err := crops.Get(ctx, t, *plot.CropTypeID)
		if err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvUseItem,
			fmt.Sprintf("對第 %d 格%s施%s，剩餘時間減少 %d%%", plotNumber, crop.Name, name, balance.FertilizerPct),
			rec{ItemCode: balance.ItemFertilizer, Quantity: 1}); err != nil {
			return ActionResult{}, err
		}
		left := rules.FormatDuration(rules.RemainingSeconds(&newReady, now))
		return result(fmt.Sprintf("已對第 %d 格施%s，剩餘時間減少 %d%%，還需要 %s成熟。", plotNumber, name, balance.FertilizerPct, left),
			farm.Gold, 0, "", 0), nil
	})
}

func (s *GameService) UseEarthworm(ctx context.Context, playerID int64) (ActionResult, error) {
	name := balance.Items[balance.ItemEarthworm].Name
	duration := time.Duration(balance.EarthwormDurationSeconds) * time.Second
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, balance.ItemEarthworm, 1, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "沒有%s，請先購買（buy earthworm 1）。", name)
		}
		now := s.now()
		effect, err := items.GetEffect(ctx, t, farm.ID, balance.EffectEarthworm)
		if err != nil {
			return ActionResult{}, err
		}
		action := "啟用"
		boosted := 0
		switch {
		case effect != nil && !now.Before(effect.StartedAt) && now.Before(effect.ExpiresAt):
			effect.ExpiresAt = effect.ExpiresAt.Add(duration) // 有效期間再次使用：延長
			action = "延長"
			_, err = db.UpdateByID(ctx, t, models.TableFarmEffect, effect.ID, db.C("expires_at", effect.ExpiresAt))
		case effect != nil:
			// 上一次效果結束後才澆水的作物還沒加速過（效果期間澆水或當時正在生長的都已經加速）
			since := effect.ExpiresAt
			if boosted, err = s.earthwormGrowing(ctx, t, farm, now, &since); err != nil {
				return ActionResult{}, err
			}
			effect.StartedAt, effect.ExpiresAt = now, now.Add(duration)
			effect.Multiplier = balance.EarthwormGrowMultiplier
			_, err = db.UpdateByID(ctx, t, models.TableFarmEffect, effect.ID, db.C("started_at", effect.StartedAt),
				db.C("expires_at", effect.ExpiresAt), db.C("multiplier", effect.Multiplier))
		default:
			effect = &models.FarmEffect{FarmID: farm.ID, EffectCode: balance.EffectEarthworm,
				Multiplier: balance.EarthwormGrowMultiplier, StartedAt: now, ExpiresAt: now.Add(duration)}
			effect.ID, err = db.InsertReturningID(ctx, t, models.TableFarmEffect, db.C("farm_id", effect.FarmID),
				db.C("effect_code", effect.EffectCode), db.C("multiplier", effect.Multiplier),
				db.C("started_at", effect.StartedAt), db.C("expires_at", effect.ExpiresAt))
			if err == nil {
				boosted, err = s.earthwormGrowing(ctx, t, farm, now, nil)
			}
		}
		if err != nil {
			return ActionResult{}, err
		}
		until := taipeiFmt(effect.ExpiresAt, "01-02 15:04")
		detail := fmt.Sprintf("使用%s（%s），生長加速至 %s", name, action, until)
		growing := ""
		if boosted > 0 {
			detail += fmt.Sprintf("，生長中的 %d 格一起加速", boosted)
			growing = fmt.Sprintf("正在生長的 %d 格剩餘時間也 ×%v；", boosted, balance.EarthwormGrowMultiplier)
		}
		if err := s.record(ctx, t, farm, rules.EvUseItem, detail, rec{ItemCode: balance.ItemEarthworm, Quantity: 1}); err != nil {
			return ActionResult{}, err
		}
		left := LongDuration(rules.RemainingSeconds(&effect.ExpiresAt, now))
		return result(fmt.Sprintf("已%s%s效果：%s之後澆水的作物生長時間 ×%v，剩餘 %s。", action, name, growing,
			balance.EarthwormGrowMultiplier, left), farm.Gold, 0, "", 0), nil
	})
}

// earthwormGrowing 啟用蚯蚓時，正在生長的作物剩餘時間 ×EarthwormGrowMultiplier；since 不是 nil 時只算在這之後澆水的
// （之前的已經在上一次效果中加速過）。呼叫端已鎖農場；回傳加速的格數（Python _earthworm_growing）。
func (s *GameService) earthwormGrowing(ctx context.Context, t *db.Tx, farm *models.Farm, now time.Time, since *time.Time) (int, error) {
	plots, err := s.plotsInState(ctx, t, farm, rules.PlotGrowing)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, plot := range plots {
		if since != nil && plot.WateredAt.Before(*since) {
			continue
		}
		newReady := now.Add(mulDuration(plot.ReadyAt.Sub(now), balance.EarthwormGrowMultiplier))
		if floor := plot.WateredAt.Add(time.Microsecond); newReady.Before(floor) {
			newReady = floor
		}
		if _, err := t.Exec(ctx, "UPDATE farm_plots SET ready_at = $2, updated_at = now() WHERE id = $1", plot.ID, newReady); err != nil {
			return 0, err
		}
		plot.ReadyAt = &newReady
		count++
	}
	return count, nil
}

// UseItem plotNumber 只用於化肥（其他道具傳 nil）。
func (s *GameService) UseItem(ctx context.Context, playerID int64, key string, plotNumber *int) (ActionResult, error) {
	switch FindItem(key) {
	case balance.ItemFertilizer:
		if plotNumber == nil {
			return ActionResult{}, gameerr.Validationf("使用化肥需要指定土地：use fertilizer <土地編號>")
		}
		return s.UseFertilizer(ctx, playerID, *plotNumber)
	case balance.ItemEarthworm:
		return s.UseEarthworm(ctx, playerID)
	case balance.ToolAutoWaterer:
		return ActionResult{}, gameerr.Validationf("自動澆水器是永久工具，不需要使用；可用 tool on/off auto_waterer 切換。")
	}
	return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種道具。", key)
}

func (s *GameService) SetToolEnabled(ctx context.Context, playerID int64, toolKey string, enabled bool) (ActionResult, error) {
	code := FindItem(toolKey)
	if code == "" || balance.Items[code].Kind != "tool" {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種工具。", toolKey)
	}
	name := balance.Items[code].Name
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, err := items.GetTool(ctx, t, farm.ID, code)
		if err != nil {
			return ActionResult{}, err
		}
		if tool == nil {
			return ActionResult{}, gameerr.NotFoundf("你還沒有%s。", name)
		}
		if _, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("enabled", enabled)); err != nil {
			return ActionResult{}, err
		}
		word := "停用"
		if enabled {
			word = "啟用"
		}
		return result(fmt.Sprintf("%s已%s。", name, word), farm.Gold, 0, "", 0), nil
	})
}
