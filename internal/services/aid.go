package services

// 送禮與互助（Go 版新增，2026-11-17；規則與數值在 game/aid）：同群組的玩家每天可以互送一份作物、互相幫忙澆水，
// 雙方都拿少量金幣。雙方都要達到等級門檻與農場天數；每天的送出、收到次數各有上限，同一對玩家同一種互助一天只能一次。

import (
	"context"
	"fmt"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/aid"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	aidrepo "farmgm/internal/repositories/aid"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/players"
)

// AidTargetView 可以互助的對象。
type AidTargetView struct {
	PlayerID     int64
	Name         string
	FarmName     string
	Level        int
	Waiting      int    // 等待澆水的土地數
	Blocked      string // 對方不符合門檻的原因；"" = 可以
	GiftedToday  bool   // 今天已經送過禮給他
	WateredToday bool   // 今天已經幫他澆過水
}

// AidGiftItem 可以當禮物的作物（普通品質、沒有突變）。
type AidGiftItem struct {
	Code      string
	Name      string
	Quantity  int // 持有數量
	Limit     int // 一份最多送幾個（數量與價值上限，不超過持有數量）
	UnitPrice int // 作物基本售價
}

// AidView 送禮與互助畫面。
type AidView struct {
	Config        aid.Config
	Blocked       string // 自己不符合門檻的原因；"" = 可以
	GiftSent      int
	GiftReceived  int
	WaterSent     int
	WaterReceived int
	Targets       []AidTargetView
	Items         []AidGiftItem
}

// aidBlocked 不符合等級或農場天數門檻的原因（who 例如「你」「阿明」）；符合時回傳 ""。
func aidBlocked(who string, level int, createdAt, now time.Time) string {
	c := aid.Current()
	if level < c.MinLevel {
		return fmt.Sprintf("%s的農場要 Lv.%d 才能送禮與互助（目前 Lv.%d）", who, c.MinLevel, level)
	}
	if need := createdAt.Add(time.Duration(c.MinFarmDays) * 24 * time.Hour); now.Before(need) {
		return fmt.Sprintf("%s的農場要建立滿 %d 天才能送禮與互助", who, c.MinFarmDays)
	}
	return ""
}

// AidOverview 送禮與互助畫面：今天的次數、同群組的對象與可以送的作物。
func (s *GameService) AidOverview(ctx context.Context, groupID, playerID int64) (AidView, error) {
	return inTx(ctx, s, func(t *db.Tx) (AidView, error) {
		if err := requireMember(ctx, t, groupID, playerID); err != nil {
			return AidView{}, err
		}
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return AidView{}, err
		}
		now := s.now()
		day := taipeiDay(now)
		v := AidView{Config: *aid.Current(), Blocked: aidBlocked("你", farm.Level, farm.CreatedAt, now)}
		counts, err := aidrepo.Counts(ctx, t, farm.ID, day)
		if err != nil {
			return AidView{}, err
		}
		v.GiftSent, v.GiftReceived, v.WaterSent, v.WaterReceived = counts.GiftSent, counts.GiftReceived, counts.WaterSent, counts.WaterReceived
		targets, err := aidrepo.Targets(ctx, t, groupID, playerID)
		if err != nil {
			return AidView{}, err
		}
		for _, r := range targets {
			v.Targets = append(v.Targets, AidTargetView{
				PlayerID: r.PlayerID, Name: r.PlayerName, FarmName: r.FarmName, Level: r.Level, Waiting: r.Waiting,
				Blocked:     aidBlocked(r.PlayerName, r.Level, r.CreatedAt, now),
				GiftedToday: counts.GiftedTo[r.FarmID], WateredToday: counts.WateredFor[r.FarmID],
			})
		}
		items, err := inventory.ListItems(ctx, t, farm.ID)
		if err != nil {
			return AidView{}, err
		}
		cropsByCode, err := s.giftCrops(ctx, t)
		if err != nil {
			return AidView{}, err
		}
		for _, it := range items {
			c, ok := cropsByCode[it.ItemCode]
			if !ok || it.Quality != "normal" || it.Mutation != "none" || it.Quantity <= 0 {
				continue
			}
			if limit := min(aid.GiftLimit(c.SellPrice), it.Quantity); limit > 0 {
				v.Items = append(v.Items, AidGiftItem{c.Code, c.Name, it.Quantity, limit, c.SellPrice})
			}
		}
		return v, nil
	})
}

// giftCrops 可以當禮物的作物（依代碼；珍稀變種的代碼不算，變種一律以突變區分）。
func (s *GameService) giftCrops(ctx context.Context, t *db.Tx) (map[string]models.CropType, error) {
	all, err := crops.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.CropType, len(all))
	for _, c := range all {
		out[c.Code] = c
	}
	return out, nil
}

