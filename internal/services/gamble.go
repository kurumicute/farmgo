package services

// 賭場（對應 Python GameService 的賭場段落與 prize_views）。

import (
	"context"
	"fmt"
	"slices"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/inventory"

	eventrules "farmgm/internal/game/events"
	generepo "farmgm/internal/repositories/genetics"
)

// GambleEligible 只有稀有／傳說品質，或巨型作物／黃金作物／珍稀變種可以下注。
func GambleEligible(quality, mutation string) bool {
	return slices.Contains(balance.GambleQualities, quality) || slices.Contains(balance.GambleMutations, mutation)
}

// GambleOptions 背包中可以下注的作物（依品質、突變分開）。
func (s *GameService) GambleOptions(ctx context.Context, playerID int64) ([]GambleOption, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]GambleOption, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		produce, err := crops.ProduceMap(ctx, t)
		if err != nil {
			return nil, err
		}
		settings, err := s.geneticsSettings(ctx, t)
		if err != nil {
			return nil, err
		}
		stacks, err := inventory.ListItems(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		rows := []GambleOption{}
		for _, i := range stacks {
			entry, ok := produce[i.ItemCode]
			if !ok || !GambleEligible(i.Quality, i.Mutation) {
				continue
			}
			c := entry.Crop
			rows = append(rows, GambleOption{
				ItemCode: i.ItemCode, CropCode: c.Code, Quality: i.Quality, Mutation: i.Mutation, Quantity: i.Quantity,
				Name:      genetics.ProduceName(c.Name, c.VariantName, i.Mutation) + GeneticsLabel(i.Quality, i.Mutation),
				UnitPrice: genetics.UnitPrice(c.SellPrice, i.Quality, i.Mutation, settings),
			})
		}
		return rows, nil
	})
}

// Gamble 下注：擲 1–10。比大（6–10）／比小（1–5）贏了價值 ×2，猜中數字 ×5，存成賭場獎品；輸了沒收下注的作物。
// guess 只用於 mode == "exact"（其他玩法傳 nil）。
func (s *GameService) Gamble(ctx context.Context, playerID int64, itemCode, quality, mutation string, quantity int,
	mode string, guess *int) (GambleResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return GambleResult{}, err
	}
	if _, ok := balance.GambleModes[mode]; !ok {
		return GambleResult{}, gameerr.Validationf("玩法只能是比大、比小或猜數字。")
	}
	if mode == "exact" && (guess == nil || *guess < balance.GambleMin || *guess > balance.GambleMax) {
		return GambleResult{}, gameerr.Validationf("請猜 %d–%d 的整數。", balance.GambleMin, balance.GambleMax)
	}
	return inTx(ctx, s, func(t *db.Tx) (GambleResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return GambleResult{}, err
		}
		produce, err := crops.ProduceMap(ctx, t)
		if err != nil {
			return GambleResult{}, err
		}
		entry, ok := produce[itemCode]
		if !ok || !GambleEligible(quality, mutation) {
			return GambleResult{}, gameerr.Validationf("只能用稀有、傳說品質，或巨型作物、黃金作物、珍稀變種的作物下注。")
		}
		c := entry.Crop
		name := genetics.ProduceName(c.Name, c.VariantName, mutation) + GeneticsLabel(quality, mutation)
		removed, err := inventory.Remove(ctx, t, farm.ID, itemCode, quantity, quality, mutation)
		if err != nil {
			return GambleResult{}, err
		}
		if !removed {
			have, err := inventory.GetQuantity(ctx, t, farm.ID, itemCode, quality, mutation)
			if err != nil {
				return GambleResult{}, err
			}
			return GambleResult{}, gameerr.New(gameerr.InsufficientItems, "%s數量不足：想下注 %d 個，倉庫只有 %d 個。", name, quantity, have)
		}
		roll := s.dice()
		var won bool
		switch mode {
		case "big":
			won = roll >= balance.GambleBigFrom
		case "small":
			won = roll < balance.GambleBigFrom
		default:
			won = roll == *guess
		}
		multiplier, choice := balance.GambleHiloMultiplier, balance.GambleModes[mode]
		if mode == "exact" {
			multiplier, choice = balance.GambleExactMultiplier, fmt.Sprintf("猜 %d", *guess)
		}
		outcome := "輸了，作物沒收"
		if won {
			// 贏得的作物從一般庫存移到獎品（總數不變，不會超過倉庫容量）
			if err := inventory.AddPrize(ctx, t, farm.ID, itemCode, quality, mutation, multiplier, quantity); err != nil {
				return GambleResult{}, err
			}
			outcome = fmt.Sprintf("贏了，價值 ×%d", multiplier)
		}
		if err := s.record(ctx, t, farm, rules.EvGamble,
			fmt.Sprintf("賭場（%s）下注 %d 個%s：擲出 %d，%s", choice, quantity, name, roll, outcome),
			rec{ItemCode: itemCode, Quantity: quantity, Quality: quality, Mutation: mutation}); err != nil {
			return GambleResult{}, err
		}
		settings, err := s.geneticsSettings(ctx, t)
		if err != nil {
			return GambleResult{}, err
		}
		unit := genetics.UnitPrice(c.SellPrice, quality, mutation, settings)
		message := fmt.Sprintf("🎲 擲出 **%d**… 💀 你輸了，失去 %d 個%s。", roll, quantity, name)
		if won {
			message = fmt.Sprintf("🎲 擲出 **%d**！🎉 你贏了！%d 個%s的價值變成 ×%d（每個 %d 金），已存進倉庫的「🏆 賭場獎品」。",
				roll, quantity, name, multiplier, unit*multiplier)
		}
		return GambleResult{Roll: roll, Won: won, Multiplier: multiplier, Quantity: quantity, Name: name, Message: message}, nil
	})
}

