package services

// 群組、外部平台（Discord）對應、偷菜、防守設施與排行榜（對應 Python app/services/social_service.py）。
//
// 群組上下文一律由呼叫端以 groupID 明確傳入（CMD 傳目前選取的群組；Discord 以 GetGroupByGuild 取得），
// 服務本身不保存任何選取狀態。

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/defenses"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/repositories/pets"
	"farmgm/internal/repositories/players"
	"farmgm/internal/repositories/rumors"
	"farmgm/internal/repositories/steals"

	newsrules "farmgm/internal/game/news"
	petrules "farmgm/internal/game/pets"
	newsrepo "farmgm/internal/repositories/news"
)

var guildIDRe = regexp.MustCompile(`^[0-9]{1,20}$`)

const LeaderboardMax = 50

// SocialService = FishingService + 群組、偷菜、防守、排行榜。
type SocialService struct {
	*FishingService
}

func NewSocialService(d *db.DB, opt Options) *SocialService {
	return &SocialService{NewFishingService(d, opt)}
}

func (s *SocialService) InGroupContext(groupID *int64) *SocialService {
	return &SocialService{s.FishingService.InGroupContext(groupID)}
}

func groupInfo(g models.Group, members int, isMember bool) GroupInfo {
	return GroupInfo{g.ID, g.Name, g.DiscordGuildID, members, g.CreatedAt, isMember}
}

// ExternalName 把外部平台的顯示名稱整理成合法名稱：去除控制字元與首尾空白、截斷長度，空白時使用 fallback。
func ExternalName(raw string, maxLen int, fallback string) string {
	var b strings.Builder
	for _, ch := range raw {
		if ch >= 32 {
			b.WriteRune(ch)
		}
	}
	name := strings.TrimSpace(truncRunes(strings.TrimSpace(b.String()), maxLen))
	if name == "" {
		return fallback
	}
	return name
}

// ---------- 外部平台（Discord）對應 ----------

func (s *SocialService) ensureGuildGroup(ctx context.Context, t *db.Tx, guildID, guildName string) (*models.Group, error) {
	if !guildIDRe.MatchString(guildID) {
		return nil, gameerr.Validationf("Discord Guild ID 必須是 1–20 位數字。")
	}
	name := ExternalName(guildName, balance.GroupNameMaxLen, "伺服器 "+guildID)
	// 並行的第一次互動只會建立一筆（discord_guild_id 唯一）
	if _, err := t.Exec(ctx, "INSERT INTO groups (name, discord_guild_id) VALUES ($1, $2) ON CONFLICT (discord_guild_id) DO NOTHING",
		name, guildID); err != nil {
		return nil, err
	}
	g, err := groups.GetByGuild(ctx, t, guildID)
	if err != nil {
		return nil, err
	}
	if g.Name != name { // 伺服器改名時同步顯示名稱
		g.Name = name
		if _, err := db.UpdateByID(ctx, t, models.TableGroup, g.ID, db.C("name", name)); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// EnsureGuildGroup Bot 加入伺服器時建立（或取得）對應群組，回傳群組 ID。guildName 為 "" 時使用預設名稱。
func (s *SocialService) EnsureGuildGroup(ctx context.Context, guildID, guildName string) (int64, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (int64, error) {
		g, err := s.ensureGuildGroup(ctx, t, guildID, guildName)
		if err != nil {
			return 0, err
		}
		return g.ID, nil
	})
}

// ExternalContext 取得（必要時建立）伺服器對應的群組；玩家已存在時確保其為該群組成員。不會建立玩家。guildID 為 nil 表示私訊。
func (s *SocialService) ExternalContext(ctx context.Context, platform, userID string, guildID *string, guildName string) (ExternalContext, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (ExternalContext, error) {
		var out ExternalContext
		var g *models.Group
		if guildID != nil {
			var err error
			if g, err = s.ensureGuildGroup(ctx, t, *guildID, guildName); err != nil {
				return out, err
			}
			out.GroupID, out.GroupName = &g.ID, &g.Name
		}
		p, err := players.GetByExternal(ctx, t, platform, userID)
		if err != nil || p == nil {
			return out, err
		}
		out.PlayerID, out.PlayerName = &p.ID, &p.DisplayName
		if g != nil {
			if _, err := groups.AddMember(ctx, t, g.ID, p.ID); err != nil {
				return out, err
			}
		}
		out.HasFarm, err = s.hasFarm(ctx, t, p.ID)
		return out, err
	})
}

// EnsureExternalPlayer 依外部帳號取得或建立玩家（並行建立只會成功一筆），回傳玩家 ID。
func (s *SocialService) EnsureExternalPlayer(ctx context.Context, platform, userID, displayName string) (int64, error) {
	if platform == "" || userID == "" || len([]rune(userID)) > 64 {
		return 0, gameerr.Validationf("外部帳號資料無效。")
	}
	r := []rune(userID)
	name := ExternalName(displayName, balance.PlayerNameMaxLen, "玩家"+string(r[max(0, len(r)-6):]))
	return inTx(ctx, s.GameService, func(t *db.Tx) (int64, error) {
		if _, err := t.Exec(ctx, `INSERT INTO players (display_name, external_platform, external_user_id) VALUES ($1, $2, $3)
			ON CONFLICT (external_platform, external_user_id) WHERE external_platform IS NOT NULL DO NOTHING`,
			name, platform, userID); err != nil {
			return 0, err
		}
		id, _, err := db.QueryValue[int64](ctx, t,
			"SELECT id FROM players WHERE external_platform = $1 AND external_user_id = $2", platform, userID)
		return id, err
	})
}

