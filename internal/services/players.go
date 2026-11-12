package services

// 玩家、農場、所屬群組（對應 Python GameService 的玩家／農場段落）。

import (
	"context"
	"fmt"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/pets"
	"farmgm/internal/repositories/players"
)

func cropInfo(c *models.CropType, now *time.Time) CropInfo {
	info := CropInfo{
		SeasonText:      seasons.Describe(c.Seasons, c.HourStart, c.HourEnd),
		InSeason:        now == nil || seasons.Available(c.Seasons, c.HourStart, c.HourEnd, *now),
		Code:            c.Code,
		Name:            c.Name,
		SeedPrice:       c.SeedPrice,
		GrowSeconds:     c.GrowSeconds,
		HarvestQuantity: c.HarvestQuantity,
		SellPrice:       c.SellPrice,
		SeasonSellPct:   100,
		ShopBuyable:     c.ShopBuyable,
	}
	if now != nil { // 目前的市場價格：非當季物以稀為貴（Go 版新增）
		info.SeasonSellPct = seasons.SellPct(c.Seasons, *now)
		info.SellPrice = seasons.SellPrice(c.SellPrice, c.Seasons, *now)
	}
	return info
}

// ---------- 玩家 ----------

// CreatePlayer externalPlatform 與 externalUserID 必須同時為 nil 或同時提供。
func (s *GameService) CreatePlayer(ctx context.Context, displayName string, externalPlatform, externalUserID *string) (PlayerInfo, error) {
	name, err := rules.ValidateName(displayName, "玩家名稱", balance.PlayerNameMaxLen)
	if err != nil {
		return PlayerInfo{}, err
	}
	if (externalPlatform == nil) != (externalUserID == nil) {
		return PlayerInfo{}, gameerr.Validationf("外部平台與外部使用者 ID 必須同時提供。")
	}
	info, err := inTx(ctx, s, func(t *db.Tx) (PlayerInfo, error) {
		p, err := players.Create(ctx, t, name, externalPlatform, externalUserID)
		if err != nil {
			return PlayerInfo{}, err
		}
		return PlayerInfo{p.ID, p.DisplayName, false, externalPlatform, externalUserID}, nil
	})
	if isIntegrity(err) {
		return PlayerInfo{}, gameerr.New(gameerr.AlreadyExists, "此外部帳號已綁定其他玩家。")
	}
	return info, err
}

func (s *GameService) hasFarm(ctx context.Context, t *db.Tx, playerID int64) (bool, error) {
	f, err := farms.GetByPlayer(ctx, t, playerID, false)
	return f != nil, err
}

// GetPlayerByExternal 供 Discord 等平台依外部使用者 ID 找到對應玩家；找不到時回傳 nil。
func (s *GameService) GetPlayerByExternal(ctx context.Context, platform, userID string) (*PlayerInfo, error) {
	return inTx(ctx, s, func(t *db.Tx) (*PlayerInfo, error) {
		p, err := players.GetByExternal(ctx, t, platform, userID)
		if err != nil || p == nil {
			return nil, err
		}
		has, err := s.hasFarm(ctx, t, p.ID)
		if err != nil {
			return nil, err
		}
		return &PlayerInfo{p.ID, p.DisplayName, has, &platform, &userID}, nil
	})
}

func (s *GameService) GetPlayer(ctx context.Context, playerID int64) (PlayerInfo, error) {
	return inTx(ctx, s, func(t *db.Tx) (PlayerInfo, error) {
		p, err := players.Get(ctx, t, playerID, false)
		if err != nil {
			return PlayerInfo{}, err
		}
		if p == nil {
			return PlayerInfo{}, gameerr.NotFoundf("找不到玩家 #%d。", playerID)
		}
		has, err := s.hasFarm(ctx, t, p.ID)
		if err != nil {
			return PlayerInfo{}, err
		}
		return PlayerInfo{p.ID, p.DisplayName, has, p.ExternalPlatform, p.ExternalUserID}, nil
	})
}

