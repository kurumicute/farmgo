package services

// 總覽頁待辦區的摘要（Go 版新增，2026-11-18）：菇房、種子實驗室、魚簍的狀態。
// 只讀取、不鎖農場，也不結算自動釣魚機（結算在打開釣魚頁時做），讓總覽頁維持輕量；
// 農田、動物、加工的資料總覽頁本來就有，不在這裡重複查。

import (
	"context"
	"slices"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/autofish"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/npcorder"
	"farmgm/internal/game/questboard"
	"farmgm/internal/game/seedlab"
	"farmgm/internal/game/treasure"
	"farmgm/internal/game/weather"
	fishrepo "farmgm/internal/repositories/fishing"
	labrepo "farmgm/internal/repositories/seedlab"
)

// TodoSummary 各設施的完成狀態；沒有解鎖或沒有建造的設施，對應的欄位維持零值。
type TodoSummary struct {
	CellarUnlocked bool
	CellarReady    int        // 可以採收的菇床
	CellarNextAt   *time.Time // 下一個菇床成熟的時間
	LabBuilt       bool
	LabReady       int // 完成、可以領取的研究
	LabNextAt      *time.Time
	LabFreeSlots   int // 空著的研究槽
	BasketCount    int // 魚簍中的魚
	BasketCapacity int
	TripArea       string     // 進行中或待領取的探險區域名稱（沒有探險時為空字串）
	TripReturnAt   *time.Time // 探險隊回來的時間（2026-11-19 待辦區新增）
	TripBack       bool       // 探險隊已經回來、可以領取
	// NPC 訂單與藏寶圖（2026-11-20）：在同一條查詢裡讀 farm_tools 的 settings，不額外查詢
	OrdersOpen       int        // 還沒過期的 NPC 訂單
	OrdersNextExpire *time.Time // 最快過期的訂單
	TreasureDone     bool       // 尋寶隊回來了
	TreasureReturnAt *time.Time // 尋寶隊回來的時間（尋寶中）
	RainbowClaimable bool       // 彩虹日、今天還沒領彩虹碎片
	BoardBuffs       []string   // 所屬群組生效中的委託增益（難度，依 questboard.Tiers 排序）
}

func (s *GameService) TodoSummary(ctx context.Context, playerID int64) (TodoSummary, error) {
	now := s.now()
	return inTx(ctx, s, func(t *db.Tx) (TodoSummary, error) {
		var v TodoSummary
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return v, err
		}
		if farm.Level >= mushroom.Unlock() {
			tool, err := cellarTool(ctx, t, farm.ID, false)
			if err != nil {
				return v, err
			}
			if tool != nil { // 還沒打開過菇房時沒有資料，不提示
				v.CellarUnlocked = true
				beds, _ := mushroom.ParseBeds(tool.Settings, now)
				for _, b := range beds {
					if b.Ready(now) {
						v.CellarReady++
					} else if at := b.ReadyAt(); v.CellarNextAt == nil || at.Before(*v.CellarNextAt) {
						v.CellarNextAt = &at
					}
				}
			}
		}
		// 實驗室等級、魚簍數量與探險回來時間合成一條查詢（總覽頁每次重繪都會讀，SQL 次數有上限，見 bot/querycount_test.go）
		var labLevel, labExtra *int
		var tripArea *string
		var ordersRaw, treasureRaw map[string]any
		var boardRaw *string
		if err := t.QueryRow(ctx, `SELECT l.level, l.extra_slots,
				(SELECT count(*) FROM farm_fish f WHERE f.farm_id = $1 AND `+fishrepo.InBasket("f")+`),
				e.area_code, e.next_available_at,
				(SELECT settings FROM farm_tools WHERE farm_id = $1 AND tool_code = $2 ORDER BY id DESC LIMIT 1),
				(SELECT settings FROM farm_tools WHERE farm_id = $1 AND tool_code = $3 ORDER BY id DESC LIMIT 1),
				(SELECT string_agg(DISTINCT created_by, ',') FROM quests WHERE group_id = $4 AND status = 'completed'
					AND created_by LIKE 'questboard:%' AND completed_at > $5)
			FROM (SELECT 1) AS one LEFT JOIN seed_labs l ON l.farm_id = $1
			LEFT JOIN exploration_logs e ON e.farm_id = $1 AND e.claimed_at IS NULL`, farm.ID, npcorder.ToolCode, treasure.ToolCode,
			farm.HomeGroupID, now.Add(-questboard.BuffHours*time.Hour)).Scan(
			&labLevel, &labExtra, &v.BasketCount, &tripArea, &v.TripReturnAt, &ordersRaw, &treasureRaw, &boardRaw); err != nil {
			return v, err
		}
		if boardRaw != nil {
			done := strings.Split(*boardRaw, ",")
			for _, tier := range questboard.Tiers {
				if slices.Contains(done, questboard.CreatorPrefix+tier) {
					v.BoardBuffs = append(v.BoardBuffs, tier)
				}
			}
		}
		for _, o := range npcorder.Parse(ordersRaw).Orders {
			if now.Before(o.Expires) {
				v.OrdersOpen++
				if exp := o.Expires; v.OrdersNextExpire == nil || exp.Before(*v.OrdersNextExpire) {
					v.OrdersNextExpire = &exp
				}
			}
		}
		tm := treasure.Parse(treasureRaw)
		v.TreasureDone = tm.HuntDone(now)
		if tm.Hunting() && !v.TreasureDone {
			v.TreasureReturnAt = tm.HuntEnd
		}
		v.RainbowClaimable = weather.HasLuck(now, weather.LuckRainbow) && tm.RainbowDay != weather.DayKey(now)
		if tripArea != nil {
			v.TripArea = exploration.AreaOf(*tripArea).Name
			v.TripBack = v.TripReturnAt != nil && !now.Before(*v.TripReturnAt)
		}
		if labLevel != nil {
			v.LabBuilt = true
			jobs, err := labrepo.OpenJobs(ctx, t, farm.ID)
			if err != nil {
				return v, err
			}
			for _, j := range jobs {
				if !now.Before(j.ReadyAt) {
					v.LabReady++
				} else if v.LabNextAt == nil || j.ReadyAt.Before(*v.LabNextAt) {
					at := j.ReadyAt
					v.LabNextAt = &at
				}
			}
			v.LabFreeSlots = max(0, seedlab.SlotsFor(*labLevel, deref0(labExtra))-len(jobs))
		}
		if v.BasketCount > 0 {
			_, raw, err := fishrepo.CurrentSettings(ctx, t)
			if err != nil {
				return v, err
			}
			settings, err := decodeSettings(raw)
			if err != nil {
				return v, err
			}
			v.BasketCapacity = autofish.BasketCapacity(farm.FishBasketLevel, settings.BasketCapacity)
		}
		return v, nil
	})
}

func deref0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
