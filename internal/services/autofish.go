package services

// 自動釣魚機與魚簍升級（Go 版新增，2026-11-15；規則與數值在 game/autofish）。
//
//   - 不用背景排程：玩家打開釣魚中心、魚簍、販售或拋竿時（settleAutoFish），依上次結算的時間補算這段時間釣到的魚。
//     結算時間記在 farm_tools.settings 的 last_at（自動釣魚機那一列）。
//   - 一般情況：每次間隔釣 1 條放進魚簍；魚簍滿了就暫停，結算時間直接推到現在（滿的期間不會累積）。
//   - 自動販售機也啟用（且設定允許）時：釣到的魚直接賣掉，最多補算 AutoSellMaxHours 小時。
//   - 釣到的魚：不需要特定魚餌、當下（每條魚各自的釣到時間）季節與時段有的魚；稀有度用釣魚設定的基礎機率。
//     farm_fish.session_id 為 NULL、auto = true（不算進釣魚排行榜，圖鑑照算）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/autofish"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/numfmt"
	fishrepo "farmgm/internal/repositories/fishing"
	"farmgm/internal/repositories/items"
)

// AutoFishStatus 自動釣魚機的狀態（釣魚中心與魚簍顯示用）。
type AutoFishStatus struct {
	Owned, Enabled bool
	AutoSell       bool       // 自動販售機也啟用：釣到直接賣掉
	Paused         bool       // 魚簍滿了（沒有自動販售）
	NextAt         *time.Time // 下一條的時間（沒有運作時為 nil）
	Interval       time.Duration
	Caught, Sold   int // 這次結算釣到的魚、其中直接賣掉的
	SoldGold       int
}

// Note 這次結算的提示（沒有新的魚時為 ""）。
func (a AutoFishStatus) Note() string {
	switch {
	case a.Sold > 0:
		return fmt.Sprintf("🤖 自動釣魚機釣到 %d 條魚，自動販售機賣出，獲得 %s 金幣。", a.Sold, numfmt.Comma(a.SoldGold))
	case a.Caught > 0:
		return fmt.Sprintf("🤖 自動釣魚機釣到 %d 條魚，已放進魚簍。", a.Caught)
	}
	return ""
}

const autoFishLastKey = "last_at"

func autoFishLast(tool *models.FarmTool) time.Time {
	if s, ok := tool.Settings[autoFishLastKey].(string); ok {
		if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return at
		}
	}
	return tool.AcquiredAt
}

func setAutoFishLast(ctx context.Context, t *db.Tx, tool *models.FarmTool, at time.Time) error {
	settings := map[string]any{}
	for k, v := range tool.Settings {
		settings[k] = v
	}
	settings[autoFishLastKey] = at.UTC().Format(time.RFC3339Nano)
	tool.Settings = settings
	_, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("settings", settings))
	return err
}

// fishBasketCapacity 這座農場的魚簍容量（升級等級 + 釣魚設定的基本容量）。
func (s *GameService) fishBasketCapacity(ctx context.Context, t *db.Tx, farm *models.Farm) (int, error) {
	_, raw, err := fishrepo.CurrentSettings(ctx, t)
	if err != nil {
		return 0, err
	}
	settings, err := decodeSettings(raw)
	if err != nil {
		return 0, err
	}
	return autofish.BasketCapacity(farm.FishBasketLevel, settings.BasketCapacity), nil
}

// autoFishFarm 讀農場；有自動釣魚機時鎖定農場並結算。沒有自動釣魚機時不鎖定（讀取畫面不用排隊）。
func (s *GameService) autoFishFarm(ctx context.Context, t *db.Tx, playerID int64) (*models.Farm, AutoFishStatus, error) {
	farm, err := s.farmOf(ctx, t, playerID)
	if err != nil {
		return nil, AutoFishStatus{}, err
	}
	tool, err := items.GetTool(ctx, t, farm.ID, balance.ToolAutoFisher)
	if err != nil || tool == nil {
		return farm, AutoFishStatus{}, err
	}
	if farm, err = s.lockedFarm(ctx, t, playerID); err != nil {
		return nil, AutoFishStatus{}, err
	}
	st, err := s.settleAutoFish(ctx, t, farm)
	return farm, st, err
}

// settleAutoFish 結算自動釣魚機（農場必須已鎖定）。沒有自動釣魚機時回傳零值。
func (s *GameService) settleAutoFish(ctx context.Context, t *db.Tx, farm *models.Farm) (AutoFishStatus, error) {
	var st AutoFishStatus
	tool, err := items.GetTool(ctx, t, farm.ID, balance.ToolAutoFisher)
	if err != nil || tool == nil {
		return st, err
	}
	now := s.now()
	cfg := autofish.Current()
	interval := autofish.Interval()
	st.Owned, st.Enabled, st.Interval = true, tool.Enabled, interval
	last := autoFishLast(tool)
	if !tool.Enabled { // 停用期間不累積：重新啟用時從啟用的時間開始算
		if now.Sub(last) >= interval {
			return st, setAutoFishLast(ctx, t, tool, now)
		}
		return st, nil
	}
	if last.After(now) {
		last = now
	}
	if st.AutoSell, err = s.toolEnabled(ctx, t, farm.ID, balance.ToolAutoSeller); err != nil {
		return st, err
	}
	st.AutoSell = st.AutoSell && cfg.AutoSellFish
	due := int(now.Sub(last) / interval)
	capacity, err := s.fishBasketCapacity(ctx, t, farm)
	if err != nil {
		return st, err
	}
	inBasket, err := fishrepo.BasketCount(ctx, t, farm.ID)
	if err != nil {
		return st, err
	}
	catches, nextLast := due, last.Add(time.Duration(due)*interval)
	if st.AutoSell {
		if limit := cfg.AutoSellMaxHours * 60 / cfg.IntervalMinutes; catches > limit {
			catches = max(1, limit)
		}
	} else if space := max(0, capacity-inBasket); due > space {
		catches, nextLast = space, now // 魚簍滿了：只釣到放得下的，之後暫停（滿的期間不累積）
	}
	if catches > 0 {
		if err := s.autoCatch(ctx, t, farm, last, interval, catches, st.AutoSell, now, &st); err != nil {
			return st, err
		}
	}
	if due > 0 {
		if err := setAutoFishLast(ctx, t, tool, nextLast); err != nil {
			return st, err
		}
	}
	st.Paused = !st.AutoSell && inBasket+st.Caught >= capacity
	if !st.Paused {
		next := nextLast.Add(interval)
		st.NextAt = &next
	}
	return st, nil
}

