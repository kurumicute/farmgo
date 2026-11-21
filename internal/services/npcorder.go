package services

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/npcorder"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/game/treasure"
	"farmgm/internal/game/weather"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/ranch"
)

// NPC 限時訂單（Go 版新增，2026-11-20，規則見 game/npcorder）。狀態存在 farm_tools 的 settings；
// 交貨記成 sell（把東西賣給 NPC，也會算進販賣類的群組任務與競賽），換單費記成 buy_item，都不需要 Migration。

// orderMutations 交貨時收的突變：和加工原料相同（巨型、快速生長、水晶的售價與普通相同），黃金、珍稀變種不收。
var orderMutations = CraftMutations

// orderEligible 這座農場現在做得出來的物品 → 基準售價（當季作物、已蓋設施的加工品、有養的動物產品、已開放的菇類）。
func (r *RanchService) orderEligible(ctx context.Context, t *db.Tx, farm *models.Farm) (map[string]int, error) {
	prices, err := r.unitSellPrices(ctx, t)
	if err != nil {
		return nil, err
	}
	now := r.now()
	out := map[string]int{}
	add := func(code string) {
		if prices[code] > 0 {
			out[code] = prices[code]
		}
	}
	cropList, err := crops.ListAll(ctx, t, true)
	if err != nil {
		return nil, err
	}
	for _, c := range cropList {
		if c.ShopBuyable && seasons.Available(c.Seasons, c.HourStart, c.HourEnd, now) {
			add(c.Code)
		}
	}
	levels, err := ranch.BuildingLevels(ctx, t, farm.ID)
	if err != nil {
		return nil, err
	}
	for _, code := range balance.RecipeOrder {
		if _, built := levels[balance.Recipes[code].Building]; built {
			add(balance.Recipes[code].Output)
		}
	}
	if farm.Level >= mushroom.Unlock() {
		for _, code := range mushroom.SpeciesOrder {
			add(code)
		}
	}
	owned, err := ranch.ListAnimals(ctx, t, farm.ID, false)
	if err != nil {
		return nil, err
	}
	if len(owned) > 0 {
		types, err := animals.ByCode(ctx, t)
		if err != nil {
			return nil, err
		}
		for _, a := range owned {
			add(types[a.AnimalCode].ProductCode)
		}
	}
	return out, nil
}

// orderStock 每種物品可以拿來交貨的數量（任何品質、orderMutations）。
func orderStock(ctx context.Context, t *db.Tx, farmID int64) (map[string]int, error) {
	list, err := inventory.ListItems(ctx, t, farmID)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, it := range list {
		if slices.Contains(orderMutations, it.Mutation) {
			out[it.ItemCode] += it.Quantity
		}
	}
	return out, nil
}

// removeOrderStock 先扣品質最低的（普通 → 傳說），同品質先扣無突變；不夠時回傳 false（呼叫端應回滾）。
func removeOrderStock(ctx context.Context, t *db.Tx, farmID int64, code string, quantity int) (bool, error) {
	left := quantity
	for _, q := range models.QualityCodes {
		for _, m := range orderMutations {
			if left == 0 {
				return true, nil
			}
			n, err := inventory.GetQuantity(ctx, t, farmID, code, q, m)
			if err != nil {
				return false, err
			}
			if take := min(n, left); take > 0 {
				ok, err := inventory.Remove(ctx, t, farmID, code, take, q, m)
				if err != nil || !ok {
					return false, err
				}
				left -= take
			}
		}
	}
	return left == 0, nil
}

// lockedOrders 鎖定農場後讀出並整理訂單（移除逾時、到時間補單）；farm 必須已鎖定。
func (r *RanchService) lockedOrders(ctx context.Context, t *db.Tx, farm *models.Farm) (*models.FarmTool, npcorder.State, map[string]int, error) {
	tool, err := settingsTool(ctx, t, farm.ID, npcorder.ToolCode, true)
	if err != nil {
		return nil, npcorder.State{}, nil, err
	}
	eligible, err := r.orderEligible(ctx, t, farm)
	if err != nil {
		return nil, npcorder.State{}, nil, err
	}
	st := npcorder.Parse(toolSettings(tool))
	st.Refresh(r.now(), eligible, farm.Level, r.TreasureRng)
	return tool, st, eligible, nil
}

