package services

// 種子實驗室（Go 版新增，2026-11-13；規則見 game/seedlab，資料表見 Migration 0039）。
//
// 所有操作都先鎖定農場列（分享時依 farm_id 由小到大鎖定雙方），再鎖定實驗室列，在同一筆交易中檢查、扣料、擲骰與寫入，
// 重複點擊或並行請求不會重複扣料、重複領取或超過每日分享上限。研究槽在打開實驗室或領取時才判斷是否完成，不需要背景排程。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/game/seedlab"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/codex"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	labrepo "farmgm/internal/repositories/seedlab"
)

// LabSeedView 一顆特殊種子。
type LabSeedView struct {
	ID           int64
	CropCode     string
	CropName     string
	Genes        seedlab.Genes
	Generation   int
	Mutation     string // "none" = 沒有
	MutationName string // 沒有突變時為 ""
	Busy         bool   // 培育中
	Hybrid       bool   // 雜交作物
	OffSeason    string // 現在不是這個作物的季節時，說明為什麼不能種（可以種時為 ""）
}

// Label 例如「紫莓 第 3 代 ✨夜光作物」。
func (v LabSeedView) Label() string {
	s := fmt.Sprintf("%s 第 %d 代", v.CropName, v.Generation)
	if v.MutationName != "" {
		s += " ✨" + v.MutationName
	}
	return s
}

// ---------- 擴充（2026-11-15，Go 版新增） ----------

// ExpandSeedLibrary 種子庫擴充：每級增加 LibraryStep 格，價格逐級提高（管理網站調整）。
func (s *GameService) ExpandSeedLibrary(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lab, err := s.lockedLab(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		before := seedlab.LibraryCap(lab.LibraryLevel)
		price, ok := seedlab.NextLibrary(lab.LibraryLevel)
		if !ok {
			return ActionResult{}, gameerr.Validationf("種子庫已經擴充到最大的 %d 格了。", before)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：種子庫擴充需要 %d，目前只有 %d。", price, farm.Gold)
		}
		farm.Gold -= price
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		lab.LibraryLevel++
		if _, err := t.Exec(ctx, "UPDATE seed_labs SET library_level = $2 WHERE farm_id = $1", farm.ID, lab.LibraryLevel); err != nil {
			return ActionResult{}, err
		}
		after := seedlab.LibraryCap(lab.LibraryLevel)
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("種子庫擴充：%d → %d 格，金幣 -%d", before, after, price),
			rec{ItemCode: "seed_library", Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		tail := "已經擴充到最大了。"
		if p, ok := seedlab.NextLibrary(lab.LibraryLevel); ok {
			tail = fmt.Sprintf("下一次擴充需要 %s 金幣。", numfmt.Comma(p))
		}
		return result(fmt.Sprintf("🌰 種子庫擴充：%d → **%d** 格，花費 %s 金幣。%s", before, after, numfmt.Comma(price), tail),
			farm.Gold, -price, "", 0), nil
	})
}

// BuyLabSlot 研究槽擴充：研究等級 Lv.3 後才能買，價格在管理網站調整。
func (s *GameService) BuyLabSlot(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lab, err := s.lockedLab(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		if lab.Level < seedlab.MaxLevel {
			return ActionResult{}, gameerr.Validationf("研究等級到 Lv.%d 才能擴充研究槽（目前 Lv.%d）。", seedlab.MaxLevel, lab.Level)
		}
		before := seedlab.SlotsFor(lab.Level, lab.ExtraSlots)
		price, ok := seedlab.NextExtraSlot(lab.ExtraSlots)
		if !ok {
			return ActionResult{}, gameerr.Validationf("研究槽已經擴充到最多的 %d 個了。", before)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：研究槽擴充需要 %d，目前只有 %d。", price, farm.Gold)
		}
		farm.Gold -= price
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		lab.ExtraSlots++
		if _, err := t.Exec(ctx, "UPDATE seed_labs SET extra_slots = $2 WHERE farm_id = $1", farm.ID, lab.ExtraSlots); err != nil {
			return ActionResult{}, err
		}
		after := seedlab.SlotsFor(lab.Level, lab.ExtraSlots)
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("研究槽擴充：%d → %d 個，金幣 -%d", before, after, price),
			rec{ItemCode: "lab_slot", Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🔬 研究槽擴充：%d → **%d** 個，花費 %s 金幣。", before, after, numfmt.Comma(price)),
			farm.Gold, -price, "", 0), nil
	})
}

// LabJobView 研究槽中的工作。
type LabJobView struct {
	Slot       int
	Kind       string // cross／mutate／clone
	Mother     LabSeedView
	Father     *LabSeedView // 突變研究為 nil
	ResultName string       // 雜交成功時的品種
	SuccessPct float64      // 成功率（%）
	Unknown    bool         // 未發現的配方（ResultName 是「結果未知」，SuccessPct 不顯示）
	Batches    int          // 育苗排程的次數（其他工作為 0）
	Materials  string       // 使用的材料說明
	ReadyAt    time.Time
	Ready      bool
	Progress   float64 // 0..1
}

// LabIdentifyOption 可以鑑定的一般種子。
type LabIdentifyOption struct {
	CropCode, Name string
	Have, Fee      int
	Rare           bool
}

// LabShareTargetView 可以分享的對象。
type LabShareTargetView struct {
	PlayerID   int64
	PlayerName string
	FarmName   string
}

// SeedLabView 種子實驗室畫面。種子庫、空地與分享紀錄不論是否建造都會讀取（沒有實驗室也能收分享的種子並種下）；
// 研究等級、研究槽與可鑑定的種子只有建造後才有。
type SeedLabView struct {
	Unlocked    bool
	Built       bool
	Level       int // 農場等級
	UnlockLevel int
	Gold        int
	BuildGold   int

	LabLevel, Exp, NextExp, Slots int
	Jobs                          []LabJobView
	FreeSlots                     int
	ReadyCount                    int
	NextReadyAt                   *time.Time
	Seeds                         []LabSeedView // 種子庫（含培育中）
	Capacity                      int
	Identify                      []LabIdentifyOption
	Materials                     map[string]int // 培育材料持有量（樹脂、藥草、水晶、古代核心）
	EmptyPlots                    []int          // 可以種下特殊種子的空地
	SentToday                     int
	ReceivedFrom                  []string // 今天分享給自己的玩家
	MaterialNames                 map[string]string
	CropNames                     map[string]string // 作物代碼 → 名稱（雜交表用）
	// KnownHybrids 已發現的雜交配方（雜交作物代碼；取得過這種雜交作物的種子才算發現，2026-11-14 配方隱藏）
	KnownHybrids map[string]bool
	// 擴充（2026-11-15）：目前等級與下一級的價格（0 = 已滿級或還不能買；研究槽要研究 Lv.3）
	LibraryLevel, ExtraSlots        int
	NextLibraryPrice, NextSlotPrice int
	Now                             time.Time // 讀取的時間（面板計算剩餘時間用）
}

// LabRecordView 一筆培育紀錄。
type LabRecordView struct {
	Kind       string // identify／cross／mutate
	CreatedAt  time.Time
	Text       string
	Success    bool
	Generation int
}

// labMaterialOrder 實驗室用到的素材（畫面顯示順序）。
// 加速沙漏（2026-11-15）也一起讀，實驗室頁的研究槽可以直接使用。
var labMaterialOrder = []string{seedlab.MatCrystal, seedlab.MatResin, seedlab.MatHerb, seedlab.MatAncientCore, balance.ItemHourglass}

func labLocked(farm *models.Farm) error {
	return gameerr.Validationf("種子實驗室在農場 Lv.%d 開放（目前 Lv.%d）。", seedlab.Unlock(), farm.Level)
}

// lockedLab 已鎖定農場後取得並鎖定實驗室；還沒建造時回傳錯誤。
func (s *GameService) lockedLab(ctx context.Context, t *db.Tx, farm *models.Farm) (*models.SeedLab, error) {
	if farm.Level < seedlab.Unlock() {
		return nil, labLocked(farm)
	}
	lab, err := labrepo.Get(ctx, t, farm.ID, true)
	if err != nil {
		return nil, err
	}
	if lab == nil {
		return nil, gameerr.Validationf("還沒有建造種子實驗室。")
	}
	return lab, nil
}