// StartExternalFarm 外部平台建立農場：所屬群組固定為執行指令的伺服器。一個外部帳號只會有一座農場。
func (s *SocialService) StartExternalFarm(ctx context.Context, platform, userID, displayName, guildID, guildName,
	farmName string) (FarmStatus, error) {
	ec, err := s.ExternalContext(ctx, platform, userID, &guildID, guildName)
	if err != nil {
		return FarmStatus{}, err
	}
	pid, err := s.EnsureExternalPlayer(ctx, platform, userID, displayName)
	if err != nil {
		return FarmStatus{}, err
	}
	return s.CreateFarm(ctx, pid, farmName, ec.GroupID)
}

// ---------- 群組 ----------

// CreateGroup discordGuildID 為 nil 表示沒有對應的 Discord 伺服器。
func (s *SocialService) CreateGroup(ctx context.Context, name string, discordGuildID *string) (GroupInfo, error) {
	name, err := rules.ValidateName(name, "群組名稱", balance.GroupNameMaxLen)
	if err != nil {
		return GroupInfo{}, err
	}
	if discordGuildID != nil && !guildIDRe.MatchString(*discordGuildID) {
		return GroupInfo{}, gameerr.Validationf("Discord Guild ID 必須是 1–20 位數字。")
	}
	info, err := inTx(ctx, s.GameService, func(t *db.Tx) (GroupInfo, error) {
		g, err := groups.Create(ctx, t, name, discordGuildID)
		if err != nil {
			return GroupInfo{}, err
		}
		return groupInfo(*g, 0, false), nil
	})
	if isIntegrity(err) {
		return GroupInfo{}, gameerr.New(gameerr.AlreadyExists, "此 Discord Guild ID 已對應到其他群組。")
	}
	return info, err
}

// ListGroups playerID 為 nil 時 IsMember 一律 false。
func (s *SocialService) ListGroups(ctx context.Context, playerID *int64) ([]GroupInfo, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) ([]GroupInfo, error) {
		rows, err := groups.ListWithCounts(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		out := make([]GroupInfo, len(rows))
		for i, r := range rows {
			out[i] = groupInfo(r.Group, r.MemberCount, r.IsMember)
		}
		return out, nil
	})
}

func (s *SocialService) GetGroup(ctx context.Context, groupID int64) (GroupInfo, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (GroupInfo, error) {
		rows, err := groups.ListWithCounts(ctx, t, nil)
		if err != nil {
			return GroupInfo{}, err
		}
		for _, r := range rows {
			if r.Group.ID == groupID {
				return groupInfo(r.Group, r.MemberCount, false), nil
			}
		}
		return GroupInfo{}, gameerr.NotFoundf("找不到群組 #%d。", groupID)
	})
}

// GetGroupByGuild 依 Discord Guild ID 決定群組上下文；找不到時回傳 nil。
func (s *SocialService) GetGroupByGuild(ctx context.Context, guildID string) (*GroupInfo, error) {
	g, err := inTx(ctx, s.GameService, func(t *db.Tx) (*models.Group, error) { return groups.GetByGuild(ctx, t, guildID) })
	if err != nil || g == nil {
		return nil, err
	}
	info, err := s.GetGroup(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func (s *SocialService) AddMember(ctx context.Context, groupID, playerID int64) (bool, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (bool, error) {
		g, err := groups.Get(ctx, t, groupID)
		if err != nil {
			return false, err
		}
		if g == nil {
			return false, gameerr.NotFoundf("找不到群組 #%d。", groupID)
		}
		p, err := players.Get(ctx, t, playerID, false)
		if err != nil {
			return false, err
		}
		if p == nil {
			return false, gameerr.NotFoundf("找不到玩家 #%d。", playerID)
		}
		return groups.AddMember(ctx, t, groupID, playerID)
	})
}

// RemoveMember 只移除成員關係，玩家的農場、金幣與所有紀錄都保留。
func (s *SocialService) RemoveMember(ctx context.Context, groupID, playerID int64) (bool, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (bool, error) {
		g, err := groups.Get(ctx, t, groupID)
		if err != nil {
			return false, err
		}
		if g == nil {
			return false, gameerr.NotFoundf("找不到群組 #%d。", groupID)
		}
		return groups.RemoveMember(ctx, t, groupID, playerID)
	})
}

func (s *SocialService) GroupMembers(ctx context.Context, groupID int64) ([]groups.Member, error) {
	now := s.now()
	return inTx(ctx, s.GameService, func(t *db.Tx) ([]groups.Member, error) {
		g, err := groups.Get(ctx, t, groupID)
		if err != nil {
			return nil, err
		}
		if g == nil {
			return nil, gameerr.NotFoundf("找不到群組 #%d。", groupID)
		}
		return groups.MembersWithFarms(ctx, t, groupID, now, witherCutoff(now))
	})
}

func (s *SocialService) IsMember(ctx context.Context, groupID, playerID int64) (bool, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (bool, error) { return groups.IsMember(ctx, t, groupID, playerID) })
}

func witherCutoff(now time.Time) time.Time { return now.Add(-balance.WitherSeconds * time.Second) }

func requireMember(ctx context.Context, t *db.Tx, groupID, playerID int64) error {
	g, err := groups.Get(ctx, t, groupID)
	if err != nil {
		return err
	}
	if g == nil {
		return gameerr.NotFoundf("找不到群組 #%d。", groupID)
	}
	member, err := groups.IsMember(ctx, t, groupID, playerID)
	if err != nil {
		return err
	}
	if !member {
		return gameerr.New(gameerr.NotGroupMember, "你不是目前群組的成員，無法查看或操作此群組。")
	}
	return nil
}

// ---------- 偷菜 ----------

// taipeiDayStart 台灣今天 00:00（UTC 表示）。
func taipeiDayStart(now time.Time) time.Time {
	l := now.In(clock.TaipeiTZ)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, clock.TaipeiTZ).UTC()
}