// ListPlayers limit 預設 100。
func (s *GameService) ListPlayers(ctx context.Context, limit int) ([]PlayerInfo, error) {
	return inTx(ctx, s, func(t *db.Tx) ([]PlayerInfo, error) {
		ps, err := players.ListAll(ctx, t, limit)
		if err != nil {
			return nil, err
		}
		out := make([]PlayerInfo, 0, len(ps))
		for _, p := range ps {
			has, err := s.hasFarm(ctx, t, p.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, PlayerInfo{p.ID, p.DisplayName, has, p.ExternalPlatform, p.ExternalUserID})
		}
		return out, nil
	})
}

// SearchPlayers 依名稱關鍵字或 ID 搜尋玩家並分頁；回傳 (本頁玩家, 符合總數)。page 預設 1，pageSize 預設 20。
func (s *GameService) SearchPlayers(ctx context.Context, keyword string, page, pageSize int) ([]PlayerInfo, int, error) {
	pageSize = max(1, min(pageSize, 100))
	page = max(1, page)
	keyword = truncRunes(trimSpace(keyword), balance.PlayerNameMaxLen)
	var total int
	rows, err := inTx(ctx, s, func(t *db.Tx) ([]PlayerInfo, error) {
		rows, n, err := players.Search(ctx, t, keyword, pageSize, (page-1)*pageSize)
		if err != nil {
			return nil, err
		}
		total = n
		out := make([]PlayerInfo, len(rows))
		for i, r := range rows {
			p := r.Player
			out[i] = PlayerInfo{p.ID, p.DisplayName, r.HasFarm, p.ExternalPlatform, p.ExternalUserID}
		}
		return out, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ---------- 農場 ----------

// CreateFarm 建立全域農場並固定主要所屬群組；玩家若不是該群組成員會自動加入。farmName 為 "" 時使用「玩家名的農場」。
func (s *GameService) CreateFarm(ctx context.Context, playerID int64, farmName string, homeGroupID *int64) (FarmStatus, error) {
	if homeGroupID == nil {
		return FarmStatus{}, gameerr.Validationf(
			"建立農場必須指定所屬群組：farm start <群組ID>，或先用 group use <群組ID> 選擇群組。")
	}
	gid := *homeGroupID
	st, err := inTx(ctx, s, func(t *db.Tx) (FarmStatus, error) {
		player, err := players.Get(ctx, t, playerID, true)
		if err != nil {
			return FarmStatus{}, err
		}
		if player == nil {
			return FarmStatus{}, gameerr.NotFoundf("找不到玩家 #%d。", playerID)
		}
		if has, err := s.hasFarm(ctx, t, playerID); err != nil || has {
			if err == nil {
				err = gameerr.New(gameerr.AlreadyExists, "這位玩家已經擁有農場。")
			}
			return FarmStatus{}, err
		}
		group, err := groups.Get(ctx, t, gid)
		if err != nil {
			return FarmStatus{}, err
		}
		if group == nil {
			return FarmStatus{}, gameerr.NotFoundf("找不到群組 #%d。", gid)
		}
		if farmName == "" {
			farmName = player.DisplayName + "的農場"
		}
		name, err := rules.ValidateName(farmName, "農場名稱", balance.FarmNameMaxLen)
		if err != nil {
			return FarmStatus{}, err
		}
		farm, err := farms.Create(ctx, t, playerID, name, balance.InitialLevel, balance.InitialGold,
			balance.InitialWarehouseCapacity, gid)
		if err != nil {
			return FarmStatus{}, err
		}
		if _, err := groups.AddMember(ctx, t, gid, playerID); err != nil {
			return FarmStatus{}, err
		}
		if _, err := farms.AddPlots(ctx, t, farm.ID, balance.InitialPlots); err != nil {
			return FarmStatus{}, err
		}
		if err := farmstats.EnsureRow(ctx, t, farm); err != nil {
			return FarmStatus{}, err
		}
		// 初始金幣也記為一筆入帳，使所有交易的 gold_delta 加總等於農場金幣
		r := ctxGroup(gid)
		r.GoldDelta = balance.InitialGold
		if err := s.record(ctx, t, farm, rules.EvCreateFarm,
			fmt.Sprintf("在群組「%s」建立農場「%s」，獲得初始金幣 %d", group.Name, name, balance.InitialGold), r); err != nil {
			return FarmStatus{}, err
		}
		return s.status(ctx, t, farm, player.DisplayName)
	})
	if isIntegrity(err) {
		return FarmStatus{}, gameerr.New(gameerr.AlreadyExists, "這位玩家已經擁有農場。")
	}
	return st, err
}

// HomeGroup 農場主要所屬群組。
type HomeGroup struct {
	ID   int64
	Name string
}

// GetHomeGroup 回傳農場主要所屬群組；舊農場尚未設定時回傳 nil。
func (s *GameService) GetHomeGroup(ctx context.Context, playerID int64) (*HomeGroup, error) {
	return inTx(ctx, s, func(t *db.Tx) (*HomeGroup, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil || farm.HomeGroupID == nil {
			return nil, err
		}
		g, err := groups.Get(ctx, t, *farm.HomeGroupID)
		if err != nil {
			return nil, err
		}
		return &HomeGroup{g.ID, g.Name}, nil
	})
}

// SetHomeGroup 為尚未綁定群組的舊農場做一次性設定；已綁定的農場一律拒絕（資料庫觸發器另有保護）。
func (s *GameService) SetHomeGroup(ctx context.Context, playerID, groupID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.HomeGroupID != nil {
			current, err := groups.Get(ctx, t, *farm.HomeGroupID)
			if err != nil {
				return ActionResult{}, err
			}
			return ActionResult{}, gameerr.New(gameerr.HomeGroupLocked,
				"農場已屬於群組 #%d「%s」，不可更改一次性設定（只適用於尚未綁定群組的舊農場）。"+
					"為避免頻繁切換群組操縱排行，變更所屬群組請使用群組轉移（每 %s 一次）。", current.ID, current.Name, cooldownText())
		}
		group, err := groups.Get(ctx, t, groupID)
		if err != nil {
			return ActionResult{}, err
		}
		if group == nil {
			return ActionResult{}, gameerr.NotFoundf("找不到群組 #%d。", groupID)
		}
		farm.HomeGroupID = &groupID
		if err := s.saveFarm(ctx, t, farm, db.C("home_group_id", groupID)); err != nil {
			return ActionResult{}, err
		}
		if _, err := groups.AddMember(ctx, t, groupID, playerID); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBindGroup,
			fmt.Sprintf("農場設定主要所屬群組為「%s」（一次性設定）", group.Name), ctxGroup(groupID)); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("農場已設定為屬於群組 #%d「%s」；之後只能透過群組轉移變更（每 %s 一次）。",
			group.ID, group.Name, cooldownText()), farm.Gold, 0, "", 0), nil
	})
}