// cropMap 全部作物（代碼 → 作物）。
func (s *GameService) cropMap(ctx context.Context, t *db.Tx) (map[string]*models.CropType, error) {
	all, err := crops.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*models.CropType, len(all))
	for i := range all {
		m[all[i].Code] = &all[i]
	}
	return m, nil
}

func seedView(seed *models.LabSeed, cropsByCode map[string]*models.CropType) LabSeedView {
	name := seed.CropCode
	if c := cropsByCode[seed.CropCode]; c != nil {
		name = c.Name
	}
	v := LabSeedView{ID: seed.ID, CropCode: seed.CropCode, CropName: name, Genes: labrepo.GenesOf(seed),
		Generation: seed.Generation, Mutation: seed.Mutation, Busy: seed.Status == models.LabSeedBusy,
		Hybrid: seedlab.IsHybrid(seed.CropCode)}
	if seed.Mutation != "none" {
		v.MutationName = balance.Mutations[seed.Mutation]
	}
	return v
}

func snapView(sn labrepo.Snapshot, cropsByCode map[string]*models.CropType) LabSeedView {
	return seedView(&models.LabSeed{ID: sn.ID, CropCode: sn.CropCode, GeneGrowth: sn.Genes.Growth, GeneYield: sn.Genes.Yield,
		GeneQuality: sn.Genes.Quality, GeneMutation: sn.Genes.Mutation, Generation: sn.Generation, Mutation: sn.Mutation}, cropsByCode)
}

func materialText(m map[string]any, names map[string]string) string {
	var parts []string
	for _, code := range []string{seedlab.MatResin, seedlab.MatHerb, seedlab.MatAncientCore} {
		if n, ok := toInt(m[code]); ok && n > 0 {
			parts = append(parts, fmt.Sprintf("%s ×%d", names[code], n))
		}
	}
	if gene, _ := m["catalyst"].(string); gene != "" {
		parts = append(parts, fmt.Sprintf("%s（催化%s基因）", names[seedlab.MatCrystal], seedlab.GeneNames[gene]))
	}
	if n, ok := toInt(m["mutate_crystal"]); ok && n > 0 {
		parts = append(parts, fmt.Sprintf("%s ×%d", names[seedlab.MatCrystal], n))
	}
	if n, ok := toInt(m["clone_fee"]); ok && n > 0 {
		parts = append(parts, fmt.Sprintf("%d 金幣", n))
	}
	return strings.Join(parts, "、")
}

// toInt JSON 讀回的數字（float64）或程式內的 int。
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func taipeiDay(now time.Time) time.Time { return now.In(clock.TaipeiTZ) }

// SeedLab 種子實驗室畫面。
func (s *GameService) SeedLab(ctx context.Context, playerID int64) (SeedLabView, error) {
	return inTx(ctx, s, func(t *db.Tx) (SeedLabView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return SeedLabView{}, err
		}
		cfg := seedlab.Current()
		v := SeedLabView{Level: farm.Level, UnlockLevel: cfg.UnlockLevel, Gold: farm.Gold, BuildGold: cfg.BuildGold,
			Capacity: cfg.LibraryCapacity, Materials: map[string]int{}}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return v, err
		}
		v.MaterialNames = names
		for _, code := range labMaterialOrder {
			if v.Materials[code], err = inventory.GetQuantity(ctx, t, farm.ID, code, "normal", "none"); err != nil {
				return v, err
			}
		}
		cropsByCode, err := s.cropMap(ctx, t)
		if err != nil {
			return v, err
		}
		v.CropNames = make(map[string]string, len(cropsByCode))
		for code, c := range cropsByCode {
			v.CropNames[code] = c.Name
		}
		seeds, err := labrepo.ListSeeds(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		byID := map[int64]*models.LabSeed{}
		now := s.now()
		for i := range seeds {
			byID[seeds[i].ID] = &seeds[i]
			sv := seedView(&seeds[i], cropsByCode)
			if c := cropsByCode[seeds[i].CropCode]; c != nil && !seasons.Available(c.Seasons, c.HourStart, c.HourEnd, now) {
				sv.OffSeason = fmt.Sprintf("%s是%s的作物，現在不能種植（%s）", c.Name,
					seasons.Describe(c.Seasons, c.HourStart, c.HourEnd), seasons.NowText(now))
			}
			v.Seeds = append(v.Seeds, sv)
		}
		plots, err := farms.ListPlots(ctx, t, farm.ID, false)
		if err != nil {
			return v, err
		}
		for _, p := range plots {
			if p.CropTypeID == nil {
				v.EmptyPlots = append(v.EmptyPlots, p.PlotNumber)
			}
		}
		day := taipeiDay(now)
		if v.SentToday, err = labrepo.SentCount(ctx, t, farm.ID, day); err != nil {
			return v, err
		}
		senders, err := labrepo.SendersTo(ctx, t, farm.ID, day)
		if err != nil {
			return v, err
		}
		for _, id := range senders {
			if f, err := farms.GetByID(ctx, t, id, false); err == nil && f != nil {
				v.ReceivedFrom = append(v.ReceivedFrom, f.Name)
			}
		}
		v.Unlocked = farm.Level >= cfg.UnlockLevel
		lab, err := labrepo.Get(ctx, t, farm.ID, false)
		if err != nil || lab == nil {
			return v, err
		}
		v.Built, v.Now = true, s.now()
		v.LabLevel, v.Exp, v.NextExp, v.Slots = lab.Level, lab.Exp, seedlab.NextLevelExp(lab.Level), seedlab.SlotsFor(lab.Level, lab.ExtraSlots)
		v.Capacity = seedlab.LibraryCap(lab.LibraryLevel)
		v.LibraryLevel, v.ExtraSlots = lab.LibraryLevel, lab.ExtraSlots
		v.NextLibraryPrice, _ = seedlab.NextLibrary(lab.LibraryLevel)
		if lab.Level >= seedlab.MaxLevel {
			v.NextSlotPrice, _ = seedlab.NextExtraSlot(lab.ExtraSlots)
		}
		if v.KnownHybrids, err = knownHybrids(ctx, t, farm.ID); err != nil {
			return v, err
		}
		jobs, err := labrepo.OpenJobs(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		for _, j := range jobs {
			jv, err := s.jobView(ctx, t, j, byID, cropsByCode, names, v.KnownHybrids, now)
			if err != nil {
				return v, err
			}
			if jv.Ready {
				v.ReadyCount++
			} else if v.NextReadyAt == nil || jv.ReadyAt.Before(*v.NextReadyAt) {
				at := jv.ReadyAt
				v.NextReadyAt = &at
			}
			v.Jobs = append(v.Jobs, jv)
		}
		v.FreeSlots = max(0, v.Slots-len(jobs))
		// 可以鑑定的一般種子（雜交作物沒有一般種子）
		items, err := inventory.ListItems(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		for _, it := range items {
			code, ok := strings.CutPrefix(it.ItemCode, "seed_")
			if !ok || it.Quantity <= 0 || it.Quality != "normal" || it.Mutation != "none" {
				continue
			}
			c := cropsByCode[code]
			if c == nil || seedlab.IsHybrid(code) {
				continue
			}
			v.Identify = append(v.Identify, LabIdentifyOption{code, c.Name, it.Quantity, seedlab.IdentifyFee(c.SeedPrice), !c.ShopBuyable})
		}
		sort.Slice(v.Identify, func(i, j int) bool { return v.Identify[i].Fee < v.Identify[j].Fee })
		return v, nil
	})
}

// LabCodexPrefix 特殊種子圖鑑的代碼前綴（farm_codex.item_code = "labseed_<作物代碼>"，不會和物品代碼重複）。
const LabCodexPrefix = "labseed_"

// registerLabCodex 登記特殊種子圖鑑（取得一顆某作物的特殊種子：鑑定、雜交、收成取回、收到分享）。
func (s *GameService) registerLabCodex(ctx context.Context, t *db.Tx, farmID int64, cropCode string, now time.Time) error {
	return codex.Register(ctx, t, farmID, LabCodexPrefix+cropCode, 1, "normal", now)
}

// knownHybrids 已發現的雜交配方：特殊種子圖鑑登記過的雜交作物（雜交成功、收成取回或收到分享都算）。
// 配方隱藏（2026-11-14 使用者要求）：沒發現的組合在面板只顯示「❔」，要自己把不同品種配對試驗。
func knownHybrids(ctx context.Context, t *db.Tx, farmID int64) (map[string]bool, error) {
	codes := make([]string, len(seedlab.Hybrids))
	for i, h := range seedlab.Hybrids {
		codes[i] = LabCodexPrefix + h.Code
	}
	rows, err := db.QueryValues[string](ctx, t,
		"SELECT item_code FROM farm_codex WHERE farm_id = $1 AND item_code = ANY($2)", farmID, codes)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(rows))
	for _, code := range rows {
		known[strings.TrimPrefix(code, LabCodexPrefix)] = true
	}
	return known, nil
}