// aidPair 確認雙方都在群組、都有農場、都符合門檻，並依 farm_id 由小到大鎖定雙方農場（避免互相送禮時死結）。
func (s *GameService) aidPair(ctx context.Context, t *db.Tx, groupID, playerID, targetPlayerID int64) (*models.Farm, *models.Farm, string, string, error) {
	if playerID == targetPlayerID {
		return nil, nil, "", "", gameerr.Validationf("不能送禮或幫忙自己。")
	}
	if err := requireMember(ctx, t, groupID, playerID); err != nil {
		return nil, nil, "", "", err
	}
	member, err := groups.IsMember(ctx, t, groupID, targetPlayerID)
	if err != nil {
		return nil, nil, "", "", err
	}
	if !member {
		return nil, nil, "", "", gameerr.Validationf("對方不在這個伺服器的群組中。")
	}
	self, err := s.farmOf(ctx, t, playerID)
	if err != nil {
		return nil, nil, "", "", err
	}
	other, err := farms.GetByPlayer(ctx, t, targetPlayerID, false)
	if err != nil {
		return nil, nil, "", "", err
	}
	if other == nil {
		return nil, nil, "", "", gameerr.NotFoundf("對方還沒有農場。")
	}
	var me, them *models.Farm
	for _, id := range []int64{min(self.ID, other.ID), max(self.ID, other.ID)} {
		f, err := farms.GetByID(ctx, t, id, true)
		if err != nil {
			return nil, nil, "", "", err
		}
		if id == self.ID {
			me = f
		} else {
			them = f
		}
	}
	names := map[int64]string{}
	for _, id := range []int64{playerID, targetPlayerID} {
		p, err := players.Get(ctx, t, id, false)
		if err != nil {
			return nil, nil, "", "", err
		}
		names[id] = p.DisplayName
	}
	myName, theirName := names[playerID], names[targetPlayerID]
	now := s.now()
	if why := aidBlocked("你", me.Level, me.CreatedAt, now); why != "" {
		return nil, nil, "", "", gameerr.Validationf("%s。", why)
	}
	if why := aidBlocked(theirName, them.Level, them.CreatedAt, now); why != "" {
		return nil, nil, "", "", gameerr.Validationf("%s。", why)
	}
	return me, them, myName, theirName, nil
}

// aidReward 雙方各得 gold 金幣並各寫一筆交易紀錄。
func (s *GameService) aidReward(ctx context.Context, t *db.Tx, ev rules.EventType, gold int,
	me *models.Farm, myDesc string, myRec rec, them *models.Farm, theirDesc string, theirRec rec) error {
	for _, side := range []struct {
		farm *models.Farm
		desc string
		r    rec
	}{{me, myDesc, myRec}, {them, theirDesc, theirRec}} {
		if gold > 0 {
			side.farm.Gold += gold
			if err := s.saveFarm(ctx, t, side.farm, db.C("gold", side.farm.Gold)); err != nil {
				return err
			}
		}
		side.r.GoldDelta = gold
		if err := s.record(ctx, t, side.farm, ev, truncRunes(side.desc, 200), side.r); err != nil {
			return err
		}
	}
	return nil
}

// SendGift 送一份作物（普通品質、沒有突變）給同群組的玩家，雙方各得金幣。
func (s *GameService) SendGift(ctx context.Context, groupID, playerID, targetPlayerID int64, itemCode string, quantity int) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		cfg := aid.Current()
		if cfg.GiftSendPerDay == 0 || cfg.GiftReceivePerDay == 0 {
			return ActionResult{}, gameerr.Validationf("送禮目前沒有開放。")
		}
		me, them, myName, theirName, err := s.aidPair(ctx, t, groupID, playerID, targetPlayerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		day := taipeiDay(now)
		mine, err := aidrepo.Counts(ctx, t, me.ID, day)
		if err != nil {
			return ActionResult{}, err
		}
		if mine.GiftSent >= cfg.GiftSendPerDay {
			return ActionResult{}, gameerr.Validationf("今天已經送過 %d 份禮物了（每天最多 %d 份）。", mine.GiftSent, cfg.GiftSendPerDay)
		}
		if mine.GiftedTo[them.ID] {
			return ActionResult{}, gameerr.Validationf("今天已經送過禮物給 %s 了。", theirName)
		}
		theirs, err := aidrepo.Counts(ctx, t, them.ID, day)
		if err != nil {
			return ActionResult{}, err
		}
		if theirs.GiftReceived >= cfg.GiftReceivePerDay {
			return ActionResult{}, gameerr.Validationf("%s今天已經收到 %d 份禮物了（每人每天最多收 %d 份）。", theirName, theirs.GiftReceived, cfg.GiftReceivePerDay)
		}
		all, err := s.giftCrops(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		crop, ok := all[itemCode]
		if !ok {
			return ActionResult{}, gameerr.Validationf("只能送作物。")
		}
		limit := aid.GiftLimit(crop.SellPrice)
		if limit == 0 {
			return ActionResult{}, gameerr.Validationf("%s太貴重了，不能當禮物（一份禮物最多值 %d 金）。", crop.Name, cfg.GiftMaxValue)
		}
		if quantity < 1 || quantity > limit {
			return ActionResult{}, gameerr.Validationf("%s一份最多送 %d 個。", crop.Name, limit)
		}
		if err := s.ensureSpace(ctx, t, them, quantity); err != nil {
			return ActionResult{}, gameerr.Validationf("%s的倉庫放不下了。", theirName)
		}
		removed, err := inventory.Remove(ctx, t, me.ID, crop.Code, quantity, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !removed {
			return ActionResult{}, gameerr.Validationf("倉庫裡普通品質的%s不夠 %d 個。", crop.Name, quantity)
		}
		if err := inventory.Add(ctx, t, them.ID, crop.Code, quantity, "normal", "none"); err != nil {
			return ActionResult{}, err
		}
		if err := aidrepo.Add(ctx, t, "gift", me.ID, them.ID, &groupID, crop.Code, quantity, day, now); err != nil {
			return ActionResult{}, err
		}
		gold := cfg.GiftRewardGold
		if err := s.aidReward(ctx, t, rules.EvAidGift, gold,
			me, fmt.Sprintf("送 %d 個%s給 %s", quantity, crop.Name, theirName),
			rec{ItemCode: crop.Code, Quantity: quantity, Quality: "normal", Mutation: "none"},
			them, fmt.Sprintf("收到 %s 送的 %d 個%s", myName, quantity, crop.Name),
			rec{ItemCode: crop.Code, Quantity: quantity, Quality: "normal", Mutation: "none"}); err != nil {
			return ActionResult{}, err
		}
		msg := fmt.Sprintf("🎁 已送 %d 個%s給 %s", quantity, crop.Name, theirName)
		if gold > 0 {
			msg += fmt.Sprintf("，雙方各得 %d 金幣", gold)
		}
		return result(msg+fmt.Sprintf("（今天已送 %d／%d 份）。", mine.GiftSent+1, cfg.GiftSendPerDay), me.Gold, gold, crop.Code, quantity), nil
	})
}