func (r *RanchService) saveOrders(ctx context.Context, t *db.Tx, farmID int64, tool *models.FarmTool, st npcorder.State) error {
	return saveSettingsTool(ctx, t, farmID, tool, npcorder.ToolCode, st.Encode(), r.now())
}

// NpcOrderLine 訂單需要的一種物品。
type NpcOrderLine struct {
	Code, Name string
	Qty, Have  int
}

// NpcOrderView 一張訂單。
type NpcOrderView struct {
	ID      int
	NPC     npcorder.NPC
	Items   []NpcOrderLine
	Reward  int
	Value   int // 物品照目前售價直接賣的總值（比較用）
	Expires time.Time
	Ready   bool // 物品都夠，可以交貨
}

// NpcOrdersView NPC 訂單頁。
type NpcOrdersView struct {
	Now       time.Time
	Orders    []NpcOrderView
	RefillAt  time.Time
	RerollFee int // 下一次換單的費用（0 = 免費）
	Streak    int
	Gold      int
	Rainbow   bool // 彩虹日（碎片掉率加倍）
}

// NpcOrders 訂單頁；需要補單或移除逾時訂單時順便寫回（第一次打開也會建立）。
func (r *RanchService) NpcOrders(ctx context.Context, playerID int64) (NpcOrdersView, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (NpcOrdersView, error) {
		farm, err := r.farmOf(ctx, t, playerID)
		if err != nil {
			return NpcOrdersView{}, err
		}
		now := r.now()
		tool, err := settingsTool(ctx, t, farm.ID, npcorder.ToolCode, false)
		if err != nil {
			return NpcOrdersView{}, err
		}
		st := npcorder.Parse(toolSettings(tool))
		if expired := slices.ContainsFunc(st.Orders, func(o npcorder.Order) bool { return !now.Before(o.Expires) }); expired || !now.Before(st.RefillAt) {
			if farm, err = r.lockedFarm(ctx, t, playerID); err != nil {
				return NpcOrdersView{}, err
			}
			if tool, st, _, err = r.lockedOrders(ctx, t, farm); err != nil {
				return NpcOrdersView{}, err
			}
			if err := r.saveOrders(ctx, t, farm.ID, tool, st); err != nil {
				return NpcOrdersView{}, err
			}
		}
		stock, err := orderStock(ctx, t, farm.ID)
		if err != nil {
			return NpcOrdersView{}, err
		}
		prices, err := r.unitSellPrices(ctx, t)
		if err != nil {
			return NpcOrdersView{}, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return NpcOrdersView{}, err
		}
		v := NpcOrdersView{Now: now, RefillAt: st.RefillAt, RerollFee: st.RerollFeeToday(weather.DayKey(now)),
			Streak: st.Streak, Gold: farm.Gold, Rainbow: weather.HasLuck(now, weather.LuckRainbow)}
		for _, o := range st.Orders {
			ov := NpcOrderView{ID: o.ID, NPC: npcorder.NPCs[o.NPC], Reward: o.Reward, Expires: o.Expires, Ready: true,
				Value: npcorder.Value(o.Items, prices)}
			for _, l := range o.Items {
				ov.Items = append(ov.Items, NpcOrderLine{Code: l.Code, Name: ItemName(l.Code, names), Qty: l.Qty, Have: stock[l.Code]})
				if stock[l.Code] < l.Qty {
					ov.Ready = false
				}
			}
			v.Orders = append(v.Orders, ov)
		}
		return v, nil
	})
}

