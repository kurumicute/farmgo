package services

// 商店與倉庫、種植與收成、一鍵操作、販售與擴建（對應 Python GameService 的同名段落）。

import (
	"context"
	"farmgm/i18n"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/beehive"
	"farmgm/internal/game/expansion"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/game/seedlab"
	"farmgm/internal/game/specialshop"
	"farmgm/internal/game/weather"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/defenses"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/feedback"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/steals"

	eventrules "farmgm/internal/game/events"
	petrules "farmgm/internal/game/pets"
)

func trimSpace(s string) string { return strings.TrimSpace(s) }

// ---------- 商店與倉庫 ----------

func (s *GameService) GetMarketPrices(ctx context.Context) ([]CropInfo, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]CropInfo, error) {
		now := s.now()
		all, err := crops.ListAll(ctx, t, true)
		if err != nil {
			return nil, err
		}
		out := make([]CropInfo, len(all))
		for i := range all {
			out[i] = cropInfo(&all[i], &now)
		}
		return out, nil
	})
}

func (s *GameService) GetInventory(ctx context.Context, playerID int64) (Inventory, error) {
	return inTx(ctx, s, func(t *db.Tx) (Inventory, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return Inventory{}, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return Inventory{}, err
		}
		stacks, err := inventory.ListItems(ctx, t, farm.ID)
		if err != nil {
			return Inventory{}, err
		}
		inv := Inventory{Items: []InventoryEntry{}, Capacity: farm.WarehouseCapacity}
		for _, i := range stacks {
			name, ok := names[i.ItemCode]
			if !ok {
				name = i.ItemCode
			}
			inv.Items = append(inv.Items, InventoryEntry{i.ItemCode, name + GeneticsLabel(i.Quality, i.Mutation), i.Quantity})
			inv.Used += i.Quantity
		}
		return inv, nil
	})
}

func (s *GameService) BuySeeds(ctx context.Context, playerID int64, cropKey string, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		crop, err := s.crop(ctx, t, cropKey, true)
		if err != nil {
			return ActionResult{}, err
		}
		if !crop.ShopBuyable {
			return ActionResult{}, gameerr.Validationf("商店不販售%s種子，只能從山洞探索或稀有作物培育器取得。", crop.Name)
		}
		if !seasons.Available(crop.Seasons, nil, nil, s.now()) { // 商店只賣當季的種子（Go 版新增）
			return ActionResult{}, gameerr.Validationf("現在不是%s的季節，商店沒有販售%s種子（%s）。", crop.Name, crop.Name,
				seasons.Describe(crop.Seasons, nil, nil))
		}
		cost := crop.SeedPrice * quantity
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：需要 %d，目前只有 %d。", cost, farm.Gold)
		}
		if err := s.ensureMaterialSpace(ctx, t, farm, quantity); err != nil { // 種子放素材背包（2026-11-12 起）
			return ActionResult{}, err
		}
		farm.Gold -= cost
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		item := rules.SeedItemCode(crop.Code)
		if err := inventory.Add(ctx, t, farm.ID, item, quantity, "normal", "none"); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuySeed, fmt.Sprintf("購買 %d 顆%s種子，金幣 -%d", quantity, crop.Name, cost),
			rec{ItemCode: item, Quantity: quantity, GoldDelta: -cost}); err != nil {
			return ActionResult{}, err
		}
		auto, err := s.autoPlant(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "已購買 %d 顆%s種子，花費 %d 金幣。%s", quantity, crop.Name, cost, auto),
			farm.Gold, -cost, item, quantity), nil
	})
}

// ---------- 種植與收成 ----------

func (s *GameService) PlantCrop(ctx context.Context, playerID int64, plotNumber int, cropKey string) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		crop, err := s.crop(ctx, t, cropKey, true)
		if err != nil {
			return ActionResult{}, err
		}
		if err := s.checkSeason(crop); err != nil {
			return ActionResult{}, err
		}
		plot, err := farms.GetPlot(ctx, t, farm.ID, plotNumber, true)
		if err != nil {
			return ActionResult{}, err
		}
		if plot == nil {
			return ActionResult{}, gameerr.NotFoundf("沒有第 %d 格土地。", plotNumber)
		}
		if plot.CropTypeID != nil {
			return ActionResult{}, gameerr.New(gameerr.PlotOccupied, "第 %d 格土地已經種植作物。", plotNumber)
		}
		item := rules.SeedItemCode(crop.Code)
		ok, err := inventory.Remove(ctx, t, farm.ID, item, 1, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "沒有%s種子，請先購買（buy %s 1）。", crop.Name, crop.Code)
		}
		label, seconds, err := s.plant(ctx, t, farm, plot, crop, "")
		if err != nil {
			return ActionResult{}, err
		}
		tail := txf(ctx, "請記得澆水（water %d），澆水後開始生長。", plotNumber)
		if seconds != nil {
			tail = txf(ctx, "已自動澆水，%s後成熟。", rules.FormatDuration(*seconds))
		}
		return result(txf(ctx, "已在第 %d 格種下%s%s，%s", plotNumber, crop.Name, label, tail), farm.Gold, 0, item, 1), nil
	})
}

// checkSeason 季節／時段限定的作物只能在那段期間種下（種下後過了季節照樣會長大、收成）。
func (s *GameService) checkSeason(crop *models.CropType) error {
	now := s.now()
	if !seasons.Available(crop.Seasons, crop.HourStart, crop.HourEnd, now) {
		limit := seasons.Describe(crop.Seasons, crop.HourStart, crop.HourEnd)
		return gameerr.Validationf("%s是%s的作物，現在不能種植（%s）。", crop.Name, limit, seasons.NowText(now))
	}
	return nil
}

// plant 在已鎖定、空著的土地種下一顆（種子由呼叫端扣除）。回傳 (品質標籤, 自動澆水後的生長秒數或 nil)。
func (s *GameService) plant(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot, crop *models.CropType,
	source string) (string, *int, error) {
	return s.plantWith(ctx, t, farm, plot, crop, source, nil)
}

// plantWith 同 plant；labSeed 不是 nil 時種的是種子實驗室的特殊種子（已由呼叫端標記為已種下）：
// 品質基因、突變基因加到加成，種子本身的突變直接成為作物的突變，產量基因增加收成數量（生長基因在澆水時套用）。
func (s *GameService) plantWith(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot, crop *models.CropType,
	source string, labSeed *models.LabSeed) (string, *int, error) {
	return s.plantForced(ctx, t, farm, plot, crop, source, labSeed, "")
}

