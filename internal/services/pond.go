package services

// 魚塘（Go 版新增，2026-11-24，規則見 game/pond，設計見「魚塘_流程圖.md」）。
//   - 魚塘的等級、飼料、上次結算時間與每隻魚放進來的時間存在 farm_tools（pond.ToolCode）的 settings；
//     魚的養殖重量在 farm_fish.pond_weight_g，魚苗是 farm_fish.bred（Migration 0049）。
//   - 所有操作都先鎖定農場，再結算到現在（成長、吃飼料、繁殖）；打開魚塘頁也會結算（會寫入），
//     所以兩個面板同時操作時依農場鎖定排隊，不會重複成長或重複繁殖。
//   - 魚塘裡的魚不在魚簍（fishrepo.InBasket 排除 pond_weight_g 有值的魚），只能從這裡賣出：
//     寫 sold_at、sold_price（實際售價），weight_g、price 保留捕獲時的數值，排行榜與圖鑑不受影響。
//   - 建造、升級、補飼料記成 buy_item，賣魚記成 fish_sell，不需要新的交易類型。

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/pond"
	"farmgm/internal/game/rules"
	fishrepo "farmgm/internal/repositories/fishing"
	"farmgm/internal/repositories/inventory"
)

// PondFishView 魚塘裡的一隻魚。
type PondFishView struct {
	ID           int64
	Code         string
	Name         string
	Emoji        string
	Rarity       string
	RarityEmoji  string
	RarityName   string
	WeightG      int
	MaxWeightG   int
	WeightText   string
	MaxText      string
	Progress     int // 0–100
	Price        int // 現在賣出的價格
	Premium      bool
	PremiumPrice int        // 長滿後（以目前等級）的極品價格
	FullAt       *time.Time // 長滿了但還沒待滿 1 個週期時，變成極品的時間
	PeriodsLeft  int        // 晴天、每週期都吃到飼料時，還要幾個週期長滿
	Bred         bool
}

// PondCandidate 魚簍裡的一隻魚，以及能不能放進魚塘。
type PondCandidate struct {
	BasketFish
	Reason string // 不能放的原因（可以放時為 ""）
}

// PondView 魚塘頁。
type PondView struct {
	Now        time.Time
	FarmLevel  int
	Unlocked   bool
	Gold       int
	Level      int // 0 = 還沒建造
	MaxLevel   int
	Capacity   int
	EpicLimit  int
	EpicCount  int
	Feed       int
	FeedCap    int
	FeedInBag  int
	FeedPrice  int
	FeedHours  int // 目前的飼料還能吃幾小時（沒有魚時為 -1）
	Fish       []PondFishView
	PremiumSum int // 全部極品魚的售價合計
	Premiums   int // 極品魚幾隻
	Candidates []PondCandidate
	BreedHint  []string // 符合繁殖條件的魚種名稱（今天還沒生過時才有）
	Settled    pond.Result
	NextPrice  int // 升到下一級的價格（已滿級為 0）
}

func pondLocked(farm *models.Farm) error {
	return gameerr.Validationf("魚塘在農場 Lv.%d 開放（目前 Lv.%d）。", pond.UnlockLevel(), farm.Level)
}

// pondFishOf 讀出魚塘裡的魚（規則用的資料與魚種）。
func pondFishOf(ctx context.Context, t *db.Tx, farmID int64, st pond.State) ([]pond.Fish, map[int64]fishrepo.FishWithType, error) {
	rows, err := fishrepo.PondFish(ctx, t, farmID)
	if err != nil {
		return nil, nil, err
	}
	list := make([]pond.Fish, len(rows))
	byID := make(map[int64]fishrepo.FishWithType, len(rows))
	for i, r := range rows {
		w := r.Fish.WeightG
		if r.Fish.PondWeightG != nil {
			w = *r.Fish.PondWeightG
		}
		list[i] = pond.Fish{ID: r.Fish.ID, Code: r.Fish.FishCode, Rarity: r.Fish.Rarity, MinG: r.Type.MinWeightG,
			MaxG: r.Type.MaxWeightG, MinPrice: r.Type.MinPrice, MaxPrice: r.Type.MaxPrice, WeightG: w}
		byID[r.Fish.ID] = r
	}
	st.Attach(list)
	return list, byID, nil
}

