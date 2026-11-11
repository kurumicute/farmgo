package services

// 背包、限時活動、作物品質與突變、管理調整（對應 Python GameService 的同名段落與 build_backpack）。

import (
	"context"
	"fmt"
	"slices"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/events"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"

	eventrules "farmgm/internal/game/events"
	generepo "farmgm/internal/repositories/genetics"
)

// ---------- 背包 ----------

func (s *GameService) GetBackpack(ctx context.Context, playerID int64) (Backpack, error) {
	return inTx(ctx, s, func(t *db.Tx) (Backpack, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return Backpack{}, err
		}
		return BuildBackpack(ctx, t, farm, s.now())
	})
}

// BuildBackpack 組合完整背包；CMD 與網頁監控共用，確保顯示一致。
func BuildBackpack(ctx context.Context, t *db.Tx, farm *models.Farm, now time.Time) (Backpack, error) {
	stacks, err := inventory.ListItems(ctx, t, farm.ID)
	if err != nil {
		return Backpack{}, err
	}
	// 種子、道具、商品只有普通品質；農產品依 (代碼, 品質, 突變) 分開顯示
	quantities := map[string]int{}
	for _, i := range stacks {
		if i.Quality == "normal" && i.Mutation == "none" {
			quantities[i.ItemCode] = i.Quantity
		}
	}
	_, settings, err := generepo.Current(ctx, t)
	if err != nil {
		return Backpack{}, err
	}
	cropList, err := crops.ListAll(ctx, t, false)
	if err != nil {
		return Backpack{}, err
	}
	produceMap, err := crops.ProduceMap(ctx, t)
	if err != nil {
		return Backpack{}, err
	}
	bp := Backpack{PlayerID: farm.PlayerID, Capacity: farm.WarehouseCapacity, Now: now,
		MaterialCapacity: exploration.MaterialBagCapacity}
	for _, c := range cropList {
		if n := quantities[rules.SeedItemCode(c.Code)]; n != 0 {
			bp.Seeds = append(bp.Seeds, line(c.Code, c.Name+"種子", n, c.SeedPrice, ""))
		}
	}
	for _, i := range stacks {
		entry, ok := produceMap[i.ItemCode]
		if !ok {
			continue
		}
		c := entry.Crop
		l := line(i.ItemCode, genetics.ProduceName(c.Name, c.VariantName, i.Mutation), i.Quantity,
			genetics.UnitPrice(c.SellPrice, i.Quality, i.Mutation, settings), "")
		l.Quality, l.Mutation = i.Quality, i.Mutation
		bp.Produce = append(bp.Produce, l)
	}
	tools, err := items.ListTools(ctx, t, farm.ID)
	if err != nil {
		return Backpack{}, err
	}
	owned := map[string]*models.FarmTool{}
	for i := range tools {
		owned[tools[i].ToolCode] = &tools[i]
	}
	for _, code := range balance.ItemOrder {
		it := balance.Items[code]
		switch it.Kind {
		case "consumable":
			if n := quantities[code]; n != 0 {
				bp.Consumables = append(bp.Consumables, line(code, it.Name, n, it.Price, it.Desc))
			}
		case "tool":
			ts := ToolStatus{Code: code, Name: it.Name, Description: it.Desc}
			if tool := owned[code]; tool != nil {
				acquired := tool.AcquiredAt
				ts.Owned, ts.Enabled, ts.AcquiredAt = true, tool.Enabled, &acquired
			}
			bp.Tools = append(bp.Tools, ts)
		}
	}
	effect, err := items.GetEffect(ctx, t, farm.ID, balance.EffectEarthworm)
	if err != nil {
		return Backpack{}, err
	}
	es := EffectStatus{Code: balance.EffectEarthworm, Name: balance.Items[balance.ItemEarthworm].Name, Status: "inactive",
		Description: fmt.Sprintf("澆水的作物生長時間 ×%v", balance.EarthwormGrowMultiplier)}
	if effect != nil {
		es.Status = "expired"
		if !now.Before(effect.StartedAt) && now.Before(effect.ExpiresAt) {
			es.Status = "active"
			es.RemainingSeconds = rules.RemainingSeconds(&effect.ExpiresAt, now)
		}
		started, expires, mult := effect.StartedAt, effect.ExpiresAt, effect.Multiplier
		es.StartedAt, es.ExpiresAt, es.Multiplier = &started, &expires, &mult
	}
	bp.Effects = []EffectStatus{es}
	animalList, err := animals.ListAll(ctx, t, false)
	if err != nil {
		return Backpack{}, err
	}
	for _, a := range animalList {
		if n := quantities[a.ProductCode]; n != 0 {
			bp.Goods = append(bp.Goods, line(a.ProductCode, a.ProductName, n, a.ProductPrice, ""))
		}
	}
	for _, code := range balance.GoodOrder {
		if n := quantities[code]; n != 0 {
			g := balance.Goods[code]
			bp.Goods = append(bp.Goods, line(code, g.Name, n, g.SellPrice, ""))
		}
	}
	slices.SortStableFunc(bp.Produce, func(a, b BackpackLine) int {
		if a.Code != b.Code {
			return cmpStr(a.Code, b.Code)
		}
		qa, qb := slices.Index(balance.QualityOrder, a.Quality), slices.Index(balance.QualityOrder, b.Quality)
		if qa != qb {
			return qa - qb
		}
		return cmpStr(a.Mutation, b.Mutation)
	})
	if bp.Prizes, err = prizeViews(ctx, t, farm, &settings); err != nil {
		return Backpack{}, err
	}
	for _, code := range exploration.MaterialOrder {
		if n := quantities[code]; n != 0 {
			m := exploration.Materials[code]
			price := 0
			if code == exploration.FakeCoin {
				price = exploration.FakeCoinValue
			}
			bp.Materials = append(bp.Materials, line(code, m.Name, n, price, m.Rarity+"・"+m.Usage))
		}
	}
	// 賭場獎品也佔倉庫空間；山洞素材、假幣與稀有種子放在素材背包，不佔倉庫
	for _, i := range stacks {
		if inventory.IsMaterial(i.ItemCode) {
			bp.MaterialUsed += i.Quantity
		} else {
			bp.Used += i.Quantity
		}
	}
	for _, p := range bp.Prizes {
		bp.Used += p.Quantity
	}
	return bp, nil
}