// plantForced 同 plantWith；forced 不是 "" 時作物的突變固定為 forced（天氣奇遇的雷擊種子，2026-11-20）。
func (s *GameService) plantForced(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot, crop *models.CropType,
	source string, labSeed *models.LabSeed, forced string) (string, *int, error) {
	now := s.now()
	// 播種時一次決定整批的品質與主要突變，之後固定不變（收成、偷菜、重啟都不重新抽取）
	settings, err := s.geneticsSettings(ctx, t)
	if err != nil {
		return "", nil, err
	}
	// 限時事件的品質、突變與收成加成在播種當下決定，之後事件結束也不會改變
	active, err := s.activeEvents(ctx, t, farm)
	if err != nil {
		return "", nil, err
	}
	q := eventrules.BonusOf(active, eventrules.PlantEffects[0], &crop.ID)
	m := eventrules.BonusOf(active, eventrules.PlantEffects[1], &crop.ID)
	h := eventrules.BonusOf(active, eventrules.PlantEffects[2], &crop.ID)
	qualityBonus, mutationBonus := q.Value, m.Value
	if labSeed != nil {
		qualityBonus += seedlab.QualityBonusBP(labSeed.GeneQuality)
		mutationBonus += seedlab.MutationBonusBP(labSeed.GeneMutation)
	}
	// 幸運香包（2026-11-18）：效果期間種下的作物突變機率提高
	sachet, err := s.boostActive(ctx, t, farm.ID, balance.ItemLuckySachet, now)
	if err != nil {
		return "", nil, err
	}
	if sachet {
		mutationBonus += balance.SachetMutationBP
	}
	// 蜂箱（2026-11-20）：那一格不能種；周圍 8 格授粉，突變機率提高
	hive, err := s.hiveOf(ctx, t, farm)
	if err != nil {
		return "", nil, err
	}
	if hive.Placed() {
		if plot.PlotNumber == hive.Plot {
			return "", nil, gameerr.New(gameerr.PlotOccupied, "第 %d 格放了蜂箱，不能種植。", plot.PlotNumber)
		}
		if beehive.IsNeighbor(hive.Plot, plot.PlotNumber) {
			mutationBonus += beehive.MutationBP[hive.Level]
		}
	}
	quality := genetics.RollQuality(genetics.EffectiveQualityBP(settings, farm.Level, qualityBonus), s.geneticsRng())
	mutation := genetics.RollMutation(settings, s.geneticsRng(), mutationBonus)
	if labSeed != nil && labSeed.Mutation != "none" {
		mutation = labSeed.Mutation
	}
	if forced != "" {
		mutation = forced
	}
	quantity := genetics.HarvestQuantity(crop.HarvestQuantity, mutation, settings, h.Value)
	if labSeed != nil {
		quantity += seedlab.YieldBonus(labSeed.GeneYield, crop.HarvestQuantity)
	}
	if err := farms.Plant(ctx, t, plot, crop.ID, quantity, now, quality, mutation); err != nil {
		return "", nil, err
	}
	if labSeed != nil {
		if err := farms.SetLabSeed(ctx, t, plot, labSeed.ID); err != nil {
			return "", nil, err
		}
		source += "特殊種子："
	}
	label := GeneticsLabel(quality, mutation)
	applied := eventrules.MergeApplied(q, m, h)
	if err := s.record(ctx, t, farm, rules.EvPlant,
		fmt.Sprintf("%s在第 %d 格土地種植%s%s%s", source, plot.PlotNumber, crop.Name, label, eventsNote(applied)),
		rec{ItemCode: rules.SeedItemCode(crop.Code), Quantity: 1, Quality: quality, Mutation: mutation, Applied: applied}); err != nil {
		return "", nil, err
	}
	// 免澆水活動期間，或持有並啟用自動澆水器時，種下後立即澆水
	free := eventrules.BonusOf(active, "free_water", &crop.ID)
	if free.Value > 0 {
		sec, err := s.water(ctx, t, farm, plot, crop, now, true, &free)
		return label, &sec, err
	}
	on, err := s.toolEnabled(ctx, t, farm.ID, balance.ToolAutoWaterer)
	if err != nil {
		return "", nil, err
	}
	if on {
		sec, err := s.water(ctx, t, farm, plot, crop, now, true, nil)
		return label, &sec, err
	}
	return label, nil, nil
}