// settlePond 鎖定魚塘並結算到現在（farm 必須已鎖定）：寫回成長後的重量、新生的魚苗與 settings。
// 回傳 settings 列、狀態、結算後的魚與魚的資料列（魚苗也在裡面）。
func (f *FishingService) settlePond(ctx context.Context, t *db.Tx, farm *models.Farm, now time.Time) (
	*models.FarmTool, pond.State, []pond.Fish, map[int64]fishrepo.FishWithType, pond.Result, error) {
	tool, err := settingsTool(ctx, t, farm.ID, pond.ToolCode, true)
	if err != nil {
		return nil, pond.State{}, nil, nil, pond.Result{}, err
	}
	st := pond.Parse(toolSettings(tool))
	if !st.Built() {
		return tool, st, nil, nil, pond.Result{}, nil
	}
	list, byID, err := pondFishOf(ctx, t, farm.ID, st)
	if err != nil {
		return nil, st, nil, nil, pond.Result{}, err
	}
	before := map[int64]int{}
	for _, x := range list {
		before[x.ID] = x.WeightG
	}
	res := pond.Settle(&st, &list, now, f.PondRng)
	var ids []int64
	var weights []int
	for i := range list {
		x := &list[i]
		if x.ID == 0 { // 魚苗：新增一列（捕獲重量、價格是最小值；養殖重量是結算到現在的重量）
			ft := byCode(byID, x.Code)
			id, err := db.InsertReturningID(ctx, t, models.TableFarmFish, db.C("farm_id", farm.ID), db.C("fish_code", x.Code),
				db.C("rarity", x.Rarity), db.C("weight_g", max(1, x.MinG)), db.C("price", x.MinPrice), db.C("caught_at", x.PlacedAt),
				db.C("bred", true), db.C("pond_weight_g", max(1, x.WeightG)))
			if err != nil {
				return nil, st, nil, nil, res, err
			}
			x.ID = id
			st.Placed[id] = x.PlacedAt
			w := max(1, x.WeightG)
			ft.Fish = models.FarmFish{ID: id, FarmID: farm.ID, FishCode: x.Code, Rarity: x.Rarity, WeightG: max(1, x.MinG),
				Price: x.MinPrice, CaughtAt: x.PlacedAt, Bred: true, PondWeightG: &w}
			byID[id] = ft
			continue
		}
		if before[x.ID] != x.WeightG {
			ids = append(ids, x.ID)
			weights = append(weights, x.WeightG)
		}
	}
	if err := fishrepo.SetPondWeights(ctx, t, farm.ID, ids, weights); err != nil {
		return nil, st, nil, nil, res, err
	}
	for id := range st.Placed { // 已經不在魚塘裡的魚（賣掉了）不用再記
		if _, ok := byID[id]; !ok {
			delete(st.Placed, id)
		}
	}
	if res.Periods > 0 || res.Born > 0 {
		if err := saveSettingsTool(ctx, t, farm.ID, tool, pond.ToolCode, st.Encode(), now); err != nil {
			return nil, st, nil, nil, res, err
		}
	}
	return tool, st, list, byID, res, nil
}