func hasGlove(ctx context.Context, t *db.Tx, farmID int64) (bool, error) {
	return items.ToolEnabled(ctx, t, farmID, balance.ToolThiefGlove)
}

// stealCandidates （可偷的土地, 偷菜者倉庫剩餘空間）；倉庫已滿時沒有可偷的土地。
func (s *SocialService) stealCandidates(ctx context.Context, t *db.Tx, groupID, playerID int64, now time.Time) ([]StealTarget, int, error) {
	farm, err := s.farmOf(ctx, t, playerID)
	if err != nil {
		return nil, 0, err
	}
	used, err := inventory.TotalQuantity(ctx, t, farm.ID)
	if err != nil {
		return nil, 0, err
	}
	free := farm.WarehouseCapacity - used
	if free <= 0 {
		return []StealTarget{}, free, nil
	}
	glove, err := hasGlove(ctx, t, farm.ID)
	if err != nil {
		return nil, 0, err
	}
	var plots, thieves *int
	if !glove {
		plots, thieves = intPtr(balance.StealPlotsPerVictimPerDay), intPtr(balance.StealThievesPerVictimPerDay)
	}
	rows, err := steals.CandidatePlots(ctx, t, groupID, playerID, now, witherCutoff(now), taipeiDayStart(now), plots, thieves)
	if err != nil {
		return nil, 0, err
	}
	targets := make([]StealTarget, 0, len(rows))
	for _, r := range rows {
		targets = append(targets, StealTarget{r.PlayerID, r.PlayerName, r.FarmName, r.PlotNumber, r.CropName,
			r.RemainingQuantity, min(balance.StealMax(r.CropCode), r.RemainingQuantity, free)})
	}
	return targets, free, nil
}

func (s *SocialService) ThiefGloveEnabled(ctx context.Context, playerID int64) (bool, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (bool, error) {
		farm, err := farms.GetByPlayer(ctx, t, playerID, false)
		if err != nil || farm == nil {
			return false, err
		}
		return hasGlove(ctx, t, farm.ID)
	})
}

func (s *SocialService) StealTargets(ctx context.Context, groupID, playerID int64) ([]StealTarget, error) {
	now := s.now()
	return inTx(ctx, s.GameService, func(t *db.Tx) ([]StealTarget, error) {
		if err := requireMember(ctx, t, groupID, playerID); err != nil {
			return nil, err
		}
		targets, _, err := s.stealCandidates(ctx, t, groupID, playerID, now)
		return targets, err
	})
}

// StealPlayer 偷菜名單的一位成員。
type StealPlayer struct {
	PlayerID     int64
	PlayerName   string
	FarmName     string
	Stealable    int
	LimitReached bool
	VictimFull   bool
}

