package services

import (
	"context"
	"fmt"
	"slices"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/beehive"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/inventory"
)

// 蜂箱（Go 版新增，2026-11-20，規則見 game/beehive）。狀態存在 farm_tools 的 settings；
// 放置、升級、搬移的費用記成 buy_item，收蜂蜜記成 craft_collect（會登記圖鑑），都不需要 Migration。

type hiveKey struct{ farmID int64 }

// hiveOf 蜂箱狀態。農場等級還沒到開放等級時不查資料庫；批次操作（Bulk）中同一座農場只查一次。
func (s *GameService) hiveOf(ctx context.Context, t *db.Tx, farm *models.Farm) (beehive.State, error) {
	if farm.Level < beehive.UnlockLevel {
		return beehive.State{}, nil
	}
	return db.Memo(t, hiveKey{farm.ID}, func() (beehive.State, error) {
		tool, err := settingsTool(ctx, t, farm.ID, beehive.ToolCode, false)
		return beehive.Parse(toolSettings(tool)), err
	})
}

// hivePlot 蜂箱所在的格子（沒有蜂箱時為 0）。挑空地種植的地方都要跳過這一格。
func (s *GameService) hivePlot(ctx context.Context, t *db.Tx, farm *models.Farm) (int, error) {
	h, err := s.hiveOf(ctx, t, farm)
	if err != nil || !h.Placed() {
		return 0, err
	}
	return h.Plot, nil
}

// lockedHive 鎖定並讀出蜂箱（farm 必須已鎖定）。
func lockedHive(ctx context.Context, t *db.Tx, farm *models.Farm) (*models.FarmTool, beehive.State, error) {
	tool, err := settingsTool(ctx, t, farm.ID, beehive.ToolCode, true)
	if err != nil {
		return nil, beehive.State{}, err
	}
	return tool, beehive.Parse(toolSettings(tool)), nil
}

func hiveLocked(farm *models.Farm) error {
	return gameerr.Validationf("蜂箱在農場 Lv.%d 開放（目前 Lv.%d）。", beehive.UnlockLevel, farm.Level)
}

// BeehiveView 蜂箱頁。
type BeehiveView struct {
	Now        time.Time
	FarmLevel  int
	Unlocked   bool
	Hive       beehive.State // Honey 已結算到現在（只顯示，不寫回）
	Capacity   int
	NextAt     *time.Time
	Neighbors  []int
	EmptyPlots []int // 可以放置／搬移的空地
	Gold       int
}

func (s *GameService) Beehive(ctx context.Context, playerID int64) (BeehiveView, error) {
	return inTx(ctx, s, func(t *db.Tx) (BeehiveView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return BeehiveView{}, err
		}
		now := s.now()
		v := BeehiveView{Now: now, FarmLevel: farm.Level, Unlocked: farm.Level >= beehive.UnlockLevel, Gold: farm.Gold}
		if !v.Unlocked {
			return v, nil
		}
		tool, err := settingsTool(ctx, t, farm.ID, beehive.ToolCode, false)
		if err != nil {
			return v, err
		}
		plots, err := farms.ListPlots(ctx, t, farm.ID, false)
		if err != nil {
			return v, err
		}
		h := beehive.Parse(toolSettings(tool))
		for _, p := range plots {
			if p.CropTypeID == nil && p.PlotNumber != h.Plot {
				v.EmptyPlots = append(v.EmptyPlots, p.PlotNumber)
			}
		}
		if h.Placed() {
			h.Accrue(now)
			v.Capacity = beehive.Capacity
			v.NextAt = h.NextAt()
			for _, n := range beehive.Neighbors(h.Plot) {
				if slices.ContainsFunc(plots, func(p farms.PlotWithCrop) bool { return p.PlotNumber == n }) {
					v.Neighbors = append(v.Neighbors, n)
				}
			}
		}
		v.Hive = h
		return v, nil
	})
}