// autoPlant 自動播種機：從背包隨機挑選種子把所有空地種滿。未持有或停用時不做任何事，回傳空字串。
func (s *GameService) autoPlant(ctx context.Context, t *db.Tx, farm *models.Farm) (string, error) {
	on, err := s.toolEnabled(ctx, t, farm.ID, balance.ToolAutoPlanter)
	if err != nil || !on {
		return "", err
	}
	// 一次鎖定全部土地再挑空地（同 plotsInState）
	all, err := farms.ListPlots(ctx, t, farm.ID, true)
	if err != nil {
		return "", err
	}
	hive, err := s.hivePlot(ctx, t, farm) // 蜂箱那一格不能種（2026-11-20）
	if err != nil {
		return "", err
	}
	var empty []*models.FarmPlot
	for i := range all {
		if all[i].CropTypeID == nil && all[i].PlotNumber != hive {
			empty = append(empty, &all[i].FarmPlot)
		}
	}
	if len(empty) == 0 {
		return "", nil
	}
	list, err := crops.ListAll(ctx, t, true)
	if err != nil {
		return "", err
	}
	type entry struct {
		crop  *models.CropType
		stock int
	}
	var stock []*entry
	for i := range list {
		c := &list[i]
		if !seasons.Available(c.Seasons, c.HourStart, c.HourEnd, s.now()) { // 不在季節的種子不種
			continue
		}
		n, err := inventory.GetQuantity(ctx, t, farm.ID, rules.SeedItemCode(c.Code), "normal", "none")
		if err != nil {
			return "", err
		}
		stock = append(stock, &entry{c, n})
	}
	var order []string
	planted := map[string]int{}
	err = t.Bulk(ctx, func() error {
		for _, plot := range empty {
			var choices []*entry
			for _, e := range stock {
				if e.stock > 0 {
					choices = append(choices, e)
				}
			}
			if len(choices) == 0 {
				break
			}
			e := choices[s.Pick(len(choices))]
			if _, err := inventory.Remove(ctx, t, farm.ID, rules.SeedItemCode(e.crop.Code), 1, "normal", "none"); err != nil {
				return err
			}
			e.stock--
			if _, _, err := s.plant(ctx, t, farm, plot, e.crop, "自動播種機："); err != nil {
				return err
			}
			name := cropNameCtx(ctx, e.crop)
			if planted[name] == 0 {
				order = append(order, name)
			}
			planted[name]++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(order) == 0 {
		return tr(ctx, "svc.autoplant.no_seed"), nil
	}
	total, parts := 0, make([]string, len(order))
	for i, name := range order {
		parts[i] = fmt.Sprintf("%s×%d", name, planted[name])
		total += planted[name]
	}
	return tr(ctx, "svc.autoplant.done", "n", total, "list", strings.Join(parts, tr(ctx, "svc.list_sep"))), nil
}

// growBoosts 這座農場目前的生長加速（澆水與種植選單的預估時間共用，2026-11-19 抽出）。
type growBoosts struct {
	earthworm  bool
	active     []*eventrules.Event
	settings   balance.GeneticsSettings
	greenhouse int // 溫室的減少 %（與限時活動相加）
	sprinkler  int // 灑水器（2026-11-18）：效果期間不是晴天時，也有晴天的生長加速
}

func (s *GameService) growBoostsOf(ctx context.Context, t *db.Tx, farm *models.Farm, now time.Time) (growBoosts, error) {
	var g growBoosts
	var err error
	if g.earthworm, err = db.Memo(t, effectKey{farm.ID, balance.EffectEarthworm}, func() (bool, error) {
		return items.EffectActive(ctx, t, farm.ID, balance.EffectEarthworm, now)
	}); err != nil {
		return g, err
	}
	if g.active, err = s.activeEvents(ctx, t, farm); err != nil {
		return g, err
	}
	if g.settings, err = s.geneticsSettings(ctx, t); err != nil {
		return g, err
	}
	g.greenhouse = specialshop.GreenhousePct(farm.GreenhouseLevel)
	if w := weather.Today(now); w.Code != weather.Sunny {
		on, err := s.boostActive(ctx, t, farm.ID, balance.ItemSprinkler, now)
		if err != nil {
			return g, err
		}
		if on {
			g.sprinkler = weather.Kinds[weather.Sunny].GrowReductionPct
		}
	}
	return g, nil
}

// seconds 這種作物澆水後的生長秒數（mutation：快速生長突變；labPct：特殊種子的生長基因）。
func (g growBoosts) seconds(crop *models.CropType, mutation string, labPct int) (int, eventrules.Bonus) {
	grow := eventrules.BonusOf(g.active, "grow_reduction_pct", &crop.ID)
	return genetics.GrowSeconds(crop.GrowSeconds, mutation, g.earthworm, g.settings, grow.Value+g.greenhouse+labPct+g.sprinkler), grow
}

// GrowTimes 種植選單用：每種作物種下並澆水後實際需要的生長秒數（限時活動、溫室、蚯蚓、灑水器、天氣等加速都算進去；
// 不含快速生長突變與特殊種子的基因，那些要看種下的那一顆）。
func (s *GameService) GrowTimes(ctx context.Context, playerID int64) (map[string]int, error) {
	return inTx(ctx, s, func(t *db.Tx) (map[string]int, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		g, err := s.growBoostsOf(ctx, t, farm, s.now())
		if err != nil {
			return nil, err
		}
		all, err := crops.ListAll(ctx, t, true)
		if err != nil {
			return nil, err
		}
		out := make(map[string]int, len(all))
		for i := range all {
			out[all[i].Code], _ = g.seconds(&all[i], "none", 0)
		}
		return out, nil
	})
}

// water auto：自動澆水器；reason：免澆水活動（nil 表示不是）。回傳生長秒數。
func (s *GameService) water(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot, crop *models.CropType,
	now time.Time, auto bool, reason *eventrules.Bonus) (int, error) {
	g, err := s.growBoostsOf(ctx, t, farm, now)
	if err != nil {
		return 0, err
	}
	earthworm, greenhouse, sprinkler := g.earthworm, g.greenhouse, g.sprinkler
	mutation := deref(plot.Mutation)
	// 快速生長突變也只在澆水後才開始計時，不會繞過澆水與乾枯規則；特殊種子（種子實驗室）的生長基因也一起相加
	labPct, err := labGrowthPct(ctx, t, plot)
	if err != nil {
		return 0, err
	}
	seconds, grow := g.seconds(crop, mutation, labPct)
	if err := farms.Water(ctx, t, plot, now, rules.ReadyTime(now, seconds)); err != nil {
		return 0, err
	}
	extra := ""
	if earthworm {
		extra += "（蚯蚓加速）"
	}
	if mutation == "fast" {
		extra += "（快速生長）"
	}
	if farm.GreenhouseLevel != 0 {
		extra += fmt.Sprintf("（溫室 -%d%%）", greenhouse)
	}
	if labPct > 0 {
		extra += fmt.Sprintf("（生長基因 -%d%%）", labPct)
	}
	if sprinkler > 0 {
		extra += fmt.Sprintf("（灑水器 -%d%%）", sprinkler)
	}
	bonuses := []eventrules.Bonus{grow}
	source := "澆水"
	if reason != nil {
		bonuses = append(bonuses, *reason)
		source = "免澆水活動"
		if len(reason.Applied) == 1 && reason.Applied[0].ID == weather.EventID {
			source = "下雨自動澆水"
		}
	} else if auto {
		source = "自動澆水器"
	}
	applied := eventrules.MergeApplied(bonuses...)
	if err := s.record(ctx, t, farm, rules.EvWater,
		fmt.Sprintf("%s：第 %d 格%s開始生長%s%s", source, plot.PlotNumber, crop.Name, extra, eventsNote(applied)),
		rec{Applied: applied}); err != nil {
		return 0, err
	}
	return seconds, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *GameService) plotForAction(ctx context.Context, t *db.Tx, farm *models.Farm, plotNumber int) (*models.FarmPlot, time.Time, rules.PlotState, error) {
	plot, err := farms.GetPlot(ctx, t, farm.ID, plotNumber, true)
	if err != nil {
		return nil, time.Time{}, "", err
	}
	if plot == nil {
		return nil, time.Time{}, "", gameerr.NotFoundf("沒有第 %d 格土地。", plotNumber)
	}
	now := s.now()
	return plot, now, rules.PlotStateAt(plot.CropTypeID, plot.WateredAt, plot.ReadyAt, now), nil
}

func (s *GameService) WaterCrop(ctx context.Context, playerID int64, plotNumber int) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plot, now, state, err := s.plotForAction(ctx, t, farm, plotNumber)
		if err != nil {
			return ActionResult{}, err
		}
		if state == rules.PlotEmpty {
			return ActionResult{}, gameerr.New(gameerr.PlotEmpty, "第 %d 格土地沒有作物。", plotNumber)
		}
		if state != rules.PlotWaitingWater {
			return ActionResult{}, gameerr.Validationf("第 %d 格已經澆過水了。", plotNumber)
		}
		crop, err := crops.Get(ctx, t, *plot.CropTypeID)
		if err != nil {
			return ActionResult{}, err
		}
		seconds, err := s.water(ctx, t, farm, plot, crop, now, false, nil)
		if err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "已為第 %d 格的%s澆水，%s後成熟。", plotNumber, crop.Name, rules.FormatDuration(seconds)),
			farm.Gold, 0, "", 0), nil
	})
}

func (s *GameService) HarvestCrop(ctx context.Context, playerID int64, plotNumber int) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plot, now, state, err := s.plotForAction(ctx, t, farm, plotNumber)
		if err != nil {
			return ActionResult{}, err
		}
		if state == rules.PlotEmpty {
			return ActionResult{}, gameerr.New(gameerr.PlotEmpty, "第 %d 格土地沒有作物。", plotNumber)
		}
		crop, err := crops.Get(ctx, t, *plot.CropTypeID)
		if err != nil {
			return ActionResult{}, err
		}
		switch state {
		case rules.PlotWaitingWater:
			return ActionResult{}, gameerr.New(gameerr.PlotNotWatered, "第 %d 格的%s還沒澆水（water %d）。", plotNumber, crop.Name, plotNumber)
		case rules.PlotGrowing:
			left := rules.FormatDuration(rules.RemainingSeconds(plot.ReadyAt, now))
			return ActionResult{}, gameerr.New(gameerr.PlotNotReady, "第 %d 格的%s尚未成熟，還需要 %s。", plotNumber, crop.Name, left)
		case rules.PlotWithered:
			return ActionResult{}, gameerr.New(gameerr.PlotWithered, "第 %d 格的%s已經乾枯，請用 clear %d 清除。", plotNumber, crop.Name, plotNumber)
		}
		var details []HarvestDetail
		message, quantity, item, err := s.harvestLocked(ctx, t, farm, plot, crop, &details)
		if err != nil {
			return ActionResult{}, err
		}
		message = HarvestHighlight(details) + message + HarvestValueNote(details)
		note, err := s.recordNote(ctx, t, farm, "harvest_quantity", quantity)
		if err != nil {
			return ActionResult{}, err
		}
		auto, err := s.autoPlant(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		return result(message+note+auto, farm.Gold, 0, item, quantity), nil
	})
}