// StealPlayers 目前群組中其他有農場的成員，附上這次可偷的土地數與今天是否已偷過（可偷的排前面）。
func (s *SocialService) StealPlayers(ctx context.Context, groupID, playerID int64) ([]StealPlayer, error) {
	now := s.now()
	rows, err := inTx(ctx, s.GameService, func(t *db.Tx) ([]StealPlayer, error) {
		if err := requireMember(ctx, t, groupID, playerID); err != nil {
			return nil, err
		}
		targets, _, err := s.stealCandidates(ctx, t, groupID, playerID, now)
		if err != nil {
			return nil, err
		}
		counts := map[int64]int{}
		for _, tg := range targets {
			counts[tg.PlayerID]++
		}
		day := taipeiDayStart(now)
		farm, err := farms.GetByPlayer(ctx, t, playerID, false)
		if err != nil {
			return nil, err
		}
		glove, err := hasGlove(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		var limited, full []int64
		if !glove {
			if limited, err = steals.VictimsAtLimit(ctx, t, playerID, day, balance.StealPlotsPerVictimPerDay); err != nil {
				return nil, err
			}
			if full, err = steals.VictimsFull(ctx, t, playerID, day, balance.StealThievesPerVictimPerDay); err != nil {
				return nil, err
			}
		}
		members, err := groups.MembersWithFarms(ctx, t, groupID, now, witherCutoff(now))
		if err != nil {
			return nil, err
		}
		out := []StealPlayer{}
		for _, m := range members {
			if m.Farm == nil || m.PlayerID == playerID {
				continue
			}
			lim := slices.Contains(limited, m.PlayerID)
			out = append(out, StealPlayer{m.PlayerID, m.PlayerName, m.Farm.FarmName, counts[m.PlayerID], lim,
				slices.Contains(full, m.PlayerID) && !lim})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Stealable != b.Stealable {
			return a.Stealable > b.Stealable
		}
		ab, bb := a.LimitReached || a.VictimFull, b.LimitReached || b.VictimFull
		if ab != bb {
			return !ab
		}
		if a.PlayerName != b.PlayerName {
			return a.PlayerName < b.PlayerName
		}
		return a.PlayerID < b.PlayerID
	})
	return rows, nil
}

// StealFarmView 查看同群組成員的農田與這次可以偷的土地；只能看目前群組的成員，不能看自己。
func (s *SocialService) StealFarmView(ctx context.Context, groupID, thiefPlayerID, victimPlayerID int64) (StealFarmView, error) {
	if thiefPlayerID == victimPlayerID {
		return StealFarmView{}, gameerr.Validationf("不能偷自己的菜。")
	}
	now := s.now()
	return inTx(ctx, s.GameService, func(t *db.Tx) (StealFarmView, error) {
		if err := requireMember(ctx, t, groupID, thiefPlayerID); err != nil {
			return StealFarmView{}, err
		}
		// 不透露目標在其他群組的任何資訊
		member, err := groups.IsMember(ctx, t, groupID, victimPlayerID)
		if err != nil {
			return StealFarmView{}, err
		}
		if !member {
			return StealFarmView{}, gameerr.New(gameerr.NotGroupMember, "目標玩家不在目前群組。")
		}
		victimFarm, err := farms.GetByPlayer(ctx, t, victimPlayerID, false)
		if err != nil {
			return StealFarmView{}, err
		}
		if victimFarm == nil {
			return StealFarmView{}, gameerr.NotFoundf("目標玩家還沒有農場。")
		}
		victim, err := players.Get(ctx, t, victimPlayerID, false)
		if err != nil {
			return StealFarmView{}, err
		}
		st, err := s.status(ctx, t, victimFarm, victim.DisplayName)
		if err != nil {
			return StealFarmView{}, err
		}
		targets, free, err := s.stealCandidates(ctx, t, groupID, thiefPlayerID, now)
		if err != nil {
			return StealFarmView{}, err
		}
		day := taipeiDayStart(now)
		stolen, err := steals.PlotsStolenSince(ctx, t, thiefPlayerID, victimPlayerID, day)
		if err != nil {
			return StealFarmView{}, err
		}
		others, err := steals.ThievesSince(ctx, t, victimPlayerID, day, &thiefPlayerID)
		if err != nil {
			return StealFarmView{}, err
		}
		thiefFarm, err := farms.GetByPlayer(ctx, t, thiefPlayerID, false)
		if err != nil {
			return StealFarmView{}, err
		}
		glove := false
		if thiefFarm != nil {
			if glove, err = hasGlove(ctx, t, thiefFarm.ID); err != nil {
				return StealFarmView{}, err
			}
		}
		if glove {
			stolen, others = 0, 0
		}
		v := StealFarmView{PlayerID: victimPlayerID, PlayerName: st.PlayerName, FarmName: st.Name, Plots: st.Plots, Now: st.Now,
			Targets: []StealTarget{}, LimitReached: stolen >= balance.StealPlotsPerVictimPerDay, WarehouseFull: free <= 0,
			VictimFull: stolen < balance.StealPlotsPerVictimPerDay && others >= balance.StealThievesPerVictimPerDay, Glove: glove}
		for _, tg := range targets {
			if tg.PlayerID == victimPlayerID {
				v.Targets = append(v.Targets, tg)
			}
		}
		return v, nil
	})
}

// StealCrop 偷菜。被防守設施或狗狗阻止時操作仍會完成並記錄，回傳 Success = false。
func (s *SocialService) StealCrop(ctx context.Context, groupID, thiefPlayerID, victimPlayerID int64, plotNumber, quantity int) (ActionResult, error) {
	if quantity <= 0 {
		return ActionResult{}, gameerr.Validationf("偷取數量必須是正整數。")
	}
	if quantity > balance.StealMaxPerAction {
		return ActionResult{}, gameerr.Validationf("每次最多只能偷 %d 個。", balance.StealMaxPerAction)
	}
	if thiefPlayerID == victimPlayerID {
		return ActionResult{}, gameerr.Validationf("不能偷自己的菜。")
	}
	return inTx(ctx, s.GameService, func(t *db.Tx) (ActionResult, error) {
		if err := requireMember(ctx, t, groupID, thiefPlayerID); err != nil {
			return ActionResult{}, err
		}
		// 不透露目標在其他群組的任何資訊
		member, err := groups.IsMember(ctx, t, groupID, victimPlayerID)
		if err != nil {
			return ActionResult{}, err
		}
		if !member {
			return ActionResult{}, gameerr.New(gameerr.NotGroupMember, "目標玩家不在目前群組。")
		}
		thiefFarm, err := farms.GetByPlayer(ctx, t, thiefPlayerID, false)
		if err != nil {
			return ActionResult{}, err
		}
		victimFarm, err := farms.GetByPlayer(ctx, t, victimPlayerID, false)
		if err != nil {
			return ActionResult{}, err
		}
		if thiefFarm == nil {
			return ActionResult{}, gameerr.NotFoundf("你還沒有農場，請先建立農場（farm start）。")
		}
		if victimFarm == nil {
			return ActionResult{}, gameerr.NotFoundf("目標玩家還沒有農場。")
		}
		// 依農場 ID 順序鎖定雙方，避免互偷時死結；與主人的收成也會因此序列化
		first, second := thiefFarm.ID, victimFarm.ID
		if first > second {
			first, second = second, first
		}
		locked := map[int64]*models.Farm{}
		for _, id := range []int64{first, second} {
			if locked[id], err = farms.GetByID(ctx, t, id, true); err != nil {
				return ActionResult{}, err
			}
		}
		thiefFarm, victimFarm = locked[thiefFarm.ID], locked[victimFarm.ID]

		plot, err := farms.GetPlot(ctx, t, victimFarm.ID, plotNumber, true)
		if err != nil {
			return ActionResult{}, err
		}
		if plot == nil {
			return ActionResult{}, gameerr.NotFoundf("目標農場沒有第 %d 格土地。", plotNumber)
		}
		now := s.now()
		switch rules.PlotStateAt(plot.CropTypeID, plot.WateredAt, plot.ReadyAt, now) {
		case rules.PlotEmpty:
			return ActionResult{}, gameerr.New(gameerr.PlotEmpty, "這塊土地已經沒有作物了（可能已被收成或偷光）。")
		case rules.PlotWaitingWater:
			return ActionResult{}, gameerr.New(gameerr.PlotNotWatered, "這塊土地的作物還沒澆水，不能偷。")
		case rules.PlotGrowing:
			return ActionResult{}, gameerr.New(gameerr.PlotNotReady, "這塊土地的作物還沒成熟，不能偷。")
		case rules.PlotWithered:
			return ActionResult{}, gameerr.New(gameerr.PlotWithered, "這塊土地的作物已經乾枯，不能偷。")
		}
		already, err := steals.AlreadyStolen(ctx, t, plot.ID, plot.PlantingNo, thiefPlayerID)
		if err != nil {
			return ActionResult{}, err
		}
		if already {
			return ActionResult{}, gameerr.New(gameerr.AlreadyStolen, "你已經偷過這株作物了，每株作物每人只能偷一次。")
		}
		crop, err := crops.Get(ctx, t, *plot.CropTypeID)
		if err != nil {
			return ActionResult{}, err
		}
		if limit := balance.StealMax(crop.Code); quantity > limit {
			return ActionResult{}, gameerr.Validationf("%s每次最多只能偷 %d 個。", crop.Name, limit)
		}
		// 傳說扒手手套：不受每日土地與被偷人數上限（每株仍只能偷一次）
		glove, err := hasGlove(ctx, t, thiefFarm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		victim, err := players.Get(ctx, t, victimPlayerID, false)
		if err != nil {
			return ActionResult{}, err
		}
		if !glove {
			day := taipeiDayStart(now)
			n, err := steals.PlotsStolenSince(ctx, t, thiefPlayerID, victimPlayerID, day)
			if err != nil {
				return ActionResult{}, err
			}
			if n >= balance.StealPlotsPerVictimPerDay {
				p := balance.StealPlotsPerVictimPerDay
				return ActionResult{}, gameerr.New(gameerr.AlreadyStolen,
					"你今天已經偷過這位玩家的 %d 格土地了，每天只能偷同一位玩家 %d 格田，明天（台灣時間 0 點後）再來。", p, p)
			}
			// 雙方農場列已鎖定，同一位被偷者的偷菜依序執行，人數上限不會被同時偷菜突破
			others, err := steals.ThievesSince(ctx, t, victimPlayerID, day, &thiefPlayerID)
			if err != nil {
				return ActionResult{}, err
			}
			if others >= balance.StealThievesPerVictimPerDay {
				l := balance.StealThievesPerVictimPerDay
				return ActionResult{}, gameerr.New(gameerr.AlreadyStolen,
					"%s 今天已經被 %d 個人偷過了，每位玩家每天最多被 %d 人偷，明天（台灣時間 0 點後）再來。", victim.DisplayName, l, l)
			}
		}
		if *plot.RemainingQuantity < quantity {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "剩餘數量不足：只剩 %d 個。", *plot.RemainingQuantity)
		}
		used, err := inventory.TotalQuantity(ctx, t, thiefFarm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if used+quantity > thiefFarm.WarehouseCapacity {
			return ActionResult{}, gameerr.New(gameerr.WarehouseFull, "背包空間不足：目前 %d/%d，需要再放入 %d 個。",
				used, thiefFarm.WarehouseCapacity, quantity)
		}

		// 偷到的作物保留原本的品質與突變（播種時已決定，不重新抽取）
		quality, mutation := deref(plot.Quality), deref(plot.Mutation)
		item := genetics.ProduceCode(crop.Code, crop.VariantCode, mutation)
		label := genetics.ProduceName(crop.Name, crop.VariantName, mutation) + GeneticsLabel(quality, mutation)
		plantingNo := plot.PlantingNo
		thief, err := players.Get(ctx, t, thiefPlayerID, false)
		if err != nil {
			return ActionResult{}, err
		}
		plotID := plot.ID

		// 一輪防守判定（所有驗證通過後才判定；背包不足等錯誤不會消耗機會）
		active, err := activeDefenses(ctx, t, victimFarm.ID, plotNumber)
		if err != nil {
			return ActionResult{}, err
		}
		blockedBy := ""
		var chance, roll *float64
		if len(active) > 0 {
			r := s.rng()
			by, c := rules.ResolveDefense(active, r, balance.DefenseMaxBlockChance)
			blockedBy, chance, roll = by, &c, &r
		}
		if blockedBy == "" {
			// 狗狗：設施都沒擋住時再判定一次（不受設施的整體上限限制）
			lv, err := pets.Level(ctx, t, victimFarm.ID, petrules.Dog)
			if err != nil {
				return ActionResult{}, err
			}
			if dog := petrules.BonusPct(lv); dog != 0 {
				dr := s.rng()
				if dr < float64(dog)/100 {
					c := float64(dog) / 100
					blockedBy, chance, roll = petrules.DogBlocker, &c, &dr
				}
			}
		}
		if blockedBy != "" {
			// 被阻止：雙方作物與物品都不變；紀錄同樣受唯一限制保護，同一株作物不會再次判定
			device := petrules.BlockerName(blockedBy)
			by := blockedBy
			if _, err := steals.Add(ctx, t, steals.NewRecord{GroupID: groupID, ThiefPlayerID: thiefPlayerID,
				VictimPlayerID: victimPlayerID, PlotID: &plotID, PlotNumber: plotNumber, PlantingNo: plantingNo,
				CropTypeID: crop.ID, Quantity: quantity, CreatedAt: now, Outcome: "blocked", BlockedBy: &by,
				BlockChance: chance, Roll: roll}); err != nil {
				return ActionResult{}, err
			}
			r := ctxGroup(groupID)
			r.ItemCode = item
			if err := s.record(ctx, t, thiefFarm, rules.EvStealBlocked,
				fmt.Sprintf("偷 %s 第 %d 格的%s時被%s阻止", victim.DisplayName, plotNumber, label, device), r); err != nil {
				return ActionResult{}, err
			}
			if err := s.record(ctx, t, victimFarm, rules.EvDefended,
				fmt.Sprintf("%s在第 %d 格阻止了一次偷菜（%s）", device, plotNumber, label), r); err != nil {
				return ActionResult{}, err
			}
			text := fmt.Sprintf("🛡️ 偷菜失敗！%s 的%s守住了第 %d 格的%s。", victim.DisplayName, device, plotNumber, label)
			if blockedBy == petrules.DogBlocker {
				text = fmt.Sprintf("🐶 偷菜失敗！%s 的狗狗發現了你，汪汪大叫把你趕跑，第 %d 格的%s一個也沒偷到。",
					victim.DisplayName, plotNumber, label)
			}
			res := result(text+"這株作物你已無法再偷。", thiefFarm.Gold, 0, item, 0)
			res.Success = false
			return res, nil
		}

		remaining := *plot.RemainingQuantity - quantity
		emptied := remaining == 0
		if emptied {
			if err := farms.Clear(ctx, t, plot); err != nil {
				return ActionResult{}, err
			}
		} else {
			plot.RemainingQuantity = &remaining
			if _, err := t.Exec(ctx, "UPDATE farm_plots SET remaining_quantity = $2, updated_at = now() WHERE id = $1",
				plot.ID, remaining); err != nil {
				return ActionResult{}, err
			}
		}
		if err := inventory.Add(ctx, t, thiefFarm.ID, item, quantity, quality, mutation); err != nil {
			return ActionResult{}, err
		}
		// 唯一限制 (plot_id, planting_no, thief) 是重複指令與並行請求的最後防線
		if _, err := steals.Add(ctx, t, steals.NewRecord{GroupID: groupID, ThiefPlayerID: thiefPlayerID,
			VictimPlayerID: victimPlayerID, PlotID: &plotID, PlotNumber: plotNumber, PlantingNo: plantingNo,
			CropTypeID: crop.ID, Quantity: quantity, CreatedAt: now, Outcome: "success", BlockChance: chance, Roll: roll}); err != nil {
			return ActionResult{}, err
		}
		r := ctxGroup(groupID)
		r.ItemCode, r.Quantity, r.Quality, r.Mutation = item, quantity, quality, mutation
		if err := s.record(ctx, t, thiefFarm, rules.EvSteal,
			fmt.Sprintf("從 %s 的第 %d 格偷走 %d 個%s", victim.DisplayName, plotNumber, quantity, label), r); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, victimFarm, rules.EvStolen,
			fmt.Sprintf("第 %d 格被 %s 偷走 %d 個%s", plotNumber, thief.DisplayName, quantity, label), r); err != nil {
			return ActionResult{}, err
		}
		tail := ""
		if emptied {
			tail = "，這塊土地已被偷光，恢復為空地"
		}
		value, err := s.estimateValue(ctx, t, crop, quantity, quality, mutation)
		if err != nil {
			return ActionResult{}, err
		}
		head := "🥷 得手了！"
		if IsRare(quality, mutation) {
			head = "✨ 偷到稀有貨！"
		}
		dig, err := s.rumorDig(ctx, t, thiefFarm, victimFarm, now)
		if err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("%s成功從 %s 的第 %d 格偷走 %d 個%s%s。\n📦 約值 %s 金\n👀 %s 下次打開農場時會知道是你偷的%s",
			head, victim.DisplayName, plotNumber, quantity, label, tail, numfmt.Comma(value), victim.DisplayName, dig),
			thiefFarm.Gold, 0, item, quantity), nil
	})
}

// rumorDig 偷菜成功後，被偷的農場剛好是今天日報傳聞的主角時順手挖寶（每人每天一次；寶藏只有第一個人挖得到）。
// 雙方農場列已鎖定：同一個傳聞的挖寶都會鎖同一座被偷的農場，判斷「已被挖走」與寫入不會交錯。
func (s *SocialService) rumorDig(ctx context.Context, t *db.Tx, thiefFarm, victimFarm *models.Farm, now time.Time) (string, error) {
	if victimFarm.HomeGroupID == nil {
		return "", nil
	}
	groupID := *victimFarm.HomeGroupID
	day := newsrules.NewsDay(now)
	start, end := newsrules.DayRange(day)
	events, err := newsrepo.FarmEvents(ctx, t, groupID, start, end)
	if err != nil {
		return "", err
	}
	var candidates []int64
	for _, e := range events {
		if !slices.Contains(candidates, e.FarmID) {
			candidates = append(candidates, e.FarmID)
		}
	}
	slices.Sort(candidates)
	rumor := makeRumor(groupID, day, candidates)
	if rumor == nil || rumor.Target != victimFarm.ID {
		return "", nil
	}
	dug, err := rumors.Dug(ctx, t, thiefFarm.ID, day)
	if err != nil {
		return "", err
	}
	if dug {
		return "\n🕳️ 今天你已經在傳聞說的地方挖過了。", nil
	}
	taken, err := rumors.Found(ctx, t, groupID, day)
	if err != nil {
		return "", err
	}
	found := rumor.Real && !taken && s.RumorRng() < newsrules.DigChance
	gold := 0
	if found {
		gold = newsrules.TreasureGold(s.RumorRng())
	}
	if err := rumors.Add(ctx, t, thiefFarm.ID, day, groupID, victimFarm.ID, found, gold, now); err != nil {
		return "", err
	}
	if found {
		thiefFarm.Gold += gold
		if err := s.saveFarm(ctx, t, thiefFarm, db.C("gold", thiefFarm.Gold)); err != nil {
			return "", err
		}
		if err := s.record(ctx, t, thiefFarm, rules.EvRumorTreasure,
			fmt.Sprintf("在 %s 的田裡挖到傳聞中的寶藏，金幣 +%d", victimFarm.Name, gold), rec{GoldDelta: gold}); err != nil {
			return "", err
		}
		return fmt.Sprintf("\n💎 傳聞是真的！你順手一挖，挖出一箱寶藏：金幣 **+%s**！", numfmt.Comma(gold)), nil
	}
	if rumor.Real && taken {
		return "\n🕳️ 你在傳聞說的地方挖了挖…只剩一個被挖開的空坑，寶藏已經被別人拿走了。", nil
	}
	return "\n🕳️ 你在傳聞說的地方順手挖了挖，什麼都沒有。", nil
}

// ---------- 防守設施 ----------

func FindDefense(key string) string {
	key = strings.TrimSpace(key)
	lower := strings.ToLower(key)
	for _, code := range balance.DefenseOrder {
		d := balance.Defenses[code]
		if lower == code || key == d.Name || slices.Contains(d.Aliases, lower) || slices.Contains(d.Aliases, key) {
			return code
		}
	}
	return ""
}

// activeDefenses 守護此土地的設施，依判定順序排列；同種設施重疊只計算一次。
func activeDefenses(ctx context.Context, t *db.Tx, farmID int64, plotNumber int) ([]rules.ActiveDefense, error) {
	list, err := defenses.ListForFarm(ctx, t, farmID)
	if err != nil {
		return nil, err
	}
	covering := map[string]bool{}
	for _, d := range list {
		if rules.DefenseCovers(balance.Defenses[d.DeviceCode].Range, d.StartPlot, plotNumber) {
			covering[d.DeviceCode] = true
		}
	}
	var out []rules.ActiveDefense
	for _, code := range balance.DefenseOrder {
		if covering[code] {
			out = append(out, rules.ActiveDefense{Code: code, Chance: balance.Defenses[code].BlockChance})
		}
	}
	return out, nil
}

func (s *SocialService) BuyDefense(ctx context.Context, playerID int64, key string) (ActionResult, error) {
	code := FindDefense(key)
	if code == "" {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種防守設施。可用 defense shop 查看。", key)
	}
	d := balance.Defenses[code]
	return inTx(ctx, s.GameService, func(t *db.Tx) (ActionResult, error) {
		// 鎖定農場後再計算持有數量與分配編號：並行購買不會超過上限，也不會重複扣款
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		list, err := defenses.ListForFarm(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		owned := map[int]bool{}
		for _, x := range list {
			if x.DeviceCode == code {
				owned[x.Slot] = true
			}
		}
		if len(owned) >= d.MaxPerFarm {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "%s每座農場最多 %d 個，你已經有 %d 個。", d.Name, d.MaxPerFarm, len(owned))
		}
		if farm.Gold < d.Price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：%s需要 %d，目前只有 %d。", d.Name, d.Price, farm.Gold)
		}
		slot := 1
		for owned[slot] {
			slot++
		}
		var start *int
		if d.Range != nil {
			start = intPtr(1)
		}
		if _, err := defenses.Add(ctx, t, farm.ID, code, slot, start, s.now()); err != nil {
			return ActionResult{}, err
		}
		farm.Gold -= d.Price
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("購買防守設施%s（第 %d 個），金幣 -%d", d.Name, slot, d.Price),
			rec{ItemCode: code, Quantity: 1, GoldDelta: -d.Price}); err != nil {
			return ActionResult{}, err
		}
		where := "守護全部土地"
		if d.Range != nil {
			slotArg := ""
			if d.MaxPerFarm > 1 {
				slotArg = fmt.Sprintf(" %d", slot)
			}
			where = fmt.Sprintf("守護第 1–%d 格，可用 defense place %s <起始土地>%s 調整", *d.Range, code, slotArg)
		}
		return result(fmt.Sprintf("已購買%s（第 %d 個），花費 %d 金幣；%s。", d.Name, slot, d.Price, where), farm.Gold, -d.Price, code, 1), nil
	})
}