// CrossKnown 這次配對的結果玩家知不知道：同品種（改良基因）一定知道；不同品種要已發現該雜交配方。
// 不在雜交表中的組合也回傳 false，畫面上和沒發現的配方一樣顯示「結果未知」，不洩漏哪些組合可以雜交。
func CrossKnown(mother, father string, known map[string]bool) bool {
	if mother == father {
		return true
	}
	h := seedlab.FindHybrid(mother, father)
	return h != nil && known[h.Code]
}

func (s *GameService) jobView(ctx context.Context, t *db.Tx, j models.LabJob, byID map[int64]*models.LabSeed,
	cropsByCode map[string]*models.CropType, names map[string]string, known map[string]bool, now time.Time) (LabJobView, error) {
	seedOf := func(id int64) (*models.LabSeed, error) {
		if s, ok := byID[id]; ok {
			return s, nil
		}
		return labrepo.GetSeed(ctx, t, id, false)
	}
	mother, err := seedOf(j.MotherSeedID)
	if err != nil || mother == nil {
		return LabJobView{}, fmt.Errorf("研究槽 #%d 的種子 %d 不存在：%w", j.Slot, j.MotherSeedID, err)
	}
	jv := LabJobView{Slot: j.Slot, Kind: j.Kind, Mother: seedView(mother, cropsByCode), ReadyAt: j.ReadyAt,
		Ready: !now.Before(j.ReadyAt), Materials: materialText(j.Materials, names)}
	total := j.ReadyAt.Sub(j.StartedAt).Seconds()
	if total > 0 {
		jv.Progress = math.Min(1, math.Max(0, now.Sub(j.StartedAt).Seconds()/total))
	}
	if jv.Ready {
		jv.Progress = 1
	}
	if j.Kind == "cross" && j.FatherSeedID != nil {
		father, err := seedOf(*j.FatherSeedID)
		if err != nil || father == nil {
			return LabJobView{}, fmt.Errorf("研究槽 #%d 的種子 %d 不存在：%w", j.Slot, *j.FatherSeedID, err)
		}
		fv := seedView(father, cropsByCode)
		jv.Father = &fv
		resin, _ := toInt(j.Materials[seedlab.MatResin])
		code, bp, _ := seedlab.CrossPlan{Mother: mother.CropCode, Father: father.CropCode, Resin: resin > 0}.Result()
		jv.ResultName, jv.SuccessPct = code, float64(bp)/100
		if c := cropsByCode[code]; c != nil {
			jv.ResultName = c.Name
		}
		if !CrossKnown(mother.CropCode, father.CropCode, known) { // 未發現的配方：結果與成功率都不顯示
			jv.ResultName, jv.SuccessPct, jv.Unknown = "❔ 結果未知", 0, true
		}
	} else if j.Kind == "clone" {
		jv.SuccessPct, jv.Batches = 100, cloneBatches(j.Materials)
	} else {
		core, _ := toInt(j.Materials[seedlab.MatAncientCore])
		jv.SuccessPct = float64(seedlab.MutateChanceBP(mother.GeneMutation, core > 0)) / 100
	}
	return jv, nil
}

// removeMaterials 扣除素材；不足時回傳錯誤並列出缺少的項目（不會扣除任何東西）。
func (s *GameService) removeMaterials(ctx context.Context, t *db.Tx, farm *models.Farm, need map[string]int, order []string) error {
	names, err := catalog.ItemNames(ctx, t)
	if err != nil {
		return err
	}
	var lacking []string
	for _, code := range order {
		n := need[code]
		if n <= 0 {
			continue
		}
		have, err := inventory.GetQuantity(ctx, t, farm.ID, code, "normal", "none")
		if err != nil {
			return err
		}
		if have < n {
			lacking = append(lacking, fmt.Sprintf("%s %d／%d", names[code], have, n))
		}
	}
	if len(lacking) > 0 {
		return gameerr.New(gameerr.InsufficientItems, "素材不足：%s。", strings.Join(lacking, "、"))
	}
	for _, code := range order {
		if n := need[code]; n > 0 {
			ok, err := inventory.Remove(ctx, t, farm.ID, code, n, "normal", "none")
			if err != nil {
				return err
			}
			if !ok {
				return gameerr.New(gameerr.InsufficientItems, "%s不足。", names[code])
			}
		}
	}
	return nil
}

// BuildSeedLab 建造種子實驗室（農場 Lv.5，金幣與木材、石塊、水晶）。
func (s *GameService) BuildSeedLab(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.Level < seedlab.Unlock() {
			return ActionResult{}, labLocked(farm)
		}
		lab, err := labrepo.Get(ctx, t, farm.ID, true)
		if err != nil {
			return ActionResult{}, err
		}
		if lab != nil {
			return ActionResult{}, gameerr.Validationf("已經建造過種子實驗室了。")
		}
		cost := seedlab.Current().BuildGold
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：需要 %d，目前只有 %d。", cost, farm.Gold)
		}
		farm.Gold -= cost
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := labrepo.Create(ctx, t, farm.ID, s.now()); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvSeedLab, fmt.Sprintf("建造種子實驗室，金幣 -%d", cost),
			rec{GoldDelta: -cost}); err != nil {
			return ActionResult{}, err
		}
		return result("🧪 種子實驗室建造完成！先把背包裡的一般種子拿去鑑定，就能開始培育。", farm.Gold, -cost, "", 0), nil
	})
}

// libraryFull 種子庫已滿時的錯誤。
func (s *GameService) checkLibrary(ctx context.Context, t *db.Tx, farmID int64, incoming int, who string) error {
	n, err := labrepo.CountLibrary(ctx, t, farmID)
	if err != nil {
		return err
	}
	capacity, err := s.libraryCap(ctx, t, farmID)
	if err != nil {
		return err
	}
	if n+incoming > capacity {
		return gameerr.Validationf("%s種子庫已滿（%d／%d），請先種下或分享一些種子。", who, n, capacity)
	}
	return nil
}

// libraryCap 這座農場的種子庫容量（含擴充；沒有實驗室時用基本上限）。
func (s *GameService) libraryCap(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	lab, err := labrepo.Get(ctx, t, farmID, false)
	if err != nil || lab == nil {
		return seedlab.LibraryCap(0), err
	}
	return seedlab.LibraryCap(lab.LibraryLevel), nil
}

func (s *GameService) addLabExp(ctx context.Context, t *db.Tx, lab *models.SeedLab, exp int) (string, error) {
	before := lab.Level
	level, err := labrepo.AddExp(ctx, t, lab, exp)
	if err != nil {
		return "", err
	}
	if level > before {
		return fmt.Sprintf(" 🎉 研究等級提升到 Lv.%d，研究槽增加為 %d 個！", level, seedlab.SlotsFor(level, lab.ExtraSlots)), nil
	}
	return "", nil
}

