package services

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/treasure"
	"farmgm/internal/game/weather"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
)

// 藏寶圖碎片（Go 版新增，2026-11-20，規則見 game/treasure）。碎片與尋寶進度存在 farm_tools 的 settings，
// 交易紀錄沿用既有類型（不需要 Migration）：開箱 rumor_treasure、換碎片 exchange。

// settingsTool 以 farm_tools 存狀態的功能（菇房、天氣奇遇、藏寶圖、NPC 訂單）的紀錄列；還沒有時為 nil。
func settingsTool(ctx context.Context, t *db.Tx, farmID int64, code string, forUpdate bool) (*models.FarmTool, error) {
	sql := "SELECT * FROM farm_tools WHERE farm_id = $1 AND tool_code = $2 ORDER BY id DESC LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FarmTool](ctx, t, sql, farmID, code)
}

// saveSettingsTool 寫回 settings；還沒有紀錄列時先建立。
func saveSettingsTool(ctx context.Context, t *db.Tx, farmID int64, tool *models.FarmTool, code string,
	settings map[string]any, now time.Time) error {
	if tool == nil {
		var err error
		if tool, err = items.AddTool(ctx, t, farmID, code, now); err != nil {
			return err
		}
	}
	_, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("settings", settings))
	return err
}

func toolSettings(tool *models.FarmTool) map[string]any {
	if tool == nil {
		return nil
	}
	return tool.Settings
}

// lockedTreasure 鎖定並讀出藏寶圖狀態（farm 必須已鎖定）。
func lockedTreasure(ctx context.Context, t *db.Tx, farmID int64) (*models.FarmTool, treasure.State, error) {
	tool, err := settingsTool(ctx, t, farmID, treasure.ToolCode, true)
	if err != nil {
		return nil, treasure.State{}, err
	}
	return tool, treasure.Parse(toolSettings(tool)), nil
}

func pieceName(ctx context.Context, p string) string { return txt(ctx, treasure.PieceNames[p]) }

// grantFragment 依來源擲骰掉落 1 片碎片（farm 必須已鎖定）；回傳附加在訊息後面的文字，沒有掉落時為 ""。
func (s *GameService) grantFragment(ctx context.Context, t *db.Tx, farm *models.Farm, source string) (string, error) {
	now := s.now()
	if !treasure.Dropped(source, weather.HasLuck(now, weather.LuckRainbow), s.TreasureRng()) {
		return "", nil
	}
	return s.addPiece(ctx, t, farm, treasure.RandomPiece(s.TreasureRng()), now)
}

func (s *GameService) addPiece(ctx context.Context, t *db.Tx, farm *models.Farm, piece string, now time.Time) (string, error) {
	tool, st, err := lockedTreasure(ctx, t, farm.ID)
	if err != nil {
		return "", err
	}
	st.Pieces[piece]++
	if err := saveSettingsTool(ctx, t, farm.ID, tool, treasure.ToolCode, st.Encode(), now); err != nil {
		return "", err
	}
	return txf(ctx, "（🧩 撿到藏寶圖碎片・%s）", pieceName(ctx, piece)), nil
}

// TreasureView 藏寶圖頁。
type TreasureView struct {
	Now            time.Time
	Pieces         map[string]int
	Complete       bool
	Site           treasure.Site // 本週的寶藏地點
	Hunting        bool
	HuntSite       treasure.Site
	HuntEnd        *time.Time
	Done           bool // 尋寶隊回來了，可以開箱
	Rainbow        bool // 今天是彩虹日
	RainbowClaimed bool // 今天已經領過彩虹碎片
	Found          []string
	Gold           int
}

func (s *GameService) TreasureMap(ctx context.Context, playerID int64) (TreasureView, error) {
	return inTx(ctx, s, func(t *db.Tx) (TreasureView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return TreasureView{}, err
		}
		tool, err := settingsTool(ctx, t, farm.ID, treasure.ToolCode, false)
		if err != nil {
			return TreasureView{}, err
		}
		now := s.now()
		st := treasure.Parse(toolSettings(tool))
		v := TreasureView{Now: now, Pieces: st.Pieces, Complete: st.Complete(), Site: treasure.WeekSite(now),
			Hunting: st.Hunting(), HuntEnd: st.HuntEnd, Done: st.HuntDone(now), Found: st.Found, Gold: farm.Gold,
			Rainbow: weather.HasLuck(now, weather.LuckRainbow), RainbowClaimed: st.RainbowDay == weather.DayKey(now)}
		if v.Hunting {
			v.HuntSite = treasure.Sites[st.HuntSite]
		}
		return v, nil
	})
}

// StartTreasureHunt 東南西北各用掉 1 片，出發到本週的寶藏地點。
func (s *GameService) StartTreasureHunt(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, st, err := lockedTreasure(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if st.Hunting() {
			return ActionResult{}, gameerr.Validationf("尋寶隊已經出發了，先開完這次的寶箱。")
		}
		if !st.Complete() {
			var lack []string
			for _, p := range treasure.Pieces {
				if st.Pieces[p] < 1 {
					lack = append(lack, pieceName(ctx, p))
				}
			}
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "還缺藏寶圖碎片：%s。", strings.Join(lack, txt(ctx, "、")))
		}
		now := s.now()
		for _, p := range treasure.Pieces {
			st.Pieces[p]--
		}
		site := treasure.WeekSite(now)
		end := now.Add(treasure.HuntSeconds * time.Second)
		st.HuntSite, st.HuntEnd = site.Code, &end
		if err := saveSettingsTool(ctx, t, farm.ID, tool, treasure.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "🗺️ 拼好藏寶圖，尋寶隊出發前往 %s %s！%s後回來開箱。", site.Emoji, txt(ctx, site.Name),
			rules.FormatDuration(treasure.HuntSeconds)), farm.Gold, 0, "", 0), nil
	})
}