// PlaceDefense slot 預設 1。
func (s *SocialService) PlaceDefense(ctx context.Context, playerID int64, key string, startPlot, slot int) (ActionResult, error) {
	code := FindDefense(key)
	if code == "" {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種防守設施。", key)
	}
	d := balance.Defenses[code]
	if d.Range == nil {
		return ActionResult{}, gameerr.Validationf("%s守護全部土地，不需要放置。", d.Name)
	}
	if slot < 1 || slot > d.MaxPerFarm {
		return ActionResult{}, gameerr.Validationf("%s編號必須在 1–%d 之間。", d.Name, d.MaxPerFarm)
	}
	return inTx(ctx, s.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		device, err := defenses.GetSlot(ctx, t, farm.ID, code, slot)
		if err != nil {
			return ActionResult{}, err
		}
		if device == nil {
			return ActionResult{}, gameerr.NotFoundf("你沒有第 %d 個%s，請先購買（defense buy %s）。", slot, d.Name, code)
		}
		total, err := farms.CountPlots(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if startPlot < 1 || startPlot > total {
			return ActionResult{}, gameerr.Validationf("起始土地必須在 1–%d 之間。", total)
		}
		if _, err := db.UpdateByID(ctx, t, "farm_defenses", device.ID, db.C("start_plot", startPlot)); err != nil {
			return ActionResult{}, err
		}
		end := min(startPlot+*d.Range-1, total)
		if err := s.record(ctx, t, farm, rules.EvDefensePlace,
			fmt.Sprintf("%s（第 %d 個）改為守護第 %d–%d 格", d.Name, slot, startPlot, end), rec{}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("%s（第 %d 個）現在守護第 %d–%d 格。", d.Name, slot, startPlot, end), farm.Gold, 0, "", 0), nil
	})
}

