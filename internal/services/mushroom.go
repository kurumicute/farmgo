package services

// 地下菇房（Go 版新增，2026-11-12；規則見 game/mushroom）。
//
// 菇床狀態存在 farm_tools（tool_code = mushroom.ToolCode）的 settings JSON：農場 Lv.4 以上第一次打開菇房時自動建立，
// 不用建造、不需要 Migration。投入木屑與收成都先鎖定農場列，再鎖菇房列，在同一筆交易中扣料、擲骰、發放與寫回狀態，
// 重複點擊或並行請求不會重複收成或重複扣料。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
)

// MushroomBedView 一個菇床。
type MushroomBedView struct {
	No        int
	StartedAt time.Time
	ReadyAt   time.Time
	Sawdust   int
	Ready     bool
	Progress  float64 // 0..1
	RareText  string  // 稀有菇機率說明
}

// MushroomNutrientView 一種養分（依 mushroom.NutrientOrder）。
type MushroomNutrientView struct {
	Code, Name           string
	Plots, Pct           int
	Common, Rare         string // 菇類代碼
	CommonName, RareName string
}

// MushroomCellarView 地下菇房畫面。Unlocked 為 false 時只有 Level、UnlockLevel 有意義。
type MushroomCellarView struct {
	Unlocked    bool
	Level       int
	UnlockLevel int
	Beds        []MushroomBedView
	Nutrients   []MushroomNutrientView // 只列出目前有的養分
	Planted     int                    // 上方農田有作物的格數
	Sawdust     int                    // 持有的木屑
	ReadyCount  int
	NextReadyAt *time.Time
	// CropNames 各養分目前對應的作物名稱（依 mushroom.NutrientOrder；管理網站可調整）
	CropNames map[string][]string
}

// cellarTool 讀取菇房列（直接查詢，不用 ListTools 的讀取快取，建立後同一筆交易內也讀得到）。
func cellarTool(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) (*models.FarmTool, error) {
	sql := "SELECT * FROM farm_tools WHERE farm_id = $1 AND tool_code = $2 ORDER BY id DESC LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FarmTool](ctx, t, sql, farmID, mushroom.ToolCode)
}

func mushroomLocked(farm *models.Farm) error {
	return gameerr.Validationf("地下菇房在農場 Lv.%d 開放（目前 Lv.%d）。", mushroom.Unlock(), farm.Level)
}

// lockedCellar 已鎖定農場後取得（必要時建立）菇房列並鎖定，回傳菇床。
func (r *RanchService) lockedCellar(ctx context.Context, t *db.Tx, farm *models.Farm, now time.Time) (*models.FarmTool, []mushroom.Bed, error) {
	if farm.Level < mushroom.Unlock() {
		return nil, nil, mushroomLocked(farm)
	}
	tool, err := cellarTool(ctx, t, farm.ID, true)
	if err != nil {
		return nil, nil, err
	}
	if tool == nil {
		beds := mushroom.NewBeds(now)
		if tool, err = items.AddTool(ctx, t, farm.ID, mushroom.ToolCode, now); err != nil {
			return nil, nil, err
		}
		return tool, beds, saveBeds(ctx, t, tool, beds)
	}
	beds, _ := mushroom.ParseBeds(tool.Settings, now)
	return tool, beds, nil
}

func saveBeds(ctx context.Context, t *db.Tx, tool *models.FarmTool, beds []mushroom.Bed) error {
	_, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("settings", mushroom.EncodeBeds(beds)))
	return err
}

// plantedCrops 上方農田有作物的格數對應的作物代碼（每格一個）。
func plantedCrops(ctx context.Context, t *db.Tx, farmID int64) ([]string, error) {
	plots, err := farms.ListPlots(ctx, t, farmID, false)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range plots {
		if p.Crop != nil {
			out = append(out, p.Crop.Code)
		}
	}
	return out, nil
}