// PlaceBeehive 還沒有蜂箱時放置（Prices[1]）；已經有時搬到別格（MoveFee，蜂蜜存量保留）。plot 必須是空地。
func (s *GameService) PlaceBeehive(ctx context.Context, playerID int64, plotNumber int) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.Level < beehive.UnlockLevel {
			return ActionResult{}, hiveLocked(farm)
		}
		tool, h, err := lockedHive(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		if h.Placed() && h.Plot == plotNumber {
			return ActionResult{}, gameerr.Validationf("蜂箱已經在第 %d 格了。", plotNumber)
		}
		plot, err := farms.GetPlot(ctx, t, farm.ID, plotNumber, true)
		if err != nil {
			return ActionResult{}, err
		}
		if plot == nil {
			return ActionResult{}, gameerr.NotFoundf("沒有第 %d 格土地。", plotNumber)
		}
		if plot.CropTypeID != nil {
			return ActionResult{}, gameerr.New(gameerr.PlotOccupied, "第 %d 格土地已經種植作物，蜂箱只能放在空地。", plotNumber)
		}
		now := s.now()
		fee, desc := beehive.Prices[1], fmt.Sprintf("在第 %d 格放置蜂箱", plotNumber)
		if h.Placed() {
			h.Accrue(now) // 搬家前先結算原本位置的蜂蜜
			fee, desc = beehive.MoveFee, fmt.Sprintf("蜂箱從第 %d 格搬到第 %d 格", h.Plot, plotNumber)
		} else {
			h = beehive.State{Level: 1, Last: now}
		}
		if farm.Gold < fee {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：需要 %d，目前只有 %d。", fee, farm.Gold)
		}
		old := h.Plot
		h.Plot = plotNumber
		farm.Gold -= fee
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("%s，金幣 -%d", desc, fee), rec{GoldDelta: -fee}); err != nil {
			return ActionResult{}, err
		}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, beehive.ToolCode, h.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "🐝 在第 %d 格放好蜂箱（花費 %d 金幣）！周圍 8 格種下的作物比較容易突變，每 %d 小時產一次蜂蜜。",
			plotNumber, fee, beehive.IntervalSeconds/3600)
		if old > 0 {
			msg = txf(ctx, "🐝 蜂箱從第 %d 格搬到第 %d 格（花費 %d 金幣），蜂蜜存量保留。", old, plotNumber, fee)
		}
		return result(msg, farm.Gold, -fee, "", 0), nil
	})
}

// UpgradeBeehive 升一級（Prices[下一級]）。
func (s *GameService) UpgradeBeehive(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, h, err := lockedHive(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		if !h.Placed() {
			return ActionResult{}, gameerr.Validationf("還沒有蜂箱，先選一格空地放蜂箱。")
		}
		if h.Level >= beehive.MaxLevel {
			return ActionResult{}, gameerr.Validationf("蜂箱已經是最高等級（Lv.%d）。", beehive.MaxLevel)
		}
		price := beehive.Prices[h.Level+1]
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：蜂箱升級需要 %d，目前只有 %d。", price, farm.Gold)
		}
		now := s.now()
		h.Accrue(now) // 升級前先結算
		h.Level++
		farm.Gold -= price
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("蜂箱升級到 Lv.%d，金幣 -%d", h.Level, price),
			rec{GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, beehive.ToolCode, h.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "🐝 蜂箱升級到 Lv.%d（花費 %d 金幣）：突變 +%s%%、叮小偷 %s%%。",
			h.Level, price, bpText(beehive.MutationBP[h.Level]), bpText(beehive.StingBP[h.Level])), farm.Gold, -price, "", 0), nil
	})
}