type DefenseDevice struct {
	Code       string
	Name       string
	Slot       int
	Chance     float64
	StartPlot  *int
	EndPlot    *int
	AcquiredAt time.Time
}

type DefensePlot struct {
	PlotNumber int
	Devices    []string
	Chance     float64
}

type DefenseStatus struct {
	Devices []DefenseDevice
	Owned   map[string]int
	Plots   []DefensePlot
}

func (s *SocialService) DefenseStatus(ctx context.Context, playerID int64) (DefenseStatus, error) {
	return inTx(ctx, s.GameService, func(t *db.Tx) (DefenseStatus, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return DefenseStatus{}, err
		}
		total, err := farms.CountPlots(ctx, t, farm.ID)
		if err != nil {
			return DefenseStatus{}, err
		}
		list, err := defenses.ListForFarm(ctx, t, farm.ID)
		if err != nil {
			return DefenseStatus{}, err
		}
		st := DefenseStatus{Devices: []DefenseDevice{}, Owned: map[string]int{}, Plots: []DefensePlot{}}
		for _, code := range balance.DefenseOrder {
			st.Owned[code] = 0
		}
		for _, d := range list {
			spec := balance.Defenses[d.DeviceCode]
			var end *int
			if spec.Range != nil && d.StartPlot != nil {
				end = intPtr(min(*d.StartPlot+*spec.Range-1, total))
			}
			st.Devices = append(st.Devices, DefenseDevice{d.DeviceCode, spec.Name, d.Slot, spec.BlockChance, d.StartPlot, end, d.AcquiredAt})
			st.Owned[d.DeviceCode]++
		}
		for n := 1; n <= total; n++ {
			active, err := activeDefenses(ctx, t, farm.ID, n)
			if err != nil {
				return DefenseStatus{}, err
			}
			_, chance := rules.ResolveDefense(active, 1.0, balance.DefenseMaxBlockChance)
			codes := []string{}
			for _, a := range active {
				codes = append(codes, a.Code)
			}
			st.Plots = append(st.Plots, DefensePlot{n, codes, chance})
		}
		return st, nil
	})
}