// cmpStr Python 字串比較（依 Unicode 碼位；Go 的位元組比較對 UTF-8 結果相同）。
func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ---------- 限時活動 ----------

// ActiveEvents 目前生效中的限時活動（含限定作物名稱）。
func (s *GameService) ActiveEvents(ctx context.Context) ([]ActiveEventView, error) {
	now := s.now()
	return inTx(ctx, s, func(t *db.Tx) ([]ActiveEventView, error) {
		all, err := crops.ListAll(ctx, t, false)
		if err != nil {
			return nil, err
		}
		names := map[int64]string{}
		for _, c := range all {
			names[c.ID] = c.Name
		}
		list, err := events.Active(ctx, t, now)
		if err != nil {
			return nil, err
		}
		out := make([]ActiveEventView, len(list))
		for i, e := range list {
			v := eventrules.View(e, now)
			var name *string
			if e.CropTypeID != nil {
				if n, ok := names[*e.CropTypeID]; ok {
					name = &n
				}
			}
			v["crop_name"] = name
			out[i] = ActiveEventView{View: v, CropName: name}
		}
		return out, nil
	})
}

// ---------- 作物品質與突變 ----------

// GeneticsSettings 目前的設定版本與設定。
func (s *GameService) GeneticsSettings(ctx context.Context) (int, balance.GeneticsSettings, error) {
	var version int
	settings, err := inTx(ctx, s, func(t *db.Tx) (balance.GeneticsSettings, error) {
		v, st, err := generepo.Current(ctx, t)
		version = v
		return st, err
	})
	return version, settings, err
}

// CropCatalog 各作物的基本資料、珍稀變種與各品質／突變的單價（依目前設定計算）。
func (s *GameService) CropCatalog(ctx context.Context) ([]CropCatalogEntry, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]CropCatalogEntry, error) {
		settings, err := s.geneticsSettings(ctx, t)
		if err != nil {
			return nil, err
		}
		all, err := crops.ListAll(ctx, t, false)
		if err != nil {
			return nil, err
		}
		rows := make([]CropCatalogEntry, len(all))
		for i := range all {
			c := &all[i]
			now := s.now()
			prices := map[string]map[string]int{}
			for _, q := range balance.QualityOrder {
				prices[q] = map[string]int{}
				for _, m := range []string{"none", "golden", "variant"} {
					prices[q][m] = genetics.UnitPrice(c.SellPrice, q, m, settings)
				}
			}
			rows[i] = CropCatalogEntry{
				CropInfo: cropInfo(c, &now), Active: c.Active, VariantCode: c.VariantCode, VariantName: c.VariantName,
				Prices:          prices,
				FastGrowSeconds: genetics.GrowSeconds(c.GrowSeconds, "fast", false, settings, 0),
				GiantQuantity:   genetics.HarvestQuantity(c.HarvestQuantity, "giant", settings, 0),
			}
		}
		return rows, nil
	})
}

// FindCropInfo 依作物或變種的代碼／名稱查詢。
func (s *GameService) FindCropInfo(ctx context.Context, key string) (CropCatalogEntry, error) {
	code, err := inTx(ctx, s, func(t *db.Tx) (string, error) {
		c, err := crops.FindVariant(ctx, t, key)
		if err != nil {
			return "", err
		}
		if c == nil {
			if c, err = s.crop(ctx, t, key, false); err != nil {
				return "", err
			}
		}
		return c.Code, nil
	})
	if err != nil {
		return CropCatalogEntry{}, err
	}
	list, err := s.CropCatalog(ctx)
	if err != nil {
		return CropCatalogEntry{}, err
	}
	for _, c := range list {
		if c.Code == code {
			return c, nil
		}
	}
	return CropCatalogEntry{}, gameerr.NotFoundf("沒有「%s」這種作物。", key) // Python next() 找不到時是 StopIteration；只在並行刪除時發生
}