// MushroomCellar 地下菇房畫面。Lv.4 以上第一次打開時建立菇房（4 個菇床同時開始生長）。
func (r *RanchService) MushroomCellar(ctx context.Context, playerID int64) (MushroomCellarView, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (MushroomCellarView, error) {
		farm, err := r.farmOf(ctx, t, playerID)
		if err != nil {
			return MushroomCellarView{}, err
		}
		v := MushroomCellarView{Level: farm.Level, UnlockLevel: mushroom.Unlock()}
		if farm.Level < mushroom.Unlock() {
			return v, nil
		}
		now := r.now()
		tool, err := cellarTool(ctx, t, farm.ID, false)
		if err != nil {
			return v, err
		}
		var beds []mushroom.Bed
		if tool == nil { // 第一次打開：鎖農場後建立（並行打開時只會建立一次）
			locked, err := r.lockedFarm(ctx, t, playerID)
			if err != nil {
				return v, err
			}
			if _, beds, err = r.lockedCellar(ctx, t, locked, now); err != nil {
				return v, err
			}
		} else {
			beds, _ = mushroom.ParseBeds(tool.Settings, now)
		}
		v.Unlocked = true
		for i, b := range beds {
			bv := MushroomBedView{No: i + 1, StartedAt: b.StartedAt, ReadyAt: b.ReadyAt(), Sawdust: b.Sawdust,
				Ready: b.Ready(now), Progress: b.Progress(now), RareText: mushroom.RareChanceText(b.Sawdust)}
			if bv.Ready {
				v.ReadyCount++
			} else if v.NextReadyAt == nil || bv.ReadyAt.Before(*v.NextReadyAt) {
				at := bv.ReadyAt
				v.NextReadyAt = &at
			}
			v.Beds = append(v.Beds, bv)
		}
		crops, err := plantedCrops(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		v.Planted = len(crops)
		if all, err := catalog.ItemNames(ctx, t); err == nil {
			v.CropNames = map[string][]string{}
			for code, list := range mushroom.Current().NutrientCrops {
				for _, c := range list {
					name := c
					if n, ok := all[c]; ok {
						name = n
					}
					v.CropNames[code] = append(v.CropNames[code], name)
				}
			}
		}
		mix := mushroom.Mix(crops)
		pct := mushroom.MixPct(mix)
		for _, code := range mushroom.NutrientOrder {
			if mix[code] == 0 {
				continue
			}
			n := mushroom.Nutrients[code]
			v.Nutrients = append(v.Nutrients, MushroomNutrientView{code, n.Name, mix[code], pct[code], n.Common, n.Rare,
				mushroom.SpeciesName(n.Common), mushroom.SpeciesName(n.Rare)})
		}
		if v.Sawdust, err = inventory.GetQuantity(ctx, t, farm.ID, exploration.Sawdust, "normal", "none"); err != nil {
			return v, err
		}
		return v, nil
	})
}

// AddSawdust 在菇床投入木屑。bedNo = 0 表示依序把每個菇床補到上限（木屑不夠時能補多少補多少）。
func (r *RanchService) AddSawdust(ctx context.Context, playerID int64, bedNo, quantity int) (ActionResult, error) {
	if bedNo < 0 || bedNo > mushroom.BedCount {
		return ActionResult{}, gameerr.Validationf("菇床編號必須是 1–%d。", mushroom.BedCount)
	}
	if bedNo > 0 && (quantity <= 0 || quantity > mushroom.MaxSawdust) {
		return ActionResult{}, gameerr.Validationf("木屑數量必須是 1–%d。", mushroom.MaxSawdust)
	}
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := r.now()
		tool, beds, err := r.lockedCellar(ctx, t, farm, now)
		if err != nil {
			return ActionResult{}, err
		}
		have, err := inventory.GetQuantity(ctx, t, farm.ID, exploration.Sawdust, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		add := make([]int, len(beds))
		total := 0
		if bedNo > 0 {
			b := beds[bedNo-1]
			if b.Sawdust+quantity > mushroom.MaxSawdust {
				return ActionResult{}, gameerr.Validationf("菇床 #%d 這一批已投入 %d 個木屑，最多 %d 個。", bedNo, b.Sawdust, mushroom.MaxSawdust)
			}
			add[bedNo-1], total = quantity, quantity
		} else {
			left := have
			for i, b := range beds {
				n := min(left, mushroom.MaxSawdust-b.Sawdust)
				add[i] = n
				left -= n
				total += n
			}
			if total == 0 && have > 0 {
				return ActionResult{}, gameerr.Validationf("每個菇床這一批都已投入 %d 個木屑了。", mushroom.MaxSawdust)
			}
		}
		if have < total || total == 0 {
			need := max(total, 1)
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems,
				"木屑不足：需要 %d 個、只有 %d 個。木屑可以在每次探險取得（古老森林最多）。", need, have)
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, exploration.Sawdust, total, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "木屑不足。")
		}
		var parts []string
		for i := range beds {
			if add[i] == 0 {
				continue
			}
			beds[i].Sawdust += add[i]
			parts = append(parts, fmt.Sprintf("#%d +%d（%d/%d）", i+1, add[i], beds[i].Sawdust, mushroom.MaxSawdust))
		}
		if err := saveBeds(ctx, t, tool, beds); err != nil {
			return ActionResult{}, err
		}
		text := "地下菇房投入木屑：" + strings.Join(parts, "、")
		if err := r.record(ctx, t, farm, rules.EvCraftStart, truncRunes(text, 200),
			rec{ItemCode: exploration.Sawdust, Quantity: total}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("%s，共 %d 個木屑。", text, total), farm.Gold, 0, exploration.Sawdust, total), nil
	})
}