// byCode 同魚種的任一筆資料（魚苗沿用父母的魚種資料）。
func byCode(byID map[int64]fishrepo.FishWithType, code string) fishrepo.FishWithType {
	ids := make([]int64, 0, len(byID))
	for id, r := range byID {
		if r.Fish.FishCode == code {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return fishrepo.FishWithType{}
	}
	return byID[ids[0]]
}

func pondFishView(x pond.Fish, r fishrepo.FishWithType, level int, now time.Time) PondFishView {
	price, premium := x.Price(level, now)
	v := PondFishView{ID: x.ID, Code: x.Code, Name: r.Type.Name, Emoji: r.Type.Emoji, Rarity: x.Rarity,
		RarityEmoji: fishing.RarityEmoji[x.Rarity], RarityName: fishing.RarityNames[x.Rarity], WeightG: x.WeightG,
		MaxWeightG: x.MaxG, WeightText: fishing.WeightText(x.WeightG), MaxText: fishing.WeightText(x.MaxG),
		Progress: x.Progress(), Price: price, Premium: premium, PremiumPrice: x.PremiumPrice(level),
		PeriodsLeft: pond.PeriodsToFull(x, level), Bred: r.Fish.Bred}
	if x.Full() && !premium {
		at := x.PlacedAt.Add(pond.Period)
		v.FullAt = &at
	}
	return v
}

// Pond 魚塘頁：先結算到現在（會寫入），再列出魚塘裡的魚與魚簍裡可以放的魚。
func (f *FishingService) Pond(ctx context.Context, playerID int64) (PondView, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (PondView, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return PondView{}, err
		}
		now := f.now()
		v := PondView{Now: now, FarmLevel: farm.Level, Unlocked: farm.Level >= pond.UnlockLevel(), Gold: farm.Gold,
			MaxLevel: pond.MaxLevel(), FeedPrice: balance.ItemPrice(balance.ItemFishFeed), FeedHours: -1}
		if !v.Unlocked {
			return v, nil
		}
		_, st, list, byID, res, err := f.settlePond(ctx, t, farm, now)
		if err != nil {
			return v, err
		}
		if !st.Built() {
			v.NextPrice = pond.Price(1)
			return v, nil
		}
		level := st.Effective()
		v.Level, v.Capacity, v.EpicLimit, v.Feed, v.FeedCap, v.Settled = st.Level, pond.Capacity(level), pond.EpicLimit(level),
			st.Feed, pond.FeedCapacity(level), res
		v.EpicCount = pond.CountRarity(list, "epic")
		v.FeedHours = pond.FeedHours(st.Feed, len(list))
		if st.Level < pond.MaxLevel() {
			v.NextPrice = pond.Price(st.Level + 1)
		}
		if v.FeedInBag, err = inventory.GetQuantity(ctx, t, farm.ID, balance.ItemFishFeed, "normal", "none"); err != nil {
			return v, err
		}
		for _, x := range list {
			fv := pondFishView(x, byID[x.ID], level, now)
			if fv.Premium {
				v.Premiums++
				v.PremiumSum += fv.Price
			}
			v.Fish = append(v.Fish, fv)
		}
		sort.SliceStable(v.Fish, func(i, j int) bool { // 極品在前，再依售價
			if v.Fish[i].Premium != v.Fish[j].Premium {
				return v.Fish[i].Premium
			}
			return v.Fish[i].Price > v.Fish[j].Price
		})
		v.BreedHint = breedHint(list, byID, st, now)
		// 魚簍裡的魚（自動釣魚機先結算，魚簍才是最新的）
		if _, err := f.settleAutoFish(ctx, t, farm); err != nil {
			return v, err
		}
		basket, err := fishrepo.Basket(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		for _, r := range basket {
			v.Candidates = append(v.Candidates, PondCandidate{BasketFish{NewFishView(r.Fish, r.Type), false},
				pond.PlaceError(r.Fish.Rarity, len(r.Type.RequiredBaits) > 0)})
		}
		return v, nil
	})
}

// breedHint 下一次跨過午夜時符合繁殖條件的魚種（重量、隻數；吃飼料要等結算時才知道）。
func breedHint(list []pond.Fish, byID map[int64]fishrepo.FishWithType, st pond.State, now time.Time) []string {
	c := pond.Current()
	if c.BreedPerDay <= 0 || len(list) >= pond.Capacity(st.Effective()) {
		return nil
	}
	count := map[string]int{}
	var codes []string
	for _, x := range list {
		if x.MaxG <= x.MinG || x.WeightG < x.MinG+(x.MaxG-x.MinG)*c.BreedWeight/100 {
			continue
		}
		if count[x.Code] == 0 {
			codes = append(codes, x.Code)
		}
		count[x.Code]++
	}
	slices.Sort(codes)
	var out []string
	for _, code := range codes {
		if count[code] >= c.BreedMin && c.BreedBP[byCode(byID, code).Fish.Rarity] > 0 {
			out = append(out, byCode(byID, code).Type.Name)
		}
	}
	return out
}