// IdentifySeed 鑑定一顆一般種子，變成有基因的特殊種子（扣 1 顆種子與鑑定費）。
func (s *GameService) IdentifySeed(ctx context.Context, playerID int64, cropKey string) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lab, err := s.lockedLab(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		crop, err := s.crop(ctx, t, cropKey, false)
		if err != nil {
			return ActionResult{}, err
		}
		if seedlab.IsHybrid(crop.Code) {
			return ActionResult{}, gameerr.Validationf("%s是雜交作物，沒有一般種子可以鑑定。", crop.Name)
		}
		if err := s.checkLibrary(ctx, t, farm.ID, 1, ""); err != nil {
			return ActionResult{}, err
		}
		fee := seedlab.IdentifyFee(crop.SeedPrice)
		if farm.Gold < fee {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：鑑定需要 %d，目前只有 %d。", fee, farm.Gold)
		}
		item := rules.SeedItemCode(crop.Code)
		ok, err := inventory.Remove(ctx, t, farm.ID, item, 1, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "沒有%s種子可以鑑定。", crop.Name)
		}
		farm.Gold -= fee
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		genes := seedlab.RollIdentify(!crop.ShopBuyable, s.LabRng)
		id, err := labrepo.AddSeed(ctx, t, labrepo.NewSeed{FarmID: farm.ID, BreederFarmID: farm.ID, CropCode: crop.Code,
			Genes: genes, Generation: 1}, now)
		if err != nil {
			return ActionResult{}, err
		}
		if err := s.registerLabCodex(ctx, t, farm.ID, crop.Code, now); err != nil {
			return ActionResult{}, err
		}
		if err := labrepo.AddRecord(ctx, t, labrepo.NewRecord{FarmID: farm.ID, Kind: "identify", ResultSeedID: &id,
			CropCode: crop.Code, Genes: genes, Generation: 1, Mutation: "none", Success: true}, now); err != nil {
			return ActionResult{}, err
		}
		lvNote, err := s.addLabExp(ctx, t, lab, seedlab.Current().ExpIdentify)
		if err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvSeedLab,
			fmt.Sprintf("鑑定%s種子（%s），金幣 -%d", crop.Name, genes.Text(), fee),
			rec{ItemCode: item, Quantity: 1, GoldDelta: -fee}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🔬 鑑定完成：%s種子（%s），花費 %d 金幣。%s", crop.Name, genes.Text(), fee, lvNote),
			farm.Gold, -fee, item, 1), nil
	})
}

// freeSlot 最小的空研究槽編號；沒有時回傳 0。
func freeSlot(lab *models.SeedLab, jobs []models.LabJob) int {
	used := map[int]bool{}
	for _, j := range jobs {
		used[j.Slot] = true
	}
	for slot := 1; slot <= seedlab.SlotsFor(lab.Level, lab.ExtraSlots); slot++ {
		if !used[slot] {
			return slot
		}
	}
	return 0
}

// storedSeed 鎖定並取得自己在種子庫的種子（不是培育中、已種下或已消耗）。
func storedSeed(ctx context.Context, t *db.Tx, farmID, seedID int64) (*models.LabSeed, error) {
	seed, err := labrepo.GetSeed(ctx, t, seedID, true)
	if err != nil {
		return nil, err
	}
	if seed == nil || seed.FarmID != farmID {
		return nil, gameerr.NotFoundf("找不到這顆種子。")
	}
	switch seed.Status {
	case models.LabSeedBusy:
		return nil, gameerr.Validationf("這顆種子正在培育中。")
	case models.LabSeedStored:
		return seed, nil
	}
	return nil, gameerr.NotFoundf("這顆種子已經種下或用掉了。")
}

func (s *GameService) cropName(ctx context.Context, t *db.Tx, code string) string {
	if c, err := crops.ByCode(ctx, t, code, false); err == nil && c != nil {
		return c.Name
	}
	return code
}

// StartCross 開始雜交培育：母本、父本（可以同品種）與選用的材料（樹脂、藥草、水晶催化劑）。
func (s *GameService) StartCross(ctx context.Context, playerID, motherID, fatherID int64, resin, herb bool, catalyst string) (ActionResult, error) {
	if motherID == fatherID {
		return ActionResult{}, gameerr.Validationf("母本和父本要選不同的兩顆種子。")
	}
	if catalyst != "" && seedlab.GeneNames[catalyst] == "" {
		return ActionResult{}, gameerr.Validationf("沒有這種基因。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lab, err := s.lockedLab(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		jobs, err := labrepo.OpenJobs(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		slot := freeSlot(lab, jobs)
		if slot == 0 {
			return ActionResult{}, gameerr.Validationf("研究槽已滿（%d 個），請先領取或等待完成。", seedlab.SlotsFor(lab.Level, lab.ExtraSlots))
		}
		// 依 ID 順序鎖定兩顆種子
		first, second := min(motherID, fatherID), max(motherID, fatherID)
		a, err := storedSeed(ctx, t, farm.ID, first)
		if err != nil {
			return ActionResult{}, err
		}
		b, err := storedSeed(ctx, t, farm.ID, second)
		if err != nil {
			return ActionResult{}, err
		}
		mother, father := a, b
		if mother.ID != motherID {
			mother, father = b, a
		}
		plan := seedlab.CrossPlan{Mother: mother.CropCode, Father: father.CropCode, Resin: resin, Herb: herb, Catalyst: catalyst}
		// 配方隱藏：任兩個品種都可以試驗配對；不在雜交表中的組合一定得到母本品種（和雜交失敗一樣，材料退回）
		code, bp, _ := plan.Result()
		known, err := knownHybrids(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		mats := plan.Materials()
		if err := s.removeMaterials(ctx, t, farm, mats, []string{seedlab.MatResin, seedlab.MatHerb, seedlab.MatCrystal}); err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		for _, seed := range []*models.LabSeed{mother, father} {
			if err := labrepo.SetStatus(ctx, t, seed.ID, models.LabSeedBusy, now); err != nil {
				return ActionResult{}, err
			}
		}
		stored := map[string]any{}
		for k, n := range mats {
			stored[k] = n
		}
		if catalyst != "" {
			stored["catalyst"] = catalyst
			delete(stored, seedlab.MatCrystal)
		}
		readyAt := now.Add(plan.Duration())
		if _, err := labrepo.AddJob(ctx, t, farm.ID, slot, "cross", mother.ID, &father.ID, stored, now, readyAt); err != nil {
			return ActionResult{}, err
		}
		parents := fmt.Sprintf("%s × %s", s.cropName(ctx, t, mother.CropCode), s.cropName(ctx, t, father.CropCode))
		desc := fmt.Sprintf("研究槽 #%d 開始試驗雜交：%s → ❔ 結果未知", slot, parents)
		if CrossKnown(mother.CropCode, father.CropCode, known) {
			desc = fmt.Sprintf("研究槽 #%d 開始雜交：%s → %s（成功率 %s%%）", slot, parents, s.cropName(ctx, t, code), pctText(bp))
		}
		if err := s.record(ctx, t, farm, rules.EvSeedLab, truncRunes(desc, 200), rec{}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🧬 %s，%s後完成。", desc, rules.FormatDuration(int(plan.Duration().Seconds()))),
			farm.Gold, 0, "", 0), nil
	})
}

func pctText(bp int) string {
	if bp%100 == 0 {
		return fmt.Sprint(bp / 100)
	}
	return fmt.Sprintf("%.1f", float64(bp)/100)
}

// StartMutation 開始突變研究：消耗水晶 ×2（必要）與古代核心 ×1（選用，成功率加倍）。
func (s *GameService) StartMutation(ctx context.Context, playerID, seedID int64, ancientCore bool) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lab, err := s.lockedLab(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		jobs, err := labrepo.OpenJobs(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		slot := freeSlot(lab, jobs)
		if slot == 0 {
			return ActionResult{}, gameerr.Validationf("研究槽已滿（%d 個），請先領取或等待完成。", seedlab.SlotsFor(lab.Level, lab.ExtraSlots))
		}
		seed, err := storedSeed(ctx, t, farm.ID, seedID)
		if err != nil {
			return ActionResult{}, err
		}
		crystals := seedlab.Current().MutateCrystal
		need := map[string]int{seedlab.MatCrystal: crystals}
		if ancientCore {
			need[seedlab.MatAncientCore] = 1
		}
		if err := s.removeMaterials(ctx, t, farm, need, []string{seedlab.MatCrystal, seedlab.MatAncientCore}); err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		if err := labrepo.SetStatus(ctx, t, seed.ID, models.LabSeedBusy, now); err != nil {
			return ActionResult{}, err
		}
		stored := map[string]any{"mutate_crystal": crystals}
		if ancientCore {
			stored[seedlab.MatAncientCore] = 1
		}
		readyAt := now.Add(seedlab.MutateDuration())
		if _, err := labrepo.AddJob(ctx, t, farm.ID, slot, "mutate", seed.ID, nil, stored, now, readyAt); err != nil {
			return ActionResult{}, err
		}
		chance := seedlab.MutateChanceBP(seed.GeneMutation, ancientCore)
		desc := fmt.Sprintf("研究槽 #%d 開始突變研究：%s種子（成功率 %s%%）", slot, s.cropName(ctx, t, seed.CropCode), pctText(chance))
		if err := s.record(ctx, t, farm, rules.EvSeedLab, truncRunes(desc, 200), rec{}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("☢️ %s，%s後完成。", desc, rules.FormatDuration(int(seedlab.MutateDuration().Seconds()))),
			farm.Gold, 0, "", 0), nil
	})
}