// HarvestMushrooms 收成所有長好的菇床：依上方農田目前的作物與投入的木屑擲骰，菇類放進倉庫，菇床立即開始下一批（木屑歸零）。
// 倉庫放不下全部時，依菇床編號收成放得下的部分；一個都放不下時不收成。
func (r *RanchService) HarvestMushrooms(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := r.now()
		tool, beds, err := r.lockedCellar(ctx, t, farm, now)
		if err != nil {
			return ActionResult{}, err
		}
		var ready []int
		var next *time.Time
		for i, b := range beds {
			if b.Ready(now) {
				ready = append(ready, i)
			} else if at := b.ReadyAt(); next == nil || at.Before(*next) {
				next = &at
			}
		}
		if len(ready) == 0 {
			return ActionResult{}, gameerr.Validationf("菇床還沒長好，還需要 %s。",
				rules.FormatDuration(rules.RemainingSeconds(next, now)))
		}
		// 倉庫空間：能收幾個菇床就收幾個
		fit := 0
		var spaceErr error
		for fit < len(ready) {
			if err := r.ensureSpace(ctx, t, farm, (fit+1)*mushroom.Yield()); err != nil {
				spaceErr = err
				break
			}
			fit++
		}
		if fit == 0 {
			if gameerr.Is(spaceErr, gameerr.WarehouseFull) {
				return ActionResult{}, gameerr.New(gameerr.WarehouseFull, "%s菇會留在菇床上，清出空間後再收成。", spaceErr.Error())
			}
			return ActionResult{}, spaceErr
		}
		crops, err := plantedCrops(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		mix := mushroom.Mix(crops)
		got := map[string]int{}
		rare := 0
		for _, i := range ready[:fit] {
			for _, code := range mushroom.Roll(mix, beds[i].Sawdust, mushroom.Yield(), r.MushroomRng) {
				got[code]++
				if mushroom.SpeciesMap[code].Rare {
					rare++
				}
			}
			beds[i] = mushroom.Bed{StartedAt: now} // 立即開始下一批，木屑歸零
		}
		if err := saveBeds(ctx, t, tool, beds); err != nil {
			return ActionResult{}, err
		}
		var parts []string
		total := 0
		for _, code := range mushroom.SpeciesOrder {
			n := got[code]
			if n == 0 {
				continue
			}
			if err := inventory.Add(ctx, t, farm.ID, code, n, "normal", "none"); err != nil {
				return ActionResult{}, err
			}
			// 每種菇各一筆交易紀錄（craft_collect 會登記圖鑑）
			if err := r.record(ctx, t, farm, rules.EvCraftCollect, fmt.Sprintf("地下菇房收成 %d 個%s", n, mushroom.SpeciesName(code)),
				rec{ItemCode: code, Quantity: n}); err != nil {
				return ActionResult{}, err
			}
			parts = append(parts, fmt.Sprintf("%s ×%d", mushroom.SpeciesName(code), n))
			total += n
		}
		msg := fmt.Sprintf("🍄 收成 %d 個菇床：%s", fit, strings.Join(parts, "、"))
		if rare > 0 {
			msg += fmt.Sprintf("（✨ 稀有菇 %d 朵）", rare)
		}
		msg += "。菇床已開始長下一批。"
		if fit < len(ready) {
			msg += fmt.Sprintf("倉庫空間不足，還有 %d 個菇床沒收成。", len(ready)-fit)
		}
		return result(msg, farm.Gold, 0, "", total), nil
	})
}