// ---------- 轉移所屬群組（7 天冷卻） ----------

func (s *GameService) transferView(ctx context.Context, t *db.Tx, farm *models.Farm, targetGroupID *int64) (TransferView, error) {
	now := s.now()
	cooldown := time.Duration(balance.GroupTransferCooldownSeconds) * time.Second
	last := farm.LastGroupTransferAt
	var nextAt *time.Time
	remaining := 0
	if last != nil {
		n := last.Add(cooldown)
		nextAt = &n
		remaining = rules.RemainingSeconds(nextAt, now)
	}
	v := TransferView{FarmID: farm.ID, PlayerID: farm.PlayerID, LastTransferAt: last, RemainingSeconds: remaining,
		CanTransfer: remaining <= 0, Now: now}
	if remaining > 0 {
		v.NextTransferAt = nextAt
	}
	if farm.HomeGroupID != nil {
		home, err := groups.Get(ctx, t, *farm.HomeGroupID)
		if err != nil {
			return v, err
		}
		if home != nil {
			v.HomeGroupID, v.HomeGroupName = &home.ID, &home.Name
		}
	}
	if targetGroupID != nil {
		target, err := groups.Get(ctx, t, *targetGroupID)
		if err != nil {
			return v, err
		}
		if target != nil {
			v.TargetGroupID, v.TargetGroupName = &target.ID, &target.Name
			if v.IsMember, err = groups.IsMember(ctx, t, target.ID, farm.PlayerID); err != nil {
				return v, err
			}
		}
	}
	return v, nil
}