// autoCatch 新增 n 條自動釣到的魚（第 i 條的釣到時間 = last + i × interval）；sell 時直接賣掉並入帳。
func (s *GameService) autoCatch(ctx context.Context, t *db.Tx, farm *models.Farm, last time.Time, interval time.Duration, n int,
	sell bool, now time.Time, st *AutoFishStatus) error {
	_, raw, err := fishrepo.CurrentSettings(ctx, t)
	if err != nil {
		return err
	}
	settings, err := decodeSettings(raw)
	if err != nil {
		return err
	}
	types, err := fishrepo.ListFishTypes(ctx, t, true)
	if err != nil {
		return err
	}
	rng := fishing.RNG(s.AutoFishRng)
	total, names := 0, map[string]bool{}
	var nameOrder []string
	for i := 1; i <= n; i++ {
		at := last.Add(time.Duration(i) * interval)
		if at.After(now) {
			at = now
		}
		pools := map[string][]models.FishType{}
		for _, ft := range types {
			if fishing.BaitAllows(ft.RequiredBaits, "") && seasons.Available(ft.Seasons, ft.HourStart, ft.HourEnd, at) {
				pools[ft.Rarity] = append(pools[ft.Rarity], ft)
			}
		}
		rarity := fishing.RollRarity(settings.RarityBP, fishing.Effects{}, rng)
		ftype, ok := pickFish(pools, rarity, rng)
		if !ok {
			continue // 這個時段沒有魚可以釣
		}
		weight := fishing.RollWeight(ftype.MinWeightG, ftype.MaxWeightG, rng)
		price := fishing.FishPrice(ftype.MinPrice, ftype.MaxPrice, ftype.MinWeightG, ftype.MaxWeightG, weight)
		cols := []db.Col{db.C("farm_id", farm.ID), db.C("fish_code", ftype.Code), db.C("rarity", ftype.Rarity),
			db.C("weight_g", weight), db.C("price", price), db.C("caught_at", at), db.C("auto", true)}
		if sell {
			cols = append(cols, db.C("sold_at", now), db.C("sold_price", price))
			total += price
			st.Sold++
			if !names[ftype.Name] {
				names[ftype.Name] = true
				nameOrder = append(nameOrder, ftype.Name)
			}
		}
		if err := db.Insert(ctx, t, "farm_fish", cols...); err != nil {
			return err
		}
		st.Caught++
	}
	if sell && st.Sold > 0 {
		farm.Gold += total
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return err
		}
		st.SoldGold = total
		if err := s.record(ctx, t, farm, rules.EvFishSell,
			truncRunes(fmt.Sprintf("自動販售機賣出自動釣魚機釣到的 %d 隻魚（%s），獲得 %d 金幣", st.Sold, strings.Join(nameOrder, "、"), total), 200),
			rec{Quantity: st.Sold, GoldDelta: total}); err != nil {
			return err
		}
	}
	return nil
}

// AutoFishing 自動釣魚機的狀態（會先結算）。
func (s *GameService) AutoFishing(ctx context.Context, playerID int64) (AutoFishStatus, error) {
	return inTx(ctx, s, func(t *db.Tx) (AutoFishStatus, error) {
		_, st, err := s.autoFishFarm(ctx, t, playerID)
		return st, err
	})
}

// UpgradeFishBasket 魚簍升級：每一級的容量與價格在管理網站調整（autofish.NextBasket）。
func (s *GameService) UpgradeFishBasket(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		before, err := s.fishBasketCapacity(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		_, price, ok := autofish.NextBasket(farm.FishBasketLevel)
		if !ok {
			return ActionResult{}, gameerr.Validationf("魚簍已經是最大的 %d 格了。", before)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：魚簍升級需要 %d，目前只有 %d。", price, farm.Gold)
		}
		farm.Gold -= price
		farm.FishBasketLevel++
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("fish_basket_level", farm.FishBasketLevel)); err != nil {
			return ActionResult{}, err
		}
		after, err := s.fishBasketCapacity(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem,
			fmt.Sprintf("魚簍升級：%d → %d 格，金幣 -%d", before, after, price),
			rec{ItemCode: "fish_basket", Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		tail := "已經是最大的魚簍了。"
		if c, p, ok := autofish.NextBasket(farm.FishBasketLevel); ok {
			tail = fmt.Sprintf("下一級 %d 格，需要 %s 金幣。", c, numfmt.Comma(p))
		}
		return result(fmt.Sprintf("🧺 魚簍升級：%d → **%d** 格，花費 %s 金幣。%s", before, after, numfmt.Comma(price), tail),
			farm.Gold, -price, "", 0), nil
	})
}