// DefenseLogRow 被阻止的偷菜紀錄（不含偷菜者）。
type DefenseLogRow struct {
	ID             int64
	CreatedAt      time.Time
	GroupID        int64
	GroupName      string
	VictimPlayerID int64
	VictimName     string
	CropName       string
	CropCode       string
	Quantity       int
	PlotNumber     int
	Outcome        string
	BlockedBy      *string
	BlockChance    *float64
}

// DefenseLog 自己農場被阻止的偷菜紀錄（不顯示偷菜者；要看偷菜者需使用巡查券）。limit 預設 20。
func (s *SocialService) DefenseLog(ctx context.Context, playerID int64, limit int) ([]DefenseLogRow, error) {
	rows, err := inTx(ctx, s.GameService, func(t *db.Tx) ([]steals.SearchRow, error) {
		if _, err := s.farmOf(ctx, t, playerID); err != nil {
			return nil, err
		}
		return steals.Search(ctx, t, steals.SearchFilter{Limit: max(1, min(limit, 100)), VictimPlayerID: &playerID, Outcome: "blocked"})
	})
	if err != nil {
		return nil, err
	}
	out := make([]DefenseLogRow, len(rows))
	for i, r := range rows {
		out[i] = DefenseLogRow{r.ID, r.CreatedAt, r.GroupID, r.GroupName, r.VictimPlayerID, r.VictimName, r.CropName, r.CropCode,
			r.Quantity, r.PlotNumber, r.Outcome, r.BlockedBy, r.BlockChance}
	}
	return out, nil
}

