package services

import (
	"context"
	"maps"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/game/weather"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/items"
)

// 天氣奇遇（Go 版新增，2026-11-20）：⚡ 雷擊種子。暴風的隔天，每座農場可以撿 1 次，撿起時直接種進第一格空地：
// 作物是隨機一種當季、商店有賣的作物，突變固定為 weather.LightningMutations 其中一種。
// 不放進背包、也不是特殊種子（不會在收成時取回），所以不需要新的物品代碼與 Migration；交易紀錄沿用 plant。

const lightningDayKey = "lightning_day"

// luckTool 天氣奇遇的紀錄列（還沒撿過任何東西時為 nil）。
func luckTool(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) (*models.FarmTool, error) {
	return settingsTool(ctx, t, farmID, weather.LuckToolCode, forUpdate)
}

func lightningPicked(tool *models.FarmTool, day string) bool {
	if tool == nil {
		return false
	}
	v, _ := tool.Settings[lightningDayKey].(string)
	return v == day
}

// lightningReady 今天是雷擊日、而且這座農場還沒撿過。不是雷擊日時不查資料庫（總覽與農田頁的查詢次數不變）。
func (s *GameService) lightningReady(ctx context.Context, t *db.Tx, farmID int64) (bool, error) {
	now := s.now()
	if !weather.HasLuck(now, weather.LuckLightning) {
		return false, nil
	}
	tool, err := luckTool(ctx, t, farmID, false)
	if err != nil {
		return false, err
	}
	return !lightningPicked(tool, weather.DayKey(now)), nil
}

// PickLightningSeed 撿起雷擊種子並種在第一格空地。
func (s *GameService) PickLightningSeed(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		now := s.now()
		if !weather.HasLuck(now, weather.LuckLightning) {
			return ActionResult{}, gameerr.Validationf("今天沒有雷擊種子：暴風的隔天，農田才會出現雷擊點。")
		}
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, err := luckTool(ctx, t, farm.ID, true)
		if err != nil {
			return ActionResult{}, err
		}
		day := weather.DayKey(now)
		if lightningPicked(tool, day) {
			return ActionResult{}, gameerr.Validationf("今天已經撿過雷擊種子了，下次暴風過後再來。")
		}
		plots, err := farms.ListPlots(ctx, t, farm.ID, false)
		if err != nil {
			return ActionResult{}, err
		}
		hive, err := s.hivePlot(ctx, t, farm) // 蜂箱那一格不能種（2026-11-20）
		if err != nil {
			return ActionResult{}, err
		}
		number := 0
		for _, p := range plots {
			if p.CropTypeID == nil && p.PlotNumber != hive {
				number = p.PlotNumber
				break
			}
		}
		if number == 0 {
			return ActionResult{}, gameerr.New(gameerr.PlotOccupied, "農田沒有空地，空出一格後再撿雷擊種子（今天之內都可以撿）。")
		}
		plot, err := farms.GetPlot(ctx, t, farm.ID, number, true)
		if err != nil {
			return ActionResult{}, err
		}
		if plot == nil || plot.CropTypeID != nil {
			return ActionResult{}, gameerr.New(gameerr.PlotOccupied, "第 %d 格土地已經種植作物。", number)
		}
		all, err := crops.ListAll(ctx, t, true)
		if err != nil {
			return ActionResult{}, err
		}
		var pool []models.CropType
		for _, c := range all {
			if c.ShopBuyable && seasons.Available(c.Seasons, c.HourStart, c.HourEnd, now) {
				pool = append(pool, c)
			}
		}
		if len(pool) == 0 {
			return ActionResult{}, gameerr.Validationf("現在沒有當季作物可以種，雷擊種子撿不起來。")
		}
		crop := pool[pick(len(pool), s.geneticsRng())]
		mutation := weather.LightningMutations[pick(len(weather.LightningMutations), s.geneticsRng())]
		label, seconds, err := s.plantForced(ctx, t, farm, plot, &crop, "⚡ 雷擊種子：", nil, mutation)
		if err != nil {
			return ActionResult{}, err
		}
		if tool == nil {
			if tool, err = items.AddTool(ctx, t, farm.ID, weather.LuckToolCode, now); err != nil {
				return ActionResult{}, err
			}
		}
		settings := maps.Clone(tool.Settings)
		if settings == nil {
			settings = map[string]any{}
		}
		settings[lightningDayKey] = day
		if _, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("settings", settings)); err != nil {
			return ActionResult{}, err
		}
		tail := txt(ctx, "請記得澆水，澆水後開始生長。")
		if seconds != nil {
			tail = txf(ctx, "已自動澆水，%s後成熟。", rules.FormatDuration(*seconds))
		}
		return result(txf(ctx, "⚡ 撿起雷擊種子，在第 %d 格種下%s%s（保證%s），%s", number, crop.Name, label,
			txt(ctx, balance.Mutations[mutation]), tail), farm.Gold, 0, rules.SeedItemCode(crop.Code), 1), nil
	})
}

// pick u ∈ [0, 1) 對應到 0..n-1。
func pick(n int, u float64) int { return min(n-1, max(0, int(u*float64(n)))) }