// BuildPond 建造魚塘（Price(1)）。
func (f *FishingService) BuildPond(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.Level < pond.UnlockLevel() {
			return ActionResult{}, pondLocked(farm)
		}
		tool, err := settingsTool(ctx, t, farm.ID, pond.ToolCode, true)
		if err != nil {
			return ActionResult{}, err
		}
		if pond.Parse(toolSettings(tool)).Built() {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經有魚塘了。")
		}
		price := pond.Price(1)
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：建造魚塘需要 %d，目前只有 %d。", price, farm.Gold)
		}
		now := f.now()
		farm.Gold -= price
		if err := f.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := f.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("建造魚塘，金幣 -%d", price), rec{GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		st := pond.State{Level: 1, Last: now, Placed: map[int64]time.Time{}}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, pond.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "🐟 魚塘蓋好了（花費 %d 金幣）！把魚簍裡的魚放進來、加飼料，魚就會慢慢長大，長滿後可以賣到最高價的 %d%%。",
			price, pond.PremiumPct(1)), farm.Gold, -price, "", 0), nil
	})
}

// UpgradePond 升一級（Price(下一級)）。升級前先用原本的等級結算。
func (f *FishingService) UpgradePond(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := f.now()
		tool, st, _, _, _, err := f.settlePond(ctx, t, farm, now)
		if err != nil {
			return ActionResult{}, err
		}
		if !st.Built() {
			return ActionResult{}, gameerr.Validationf("還沒有魚塘，先建造魚塘。")
		}
		if st.Level >= pond.MaxLevel() {
			return ActionResult{}, gameerr.Validationf("魚塘已經是最高等級（Lv.%d）。", pond.MaxLevel())
		}
		price := pond.Price(st.Level + 1)
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：魚塘升級需要 %d，目前只有 %d。", price, farm.Gold)
		}
		st.Level++
		farm.Gold -= price
		if err := f.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := f.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("魚塘升級到 Lv.%d，金幣 -%d", st.Level, price),
			rec{GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, pond.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		level := st.Effective()
		return result(txf(ctx, "🐟 魚塘升級到 Lv.%d（花費 %d 金幣）：容量 %d 隻（史詩魚最多 %d 隻）、成長 +%d%%、池養極品 ×%s。",
			st.Level, price, pond.Capacity(level), pond.EpicLimit(level), pond.GrowBonusPct(level), pctFactorText(pond.PremiumPct(level))),
			farm.Gold, -price, "", 0), nil
	})
}

// pctFactor 170 → "1.7"（百分比換成倍數）。
func pctFactorText(pct int) string { return fmt.Sprintf("%g", float64(pct)/100) }