// harvestLocked 收成一格已成熟、已鎖定的土地；倉庫不足時在修改任何資料前回傳 WarehouseFull 錯誤。
// details：要產生結果摘要時傳入，收成成功後加入這格的明細。
func (s *GameService) harvestLocked(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot,
	crop *models.CropType, details *[]HarvestDetail) (string, int, string, error) {
	active, err := s.activeEvents(ctx, t, farm)
	if err != nil {
		return "", 0, "", err
	}
	// 大風颳走：收成時每格少 N 個，至少保留 1 個（在收成當下判斷）
	wind := eventrules.BonusOf(active, "harvest_loss", &crop.ID)
	// 暴風（每日天氣）：每種守護這格的防守設施擋下 weather.StormDefenseBlock 個（只減輕天氣造成的部分）
	blocked := 0
	if storm := weatherLoss(active); storm > 0 {
		guards, err := s.plotGuards(ctx, t, farm.ID, plot.PlotNumber)
		if err != nil {
			return "", 0, "", err
		}
		blocked = min(storm, guards*weather.StormDefenseBlock)
		// 雨衣稻草人（2026-11-18）：效果期間暴風完全吹不壞（限時活動的大風照常）
		guard, err := s.boostActive(ctx, t, farm.ID, balance.ItemStormGuard, s.now())
		if err != nil {
			return "", 0, "", err
		}
		if guard {
			blocked = storm
		}
	}
	remaining := *plot.RemainingQuantity
	lost := min(max(0, wind.Value-blocked), max(0, remaining-1))
	quantity := remaining - lost
	// 貓咪趕走偷吃的老鼠：收成數量 +X%（只有主人收成時；被偷走的部分不會補回）
	catPct, err := s.petPct(ctx, t, farm.ID, petrules.Cat) // 寵物零食生效時 ×1.5（2026-11-18）
	if err != nil {
		return "", 0, "", err
	}
	catBonus := petrules.BonusQuantity(quantity, catPct, s.PetRng())
	quantity += catBonus
	autoSell, err := s.autoSells(ctx, t, farm.ID, autoSellKey(crop.Code)) // 販售項目沒勾的作物放進倉庫
	if err != nil {
		return "", 0, "", err
	}
	if !autoSell {
		// 倉庫空間不足時拒絕收成，作物保留在土地上，不會遺失（自動販售會立即賣出，不佔倉庫）
		if err := s.ensureSpace(ctx, t, farm, quantity); err != nil {
			return "", 0, "", err
		}
	}
	quality, mutation := deref(plot.Quality), deref(plot.Mutation)
	item := genetics.ProduceCode(crop.Code, crop.VariantCode, mutation)
	name := genetics.ProduceName(crop.Name, crop.VariantName, mutation) + GeneticsLabel(quality, mutation)
	// 這株被偷走的數量（只用於訊息）：一鍵收成時一次查出整座農場目前每格的數量
	stolenMap, err := db.Memo(t, stolenKey{farm.ID}, func() (map[int64]int, error) {
		return steals.StolenByCurrentPlantings(ctx, t, farm.ID)
	})
	if err != nil {
		return "", 0, "", err
	}
	stolen := stolenMap[plot.ID]
	if err := inventory.Add(ctx, t, farm.ID, item, quantity, quality, mutation); err != nil {
		return "", 0, "", err
	}
	if !autoSell {
		t.Adjust(warehouseUsedKey{farm.ID}, quantity)
	}
	plotNumber := plot.PlotNumber
	// 種子實驗室：水晶作物額外給水晶、特殊種子有機會取回同基因的種子（在清空土地前判斷）
	labNote, err := s.labHarvest(ctx, t, farm, plot, mutation)
	if err != nil {
		return "", 0, "", err
	}
	if err := farms.Clear(ctx, t, plot); err != nil { // 收成後土地清空，同一批作物不會再發放
		return "", 0, "", err
	}
	note := ""
	if stolen > 0 {
		note = fmt.Sprintf("（已被偷走 %d 個）", stolen)
	}
	if lost > 0 {
		note += fmt.Sprintf("（大風颳走 %d 個）", lost)
	}
	if blocked > 0 && remaining > 1 {
		note += fmt.Sprintf("（🛡️ 防守設施擋下暴風 %d 個）", min(blocked, remaining-1))
	}
	if catBonus > 0 {
		note += fmt.Sprintf("（🐱 貓咪趕走老鼠 +%d 個）", catBonus)
	}
	note += labNote
	var applied []eventrules.Applied
	if lost > 0 {
		applied = eventrules.MergeApplied(wind)
	}
	if err := s.record(ctx, t, farm, rules.EvHarvest, fmt.Sprintf("收成第 %d 格，獲得 %d 個%s%s", plotNumber, quantity, name, note),
		rec{ItemCode: item, Quantity: quantity, Quality: quality, Mutation: mutation, Applied: applied}); err != nil {
		return "", 0, "", err
	}
	message := fmt.Sprintf("收成第 %d 格，獲得 %d 個%s%s。", plotNumber, quantity, name, note)
	value := 0
	if autoSell {
		price, income, err := s.sellLocked(ctx, t, farm, crop, quantity, quality, mutation, "自動販售機：")
		if err != nil {
			return "", 0, "", err
		}
		message += fmt.Sprintf(" 🏪 自動販售機賣出（單價 %d），金幣 +%d。", price, income)
		value = income
	} else if details != nil {
		if value, err = s.estimateValue(ctx, t, crop, quantity, quality, mutation); err != nil {
			return "", 0, "", err
		}
	}
	if details != nil {
		shown := name // 交易紀錄維持中文；給玩家看的摘要用面板的語言
		if lang := i18n.LangOf(ctx); lang != i18n.Default {
			shown = ProduceNameLang(lang, crop.Code, crop.Name, crop.VariantCode, crop.VariantName, mutation) + GeneticsLabelLang(lang, quality, mutation)
		}
		*details = append(*details, HarvestDetail{shown, quantity, quality, mutation, catBonus, stolen, lost, value, autoSell})
	}
	return message, quantity, item, nil
}

// recordNote value 打破個人紀錄時回傳「🏆 新紀錄！」那一行（第一次記錄不顯示）；呼叫端必須已鎖定農場。
func (s *GameService) recordNote(ctx context.Context, t *db.Tx, farm *models.Farm, key string, value int) (string, error) {
	if value <= 0 {
		return "", nil
	}
	old, err := feedback.BumpRecord(ctx, t, farm.ID, key, value)
	if err != nil || old == nil {
		return "", err
	}
	return RecordNoteLang(i18n.LangOf(ctx), key, value, *old), nil
}

// NoteSale 面板販售後檢查「一次販售」個人紀錄（全部販售是多筆交易，由面板加總後呼叫）。
func (s *GameService) NoteSale(ctx context.Context, playerID int64, income int) (string, error) {
	if income <= 0 {
		return "", nil
	}
	return inTx(ctx, s, func(t *db.Tx) (string, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return "", err
		}
		return s.recordNote(ctx, t, farm, "sale_income", income)
	})
}