// GroupTransferStatus 目前所屬群組、冷卻狀態與（指定時）目標群組；只查詢，不修改。targetGroupID 可為 nil。
func (s *GameService) GroupTransferStatus(ctx context.Context, playerID int64, targetGroupID *int64) (TransferView, error) {
	return inTx(ctx, s, func(t *db.Tx) (TransferView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return TransferView{}, err
		}
		return s.transferView(ctx, t, farm, targetGroupID)
	})
}

func cooldownError(v TransferView) error {
	until := taipeiFmt(*v.NextTransferAt, "2006-01-02 15:04")
	return gameerr.New(gameerr.HomeGroupLocked, "群組轉移冷卻中：每次轉移後需等待 %s，還需要 %s，下次可轉移時間 %s（台灣時間）。",
		cooldownText(), LongDuration(v.RemainingSeconds), until)
}

// applyTransfer 更新農場所屬群組與轉移時間並記錄；回傳原群組名稱（沒有時 nil）。
func (s *GameService) applyTransfer(ctx context.Context, t *db.Tx, farm *models.Farm, group *models.Group, now time.Time,
	description string) (*string, error) {
	var oldName *string
	if farm.HomeGroupID != nil {
		old, err := groups.Get(ctx, t, *farm.HomeGroupID)
		if err != nil {
			return nil, err
		}
		if old != nil {
			oldName = &old.Name
		}
	}
	farm.HomeGroupID = &group.ID
	farm.LastGroupTransferAt = &now
	// 立即送出 UPDATE，讓資料庫觸發器的冷卻檢查在本交易內生效
	if err := s.saveFarm(ctx, t, farm, db.C("home_group_id", group.ID), db.C("last_group_transfer_at", now)); err != nil {
		if isIntegrity(err) {
			return nil, gameerr.New(gameerr.HomeGroupLocked, "群組轉移被資料庫保護拒絕（冷卻中或資料不一致），請稍後再試。")
		}
		return nil, err
	}
	if err := s.record(ctx, t, farm, rules.EvTransferGroup, truncRunes(description, 200), ctxGroup(group.ID)); err != nil {
		return nil, err
	}
	return oldName, nil
}

// TransferHomeGroup 把農場的主要所屬群組轉移到 groupID；只更新同一座農場，不複製或刪除任何資產。
//
// 在鎖定農場列的交易中檢查冷卻，並由資料庫觸發器再次檢查；兩個伺服器同時提交時只有一筆成功。
// 歷史交易、任務、競賽與排名獎勵紀錄不改寫；之後的群組排行立即依新群組計算。
func (s *GameService) TransferHomeGroup(ctx context.Context, playerID, groupID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.HomeGroupID == nil {
			return ActionResult{}, gameerr.Validationf("這座農場尚未設定所屬群組，請先做一次性設定。")
		}
		if *farm.HomeGroupID == groupID {
			return ActionResult{}, gameerr.Validationf("農場已經屬於這個群組，不需要轉移。")
		}
		group, err := groups.Get(ctx, t, groupID)
		if err != nil {
			return ActionResult{}, err
		}
		if group == nil {
			return ActionResult{}, gameerr.NotFoundf("找不到群組 #%d。", groupID)
		}
		member, err := groups.IsMember(ctx, t, groupID, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if !member {
			return ActionResult{}, gameerr.New(gameerr.NotGroupMember, "你不是目標群組的成員，無法把農場轉移過去。")
		}
		view, err := s.transferView(ctx, t, farm, &groupID)
		if err != nil {
			return ActionResult{}, err
		}
		if !view.CanTransfer {
			return ActionResult{}, cooldownError(view)
		}
		now := s.now()
		oldName, err := s.applyTransfer(ctx, t, farm, group, now,
			fmt.Sprintf("農場所屬群組由「%s」轉移到「%s」", pyStr(view.HomeGroupName), group.Name))
		if err != nil {
			return ActionResult{}, err
		}
		next := taipeiFmt(now.Add(time.Duration(balance.GroupTransferCooldownSeconds)*time.Second), "2006-01-02 15:04")
		return result(fmt.Sprintf("農場已從「%s」轉移到「%s」。之後的群組排行與任務、競賽計分都以新群組計算；"+
			"下次可轉移時間 %s（台灣時間）。", pyStr(oldName), group.Name, next), farm.Gold, 0, "", 0), nil
	})
}