// Patrol 消耗一張巡查券，查看自己農場近期所有偷菜嘗試（含偷菜者）。只能查看自己的農場。
func (s *SocialService) Patrol(ctx context.Context, playerID int64) ([]steals.SearchRow, error) {
	name := balance.Items[balance.ItemPatrolTicket].Name
	return inTx(ctx, s.GameService, func(t *db.Tx) ([]steals.SearchRow, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, balance.ItemPatrolTicket, 1, "normal", "none")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, gameerr.New(gameerr.InsufficientItems, "沒有%s，請先購買（defense buy 巡查券 1）。", name)
		}
		since := s.now().AddDate(0, 0, -balance.PatrolDays)
		rows, err := steals.Search(ctx, t, steals.SearchFilter{Limit: balance.PatrolLimit, VictimPlayerID: &playerID, Since: &since})
		if err != nil {
			return nil, err
		}
		if err := s.record(ctx, t, farm, rules.EvUseItem,
			fmt.Sprintf("使用%s巡查農場，查到近 %d 天 %d 筆偷菜嘗試", name, balance.PatrolDays, len(rows)),
			rec{ItemCode: balance.ItemPatrolTicket, Quantity: 1}); err != nil {
			return nil, err
		}
		return rows, nil
	})
}

// ---------- 排行榜 ----------

// Leaderboard groupID 為 nil 時是總排行榜；群組排行榜需由該群組成員（playerID）查看。limit 預設 10。
func (s *SocialService) Leaderboard(ctx context.Context, kind string, groupID, playerID *int64, limit int) ([]leaderboard.Row, error) {
	if !slices.Contains(leaderboard.Kinds, kind) {
		return nil, gameerr.Validationf("排行榜種類只能是 money、sales 或 goods。")
	}
	limit = max(1, min(limit, LeaderboardMax))
	return inTx(ctx, s.GameService, func(t *db.Tx) ([]leaderboard.Row, error) {
		if groupID != nil {
			if playerID == nil {
				return nil, gameerr.Validationf("查看群組排行榜需要先選擇玩家。")
			}
			if err := requireMember(ctx, t, *groupID, *playerID); err != nil {
				return nil, err
			}
		}
		return leaderboard.Ranking(ctx, t, kind, groupID, limit, nil, nil)
	})
}
