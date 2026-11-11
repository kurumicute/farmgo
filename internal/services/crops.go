package services

// 商店與倉庫、種植與收成、一鍵操作、販售與擴建（對應 Python GameService 的同名段落）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/game/specialshop"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/feedback"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/pets"
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
		if err := s.ensureSpace(ctx, t, farm, quantity); err != nil {
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
		return result(fmt.Sprintf("已購買 %d 顆%s種子，花費 %d 金幣。%s", quantity, crop.Name, cost, auto),
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
		tail := fmt.Sprintf("請記得澆水（water %d），澆水後開始生長。", plotNumber)
		if seconds != nil {
			tail = fmt.Sprintf("已自動澆水，%s後成熟。", rules.FormatDuration(*seconds))
		}
		return result(fmt.Sprintf("已在第 %d 格種下%s%s，%s", plotNumber, crop.Name, label, tail), farm.Gold, 0, item, 1), nil
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
	now := s.now()
	// 播種時一次決定整批的品質與主要突變，之後固定不變（收成、偷菜、重啟都不重新抽取）
	settings, err := s.geneticsSettings(ctx, t)
	if err != nil {
		return "", nil, err
	}
	// 限時事件的品質、突變與收成加成在播種當下決定，之後事件結束也不會改變
	active, err := s.activeEvents(ctx, t)
	if err != nil {
		return "", nil, err
	}
	q := eventrules.BonusOf(active, eventrules.PlantEffects[0], &crop.ID)
	m := eventrules.BonusOf(active, eventrules.PlantEffects[1], &crop.ID)
	h := eventrules.BonusOf(active, eventrules.PlantEffects[2], &crop.ID)
	quality := genetics.RollQuality(genetics.EffectiveQualityBP(settings, farm.Level, q.Value), s.geneticsRng())
	mutation := genetics.RollMutation(settings, s.geneticsRng(), m.Value)
	quantity := genetics.HarvestQuantity(crop.HarvestQuantity, mutation, settings, h.Value)
	if err := farms.Plant(ctx, t, plot, crop.ID, quantity, now, quality, mutation); err != nil {
		return "", nil, err
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
	var empty []*models.FarmPlot
	for i := range all {
		if all[i].CropTypeID == nil {
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
			if planted[e.crop.Name] == 0 {
				order = append(order, e.crop.Name)
			}
			planted[e.crop.Name]++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(order) == 0 {
		return " 🤖 自動播種機：背包沒有種子可以種。", nil
	}
	total, parts := 0, make([]string, len(order))
	for i, name := range order {
		parts[i] = fmt.Sprintf("%s×%d", name, planted[name])
		total += planted[name]
	}
	return fmt.Sprintf(" 🤖 自動播種機種下 %d 格（%s）。", total, strings.Join(parts, "、")), nil
}

// water auto：自動澆水器；reason：免澆水活動（nil 表示不是）。回傳生長秒數。
func (s *GameService) water(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot, crop *models.CropType,
	now time.Time, auto bool, reason *eventrules.Bonus) (int, error) {
	earthworm, err := db.Memo(t, effectKey{farm.ID, balance.EffectEarthworm}, func() (bool, error) {
		return items.EffectActive(ctx, t, farm.ID, balance.EffectEarthworm, now)
	})
	if err != nil {
		return 0, err
	}
	active, err := s.activeEvents(ctx, t)
	if err != nil {
		return 0, err
	}
	grow := eventrules.BonusOf(active, "grow_reduction_pct", &crop.ID)
	settings, err := s.geneticsSettings(ctx, t)
	if err != nil {
		return 0, err
	}
	mutation := deref(plot.Mutation)
	// 快速生長突變也只在澆水後才開始計時，不會繞過澆水與乾枯規則；溫室的減少 % 與限時活動相加
	greenhouse := specialshop.GreenhousePct(farm.GreenhouseLevel)
	seconds := genetics.GrowSeconds(crop.GrowSeconds, mutation, earthworm, settings, grow.Value+greenhouse)
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
	bonuses := []eventrules.Bonus{grow}
	source := "澆水"
	if reason != nil {
		bonuses = append(bonuses, *reason)
		source = "免澆水活動"
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
		return result(fmt.Sprintf("已為第 %d 格的%s澆水，%s後成熟。", plotNumber, crop.Name, rules.FormatDuration(seconds)),
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
	active, err := s.activeEvents(ctx, t)
	if err != nil {
		return "", 0, "", err
	}
	// 大風颳走：收成時每格少 N 個，至少保留 1 個（在收成當下判斷）
	wind := eventrules.BonusOf(active, "harvest_loss", &crop.ID)
	remaining := *plot.RemainingQuantity
	lost := min(wind.Value, max(0, remaining-1))
	quantity := remaining - lost
	// 貓咪趕走偷吃的老鼠：收成數量 +X%（只有主人收成時；被偷走的部分不會補回）
	cat, err := db.Memo(t, petKey{farm.ID, petrules.Cat}, func() (*int, error) { return pets.Level(ctx, t, farm.ID, petrules.Cat) })
	if err != nil {
		return "", 0, "", err
	}
	catBonus := petrules.BonusQuantity(quantity, petrules.BonusPct(cat), s.PetRng())
	quantity += catBonus
	autoSell, err := s.toolEnabled(ctx, t, farm.ID, balance.ToolAutoSeller)
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
	if catBonus > 0 {
		note += fmt.Sprintf("（🐱 貓咪趕走老鼠 +%d 個）", catBonus)
	}
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
		*details = append(*details, HarvestDetail{name, quantity, quality, mutation, catBonus, stolen, lost, value, autoSell})
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
	return RecordNote(key, value, *old), nil
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
func (s *GameService) sellPrice(ctx context.Context, t *db.Tx, crop *models.CropType, base int) (int, eventrules.Bonus, error) {
	active, err := s.activeEvents(ctx, t)
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
	price, _, err := s.sellPrice(ctx, t, crop, genetics.UnitPrice(crop.SellPrice, quality, mutation, settings))
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
		return result(fmt.Sprintf("已清除第 %d 格乾枯的%s，土地恢復為空地。%s", plotNumber, crop.Name, auto), farm.Gold, 0, "", 0), nil
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
	var out []*models.FarmPlot
	for i := range all {
		p := &all[i].FarmPlot
		if rules.PlotStateAt(p.CropTypeID, p.WateredAt, p.ReadyAt, now) == state {
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
			return ActionResult{}, gameerr.Validationf("沒有空地可以種植。")
		}
		item := rules.SeedItemCode(crop.Code)
		have, err := inventory.GetQuantity(ctx, t, farm.ID, item, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		bought := 0
		if len(plots) > have && !crop.ShopBuyable {
			if have <= 0 {
				return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "沒有%s種子（商店不販售，只能從山洞探索或培育取得）。", crop.Name)
			}
		} else if len(plots) > have {
			bought = min(len(plots)-have, numfmt.FloorDiv(farm.Gold, crop.SeedPrice))
			if bought <= 0 && have <= 0 {
				return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：%s種子每顆 %d 金幣。", crop.Name, crop.SeedPrice)
			}
			if bought > 0 {
				cost := crop.SeedPrice * bought
				farm.Gold -= cost
				if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
					return ActionResult{}, err
				}
				if err := inventory.Add(ctx, t, farm.ID, item, bought, "normal", "none"); err != nil {
					return ActionResult{}, err
				}
				if err := s.record(ctx, t, farm, rules.EvBuySeed, fmt.Sprintf("購買 %d 顆%s種子，金幣 -%d", bought, crop.Name, cost),
					rec{ItemCode: item, Quantity: bought, GoldDelta: -cost}); err != nil {
					return ActionResult{}, err
				}
			}
		}
		count := min(len(plots), have+bought)
		watered := 0
		// 種子一次扣完（have + bought ≥ count，一定扣得到）
		if count > 0 {
			ok, err := inventory.Remove(ctx, t, farm.ID, item, count, "normal", "none")
			if err != nil {
				return ActionResult{}, err
			}
			if !ok {
				return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s種子不足。", crop.Name)
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
			return ActionResult{}, err
		}
		text := fmt.Sprintf("在 %d 格種下%s", count, crop.Name)
		if bought > 0 {
			text += fmt.Sprintf("（購買 %d 顆種子）", bought)
		}
		if count < len(plots) {
			reason := "種子不夠"
			if crop.ShopBuyable {
				reason = "金幣不夠"
			}
			text += fmt.Sprintf("；%s，還有 %d 格空著", reason, len(plots)-count)
		}
		if watered == count {
			text += "，已自動澆水。"
		} else {
			text += "。記得按「💧 全部澆水」。"
		}
		return result(text, farm.Gold, 0, item, count), nil
	})
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
			return ActionResult{}, gameerr.Validationf("沒有需要澆水的土地。")
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
		return result(fmt.Sprintf("已為 %d 格澆水，最快 %s後成熟。", len(plots), rules.FormatDuration(fastest)),
			farm.Gold, 0, "", len(plots)), nil
	})
}

// HarvestAll 收成所有成熟的土地；倉庫放不下時停在那一格（之前收成的保留，之後的留在土地上），不會遺失作物。
func (s *GameService) HarvestAll(ctx context.Context, playerID int64) (ActionResult, error) {
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
			return ActionResult{}, gameerr.Validationf("目前沒有可收成的作物。")
		}
		done, total, stopped, record, auto := 0, 0, "", "", ""
		var details []HarvestDetail
		if err := t.Bulk(ctx, func() error {
			for _, plot := range plots {
				crop, err := crops.Get(ctx, t, *plot.CropTypeID)
				if err != nil {
					return err
				}
				_, quantity, _, err := s.harvestLocked(ctx, t, farm, plot, crop, &details)
				if gameerr.Is(err, gameerr.WarehouseFull) {
					stopped = fmt.Sprintf("\n⚠️ 還有 %d 格沒收：%s", len(plots)-done, err.Error())
					break
				}
				if err != nil {
					return err
				}
				done++
				total += quantity
			}
			if done == 0 {
				return gameerr.New(gameerr.WarehouseFull, strings.TrimSpace(strings.ReplaceAll(stopped, "⚠️", "")))
			}
			var err error
			if record, err = s.recordNote(ctx, t, farm, "harvest_quantity", total); err != nil {
				return err
			}
			auto, err = s.autoPlant(ctx, t, farm)
			return err
		}); err != nil {
			return ActionResult{}, err
		}
		msg := HarvestSummary(details) + record + stopped
		if auto != "" {
			msg += "\n" + strings.TrimSpace(auto)
		}
		return result(msg, farm.Gold, 0, "", total), nil
	})
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
			return ActionResult{}, gameerr.Validationf("沒有乾枯的作物。")
		}
		auto := ""
		if err := t.Bulk(ctx, func() error {
			for _, plot := range plots {
				crop, err := crops.Get(ctx, t, *plot.CropTypeID)
				if err != nil {
					return err
				}
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
		return result(fmt.Sprintf("已清除 %d 格乾枯的作物。%s", len(plots), auto), farm.Gold, 0, "", len(plots)), nil
	})
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
		return result(fmt.Sprintf("已出售 %d 個%s（單價 %d），獲得 %d 金幣。%s", quantity, name, price, income,
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
	price, sell, err := s.sellPrice(ctx, t, crop, genetics.UnitPrice(crop.SellPrice, quality, mutation, settings))
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
		cost, ok := balance.UpgradePrice(n)
		if !ok {
			return ActionResult{}, gameerr.Validationf("農田已達上限 %d 格，無法再擴建。", balance.MaxPlots)
		}
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：擴建需要 %d，目前只有 %d。", cost, farm.Gold)
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
		tail := "農田已達上限。"
		if nxt, ok := balance.UpgradePrice(total); ok {
			tail = fmt.Sprintf("下一次擴建需要 %d 金幣。", nxt)
		}
		return result(fmt.Sprintf("擴建完成！農場升到 %d 級，新增第 %d 格，共 %d 格土地，花費 %d 金幣。\n"+
			"🌱 農田 %d → %d 格｜⭐ 等級 %d → %d%s\n%s%s",
			farm.Level, numbers[0], total, cost, total-len(numbers), total, farm.Level-1, farm.Level,
			GoldChange(farm.Gold, -cost), tail, auto), farm.Gold, -cost, "", len(numbers)), nil
	})
}

// ExpandWarehouse 倉庫擴大卷：立即 +50 格，價格依目前容量遞增，最大 300 格。
func (s *GameService) ExpandWarehouse(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		price, ok := balance.WarehouseExpansionPrice(farm.WarehouseCapacity)
		if !ok {
			return ActionResult{}, gameerr.Validationf("倉庫已經是最大容量 %d 格。", balance.WarehouseMaxCapacity)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：倉庫擴大卷需要 %d，目前只有 %d。", price, farm.Gold)
		}
		before := farm.WarehouseCapacity
		farm.Gold -= price
		farm.WarehouseCapacity = min(before+balance.WarehouseExpansionStep, balance.WarehouseMaxCapacity)
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("warehouse_capacity", farm.WarehouseCapacity)); err != nil {
			return ActionResult{}, err
		}
		code := balance.WarehouseExpansionCode
		if err := s.record(ctx, t, farm, rules.EvBuyItem,
			fmt.Sprintf("使用倉庫擴大卷：倉庫 %d → %d 格，金幣 -%d", before, farm.WarehouseCapacity, price),
			rec{ItemCode: code, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		tail := "已達最大容量。"
		if nxt, ok := balance.WarehouseExpansionPrice(farm.WarehouseCapacity); ok {
			tail = fmt.Sprintf("下一次擴大需要 %d 金幣。", nxt)
		}
		return result(fmt.Sprintf("倉庫擴大為 %d 格，花費 %d 金幣。\n📦 倉庫 %d → %d 格%s\n%s",
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
		tail := "已達最高等級。"
		if nxt, ok := specialshop.GreenhousePrice(farm.GreenhouseLevel); ok {
			tail = fmt.Sprintf("下一級需要 %s 金幣。", numfmt.Comma(nxt))
		}
		return result(fmt.Sprintf("🏡 溫室升到 %d 級，之後澆水的作物生長時間 -%d%%。\n⏱️ 生長時間 -%d%% → -%d%%%s\n%s",
			farm.GreenhouseLevel, pct, specialshop.GreenhousePct(farm.GreenhouseLevel-1), pct, GoldChange(farm.Gold, -price), tail),
			farm.Gold, -price, code, 1), nil
	})
}