// sellPrice 目前的販售單價（含活動售價加成）與套用的活動。
// farm 為 nil 時不含群組的委託增益（只用於估價）。
func (s *GameService) sellPrice(ctx context.Context, t *db.Tx, farm *models.Farm, crop *models.CropType, base int) (int, eventrules.Bonus, error) {
	active, err := s.activeEvents(ctx, t, farm)
	if err != nil {
		return 0, eventrules.Bonus{}, err
	}
	sell := eventrules.BonusOf(active, "sell_price_pct", &crop.ID)
	base = seasons.SellPrice(base, crop.Seasons, s.now()) // 非當季物以稀為貴（Go 版新增）
	return eventrules.ApplySellBonus(base, sell.Value), sell, nil
}

// estimateValue 依目前價目（含活動售價加成）估計這批作物賣掉能拿多少金幣；只用於訊息，不改任何資料。
func (s *GameService) estimateValue(ctx context.Context, t *db.Tx, crop *models.CropType, quantity int, quality, mutation string) (int, error) {
	settings, err := s.geneticsSettings(ctx, t)
	if err != nil {
		return 0, err
	}
	price, _, err := s.sellPrice(ctx, t, nil, crop, genetics.UnitPrice(crop.SellPrice, quality, mutation, settings))
	return price * quantity, err
}

func (s *GameService) ClearWithered(ctx context.Context, playerID int64, plotNumber int) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plot, _, state, err := s.plotForAction(ctx, t, farm, plotNumber)
		if err != nil {
			return ActionResult{}, err
		}
		if state != rules.PlotWithered {
			return ActionResult{}, gameerr.Validationf("第 %d 格沒有乾枯的作物可清除。", plotNumber)
		}
		crop, err := crops.Get(ctx, t, *plot.CropTypeID)
		if err != nil {
			return ActionResult{}, err
		}
		saved, err := s.salvageWithered(ctx, t, farm, plot, crop)
		if err != nil {
			return ActionResult{}, err
		}
		if err := farms.Clear(ctx, t, plot); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvClear, fmt.Sprintf("清除第 %d 格乾枯的%s", plotNumber, crop.Name), rec{}); err != nil {
			return ActionResult{}, err
		}
		auto, err := s.autoPlant(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "已清除第 %d 格乾枯的%s，土地恢復為空地。%s%s", plotNumber, crop.Name, saved, auto), farm.Gold, 0, "", 0), nil
	})
}

// ---------- 一鍵操作（整批在同一筆交易中完成，只鎖一次農場） ----------

// plotsInState 先一次鎖定這座農場的全部土地再判斷狀態（一條 SELECT … FOR UPDATE）。
func (s *GameService) plotsInState(ctx context.Context, t *db.Tx, farm *models.Farm, state rules.PlotState) ([]*models.FarmPlot, error) {
	now := s.now()
	all, err := farms.ListPlots(ctx, t, farm.ID, true)
	if err != nil {
		return nil, err
	}
	hive := 0
	if state == rules.PlotEmpty { // 蜂箱那一格不算空地（2026-11-20）
		if hive, err = s.hivePlot(ctx, t, farm); err != nil {
			return nil, err
		}
	}
	var out []*models.FarmPlot
	for i := range all {
		p := &all[i].FarmPlot
		if rules.PlotStateAt(p.CropTypeID, p.WateredAt, p.ReadyAt, now) == state && p.PlotNumber != hive {
			out = append(out, p)
		}
	}
	return out, nil
}

// PlantAll 把所有空地種滿同一種作物；種子不夠時用目前金幣自動補買（買到的種子立即種下，不佔倉庫）。
func (s *GameService) PlantAll(ctx context.Context, playerID int64, cropKey string) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		crop, err := s.crop(ctx, t, cropKey, true)
		if err != nil {
			return ActionResult{}, err
		}
		if err := s.checkSeason(crop); err != nil {
			return ActionResult{}, err
		}
		plots, err := s.plotsInState(ctx, t, farm, rules.PlotEmpty)
		if err != nil {
			return ActionResult{}, err
		}
		if len(plots) == 0 {
			return ActionResult{}, gameerr.Validationf("%s", tr(ctx, "svc.plant.no_empty"))
		}
		text, count, err := s.plantOnPlots(ctx, t, farm, crop, plots)
		if err != nil {
			return ActionResult{}, err
		}
		return result(text, farm.Gold, 0, rules.SeedItemCode(crop.Code), count), nil
	})
}

// plantOnPlots 在指定的空地種下同一種作物（農場與土地必須已鎖定、季節已檢查）；種子不夠時用目前金幣自動補買，
// 買不起或商店不賣時能種多少種多少。一顆都種不了時回傳遊戲錯誤（在扣款之前判斷，呼叫端可以直接略過）。
// PlantAll 與收成並重種共用（2026-11-18 從 PlantAll 抽出）。
func (s *GameService) plantOnPlots(ctx context.Context, t *db.Tx, farm *models.Farm, crop *models.CropType, plots []*models.FarmPlot) (string, int, error) {
	item := rules.SeedItemCode(crop.Code)
	have, err := inventory.GetQuantity(ctx, t, farm.ID, item, "normal", "none")
	if err != nil {
		return "", 0, err
	}
	bought := 0
	if len(plots) > have && !crop.ShopBuyable {
		if have <= 0 {
			return "", 0, gameerr.New(gameerr.InsufficientItems, "%s", tr(ctx, "svc.plant.no_seed_rare", "crop", cropNameCtx(ctx, crop)))
		}
	} else if len(plots) > have {
		bought = min(len(plots)-have, numfmt.FloorDiv(farm.Gold, crop.SeedPrice))
		if bought <= 0 && have <= 0 {
			return "", 0, gameerr.New(gameerr.InsufficientGold, "%s", tr(ctx, "svc.plant.no_gold", "crop", cropNameCtx(ctx, crop), "price", crop.SeedPrice))
		}
		if bought > 0 {
			cost := crop.SeedPrice * bought
			farm.Gold -= cost
			if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
				return "", 0, err
			}
			if err := inventory.Add(ctx, t, farm.ID, item, bought, "normal", "none"); err != nil {
				return "", 0, err
			}
			if err := s.record(ctx, t, farm, rules.EvBuySeed, fmt.Sprintf("購買 %d 顆%s種子，金幣 -%d", bought, crop.Name, cost),
				rec{ItemCode: item, Quantity: bought, GoldDelta: -cost}); err != nil {
				return "", 0, err
			}
		}
	}
	count := min(len(plots), have+bought)
	watered := 0
	// 種子一次扣完（have + bought ≥ count，一定扣得到）
	if count > 0 {
		ok, err := inventory.Remove(ctx, t, farm.ID, item, count, "normal", "none")
		if err != nil {
			return "", 0, err
		}
		if !ok {
			return "", 0, gameerr.New(gameerr.InsufficientItems, "%s", tr(ctx, "svc.plant.seed_short", "crop", cropNameCtx(ctx, crop)))
		}
	}
	if err := t.Bulk(ctx, func() error {
		for _, plot := range plots[:count] {
			_, seconds, err := s.plant(ctx, t, farm, plot, crop, "")
			if err != nil {
				return err
			}
			if seconds != nil {
				watered++
			}
		}
		return nil
	}); err != nil {
		return "", 0, err
	}
	text := tr(ctx, "svc.plant.done", "n", count, "crop", cropNameCtx(ctx, crop))
	if bought > 0 {
		text += tr(ctx, "svc.plant.bought", "n", bought)
	}
	if count < len(plots) {
		reason := tr(ctx, "svc.plant.reason_seed")
		if crop.ShopBuyable {
			reason = tr(ctx, "svc.plant.reason_gold")
		}
		text += tr(ctx, "svc.plant.left_empty", "reason", reason, "n", len(plots)-count)
	}
	if watered == count {
		text += tr(ctx, "svc.plant.watered")
	} else {
		text += tr(ctx, "svc.plant.remember_water")
	}
	return text, count, nil
}