// CollectHoney 把存的蜂蜜收進倉庫（放不下時只收放得下的部分）。
func (s *GameService) CollectHoney(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, h, err := lockedHive(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		if !h.Placed() {
			return ActionResult{}, gameerr.Validationf("還沒有蜂箱，先選一格空地放蜂箱。")
		}
		now := s.now()
		h.Accrue(now)
		if h.Honey == 0 {
			return ActionResult{}, gameerr.Validationf("蜂箱裡還沒有蜂蜜，%s後產下一批。", nextText(h.NextAt(), now))
		}
		used, err := inventory.TotalQuantity(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		take := min(h.Honey, farm.WarehouseCapacity-used)
		if take <= 0 {
			return ActionResult{}, gameerr.New(gameerr.WarehouseFull, "倉庫已滿（%d/%d），清出空間後再收蜂蜜。", used, farm.WarehouseCapacity)
		}
		if err := inventory.Add(ctx, t, farm.ID, beehive.Honey, take, "normal", "none"); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvCraftCollect, fmt.Sprintf("蜂箱收成 %d 罐蜂蜜", take),
			rec{ItemCode: beehive.Honey, Quantity: take}); err != nil {
			return ActionResult{}, err
		}
		h.Honey -= take
		if err := saveSettingsTool(ctx, t, farm.ID, tool, beehive.ToolCode, h.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "🍯 收了 %d 罐蜂蜜！可以直接賣，或在烘焙坊、乳品工坊做成蜂蜜料理。", take)
		if h.Honey > 0 {
			msg += txf(ctx, "（倉庫空間不足，還有 %d 罐留在蜂箱）", h.Honey)
		}
		return result(msg, farm.Gold, 0, beehive.Honey, take), nil
	})
}

// RemoveBeehive 拆除蜂箱（2026-11-20 使用者要求）：那一格變回空地，等級歸零（之後要重新花錢放置、升級），不退費。
// 蜂箱裡的蜂蜜先收進倉庫；倉庫放不下全部時不拆，請玩家先清出空間，避免蜂蜜不見。
func (s *GameService) RemoveBeehive(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, h, err := lockedHive(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		if !h.Placed() {
			return ActionResult{}, gameerr.Validationf("還沒有蜂箱，不用拆除。")
		}
		now := s.now()
		h.Accrue(now)
		if h.Honey > 0 {
			used, err := inventory.TotalQuantity(ctx, t, farm.ID)
			if err != nil {
				return ActionResult{}, err
			}
			if free := farm.WarehouseCapacity - used; free < h.Honey {
				return ActionResult{}, gameerr.New(gameerr.WarehouseFull,
					"蜂箱裡還有 %d 罐蜂蜜，倉庫只剩 %d 格（%d/%d），清出空間後再拆除。", h.Honey, max(0, free), used, farm.WarehouseCapacity)
			}
			if err := inventory.Add(ctx, t, farm.ID, beehive.Honey, h.Honey, "normal", "none"); err != nil {
				return ActionResult{}, err
			}
			if err := s.record(ctx, t, farm, rules.EvCraftCollect, fmt.Sprintf("拆除蜂箱前收成 %d 罐蜂蜜", h.Honey),
				rec{ItemCode: beehive.Honey, Quantity: h.Honey}); err != nil {
				return ActionResult{}, err
			}
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("拆除第 %d 格的蜂箱（Lv.%d）", h.Plot, h.Level), rec{}); err != nil {
			return ActionResult{}, err
		}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, beehive.ToolCode, map[string]any{}, now); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "🐝 已拆除第 %d 格的蜂箱，那一格變回空地。", h.Plot)
		if h.Honey > 0 {
			msg += txf(ctx, "蜂箱裡的 %d 罐蜂蜜已收進倉庫。", h.Honey)
		}
		return result(msg, farm.Gold, 0, beehive.Honey, h.Honey), nil
	})
}

func nextText(at *time.Time, now time.Time) string {
	if at == nil {
		return rules.FormatDuration(beehive.IntervalSeconds)
	}
	return rules.FormatDuration(rules.RemainingSeconds(at, now))
}

// bpText 萬分比 → 百分比文字（例如 150 → 1.5）。
func bpText(bp int) string { return fmt.Sprintf("%g", float64(bp)/100) }