// StartClone 開始育苗（Go 版新增，2026-11-15）：在研究槽複製同基因、同世代的特殊種子，用來種滿農田。
//   - 只要金幣（seedlab.CloneFee，依基因總和）就能育苗；藥草選用（每次 CloneHerb 個），提高得到 2、3 顆的機率。
//   - 排程：一次 1–CloneMaxBatches 次，費用、藥草、時間 × 次數，全部完成後一起領取；每次得到 1–3 顆（seedlab.RollCloneCopies）。
//   - 不複製突變（避免水晶突變等無限複製）。種子庫要能放下每次至少 1 顆（含其他還沒領取的育苗）；多出來的照樣放進種子庫。
func (s *GameService) StartClone(ctx context.Context, playerID, seedID int64, batches int, herb bool) (ActionResult, error) {
	cfg := seedlab.Current()
	if batches < 1 || batches > cfg.CloneMaxBatches {
		return ActionResult{}, gameerr.Validationf("一次可以排程 1–%d 次育苗。", cfg.CloneMaxBatches)
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lab, err := s.lockedLab(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		jobs, err := labrepo.OpenJobs(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		slot := freeSlot(lab, jobs)
		if slot == 0 {
			return ActionResult{}, gameerr.Validationf("研究槽已滿（%d 個），請先領取或等待完成。", seedlab.SlotsFor(lab.Level, lab.ExtraSlots))
		}
		seed, err := storedSeed(ctx, t, farm.ID, seedID)
		if err != nil {
			return ActionResult{}, err
		}
		pending := 0
		for _, j := range jobs {
			if j.Kind == "clone" {
				pending += cloneBatches(j.Materials)
			}
		}
		if err := s.checkLibrary(ctx, t, farm.ID, batches+pending, ""); err != nil {
			return ActionResult{}, err
		}
		genes := labrepo.GenesOf(seed)
		fee := seedlab.CloneFee(genes) * batches
		if farm.Gold < fee {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：育苗 %d 次需要 %d，目前只有 %d。", batches, fee, farm.Gold)
		}
		herbs := 0
		if herb {
			herbs = cfg.CloneHerb * batches
			if err := s.removeMaterials(ctx, t, farm, map[string]int{seedlab.MatHerb: herbs}, []string{seedlab.MatHerb}); err != nil {
				return ActionResult{}, err
			}
		}
		farm.Gold -= fee
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		if err := labrepo.SetStatus(ctx, t, seed.ID, models.LabSeedBusy, now); err != nil {
			return ActionResult{}, err
		}
		stored := map[string]any{"clone_fee": fee, "batches": batches}
		if herbs > 0 {
			stored[seedlab.MatHerb] = herbs
		}
		readyAt := now.Add(seedlab.CloneDuration(batches))
		if _, err := labrepo.AddJob(ctx, t, farm.ID, slot, "clone", seed.ID, nil, stored, now, readyAt); err != nil {
			return ActionResult{}, err
		}
		times := ""
		if batches > 1 {
			times = fmt.Sprintf(" ×%d 次", batches)
		}
		desc := fmt.Sprintf("研究槽 #%d 開始育苗%s：%s種子 第 %d 代（%s）", slot, times, s.cropName(ctx, t, seed.CropCode), seed.Generation, genes.Text())
		if herbs > 0 {
			desc += fmt.Sprintf("，加藥草 ×%d", herbs)
		}
		if err := s.record(ctx, t, farm, rules.EvSeedLab, truncRunes(fmt.Sprintf("%s，金幣 -%d", desc, fee), 200),
			rec{GoldDelta: -fee}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🌱 %s，花費 %s 金幣，%s後完成。", desc, numfmt.Comma(fee),
			rules.FormatDuration(int(seedlab.CloneDuration(batches).Seconds()))), farm.Gold, -fee, "", 0), nil
	})
}

// cloneBatches 育苗工作排程的次數（2026-11-15 排程上線前的工作沒有記錄，算 1 次）。
func cloneBatches(m map[string]any) int {
	if n, ok := toInt(m["batches"]); ok && n > 0 {
		return n
	}
	return 1
}

func (s *GameService) collectClone(ctx context.Context, t *db.Tx, farm *models.Farm, j models.LabJob, now time.Time) (string, int, error) {
	seed, err := labrepo.GetSeed(ctx, t, j.MotherSeedID, true)
	if err != nil {
		return "", 0, err
	}
	if seed == nil {
		return "", 0, fmt.Errorf("研究槽 #%d 的種子不存在", j.Slot)
	}
	if err := labrepo.SetStatus(ctx, t, seed.ID, models.LabSeedStored, now); err != nil {
		return "", 0, err
	}
	breeder := farm.ID
	if seed.BreederFarmID != nil {
		breeder = *seed.BreederFarmID
	}
	genes := labrepo.GenesOf(seed)
	herbs, _ := toInt(j.Materials[seedlab.MatHerb])
	batches := cloneBatches(j.Materials)
	total, firstID := 0, int64(0)
	rolls := make([]string, batches)
	for i := range batches {
		n := seedlab.RollCloneCopies(herbs > 0, s.LabRng())
		rolls[i] = fmt.Sprint(n)
		for range n {
			id, err := labrepo.AddSeed(ctx, t, labrepo.NewSeed{FarmID: farm.ID, BreederFarmID: breeder, CropCode: seed.CropCode,
				Genes: genes, Generation: seed.Generation, Mutation: "none"}, now)
			if err != nil {
				return "", 0, err
			}
			if firstID == 0 {
				firstID = id
			}
		}
		total += n
	}
	before := labrepo.SnapshotOf(seed)
	before.Copies = total
	if err := labrepo.AddRecord(ctx, t, labrepo.NewRecord{FarmID: farm.ID, Kind: "clone", Mother: &before, ResultSeedID: &firstID,
		CropCode: seed.CropCode, Genes: genes, Generation: seed.Generation, Mutation: "none", Success: true}, now); err != nil {
		return "", 0, err
	}
	times := ""
	if batches > 1 {
		times = fmt.Sprintf("（%d 次：%s）", batches, strings.Join(rolls, "、"))
	}
	line := fmt.Sprintf("🌱 #%d 育苗完成%s：%s種子 第 %d 代（%s）**+%d**，原本的種子也放回種子庫。", j.Slot, times,
		s.cropName(ctx, t, seed.CropCode), seed.Generation, genes.Text(), total)
	capacity, err := s.libraryCap(ctx, t, farm.ID)
	if err != nil {
		return "", 0, err
	}
	if n, err := labrepo.CountLibrary(ctx, t, farm.ID); err == nil && n > capacity {
		line += fmt.Sprintf("\n⚠️ 種子庫 %d／%d 顆已超過上限，先種下或分享一些種子才能再鑑定、育苗。", n, capacity)
	} else if err != nil {
		return "", 0, err
	}
	if err := s.record(ctx, t, farm, rules.EvSeedLab, truncRunes(stripMarks(line), 200), rec{}); err != nil {
		return "", 0, err
	}
	return line, seedlab.Current().ExpClone * batches, nil
}