// RenameFarm 更改自己農場的名稱，每次花費 balance.RenameFarmCost 金幣（經 record 記錄扣款）。
func (s *GameService) RenameFarm(ctx context.Context, playerID int64, newName string) (ActionResult, error) {
	cost := balance.RenameFarmCost
	name, err := rules.ValidateName(newName, "農場名稱", balance.FarmNameMaxLen)
	if err != nil {
		return ActionResult{}, err
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.Name == name {
			return ActionResult{}, gameerr.Validationf("新名稱和目前的農場名稱相同。")
		}
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：改名需要 %d，目前只有 %d。", cost, farm.Gold)
		}
		oldName := farm.Name
		farm.Name = name
		farm.Gold -= cost
		if err := s.saveFarm(ctx, t, farm, db.C("name", farm.Name), db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvRenameFarm, truncRunes(fmt.Sprintf("農場改名：「%s」→「%s」", oldName, name), 200),
			rec{GoldDelta: -cost}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("農場名稱已從「%s」改為「%s」，花費 %d 金幣。", oldName, name, cost), farm.Gold, -cost, "", 0), nil
	})
}

func (s *GameService) status(ctx context.Context, t *db.Tx, farm *models.Farm, playerName string) (FarmStatus, error) {
	now := s.now()
	plots, err := farms.ListPlots(ctx, t, farm.ID, false)
	if err != nil {
		return FarmStatus{}, err
	}
	infos := make([]PlotInfo, len(plots))
	for i, p := range plots {
		info := PlotInfo{
			PlotNumber:        p.PlotNumber,
			State:             rules.PlotStateAt(p.CropTypeID, p.WateredAt, p.ReadyAt, now),
			PlantedAt:         p.PlantedAt,
			ReadyAt:           p.ReadyAt,
			RemainingSeconds:  rules.RemainingSeconds(p.ReadyAt, now),
			WateredAt:         p.WateredAt,
			RemainingQuantity: p.RemainingQuantity,
			Fertilized:        p.Fertilized,
			Quality:           p.Quality,
			Mutation:          p.Mutation,
		}
		if p.Crop != nil {
			info.CropCode, info.CropName = &p.Crop.Code, &p.Crop.Name
		}
		infos[i] = info
	}
	used, err := inventory.TotalQuantity(ctx, t, farm.ID)
	if err != nil {
		return FarmStatus{}, err
	}
	levels, err := pets.Levels(ctx, t, farm.ID)
	if err != nil {
		return FarmStatus{}, err
	}
	return FarmStatus{
		FarmID: farm.ID, PlayerID: farm.PlayerID, HomeGroupID: farm.HomeGroupID, PlayerName: playerName,
		Name: farm.Name, Level: farm.Level, Gold: farm.Gold, WarehouseUsed: used,
		WarehouseCapacity: farm.WarehouseCapacity, Plots: infos, Now: now, Title: farm.Title, Pets: levels,
	}, nil
}

type statusKey struct{ playerID int64 }

// GetFarmStatus 農場狀態。面板讀取整頁的共用交易中，同一位玩家只查一次（db.ReadMemo，有寫入就清空）；
// 回傳的切片與 map 唯讀。
func (s *GameService) GetFarmStatus(ctx context.Context, playerID int64) (FarmStatus, error) {
	return inTx(ctx, s, func(t *db.Tx) (FarmStatus, error) {
		return db.ReadMemo(t, statusKey{playerID}, func() (FarmStatus, error) {
			farm, err := s.farmOf(ctx, t, playerID)
			if err != nil {
				return FarmStatus{}, err
			}
			p, err := players.Get(ctx, t, farm.PlayerID, false)
			if err != nil {
				return FarmStatus{}, err
			}
			return s.status(ctx, t, farm, p.DisplayName)
		})
	})
}

// pyStr Python f"{x}"：nil 顯示為 None。
func pyStr(s *string) string {
	if s == nil {
		return "None"
	}
	return *s
}