// OpenTreasure 尋寶隊回來後開箱：金幣與素材（放素材背包；放不下時不開箱）。
func (s *GameService) OpenTreasure(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, st, err := lockedTreasure(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		if !st.Hunting() {
			return ActionResult{}, gameerr.Validationf("目前沒有在尋寶，集滿東南西北四片碎片就能出發。")
		}
		if !st.HuntDone(now) {
			return ActionResult{}, gameerr.Validationf("尋寶隊還在路上，%s後回來。", rules.FormatDuration(rules.RemainingSeconds(st.HuntEnd, now)))
		}
		site := treasure.Sites[st.HuntSite]
		reward := treasure.Roll(site, s.TreasureRng)
		total := 0
		for _, code := range reward.Order {
			total += reward.Items[code]
		}
		if err := s.ensureMaterialSpace(ctx, t, farm, total); err != nil {
			if gameerr.Is(err, gameerr.WarehouseFull) {
				return ActionResult{}, gameerr.New(gameerr.WarehouseFull, "%s 寶箱裡的東西放不下，清出空間後再開箱。", err.Error())
			}
			return ActionResult{}, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		parts := make([]string, 0, len(reward.Order))
		for _, code := range reward.Order {
			if err := inventory.Add(ctx, t, farm.ID, code, reward.Items[code], "normal", "none"); err != nil {
				return ActionResult{}, err
			}
			parts = append(parts, fmt.Sprintf("%s ×%d", ItemName(code, names), reward.Items[code]))
		}
		farm.Gold += reward.Gold
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		got := strings.Join(parts, "、")
		if err := s.record(ctx, t, farm, rules.EvRumorTreasure,
			truncRunes(fmt.Sprintf("藏寶圖：在%s挖到寶藏，金幣 +%d、%s", site.Name, reward.Gold, got), 200),
			rec{GoldDelta: reward.Gold}); err != nil {
			return ActionResult{}, err
		}
		first := !slices.Contains(st.Found, site.Code)
		if first {
			st.Found = append(st.Found, site.Code)
		}
		st.HuntSite, st.HuntEnd = "", nil
		if err := saveSettingsTool(ctx, t, farm.ID, tool, treasure.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "🎁 在 %s %s挖到寶藏：💰 %d 金幣、%s！", site.Emoji, txt(ctx, site.Name), reward.Gold, got)
		if first {
			msg += txf(ctx, "（第一次來到這裡，已去過 %d／%d 個地點）", len(st.Found), len(treasure.SiteOrder))
		}
		return result(msg, farm.Gold, reward.Gold, "", 0), nil
	})
}

// ExchangePieces 用 ExchangeCost 片 from 碎片加 ExchangeFee 金幣換 1 片 to 碎片。
func (s *GameService) ExchangePieces(ctx context.Context, playerID int64, from, to string) (ActionResult, error) {
	if !slices.Contains(treasure.Pieces, from) || !slices.Contains(treasure.Pieces, to) || from == to {
		return ActionResult{}, gameerr.Validationf("請選擇兩種不同方位的碎片。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, st, err := lockedTreasure(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if st.Pieces[from] < treasure.ExchangeCost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s方碎片不足 %d 片（目前 %d 片）。",
				pieceName(ctx, from), treasure.ExchangeCost, st.Pieces[from])
		}
		if farm.Gold < treasure.ExchangeFee {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足，換碎片需要 %d 金幣。", treasure.ExchangeFee)
		}
		now := s.now()
		st.Pieces[from] -= treasure.ExchangeCost
		st.Pieces[to]++
		farm.Gold -= treasure.ExchangeFee
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvExchange,
			fmt.Sprintf("藏寶圖碎片：%s方 ×%d 換 %s方 ×1，金幣 -%d", treasure.PieceNames[from], treasure.ExchangeCost,
				treasure.PieceNames[to], treasure.ExchangeFee), rec{GoldDelta: -treasure.ExchangeFee}); err != nil {
			return ActionResult{}, err
		}
		if err := saveSettingsTool(ctx, t, farm.ID, tool, treasure.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "🧩 用 %d 片%s方碎片和 %d 金幣換到 1 片%s方碎片。", treasure.ExchangeCost, pieceName(ctx, from),
			treasure.ExchangeFee, pieceName(ctx, to)), farm.Gold, -treasure.ExchangeFee, "", 0), nil
	})
}

// ClaimRainbowPiece 彩虹日每人領 1 片隨機碎片。
func (s *GameService) ClaimRainbowPiece(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		now := s.now()
		if !weather.HasLuck(now, weather.LuckRainbow) {
			return ActionResult{}, gameerr.Validationf("今天不是彩虹日：雨後放晴的那天才能領彩虹碎片。")
		}
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, st, err := lockedTreasure(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		day := weather.DayKey(now)
		if st.RainbowDay == day {
			return ActionResult{}, gameerr.Validationf("今天已經領過彩虹碎片了。")
		}
		piece := treasure.RandomPiece(s.TreasureRng())
		st.Pieces[piece]++
		st.RainbowDay = day
		if err := saveSettingsTool(ctx, t, farm.ID, tool, treasure.ToolCode, st.Encode(), now); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "🌈 雨後的彩虹下撿到 1 片藏寶圖碎片・%s！", pieceName(ctx, piece)), farm.Gold, 0, "", 0), nil
	})
}