func (s *GameService) WaterAll(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plots, err := s.plotsInState(ctx, t, farm, rules.PlotWaitingWater)
		if err != nil {
			return ActionResult{}, err
		}
		if len(plots) == 0 {
			return ActionResult{}, gameerr.Validationf("%s", tr(ctx, "svc.water.none"))
		}
		now := s.now()
		fastest := 0
		if err := t.Bulk(ctx, func() error {
			for i, p := range plots {
				crop, err := crops.Get(ctx, t, *p.CropTypeID)
				if err != nil {
					return err
				}
				sec, err := s.water(ctx, t, farm, p, crop, now, false, nil)
				if err != nil {
					return err
				}
				if i == 0 || sec < fastest {
					fastest = sec
				}
			}
			return nil
		}); err != nil {
			return ActionResult{}, err
		}
		return result(tr(ctx, "svc.water.done", "n", len(plots), "time", DurationLang(i18n.LangOf(ctx), fastest, false)),
			farm.Gold, 0, "", len(plots)), nil
	})
}

// HarvestAll 收成所有成熟的土地；倉庫放不下時停在那一格（之前收成的保留，之後的留在土地上），不會遺失作物。
func (s *GameService) HarvestAll(ctx context.Context, playerID int64) (ActionResult, error) {
	return s.harvestAll(ctx, playerID, false)
}

// HarvestAndReplant 一鍵收成並在收成的土地重種同一種作物（Go 版新增，2026-11-18）：同一筆交易中完成，
// 種子不夠時自動補買（同「種滿空地」）；特殊種子（種子實驗室）種的土地、非當季或買不到種子的作物不重種，
// 空著的土地之後再交給自動播種機。
func (s *GameService) HarvestAndReplant(ctx context.Context, playerID int64) (ActionResult, error) {
	return s.harvestAll(ctx, playerID, true)
}

func (s *GameService) harvestAll(ctx context.Context, playerID int64, replant bool) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plots, err := s.plotsInState(ctx, t, farm, rules.PlotReady)
		if err != nil {
			return ActionResult{}, err
		}
		if len(plots) == 0 {
			return ActionResult{}, gameerr.Validationf("%s", tr(ctx, "svc.harvest.none"))
		}
		done, total, stopped, record, auto, replanted := 0, 0, "", "", "", ""
		var details []HarvestDetail
		var again []replantPlot // 收成後要重種的土地（依收成順序）
		if err := t.Bulk(ctx, func() error {
			for _, plot := range plots {
				crop, err := crops.Get(ctx, t, *plot.CropTypeID)
				if err != nil {
					return err
				}
				fromLab := plot.LabSeedID != nil // 收成會清掉土地，先記下是不是特殊種子種的
				_, quantity, _, err := s.harvestLocked(ctx, t, farm, plot, crop, &details)
				if gameerr.Is(err, gameerr.WarehouseFull) {
					stopped = tr(ctx, "svc.harvest.stopped", "n", len(plots)-done, "reason", err.Error())
					break
				}
				if err != nil {
					return err
				}
				done++
				total += quantity
				if replant && !fromLab {
					again = append(again, replantPlot{plot.PlotNumber, crop})
				}
			}
			if done == 0 {
				return gameerr.New(gameerr.WarehouseFull, strings.TrimSpace(strings.ReplaceAll(stopped, "⚠️", "")))
			}
			var err error
			if record, err = s.recordNote(ctx, t, farm, "harvest_quantity", total); err != nil {
				return err
			}
			if replant {
				if replanted, err = s.replantLocked(ctx, t, farm, again); err != nil {
					return err
				}
			}
			auto, err = s.autoPlant(ctx, t, farm)
			return err
		}); err != nil {
			return ActionResult{}, err
		}
		msg := HarvestSummaryLang(i18n.LangOf(ctx), details) + record + stopped
		if replanted != "" {
			msg += tr(ctx, "svc.harvest.replant") + replanted
		}
		if auto != "" {
			msg += "\n" + strings.TrimSpace(auto)
		}
		return result(msg, farm.Gold, 0, "", total), nil
	})
}

// replantPlot 收成後要重種的土地與原本的作物。
type replantPlot struct {
	plotNumber int
	crop       *models.CropType
}

// replantLocked 在收成完的土地重種原本的作物（農場已鎖定）：同一種作物一起種（種子不夠時自動補買），
// 非當季、停用、買不起或沒有種子的作物略過並說明原因；回傳結果說明（沒有要重種的土地時為空字串）。
func (s *GameService) replantLocked(ctx context.Context, t *db.Tx, farm *models.Farm, again []replantPlot) (string, error) {
	if len(again) == 0 {
		return "", nil
	}
	all, err := farms.ListPlots(ctx, t, farm.ID, true)
	if err != nil {
		return "", err
	}
	hive, err := s.hivePlot(ctx, t, farm) // 蜂箱那一格不能種（2026-11-20）
	if err != nil {
		return "", err
	}
	empty := map[int]*models.FarmPlot{}
	for i := range all {
		if all[i].CropTypeID == nil && all[i].PlotNumber != hive {
			empty[all[i].PlotNumber] = &all[i].FarmPlot
		}
	}
	var order []*models.CropType
	byCrop := map[int64][]*models.FarmPlot{}
	for _, a := range again {
		plot := empty[a.plotNumber]
		if plot == nil {
			continue
		}
		if _, ok := byCrop[a.crop.ID]; !ok {
			order = append(order, a.crop)
		}
		byCrop[a.crop.ID] = append(byCrop[a.crop.ID], plot)
	}
	var parts []string
	for _, crop := range order {
		if !crop.Active {
			parts = append(parts, crop.Name+txt(ctx, "已停用，沒有重種"))
			continue
		}
		if err := s.checkSeason(crop); err != nil {
			parts = append(parts, crop.Name+txt(ctx, "現在不能種，沒有重種"))
			continue
		}
		text, _, err := s.plantOnPlots(ctx, t, farm, crop, byCrop[crop.ID])
		if ge, ok := gameerr.As(err); ok { // 種子買不起、商店不賣：不影響收成，只說明
			parts = append(parts, ge.Error())
			continue
		}
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n"), nil
}

func (s *GameService) ClearAllWithered(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plots, err := s.plotsInState(ctx, t, farm, rules.PlotWithered)
		if err != nil {
			return ActionResult{}, err
		}
		if len(plots) == 0 {
			return ActionResult{}, gameerr.Validationf("%s", tr(ctx, "svc.clear.none"))
		}
		auto, saved := "", ""
		if err := t.Bulk(ctx, func() error {
			for _, plot := range plots {
				crop, err := crops.Get(ctx, t, *plot.CropTypeID)
				if err != nil {
					return err
				}
				note, err := s.salvageWithered(ctx, t, farm, plot, crop)
				if err != nil {
					return err
				}
				saved += note
				if err := farms.Clear(ctx, t, plot); err != nil {
					return err
				}
				if err := s.record(ctx, t, farm, rules.EvClear,
					fmt.Sprintf("清除第 %d 格乾枯的%s", plot.PlotNumber, crop.Name), rec{}); err != nil {
					return err
				}
			}
			var err error
			auto, err = s.autoPlant(ctx, t, farm)
			return err
		}); err != nil {
			return ActionResult{}, err
		}
		return result(tr(ctx, "svc.clear.done", "n", len(plots))+saved+auto, farm.Gold, 0, "", len(plots)), nil
	})
}