func (s *GameService) ListPrizes(ctx context.Context, playerID int64) ([]PrizeView, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]PrizeView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		return prizeViews(ctx, t, farm, nil)
	})
}

// SellPrize 賣出賭場獎品：單價 = 價目表單價 × 倍率（再套用活動售價加成）。只能賣自己農場的獎品。
func (s *GameService) SellPrize(ctx context.Context, playerID, prizeID int64, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		list, err := inventory.ListPrizes(ctx, t, farm.ID, true)
		if err != nil {
			return ActionResult{}, err
		}
		var prize *models.GamblePrize
		for i := range list {
			if list[i].ID == prizeID {
				prize = &list[i]
				break
			}
		}
		if prize == nil {
			return ActionResult{}, gameerr.NotFoundf("找不到這個賭場獎品。")
		}
		produce, err := crops.ProduceMap(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		c := produce[prize.ItemCode].Crop
		name := genetics.ProduceName(c.Name, c.VariantName, prize.Mutation) + GeneticsLabel(prize.Quality, prize.Mutation)
		if prize.Quantity < quantity {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s（×%d）只有 %d 個。", name, prize.Multiplier, prize.Quantity)
		}
		settings, err := s.geneticsSettings(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		base := genetics.UnitPrice(c.SellPrice, prize.Quality, prize.Mutation, settings)
		price, sell, err := s.sellPrice(ctx, t, &c, base*prize.Multiplier)
		if err != nil {
			return ActionResult{}, err
		}
		income := price * quantity
		prize.Quantity -= quantity
		if _, err := t.Exec(ctx, "UPDATE gamble_prizes SET quantity = $2, updated_at = now() WHERE id = $1",
			prize.ID, prize.Quantity); err != nil {
			return ActionResult{}, err
		}
		farm.Gold += income
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		applied := eventrules.MergeApplied(sell)
		if err := s.record(ctx, t, farm, rules.EvSell,
			fmt.Sprintf("出售 %d 個%s（賭場 ×%d 獎品，單價 %d），金幣 +%d%s", quantity, name, prize.Multiplier, price, income,
				eventsNote(applied)),
			rec{ItemCode: prize.ItemCode, Quantity: quantity, GoldDelta: income, Quality: prize.Quality, Mutation: prize.Mutation,
				UnitPrice: &price, Applied: applied}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已出售 %d 個%s（賭場 ×%d，單價 %d），獲得 %d 金幣。", quantity, name, prize.Multiplier, price, income),
			farm.Gold, income, prize.ItemCode, quantity), nil
	})
}

// prizeViews 賭場獎品（顯示用）：名稱、數量、倍率與目前單價（價目表單價 × 倍率，不含活動加成）。
// settings 為 nil 時讀取目前設定。
func prizeViews(ctx context.Context, t *db.Tx, farm *models.Farm, settings *balance.GeneticsSettings) ([]PrizeView, error) {
	if settings == nil {
		_, cur, err := generepo.Current(ctx, t)
		if err != nil {
			return nil, err
		}
		settings = &cur
	}
	produce, err := crops.ProduceMap(ctx, t)
	if err != nil {
		return nil, err
	}
	list, err := inventory.ListPrizes(ctx, t, farm.ID, false)
	if err != nil {
		return nil, err
	}
	rows := []PrizeView{}
	for _, p := range list {
		c := produce[p.ItemCode].Crop
		rows = append(rows, PrizeView{
			ID: p.ID, ItemCode: p.ItemCode, CropCode: c.Code, Quality: p.Quality, Mutation: p.Mutation,
			Multiplier: p.Multiplier, Quantity: p.Quantity,
			Name:      genetics.ProduceName(c.Name, c.VariantName, p.Mutation) + GeneticsLabel(p.Quality, p.Mutation),
			UnitPrice: genetics.UnitPrice(c.SellPrice, p.Quality, p.Mutation, *settings) * p.Multiplier,
		})
	}
	return rows, nil
}