// CollectLab 領取所有已完成的研究槽。
func (s *GameService) CollectLab(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lab, err := s.lockedLab(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		jobs, err := labrepo.OpenJobs(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		var lines []string
		var next *time.Time
		exp := 0
		known, err := knownHybrids(ctx, t, farm.ID) // 同時完成兩個相同的新配方時，只有第一個算「發現」
		if err != nil {
			return ActionResult{}, err
		}
		for _, j := range jobs {
			if now.Before(j.ReadyAt) {
				if next == nil || j.ReadyAt.Before(*next) {
					at := j.ReadyAt
					next = &at
				}
				continue
			}
			var line string
			var gained int
			switch j.Kind {
			case "cross":
				line, gained, err = s.collectCross(ctx, t, farm, j, known, now)
			case "clone":
				line, gained, err = s.collectClone(ctx, t, farm, j, now)
			default:
				line, gained, err = s.collectMutation(ctx, t, farm, j, now)
			}
			if err != nil {
				return ActionResult{}, err
			}
			if err := labrepo.CollectJob(ctx, t, j.ID, now); err != nil {
				return ActionResult{}, err
			}
			lines = append(lines, line)
			exp += gained
		}
		if len(lines) == 0 {
			if next == nil {
				return ActionResult{}, gameerr.Validationf("研究槽是空的。")
			}
			return ActionResult{}, gameerr.Validationf("研究還沒完成，還需要 %s。", rules.FormatDuration(rules.RemainingSeconds(next, now)))
		}
		lvNote, err := s.addLabExp(ctx, t, lab, exp)
		if err != nil {
			return ActionResult{}, err
		}
		return result(strings.Join(lines, "\n")+fmt.Sprintf("\n研究經驗 +%d。%s", exp, lvNote), farm.Gold, 0, "", len(lines)), nil
	})
}

func (s *GameService) collectCross(ctx context.Context, t *db.Tx, farm *models.Farm, j models.LabJob, known map[string]bool,
	now time.Time) (string, int, error) {
	mother, err := labrepo.GetSeed(ctx, t, j.MotherSeedID, true)
	if err != nil {
		return "", 0, err
	}
	father, err := labrepo.GetSeed(ctx, t, *j.FatherSeedID, true)
	if err != nil {
		return "", 0, err
	}
	if mother == nil || father == nil {
		return "", 0, fmt.Errorf("研究槽 #%d 的種子不存在", j.Slot)
	}
	resin, _ := toInt(j.Materials[seedlab.MatResin])
	catalyst, _ := j.Materials["catalyst"].(string)
	plan := seedlab.CrossPlan{Mother: mother.CropCode, Father: father.CropCode, Resin: resin > 0}
	code, bp, ok := plan.Result()
	success := ok && seedlab.Success(bp, s.LabRng())
	if !success {
		code = mother.CropCode
	}
	child := seedlab.Inherit(
		seedlab.Parent{Genes: labrepo.GenesOf(mother), Generation: mother.Generation, Mutation: mother.Mutation},
		seedlab.Parent{Genes: labrepo.GenesOf(father), Generation: father.Generation, Mutation: father.Mutation},
		catalyst, s.LabRng)
	// 2026-11-15 起只消耗父本，母本放回種子庫（原本兩顆都消耗，種子數量每代減半）
	if err := labrepo.SetStatus(ctx, t, mother.ID, models.LabSeedStored, now); err != nil {
		return "", 0, err
	}
	if err := labrepo.SetStatus(ctx, t, father.ID, models.LabSeedUsed, now); err != nil {
		return "", 0, err
	}
	id, err := labrepo.AddSeed(ctx, t, labrepo.NewSeed{FarmID: farm.ID, BreederFarmID: farm.ID, CropCode: code,
		Genes: child.Genes, Generation: child.Generation, Mutation: child.Mutation}, now)
	if err != nil {
		return "", 0, err
	}
	if err := s.registerLabCodex(ctx, t, farm.ID, code, now); err != nil {
		return "", 0, err
	}
	ms, fs := labrepo.SnapshotOf(mother), labrepo.SnapshotOf(father)
	if err := labrepo.AddRecord(ctx, t, labrepo.NewRecord{FarmID: farm.ID, Kind: "cross", Mother: &ms, Father: &fs,
		ResultSeedID: &id, CropCode: code, Genes: child.Genes, Generation: child.Generation, Mutation: child.Mutation,
		Success: success}, now); err != nil {
		return "", 0, err
	}
	name := s.cropName(ctx, t, code)
	mut := ""
	if child.Mutation != "none" {
		mut = " ✨" + balance.Mutations[child.Mutation]
	}
	parents := fmt.Sprintf("%s × %s", s.cropName(ctx, t, mother.CropCode), s.cropName(ctx, t, father.CropCode))
	var line string
	exp := seedlab.Current().ExpCross
	switch {
	case !success:
		exp = seedlab.Current().ExpCrossFail
		// 失敗時退回培育材料
		refund := map[string]int{}
		for _, m := range []string{seedlab.MatResin, seedlab.MatHerb} {
			if n, ok := toInt(j.Materials[m]); ok && n > 0 {
				refund[m] += n
			}
		}
		if catalyst != "" {
			refund[seedlab.MatCrystal]++
		}
		for code, n := range refund {
			if err := inventory.Add(ctx, t, farm.ID, code, n, "normal", "none"); err != nil {
				return "", 0, err
			}
		}
		got := fmt.Sprintf("得到%s種子 第 %d 代（%s）%s，培育材料已退回。", name, child.Generation, child.Genes.Text(), mut)
		switch {
		case !ok: // 不在雜交表中的組合（配方隱藏後可以試驗）
			line = fmt.Sprintf("🧪 #%d 試驗雜交（%s）沒有產生新品種，%s", j.Slot, parents, got)
		case !CrossKnown(mother.CropCode, father.CropCode, known): // 有這個配方但還沒發現：給一點提示
			line = fmt.Sprintf("💥 #%d 雜交失敗（%s），不過這個組合好像有反應，可以再試試！%s", j.Slot, parents, got)
		default:
			line = fmt.Sprintf("💥 #%d 雜交失敗（%s），%s", j.Slot, parents, got)
		}
	case code == mother.CropCode && code == father.CropCode:
		line = fmt.Sprintf("🌱 #%d 改良完成：%s種子 第 %d 代（%s）%s。", j.Slot, name, child.Generation, child.Genes.Text(), mut)
	default:
		line = fmt.Sprintf("🎉 #%d 雜交成功（%s）：**%s**種子 第 %d 代（%s）%s！", j.Slot, parents, name, child.Generation,
			child.Genes.Text(), mut)
		if !known[code] {
			known[code] = true
			line += fmt.Sprintf("\n📖 發現新配方：%s → %s！之後在「🧬 雜交培育」的配方表就看得到。", parents, name)
		}
	}
	line += fmt.Sprintf("（♀ 母本 #%d 已放回種子庫）", mother.ID)
	if err := s.record(ctx, t, farm, rules.EvSeedLab, truncRunes(stripMarks(line), 200), rec{}); err != nil {
		return "", 0, err
	}
	return line, exp, nil
}

func (s *GameService) collectMutation(ctx context.Context, t *db.Tx, farm *models.Farm, j models.LabJob, now time.Time) (string, int, error) {
	seed, err := labrepo.GetSeed(ctx, t, j.MotherSeedID, true)
	if err != nil {
		return "", 0, err
	}
	if seed == nil {
		return "", 0, fmt.Errorf("研究槽 #%d 的種子不存在", j.Slot)
	}
	core, _ := toInt(j.Materials[seedlab.MatAncientCore])
	success := seedlab.Success(seedlab.MutateChanceBP(seed.GeneMutation, core > 0), s.LabRng())
	before := labrepo.SnapshotOf(seed)
	mutation := seed.Mutation
	if success {
		mutation = seedlab.RollLabMutation(seed.Mutation, s.LabRng())
		if err := labrepo.SetMutation(ctx, t, seed.ID, mutation, now); err != nil {
			return "", 0, err
		}
	}
	if err := labrepo.SetStatus(ctx, t, seed.ID, models.LabSeedStored, now); err != nil {
		return "", 0, err
	}
	if err := labrepo.AddRecord(ctx, t, labrepo.NewRecord{FarmID: farm.ID, Kind: "mutate", Mother: &before,
		ResultSeedID: &seed.ID, CropCode: seed.CropCode, Genes: labrepo.GenesOf(seed), Generation: seed.Generation,
		Mutation: mutation, Success: success}, now); err != nil {
		return "", 0, err
	}
	name := s.cropName(ctx, t, seed.CropCode)
	line := fmt.Sprintf("☢️ #%d 突變研究沒有成功，%s種子已放回種子庫。", j.Slot, name)
	if success {
		line = fmt.Sprintf("✨ #%d 突變成功：%s種子變成 **%s**！", j.Slot, name, balance.Mutations[mutation])
	}
	if err := s.record(ctx, t, farm, rules.EvSeedLab, truncRunes(stripMarks(line), 200), rec{}); err != nil {
		return "", 0, err
	}
	return line, seedlab.Current().ExpMutate, nil
}