// salvageWithered 刺蝟（2026-11-18）：清除乾枯作物時救回原本收成數量的 X%（小數部分依機率多 1 個），
// 放進倉庫（倉庫放不下時不救回）。要在清空土地之前呼叫；回傳附在結果後面的說明（沒有救回時為空字串）。
func (s *GameService) salvageWithered(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot, crop *models.CropType) (string, error) {
	pct, err := s.petPct(ctx, t, farm.ID, petrules.Hedgehog)
	if err != nil || pct == 0 || plot.RemainingQuantity == nil {
		return "", err
	}
	quantity := petrules.BonusQuantity(*plot.RemainingQuantity, pct, s.PetRng())
	if quantity <= 0 {
		return "", nil
	}
	if err := s.ensureSpace(ctx, t, farm, quantity); gameerr.Is(err, gameerr.WarehouseFull) {
		return txf(ctx, " 🦔 刺蝟想救回第 %d 格的%s，但倉庫放不下。", plot.PlotNumber, crop.Name), nil
	} else if err != nil {
		return "", err
	}
	quality, mutation := deref(plot.Quality), deref(plot.Mutation)
	item := genetics.ProduceCode(crop.Code, crop.VariantCode, mutation)
	if err := inventory.Add(ctx, t, farm.ID, item, quantity, quality, mutation); err != nil {
		return "", err
	}
	t.Adjust(warehouseUsedKey{farm.ID}, quantity)
	if err := s.record(ctx, t, farm, rules.EvClear, fmt.Sprintf("刺蝟從第 %d 格乾枯的%s救回 %d 個", plot.PlotNumber, crop.Name, quantity),
		rec{ItemCode: item, Quantity: quantity, Quality: quality, Mutation: mutation}); err != nil {
		return "", err
	}
	return txf(ctx, " 🦔 刺蝟從第 %d 格救回 %d 個%s。", plot.PlotNumber, quantity, crop.Name), nil
}

// ---------- 販售與擴建 ----------

// SellCrop 販售指定品質／突變的農產品；單價依統一公式計算並保存於交易紀錄。quality 預設 "normal"，mutation 預設 "none"。
func (s *GameService) SellCrop(ctx context.Context, playerID int64, cropKey string, quantity int, quality, mutation string) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	quality, mutation = genetics.ParseQuality(quality), genetics.ParseMutation(mutation)
	if quality == "" {
		return ActionResult{}, gameerr.Validationf("品質只能是 普通／優良／稀有／傳說。")
	}
	if mutation == "" {
		return ActionResult{}, gameerr.Validationf("突變只能是 無／巨型作物／黃金作物／快速生長／珍稀變種。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		crop, err := crops.FindVariant(ctx, t, cropKey)
		if err != nil {
			return ActionResult{}, err
		}
		if crop != nil {
			// 珍稀變種是獨立作物，突變固定為 variant
			mutation = "variant"
		} else {
			if crop, err = s.crop(ctx, t, cropKey, false); err != nil {
				return ActionResult{}, err
			}
			if mutation == "variant" {
				return ActionResult{}, gameerr.Validationf("珍稀變種請使用變種名稱販售（%s）。", crop.VariantName)
			}
		}
		item := genetics.ProduceCode(crop.Code, crop.VariantCode, mutation)
		name := genetics.ProduceName(crop.Name, crop.VariantName, mutation) + GeneticsLabel(quality, mutation)
		price, income, err := s.sellLocked(ctx, t, farm, crop, quantity, quality, mutation, "")
		if err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "已出售 %d 個%s（單價 %d），獲得 %d 金幣。%s", quantity, name, price, income,
			GoldChange(farm.Gold, income)), farm.Gold, income, item, quantity), nil
	})
}

// sellLocked 從背包扣除並以目前價目（含活動售價加成）賣出；回傳 (單價, 收入)。手動販售與自動販售機共用。
func (s *GameService) sellLocked(ctx context.Context, t *db.Tx, farm *models.Farm, crop *models.CropType, quantity int,
	quality, mutation, source string) (int, int, error) {
	item := genetics.ProduceCode(crop.Code, crop.VariantCode, mutation)
	name := genetics.ProduceName(crop.Name, crop.VariantName, mutation) + GeneticsLabel(quality, mutation)
	ok, err := inventory.Remove(ctx, t, farm.ID, item, quantity, quality, mutation)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		have, err := inventory.GetQuantity(ctx, t, farm.ID, item, quality, mutation)
		if err != nil {
			return 0, 0, err
		}
		return 0, 0, gameerr.New(gameerr.InsufficientItems, "%s數量不足：想賣 %d 個，倉庫只有 %d 個。", name, quantity, have)
	}
	settings, err := s.geneticsSettings(ctx, t)
	if err != nil {
		return 0, 0, err
	}
	price, sell, err := s.sellPrice(ctx, t, farm, crop, genetics.UnitPrice(crop.SellPrice, quality, mutation, settings))
	if err != nil {
		return 0, 0, err
	}
	income := price * quantity
	farm.Gold += income
	if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
		return 0, 0, err
	}
	applied := eventrules.MergeApplied(sell)
	if err := s.record(ctx, t, farm, rules.EvSell,
		fmt.Sprintf("%s出售 %d 個%s（單價 %d），金幣 +%d%s", source, quantity, name, price, income, eventsNote(applied)),
		rec{ItemCode: item, Quantity: quantity, GoldDelta: income, Quality: quality, Mutation: mutation,
			UnitPrice: &price, Applied: applied}); err != nil {
		return 0, 0, err
	}
	return price, income, nil
}