// PutFishInPond 把魚簍裡的魚放進魚塘（一次最多放到滿）。
func (f *FishingService) PutFishInPond(ctx context.Context, playerID int64, fishIDs []int64) (ActionResult, error) {
	if len(fishIDs) == 0 {
		return ActionResult{}, gameerr.Validationf("請選擇要放進魚塘的魚。")
	}
	if len(fishIDs) > fishing.SellMaxPerAction {
		return ActionResult{}, gameerr.Validationf("一次最多放 %d 隻魚。", fishing.SellMaxPerAction)
	}
	for _, id := range fishIDs {
		if id <= 0 {
			return ActionResult{}, gameerr.Validationf("魚的編號不正確。")
		}
	}
	fishIDs = slices.Compact(slices.Sorted(slices.Values(fishIDs)))
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.Level < pond.UnlockLevel() {
			return ActionResult{}, pondLocked(farm)
		}
		now := f.now()
		tool, st, list, _, _, err := f.settlePond(ctx, t, farm, now)
		if err != nil {
			return ActionResult{}, err
		}
		if !st.Built() {
			return ActionResult{}, gameerr.Validationf("還沒有魚塘，先在釣魚中心建造魚塘。")
		}
		if _, err := f.settleAutoFish(ctx, t, farm); err != nil {
			return ActionResult{}, err
		}
		rows, err := fishrepo.LockUnsold(ctx, t, farm.ID, fishIDs) // 只鎖得到自己魚簍裡的魚
		if err != nil {
			return ActionResult{}, err
		}
		if len(rows) != len(fishIDs) {
			return ActionResult{}, gameerr.Validationf("有些魚已經賣掉或不在你的魚簍裡，請重新整理後再試。")
		}
		types, err := fishrepo.ListFishTypes(ctx, t, false)
		if err != nil {
			return ActionResult{}, err
		}
		typeOf := map[string]models.FishType{}
		for _, ft := range types {
			typeOf[ft.Code] = ft
		}
		level := st.Effective()
		if free := pond.Capacity(level) - len(list); len(rows) > free {
			return ActionResult{}, gameerr.Validationf("魚塘只剩 %d 個位子（%d/%d），升級魚塘可以放更多。", max(0, free), len(list), pond.Capacity(level))
		}
		epics := pond.CountRarity(list, "epic")
		var names []string
		for _, r := range rows {
			ft := typeOf[r.FishCode]
			if reason := pond.PlaceError(r.Rarity, len(ft.RequiredBaits) > 0); reason != "" {
				return ActionResult{}, gameerr.Validationf("%s：%s", ft.Name, reason)
			}
			if r.Rarity == "epic" {
				if epics >= pond.EpicLimit(level) {
					return ActionResult{}, gameerr.Validationf("🐡 史詩魚太大了，這個魚塘最多養 %d 隻（升級後可以多放）。", pond.EpicLimit(level))
				}
				epics++
			}
			names = append(names, ft.Name)
			st.Placed[r.ID] = now
		}
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if _, err := t.Exec(ctx, "UPDATE farm_fish SET pond_weight_g = GREATEST(weight_g, 1) WHERE farm_id = $1 AND id = ANY($2) AND "+
			fishrepo.InBasket(""), farm.ID, ids); err != nil {
			return ActionResult{}, err
		}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, pond.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "🐟 放了 %d 隻魚進魚塘（%s），魚塘 %d/%d。", len(rows), strings.Join(uniq(names), "、"), len(list)+len(rows), pond.Capacity(level))
		if st.Feed == 0 {
			msg += txf(ctx, "記得加飼料，沒吃到飼料的魚不會長大。")
		}
		return result(msg, farm.Gold, 0, "", len(rows)), nil
	})
}

func uniq(list []string) []string {
	var out []string
	for _, x := range list {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// FillPondFeed 把飼料槽補滿：先用素材背包裡的魚飼料，不夠的部分用目前的價格買（金幣不夠時買得起多少就補多少）。
func (f *FishingService) FillPondFeed(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := f.now()
		tool, st, _, _, _, err := f.settlePond(ctx, t, farm, now)
		if err != nil {
			return ActionResult{}, err
		}
		if !st.Built() {
			return ActionResult{}, gameerr.Validationf("還沒有魚塘，先在釣魚中心建造魚塘。")
		}
		need := pond.FeedCapacity(st.Effective()) - st.Feed
		if need <= 0 {
			return ActionResult{}, gameerr.Validationf("飼料槽已經滿了（%d 份）。", st.Feed)
		}
		have, err := inventory.GetQuantity(ctx, t, farm.ID, balance.ItemFishFeed, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		take := min(have, need)
		price := balance.ItemPrice(balance.ItemFishFeed)
		buy := need - take
		if price > 0 {
			buy = min(buy, farm.Gold/price)
		}
		if take+buy == 0 {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "素材背包沒有魚飼料，金幣也不夠買（每份 %d 金幣）。", price)
		}
		if take > 0 {
			ok, err := inventory.Remove(ctx, t, farm.ID, balance.ItemFishFeed, take, "normal", "none")
			if err != nil {
				return ActionResult{}, err
			}
			if !ok {
				return ActionResult{}, gameerr.Validationf("魚飼料數量不足，請重新整理後再試。")
			}
		}
		cost := buy * price
		if buy > 0 {
			farm.Gold -= cost
			if err := f.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
				return ActionResult{}, err
			}
			if err := f.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("購買 %d 份魚飼料放進魚塘，金幣 -%d", buy, cost),
				rec{ItemCode: balance.ItemFishFeed, Quantity: buy, GoldDelta: -cost}); err != nil {
				return ActionResult{}, err
			}
		}
		st.Feed += take + buy
		if err := saveSettingsTool(ctx, t, farm.ID, tool, pond.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "🌾 飼料槽補了 %d 份（%d/%d）", take+buy, st.Feed, pond.FeedCapacity(st.Effective()))
		switch {
		case take > 0 && buy > 0:
			msg += txf(ctx, "：用了素材背包的 %d 份，另外買了 %d 份（%d 金幣）。", take, buy, cost)
		case buy > 0:
			msg += txf(ctx, "：買了 %d 份（%d 金幣）。", buy, cost)
		default:
			msg += txf(ctx, "：用了素材背包的 %d 份。", take)
		}
		if st.Feed < pond.FeedCapacity(st.Effective()) {
			msg += txf(ctx, "金幣不夠，沒有補滿。")
		}
		return result(msg, farm.Gold, -cost, balance.ItemFishFeed, take+buy), nil
	})
}