// jsonUnmarshal 解析 JSON 欄位；NULL（空）時不做任何事。
func jsonUnmarshal(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// stripMarks 交易紀錄不保留 Markdown 粗體。
func stripMarks(s string) string { return strings.ReplaceAll(s, "**", "") }

// PlantLabSeed 把特殊種子種到指定的空地：基因影響品質、突變、收成數量與生長時間，種子本身的突變會直接帶到作物上。
func (s *GameService) PlantLabSeed(ctx context.Context, playerID, seedID int64, plotNumber int) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		msg, crop, err := s.plantLabSeed(ctx, t, farm, seedID, plotNumber)
		if err != nil {
			return ActionResult{}, err
		}
		return result(msg, farm.Gold, 0, rules.SeedItemCode(crop.Code), 1), nil
	})
}

// plantLabSeed 在交易內把一顆特殊種子種到指定的空地（farm 必須已鎖定），回傳訊息與作物。
func (s *GameService) plantLabSeed(ctx context.Context, t *db.Tx, farm *models.Farm, seedID int64, plotNumber int) (string, *models.CropType, error) {
	// 沒有實驗室也能種下別人分享的種子
	seed, err := storedSeed(ctx, t, farm.ID, seedID)
	if err != nil {
		return "", nil, err
	}
	crop, err := crops.ByCode(ctx, t, seed.CropCode, false)
	if err != nil {
		return "", nil, err
	}
	if crop == nil {
		return "", nil, gameerr.NotFoundf("找不到作物 %s。", seed.CropCode)
	}
	if !crop.Active {
		return "", nil, gameerr.Validationf("%s已停用，不能種植。", crop.Name)
	}
	if err := s.checkSeason(crop); err != nil {
		return "", nil, err
	}
	plot, err := farms.GetPlot(ctx, t, farm.ID, plotNumber, true)
	if err != nil {
		return "", nil, err
	}
	if plot == nil {
		return "", nil, gameerr.NotFoundf("沒有第 %d 格土地。", plotNumber)
	}
	if plot.CropTypeID != nil {
		return "", nil, gameerr.New(gameerr.PlotOccupied, "第 %d 格土地已經種植作物。", plotNumber)
	}
	if err := labrepo.SetStatus(ctx, t, seed.ID, models.LabSeedPlanted, s.now()); err != nil {
		return "", nil, err
	}
	label, seconds, err := s.plantWith(ctx, t, farm, plot, crop, "", seed)
	if err != nil {
		return "", nil, err
	}
	tail := "請記得澆水，澆水後開始生長。"
	if seconds != nil {
		tail = fmt.Sprintf("已自動澆水，%s後成熟。", rules.FormatDuration(*seconds))
	}
	return fmt.Sprintf("🧪 已在第 %d 格種下特殊%s種子（%s）%s，%s", plotNumber, crop.Name,
		labrepo.GenesOf(seed).Text(), label, tail), crop, nil
}

// PlantableLabSeeds 農田頁「種下特殊種子」選單用：種子庫裡現在可以種的種子（不含培育中、停用或不在季節的作物），依編號排列。
// 沒有實驗室也會列出別人分享的種子。
func (s *GameService) PlantableLabSeeds(ctx context.Context, playerID int64) ([]LabSeedView, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]LabSeedView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		seeds, err := labrepo.ListSeeds(ctx, t, farm.ID)
		if err != nil || len(seeds) == 0 {
			return nil, err
		}
		cropsByCode, err := s.cropMap(ctx, t)
		if err != nil {
			return nil, err
		}
		now := s.now()
		var out []LabSeedView
		for i := range seeds {
			c := cropsByCode[seeds[i].CropCode]
			if seeds[i].Status != models.LabSeedStored || c == nil || !c.Active ||
				!seasons.Available(c.Seasons, c.HourStart, c.HourEnd, now) {
				continue
			}
			out = append(out, seedView(&seeds[i], cropsByCode))
		}
		return out, nil
	})
}

// PlantLabSeeds 一次種下好幾顆特殊種子，每顆依序種到一格空地（由小到大）；任一顆不能種時全部不種。
func (s *GameService) PlantLabSeeds(ctx context.Context, playerID int64, seedIDs []int64) (ActionResult, error) {
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(seedIDs))
	for _, id := range seedIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ActionResult{}, gameerr.Validationf("請選擇要種下的特殊種子。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		plots, err := farms.ListPlots(ctx, t, farm.ID, false)
		if err != nil {
			return ActionResult{}, err
		}
		var empty []int
		for _, p := range plots {
			if p.CropTypeID == nil {
				empty = append(empty, p.PlotNumber)
			}
		}
		slices.Sort(empty)
		if len(empty) == 0 {
			return ActionResult{}, gameerr.Validationf("農田沒有空地，收成後才能種下特殊種子。")
		}
		if len(ids) > len(empty) {
			return ActionResult{}, gameerr.Validationf("選了 %d 顆種子，但農田只有 %d 格空地。", len(ids), len(empty))
		}
		lines := make([]string, 0, len(ids))
		var last *models.CropType
		for i, id := range ids {
			msg, crop, err := s.plantLabSeed(ctx, t, farm, id, empty[i])
			if err != nil {
				return ActionResult{}, err
			}
			lines, last = append(lines, msg), crop
		}
		if len(ids) == 1 {
			return result(lines[0], farm.Gold, 0, rules.SeedItemCode(last.Code), 1), nil
		}
		return result(fmt.Sprintf("🧪 種下 %d 顆特殊種子：\n%s", len(ids), strings.Join(lines, "\n")), farm.Gold, 0, "", 0), nil
	})
}

// labGrowthPct 這格種的特殊種子的生長基因減少的百分比（不是特殊種子時為 0）。
func labGrowthPct(ctx context.Context, t *db.Tx, plot *models.FarmPlot) (int, error) {
	if plot.LabSeedID == nil {
		return 0, nil
	}
	seed, err := labrepo.GetSeed(ctx, t, *plot.LabSeedID, false)
	if err != nil || seed == nil {
		return 0, err
	}
	return seedlab.GrowthReductionPct(seed.GeneGrowth), nil
}

// labHarvest 收成後（土地已清空前呼叫）：水晶作物額外給水晶；特殊種子有機會取回 1 顆同基因的種子（傳承）。回傳附加訊息。
func (s *GameService) labHarvest(ctx context.Context, t *db.Tx, farm *models.Farm, plot *models.FarmPlot, mutation string) (string, error) {
	note := ""
	if mutation == "crystal" {
		n := balance.CrystalDropMin + int(s.LabRng()*float64(balance.CrystalDropMax-balance.CrystalDropMin+1))
		n = min(n, balance.CrystalDropMax)
		if err := s.ensureMaterialSpace(ctx, t, farm, n); err == nil {
			if err := inventory.Add(ctx, t, farm.ID, seedlab.MatCrystal, n, "normal", "none"); err != nil {
				return "", err
			}
			note += fmt.Sprintf("（💎 水晶 +%d）", n)
		} else if gameerr.Is(err, gameerr.WarehouseFull) {
			note += "（素材背包已滿，沒有拿到水晶）"
		} else {
			return "", err
		}
	}
	if plot.LabSeedID == nil {
		return note, nil
	}
	seed, err := labrepo.GetSeed(ctx, t, *plot.LabSeedID, true)
	if err != nil || seed == nil {
		return note, err
	}
	if !seedlab.Success(seedlab.Current().InheritBP, s.LabRng()) {
		return note, nil
	}
	capacity, err := s.libraryCap(ctx, t, farm.ID)
	if err != nil {
		return "", err
	}
	if n, err := labrepo.CountLibrary(ctx, t, farm.ID); err != nil {
		return "", err
	} else if n >= capacity {
		return note + "（🧬 本來能取回種子，但種子庫已滿）", nil
	}
	breeder := farm.ID
	if seed.BreederFarmID != nil {
		breeder = *seed.BreederFarmID
	}
	if _, err := labrepo.AddSeed(ctx, t, labrepo.NewSeed{FarmID: farm.ID, BreederFarmID: breeder, CropCode: seed.CropCode,
		Genes: labrepo.GenesOf(seed), Generation: seed.Generation, Mutation: seed.Mutation}, s.now()); err != nil {
		return "", err
	}
	if err := s.registerLabCodex(ctx, t, farm.ID, seed.CropCode, s.now()); err != nil {
		return "", err
	}
	return note + "（🧬 取回 1 顆同基因的特殊種子）", nil
}