func (s *GameService) UpgradeFarm(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		n, err := farms.CountPlots(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		cost, ok := expansion.LandPrice(n)
		if !ok {
			return ActionResult{}, gameerr.Validationf("%s", tr(ctx, "svc.upgrade.max", "n", expansion.MaxPlots()))
		}
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "%s", tr(ctx, "svc.upgrade.no_gold", "need", cost, "have", farm.Gold))
		}
		farm.Gold -= cost
		farm.Level++
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("level", farm.Level)); err != nil {
			return ActionResult{}, err
		}
		numbers, err := farms.AddPlots(ctx, t, farm.ID, balance.UpgradePlotsAdded)
		if err != nil {
			return ActionResult{}, err
		}
		total, err := farms.CountPlots(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvUpgrade, fmt.Sprintf("擴建土地 +%d 格（共 %d 格），金幣 -%d", len(numbers), total, cost),
			rec{Quantity: len(numbers), GoldDelta: -cost}); err != nil {
			return ActionResult{}, err
		}
		auto, err := s.autoPlant(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		tail := tr(ctx, "svc.upgrade.tail_max")
		if nxt, ok := expansion.LandPrice(total); ok {
			tail = tr(ctx, "svc.upgrade.tail_next", "n", nxt)
		}
		if total == balance.MaxPlots+1 {
			tail = tr(ctx, "svc.upgrade.field2") + tail
		}
		return result(tr(ctx, "svc.upgrade.done", "level", farm.Level, "plot", numbers[0], "total", total, "cost", cost,
			"before", total-len(numbers), "old_level", farm.Level-1)+
			GoldChangeLang(i18n.LangOf(ctx), farm.Gold, -cost)+"\n"+tail+auto, farm.Gold, -cost, "", len(numbers)), nil
	})
}

// ExpandMaterialBag 素材背包擴充（2026-11-19）：每級 +expansion 設定的格數，價格逐級提高。
func (s *GameService) ExpandMaterialBag(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		price, ok := expansion.MaterialBagPrice(farm.MaterialBagLevel)
		if !ok {
			return ActionResult{}, gameerr.Validationf("素材背包已經是最高等級 Lv.%d。", farm.MaterialBagLevel)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：素材背包擴充需要 %d，目前只有 %d。", price, farm.Gold)
		}
		base := exploration.BagCapacity()
		before := expansion.MaterialBagCapacity(farm.MaterialBagLevel, base)
		farm.Gold -= price
		farm.MaterialBagLevel++
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("material_bag_level", farm.MaterialBagLevel)); err != nil {
			return ActionResult{}, err
		}
		after := expansion.MaterialBagCapacity(farm.MaterialBagLevel, base)
		if err := s.record(ctx, t, farm, rules.EvBuyItem,
			fmt.Sprintf("素材背包擴充到 Lv.%d：%d → %d 格，金幣 -%d", farm.MaterialBagLevel, before, after, price),
			rec{ItemCode: "material_bag", Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		tail := txt(ctx, "已達最高等級。")
		if nxt, ok := expansion.MaterialBagPrice(farm.MaterialBagLevel); ok {
			tail = txf(ctx, "下一級需要 %d 金幣。", nxt)
		}
		return result(txf(ctx, "🎒 素材背包擴充到 Lv.%d，花費 %d 金幣。\n素材背包 %d → %d 格%s\n%s",
			farm.MaterialBagLevel, price, before, after, GoldChange(farm.Gold, -price), tail), farm.Gold, -price, "", 0), nil
	})
}

// ExpandWarehouse 倉庫擴大卷：立即 +50 格，價格依目前容量遞增，最大 300 格。
func (s *GameService) ExpandWarehouse(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		price, ok := expansion.WarehousePrice(farm.WarehouseCapacity)
		if !ok {
			return ActionResult{}, gameerr.Validationf("倉庫已經是最大容量 %d 格。", expansion.WarehouseMax())
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：倉庫擴大卷需要 %d，目前只有 %d。", price, farm.Gold)
		}
		before := farm.WarehouseCapacity
		farm.Gold -= price
		farm.WarehouseCapacity = min(before+balance.WarehouseExpansionStep, expansion.WarehouseMax())
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("warehouse_capacity", farm.WarehouseCapacity)); err != nil {
			return ActionResult{}, err
		}
		code := balance.WarehouseExpansionCode
		if err := s.record(ctx, t, farm, rules.EvBuyItem,
			fmt.Sprintf("使用倉庫擴大卷：倉庫 %d → %d 格，金幣 -%d", before, farm.WarehouseCapacity, price),
			rec{ItemCode: code, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		tail := txt(ctx, "已達最大容量。")
		if nxt, ok := expansion.WarehousePrice(farm.WarehouseCapacity); ok {
			tail = txf(ctx, "下一次擴大需要 %d 金幣。", nxt)
		}
		return result(txf(ctx, "倉庫擴大為 %d 格，花費 %d 金幣。\n📦 倉庫 %d → %d 格%s\n%s",
			farm.WarehouseCapacity, price, before, farm.WarehouseCapacity, GoldChange(farm.Gold, -price), tail),
			farm.Gold, -price, code, 1), nil
	})
}

// UpgradeGreenhouse 溫室升級：永久讓之後澆水的作物生長時間再少一些，價格逐級提高。
func (s *GameService) UpgradeGreenhouse(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		price, ok := specialshop.GreenhousePrice(farm.GreenhouseLevel)
		if !ok {
			return ActionResult{}, gameerr.Validationf("溫室已經是最高等級 %d 級。", specialshop.GreenhouseMaxLevel())
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：溫室升級需要 %d，目前只有 %d。", price, farm.Gold)
		}
		farm.Gold -= price
		farm.GreenhouseLevel++
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("greenhouse_level", farm.GreenhouseLevel)); err != nil {
			return ActionResult{}, err
		}
		pct := specialshop.GreenhousePct(farm.GreenhouseLevel)
		code := specialshop.GreenhouseCode
		if err := s.record(ctx, t, farm, rules.EvBuyItem,
			fmt.Sprintf("溫室升到 %d 級（生長時間 -%d%%），金幣 -%d", farm.GreenhouseLevel, pct, price),
			rec{ItemCode: code, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		tail := txt(ctx, "已達最高等級。")
		if nxt, ok := specialshop.GreenhousePrice(farm.GreenhouseLevel); ok {
			tail = txf(ctx, "下一級需要 %s 金幣。", numfmt.Comma(nxt))
		}
		return result(txf(ctx, "🏡 溫室升到 %d 級，之後澆水的作物生長時間 -%d%%。\n⏱️ 生長時間 -%d%% → -%d%%%s\n%s",
			farm.GreenhouseLevel, pct, specialshop.GreenhousePct(farm.GreenhouseLevel-1), pct, GoldChange(farm.Gold, -price), tail),
			farm.Gold, -price, code, 1), nil
	})
}

// weatherLoss 生效事件中天氣（暴風）造成的收成減損；沒有天氣事件時為 0。
func weatherLoss(active []*eventrules.Event) int {
	for _, e := range active {
		if e.ID == weather.EventID {
			return e.HarvestLoss
		}
	}
	return 0
}

type defenseListKey struct{ farmID int64 }

// plotGuards 守護這格的防守設施種類數（同種設施重疊只算一次）。一鍵收成時整座農場只查一次設施清單。
func (s *GameService) plotGuards(ctx context.Context, t *db.Tx, farmID int64, plotNumber int) (int, error) {
	list, err := db.Memo(t, defenseListKey{farmID}, func() ([]models.FarmDefense, error) { return defenses.ListForFarm(ctx, t, farmID) })
	if err != nil {
		return 0, err
	}
	covering := map[string]bool{}
	for _, d := range list {
		if rules.DefenseCovers(balance.Defenses[d.DeviceCode].Range, d.StartPlot, plotNumber) {
			covering[d.DeviceCode] = true
		}
	}
	return len(covering), nil
}