// SellPondFish 賣出魚塘裡的魚：fishIDs 為 nil 時賣出全部池養極品。
func (f *FishingService) SellPondFish(ctx context.Context, playerID int64, fishIDs []int64) (ActionResult, error) {
	if fishIDs != nil {
		if len(fishIDs) == 0 {
			return ActionResult{}, gameerr.Validationf("請選擇要賣的魚。")
		}
		if len(fishIDs) > fishing.SellMaxPerAction {
			return ActionResult{}, gameerr.Validationf("一次最多賣 %d 隻魚。", fishing.SellMaxPerAction)
		}
		fishIDs = slices.Compact(slices.Sorted(slices.Values(fishIDs)))
	}
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := f.now()
		tool, st, list, byID, _, err := f.settlePond(ctx, t, farm, now)
		if err != nil {
			return ActionResult{}, err
		}
		if !st.Built() {
			return ActionResult{}, gameerr.Validationf("還沒有魚塘。")
		}
		level := st.Effective()
		var chosen []pond.Fish
		for _, x := range list {
			if fishIDs == nil {
				if x.Premium(now) {
					chosen = append(chosen, x)
				}
			} else if slices.Contains(fishIDs, x.ID) {
				chosen = append(chosen, x)
			}
		}
		if fishIDs != nil && len(chosen) != len(fishIDs) {
			return ActionResult{}, gameerr.Validationf("有些魚已經賣掉或不在你的魚塘裡，請重新整理後再試。")
		}
		if len(chosen) == 0 {
			return ActionResult{}, gameerr.Validationf("魚塘裡還沒有長滿的魚（池養極品）。")
		}
		total, premiums := 0, 0
		ids := make([]int64, len(chosen))
		prices := make([]int, len(chosen))
		var names []string
		for i, x := range chosen {
			p, premium := x.Price(level, now)
			if premium {
				premiums++
			}
			ids[i], prices[i] = x.ID, p
			total += p
			names = append(names, byID[x.ID].Type.Name)
			delete(st.Placed, x.ID)
		}
		tag, err := t.Exec(ctx, `UPDATE farm_fish f SET sold_at = $2, sold_price = v.p
			FROM (SELECT unnest($3::bigint[]) AS id, unnest($4::int[]) AS p) v
			WHERE f.id = v.id AND f.farm_id = $1 AND `+fishrepo.InPond("f"), farm.ID, now, ids, prices)
		if err != nil {
			return ActionResult{}, err
		}
		if int(tag.RowsAffected()) != len(ids) {
			return ActionResult{}, gameerr.Validationf("有些魚已經賣掉，請重新整理後再試。")
		}
		farm.Gold += total
		if err := f.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		r := rec{Quantity: len(chosen), GoldDelta: total}
		if len(chosen) == 1 {
			r.ItemCode, r.UnitPrice = chosen[0].Code, &prices[0]
		}
		if err := f.record(ctx, t, farm, rules.EvFishSell, truncRunes(fmt.Sprintf("販售魚塘的 %d 隻魚（%s，池養極品 %d 隻），獲得 %d 金幣",
			len(chosen), strings.Join(uniq(names), "、"), premiums, total), 200), r); err != nil {
			return ActionResult{}, err
		}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, pond.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "💰 賣出魚塘的 %d 隻魚，獲得 %d 金幣。", len(chosen), total)
		if premiums > 0 {
			msg += txf(ctx, "其中 %d 隻是池養極品 ✨。", premiums)
		}
		return result(msg, farm.Gold, total, "", len(chosen)), nil
	})
}