// LabRecords 最近的培育紀錄。
func (s *GameService) LabRecords(ctx context.Context, playerID int64, limit int) ([]LabRecordView, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]LabRecordView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		rows, err := labrepo.Records(ctx, t, farm.ID, limit)
		if err != nil {
			return nil, err
		}
		cropsByCode, err := s.cropMap(ctx, t)
		if err != nil {
			return nil, err
		}
		name := func(code string) string {
			if c := cropsByCode[code]; c != nil {
				return c.Name
			}
			return code
		}
		out := make([]LabRecordView, 0, len(rows))
		for _, r := range rows {
			var genes seedlab.Genes
			_ = jsonUnmarshal(r.Genes, &genes)
			mut := ""
			if r.Mutation != "none" {
				mut = " ✨" + balance.Mutations[r.Mutation]
			}
			v := LabRecordView{Kind: r.Kind, CreatedAt: r.CreatedAt, Success: r.Success, Generation: r.Generation}
			switch r.Kind {
			case "identify":
				v.Text = fmt.Sprintf("🔬 鑑定 %s 第 1 代（%s）", name(r.CropCode), genes.Text())
			case "cross":
				var m, f labrepo.Snapshot
				_ = jsonUnmarshal(r.Mother, &m)
				_ = jsonUnmarshal(r.Father, &f)
				mark := "🎉"
				if !r.Success {
					mark = "💥"
				}
				v.Text = fmt.Sprintf("%s %s × %s → %s 第 %d 代（%s）%s", mark, snapView(m, cropsByCode).Label(),
					snapView(f, cropsByCode).Label(), name(r.CropCode), r.Generation, genes.Text(), mut)
			case "clone":
				var m labrepo.Snapshot
				_ = jsonUnmarshal(r.Mother, &m)
				v.Text = fmt.Sprintf("🌱 育苗：%s 第 %d 代（%s）×%d", name(r.CropCode), r.Generation, genes.Text(), max(1, m.Copies))
			case "mutate":
				if r.Success {
					v.Text = fmt.Sprintf("✨ 突變研究：%s 第 %d 代 →%s", name(r.CropCode), r.Generation, mut)
				} else {
					v.Text = fmt.Sprintf("☢️ 突變研究沒有成功：%s 第 %d 代", name(r.CropCode), r.Generation)
				}
			}
			out = append(out, v)
		}
		return out, nil
	})
}

// LabShareTargets 群組中已建造實驗室的其他玩家（分享對象）。
func (s *GameService) LabShareTargets(ctx context.Context, groupID, playerID int64) ([]LabShareTargetView, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]LabShareTargetView, error) {
		rows, err := labrepo.ShareTargets(ctx, t, groupID, playerID)
		if err != nil {
			return nil, err
		}
		out := make([]LabShareTargetView, len(rows))
		for i, r := range rows {
			out[i] = LabShareTargetView{r.PlayerID, r.PlayerName, r.FarmName}
		}
		return out, nil
	})
}

// ShareLabSeed 把種子庫的一顆種子分享給同群組、已建造實驗室的玩家。
// 每人每天最多送出 5 顆；每人每天只能收到 1 位玩家的分享（同一位玩家送的多顆都可以收）。日期以台灣時間計算。
func (s *GameService) ShareLabSeed(ctx context.Context, groupID, playerID, seedID, targetPlayerID int64) (ActionResult, error) {
	if playerID == targetPlayerID {
		return ActionResult{}, gameerr.Validationf("不能分享給自己。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		if err := requireMember(ctx, t, groupID, playerID); err != nil {
			return ActionResult{}, err
		}
		member, err := groups.IsMember(ctx, t, groupID, targetPlayerID)
		if err != nil {
			return ActionResult{}, err
		}
		if !member {
			return ActionResult{}, gameerr.Validationf("對方不在這個伺服器的群組中。")
		}
		self, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		other, err := farms.GetByPlayer(ctx, t, targetPlayerID, false)
		if err != nil {
			return ActionResult{}, err
		}
		if other == nil {
			return ActionResult{}, gameerr.NotFoundf("對方還沒有農場。")
		}
		// 依 farm_id 由小到大鎖定雙方農場，避免兩人互相分享時死結
		var sender, receiver *models.Farm
		for _, id := range []int64{min(self.ID, other.ID), max(self.ID, other.ID)} {
			f, err := farms.GetByID(ctx, t, id, true)
			if err != nil {
				return ActionResult{}, err
			}
			if id == self.ID {
				sender = f
			} else {
				receiver = f
			}
		}
		if _, err := s.lockedLab(ctx, t, sender); err != nil {
			return ActionResult{}, err
		}
		seed, err := storedSeed(ctx, t, sender.ID, seedID)
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		day := taipeiDay(now)
		sent, err := labrepo.SentCount(ctx, t, sender.ID, day)
		if err != nil {
			return ActionResult{}, err
		}
		cfg := seedlab.Current()
		if sent >= cfg.ShareSendPerDay {
			return ActionResult{}, gameerr.Validationf("今天已經分享 %d 顆種子了（每天最多 %d 顆）。", sent, cfg.ShareSendPerDay)
		}
		senders, err := labrepo.SendersTo(ctx, t, receiver.ID, day)
		if err != nil {
			return ActionResult{}, err
		}
		// 今天已經收過自己的分享時可以再收；否則收過的人數要少於上限
		if !slices.Contains(senders, sender.ID) && len(senders) >= cfg.ShareReceiveSenders {
			return ActionResult{}, gameerr.Validationf("%s今天已經收過其他玩家的分享了（每人每天只能收 %d 位玩家的分享）。",
				receiver.Name, cfg.ShareReceiveSenders)
		}
		if err := s.checkLibrary(ctx, t, receiver.ID, 1, receiver.Name+"的"); err != nil {
			return ActionResult{}, err
		}
		if err := labrepo.Transfer(ctx, t, seed.ID, receiver.ID, now); err != nil {
			return ActionResult{}, err
		}
		if err := labrepo.AddShare(ctx, t, sender.ID, receiver.ID, seed.ID, day, now); err != nil {
			return ActionResult{}, err
		}
		name := s.cropName(ctx, t, seed.CropCode)
		label := fmt.Sprintf("%s種子 第 %d 代（%s）", name, seed.Generation, labrepo.GenesOf(seed).Text())
		if seed.Mutation != "none" {
			label += " ✨" + balance.Mutations[seed.Mutation]
		}
		if err := s.record(ctx, t, sender, rules.EvSeedShare, truncRunes(fmt.Sprintf("分享%s給 %s", label, receiver.Name), 200),
			rec{}); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, receiver, rules.EvSeedShare, truncRunes(fmt.Sprintf("收到 %s 分享的%s", sender.Name, label), 200),
			rec{}); err != nil {
			return ActionResult{}, err
		}
		if err := s.registerLabCodex(ctx, t, receiver.ID, seed.CropCode, now); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🎁 已把%s分享給 %s（今天已分享 %d／%d 顆）。", label, receiver.Name, sent+1, cfg.ShareSendPerDay),
			sender.Gold, 0, "", 1), nil
	})
}