// QualityTable 基礎品質機率、售價倍率，以及（指定玩家時）依農場等級加成後的實際機率。playerID 可為 nil。
func (s *GameService) QualityTable(ctx context.Context, playerID *int64) (QualityTable, error) {
	var level *int
	settings, err := inTx(ctx, s, func(t *db.Tx) (balance.GeneticsSettings, error) {
		settings, err := s.geneticsSettings(ctx, t)
		if err != nil || playerID == nil {
			return settings, err
		}
		farm, err := s.farmOf(ctx, t, *playerID)
		if err != nil {
			return settings, err
		}
		lv := farm.Level
		level = &lv
		return settings, nil
	})
	if err != nil {
		return QualityTable{}, err
	}
	qt := QualityTable{BaseBP: settings.QualityBP, PricePct: settings.QualityPricePct, Level: level,
		LevelBonusBP: settings.LevelBonusBP, LevelBonusMaxBP: settings.LevelBonusMaxBP}
	if level != nil && *level != 0 {
		qt.EffectiveBP = genetics.EffectiveQualityBP(settings, *level, 0)
	}
	return qt, nil
}

func (s *GameService) MutationTable(ctx context.Context) ([]MutationRow, error) {
	settings, err := inTx(ctx, s, func(t *db.Tx) (balance.GeneticsSettings, error) { return s.geneticsSettings(ctx, t) })
	if err != nil {
		return nil, err
	}
	effects := map[string]string{
		"giant":  fmt.Sprintf("收成數量 ×%d", settings.GiantMultiplier),
		"golden": fmt.Sprintf("基本售價 ×%s", pyG(float64(settings.GoldenPricePct)/100)),
		"fast": fmt.Sprintf("生長時間減少 %s（最短 %s，仍需澆水）", rules.FormatDuration(settings.FastReductionSeconds),
			rules.FormatDuration(balance.MinGrowSeconds)),
		"variant": fmt.Sprintf("長成獨立的珍稀變種作物，基本售價 = 原作物 ×%s", pyG(float64(settings.VariantPricePct)/100)),
	}
	rows := make([]MutationRow, len(genetics.MutationOrder))
	for i, m := range genetics.MutationOrder {
		rows[i] = MutationRow{Code: m, Name: balance.Mutations[m], BP: settings.MutationBP[m], Effect: effects[m]}
	}
	return rows, nil
}

// UpdateGenetics 驗證後新增一個設定版本；無效設定一律拒絕。只影響之後播種的作物與之後的販售單價。
func (s *GameService) UpdateGenetics(ctx context.Context, actor string, raw map[string]any) (int, error) {
	settings, err := genetics.ValidateSettings(raw)
	if err != nil {
		return 0, err
	}
	now := s.now()
	return inTx(ctx, s, func(t *db.Tx) (int, error) {
		oldVersion, old, err := generepo.Current(ctx, t)
		if err != nil {
			return 0, err
		}
		version, err := generepo.AddVersion(ctx, t, settings, actor, now)
		if err != nil {
			return 0, err
		}
		target := int64(version)
		return version, community.AddAdminAction(ctx, t, actor, "update_genetics", "crop_genetics", &target,
			map[string]any{"from_version": oldVersion, "before": old, "after": settings}, now)
	})
}

// UpdateCropGrowSeconds 調整作物生長時間；已澆水的作物成熟時間不變，之後澆水的作物使用新時間。
func (s *GameService) UpdateCropGrowSeconds(ctx context.Context, actor, cropCode string, seconds int) (CropInfo, error) {
	r := balance.CropGrowSecondsRange
	if seconds < r.Low || seconds > r.High {
		return CropInfo{}, gameerr.Validationf("生長時間必須在 %d–%d 秒之間。", r.Low, r.High)
	}
	now := s.now()
	return inTx(ctx, s, func(t *db.Tx) (CropInfo, error) {
		crop, err := crops.Find(ctx, t, cropCode)
		if err != nil {
			return CropInfo{}, err
		}
		if crop == nil {
			return CropInfo{}, gameerr.NotFoundf("找不到作物「%s」。", cropCode)
		}
		before := crop.GrowSeconds
		updated, err := crops.SetGrowSeconds(ctx, t, crop.Code, seconds)
		if err != nil {
			return CropInfo{}, err
		}
		target := crop.ID
		if err := community.AddAdminAction(ctx, t, actor, "update_crop_grow_time", "crop", &target,
			map[string]any{"crop": crop.Code, "before": before, "after": seconds}, now); err != nil {
			return CropInfo{}, err
		}
		n := s.now()
		return cropInfo(updated, &n), nil
	})
}