// DeliverNpcOrder 交貨：扣物品（先扣品質最低的）、發報酬；連續完成 StreakTarget 張時多發 StreakBonusPct%，另有機率掉藏寶圖碎片。
func (r *RanchService) DeliverNpcOrder(ctx context.Context, playerID int64, orderID int) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, st, _, err := r.lockedOrders(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		i := st.Find(orderID)
		if i < 0 {
			// 逾時的訂單在 lockedOrders 已經移除；回報錯誤時整理結果一起回滾，下次打開頁面再整理
			return ActionResult{}, gameerr.NotFoundf("這張訂單已經過期或交過了。")
		}
		o := st.Orders[i]
		npc := npcorder.NPCs[o.NPC]
		stock, err := orderStock(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		var lacking, parts []string
		for _, l := range o.Items {
			if stock[l.Code] < l.Qty {
				lacking = append(lacking, txf(ctx, "%s 需要 %d、只有 %d", ItemName(l.Code, names), l.Qty, stock[l.Code]))
			}
			parts = append(parts, fmt.Sprintf("%s ×%d", ItemName(l.Code, names), l.Qty))
		}
		if len(lacking) > 0 {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "物品不足：%s。", strings.Join(lacking, "；"))
		}
		for _, l := range o.Items {
			ok, err := removeOrderStock(ctx, t, farm.ID, l.Code, l.Qty)
			if err != nil {
				return ActionResult{}, err
			}
			if !ok {
				return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "物品不足：%s。", ItemName(l.Code, names))
			}
		}
		st.Streak++
		bonus := 0
		if st.Streak >= npcorder.StreakTarget {
			bonus = o.Reward * npcorder.StreakBonusPct / 100
			st.Streak = 0
		}
		total := o.Reward + bonus
		farm.Gold += total
		if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		got := strings.Join(parts, "、")
		first := o.Items[0]
		if err := r.record(ctx, t, farm, rules.EvSell,
			truncRunes(fmt.Sprintf("NPC 訂單 #%d：交給%s %s，金幣 +%d", o.ID, npc.Name, got, total), 200),
			rec{ItemCode: first.Code, Quantity: first.Qty, GoldDelta: total}); err != nil {
			return ActionResult{}, err
		}
		st.Orders = slices.Delete(st.Orders, i, i+1)
		if err := r.saveOrders(ctx, t, farm.ID, tool, st); err != nil {
			return ActionResult{}, err
		}
		piece, err := r.grantFragment(ctx, t, farm, treasure.SourceOrder)
		if err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "%s %s：「謝謝你！」交出 %s，得到 💰 %d 金幣。", npc.Emoji, txt(ctx, npc.Name), got, o.Reward)
		if bonus > 0 {
			msg += txf(ctx, "\n🔥 連續完成 %d 張訂單，額外獎勵 %d 金幣！", npcorder.StreakTarget, bonus)
		}
		return result(msg+piece, farm.Gold, total, first.Code, first.Qty), nil
	})
}

// RerollNpcOrder 換掉一張訂單：每天免費 FreeRerolls 次，之後每次 RerollFee 金幣。
func (r *RanchService) RerollNpcOrder(ctx context.Context, playerID int64, orderID int) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, st, eligible, err := r.lockedOrders(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		i := st.Find(orderID)
		if i < 0 {
			return ActionResult{}, gameerr.NotFoundf("這張訂單已經過期或交過了。")
		}
		now := r.now()
		day := weather.DayKey(now)
		fee := st.RerollFeeToday(day)
		if farm.Gold < fee {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足，換訂單需要 %d 金幣。", fee)
		}
		o, ok := npcorder.Generate(st.NextID+1, eligible, farm.Level, now, r.TreasureRng)
		if !ok {
			return ActionResult{}, gameerr.Validationf("現在沒有其他客人，晚點再來看看。")
		}
		old := npcorder.NPCs[st.Orders[i].NPC]
		st.NextID++
		st.Orders[i] = o
		st.Rerolls = st.RerollsToday(day) + 1
		st.RerollDay = day
		if fee > 0 {
			farm.Gold -= fee
			if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
				return ActionResult{}, err
			}
			if err := r.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("NPC 訂單換單費，金幣 -%d", fee),
				rec{GoldDelta: -fee}); err != nil {
				return ActionResult{}, err
			}
		}
		if err := r.saveOrders(ctx, t, farm.ID, tool, st); err != nil {
			return ActionResult{}, err
		}
		npc := npcorder.NPCs[o.NPC]
		msg := txf(ctx, "👋 %s %s離開了，%s %s帶著新訂單來了。", old.Emoji, txt(ctx, old.Name), npc.Emoji, txt(ctx, npc.Name))
		if fee > 0 {
			msg += txf(ctx, "（換單費 %d 金幣）", fee)
		} else {
			msg += txt(ctx, "（今天的免費換單）")
		}
		return result(msg, farm.Gold, -fee, "", 0), nil
	})
}