// HelpWater 幫同群組的玩家把所有等待澆水的土地澆水（算一次），雙方各得金幣。
func (s *GameService) HelpWater(ctx context.Context, groupID, playerID, targetPlayerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		cfg := aid.Current()
		if cfg.WaterSendPerDay == 0 || cfg.WaterReceivePerDay == 0 {
			return ActionResult{}, gameerr.Validationf("幫忙澆水目前沒有開放。")
		}
		me, them, myName, theirName, err := s.aidPair(ctx, t, groupID, playerID, targetPlayerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		day := taipeiDay(now)
		mine, err := aidrepo.Counts(ctx, t, me.ID, day)
		if err != nil {
			return ActionResult{}, err
		}
		if mine.WaterSent >= cfg.WaterSendPerDay {
			return ActionResult{}, gameerr.Validationf("今天已經幫 %d 位玩家澆過水了（每天最多 %d 位）。", mine.WaterSent, cfg.WaterSendPerDay)
		}
		if mine.WateredFor[them.ID] {
			return ActionResult{}, gameerr.Validationf("今天已經幫 %s 澆過水了。", theirName)
		}
		theirs, err := aidrepo.Counts(ctx, t, them.ID, day)
		if err != nil {
			return ActionResult{}, err
		}
		if theirs.WaterReceived >= cfg.WaterReceivePerDay {
			return ActionResult{}, gameerr.Validationf("%s今天已經被 %d 位玩家幫忙澆過水了（每人每天最多 %d 位）。", theirName, theirs.WaterReceived, cfg.WaterReceivePerDay)
		}
		plots, err := s.plotsInState(ctx, t, them, rules.PlotWaitingWater)
		if err != nil {
			return ActionResult{}, err
		}
		if len(plots) == 0 {
			return ActionResult{}, gameerr.Validationf("%s沒有需要澆水的土地。", theirName)
		}
		for _, p := range plots {
			crop, err := crops.Get(ctx, t, *p.CropTypeID)
			if err != nil {
				return ActionResult{}, err
			}
			if _, err := s.water(ctx, t, them, p, crop, now, false, nil); err != nil {
				return ActionResult{}, err
			}
		}
		n := len(plots)
		if err := aidrepo.Add(ctx, t, "water", me.ID, them.ID, &groupID, "", n, day, now); err != nil {
			return ActionResult{}, err
		}
		gold := cfg.WaterRewardGold
		if err := s.aidReward(ctx, t, rules.EvAidWater, gold,
			me, fmt.Sprintf("幫 %s 澆了 %d 格", theirName, n), rec{},
			them, fmt.Sprintf("%s 幫忙澆了 %d 格", myName, n), rec{}); err != nil {
			return ActionResult{}, err
		}
		msg := fmt.Sprintf("💧 已幫 %s 澆了 %d 格", theirName, n)
		if gold > 0 {
			msg += fmt.Sprintf("，雙方各得 %d 金幣", gold)
		}
		return result(msg+fmt.Sprintf("（今天已幫 %d／%d 位）。", mine.WaterSent+1, cfg.WaterSendPerDay), me.Gold, gold, "", 0), nil
	})
}
