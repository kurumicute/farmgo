package services

// 世界 Boss（群組後勤活動）：查看活動、捐獻物資、排行榜、領取獎勵，以及管理員建立與控制活動、固定週期排程
// （對應 Python app/services/world_boss_service.py）。
//
//   - 規則與數值在 game/worldboss；每場活動的設定與獎勵快照存在 world_boss_events，Bot 重啟不影響。
//   - 群組隔離：玩家操作都以 groupID 比對活動所屬群組，並確認玩家是群組成員。
//   - 戰鬥由系統依時間自動推進（advance）：捐獻、領獎、管理操作或背景結算時，把 HP 推進到現在並寫回資料庫。
//     查看時（BossCurrent、監控）用 EventView 依同一公式推算，不寫入。
//   - 併發：先鎖活動列再鎖農場列。捐獻帶 requestID，(event, player, request_id) 唯一；領獎以 (event, player) 唯一。
//
// 與 Python 不同：Python 沒有世界 Boss 的 repository，查詢寫在服務檔案中；Go 也寫在本檔（全部參數化）。
// 獎勵的物品（JSON 物件）在 Go 是 map，顯示文字與發放順序依物品代碼排序（Python 依 JSON 中的順序）。

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/players"
	"farmgm/internal/repositories/titles"

	wb "farmgm/internal/game/worldboss"
	fishrepo "farmgm/internal/repositories/fishing"
	generepo "farmgm/internal/repositories/genetics"
)

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

const (
	globalLabel           = "🌏 全服"
	contributionLogLimit  = 200
	worldBossTitleSource  = "world_boss"
	worldBossTarget       = "world_boss"
	worldBossScheduleKind = "world_boss_schedule"
)

// WorldBossService = RankingService + 世界 Boss。
type WorldBossService struct {
	*RankingService
}

func NewWorldBossService(d *db.DB, opt Options) *WorldBossService {
	return &WorldBossService{NewRankingService(d, opt)}
}

func (w *WorldBossService) InGroupContext(groupID *int64) *WorldBossService {
	return &WorldBossService{w.RankingService.InGroupContext(groupID)}
}

func scopeLabel(g *models.Group) string {
	if g == nil {
		return globalLabel
	}
	return "🏠 " + g.Name
}

// ItemCategory 背包物品的捐獻類別；種子、道具、假幣等不能捐獻時回傳 ""。
func ItemCategory(kind, code string) string {
	if slices.Contains(wb.NotDonatable, code) {
		return ""
	}
	switch kind {
	case catalog.ProduceKind, catalog.Variant:
		return "crop"
	case catalog.AnimalProduct, catalog.Goods:
		return "goods"
	case catalog.Material:
		if slices.Contains(wb.SpecialMaterials, code) {
			return "special"
		}
		return "material"
	}
	return ""
}

func sortedCodes(items map[string]int) []string {
	codes := make([]string, 0, len(items))
	for c := range items {
		codes = append(codes, c)
	}
	slices.Sort(codes)
	return codes
}

// BossRewardText 一份獎勵的顯示文字（物品依代碼排序）。
func BossRewardText(r wb.Reward, names map[string]string) string {
	var bits []string
	if r.Gold != 0 {
		bits = append(bits, numfmt.Comma(r.Gold)+" 金幣")
	}
	for _, c := range sortedCodes(r.Items) {
		n, ok := names[c]
		if !ok {
			n = c
		}
		bits = append(bits, fmt.Sprintf("%s ×%d", n, r.Items[c]))
	}
	if len(bits) == 0 {
		return "無"
	}
	return strings.Join(bits, "、")
}

type BossRewardsText struct {
	MinPoints     int      `json:"min_points"`
	Victory       string   `json:"victory"`
	Participation string   `json:"participation"`
	BonusPoints   int      `json:"bonus_points"`
	Bonus         string   `json:"bonus"`
	Rank          []string `json:"rank"`
	Title         string   `json:"title"`
	TitleTop      int      `json:"title_top"`
}

func NewBossRewardsText(r wb.Rewards, names map[string]string) BossRewardsText {
	out := BossRewardsText{MinPoints: wb.MinPoints(r), Victory: BossRewardText(r.Victory, names),
		Participation: BossRewardText(r.Participation, names), BonusPoints: r.BonusPoints,
		Bonus: BossRewardText(r.Bonus, names), Rank: []string{}, Title: r.Title, TitleTop: r.TitleTop}
	for _, x := range r.Rank {
		out.Rank = append(out.Rank, BossRewardText(x, names))
	}
	return out
}

func decodeBossSettings(raw json.RawMessage) wb.Settings {
	var s wb.Settings
	_ = json.Unmarshal(raw, &s)
	return s
}

func decodeBossRewards(raw json.RawMessage) wb.Rewards {
	var r wb.Rewards
	_ = json.Unmarshal(raw, &r)
	return r
}

// toRawValue 轉成驗證函式接受的 JSON 物件（整數為 json.Number）。
func toRawValue(v any) map[string]any {
	data, _ := json.Marshal(v)
	out, _ := featurecfg.Decode(data)
	return out
}

// ---------- 查詢（只讀，監控網站與服務共用） ----------

func suppliesOf(ctx context.Context, t *db.Tx, eventID int64) (map[string]models.WorldBossSupply, error) {
	rows, err := db.QueryStructs[models.WorldBossSupply](ctx, t, "SELECT * FROM world_boss_supplies WHERE event_id = $1", eventID)
	if err != nil {
		return nil, err
	}
	out := map[string]models.WorldBossSupply{}
	for _, r := range rows {
		out[r.SupplyCode] = r
	}
	return out, nil
}

func progressOf(s map[string]models.WorldBossSupply) map[string]int {
	out := map[string]int{}
	for code, r := range s {
		out[code] = r.Progress
	}
	return out
}

func combatOf(ctx context.Context, t *db.Tx, ev *models.WorldBossEvent) (wb.CombatStats, map[string]models.WorldBossSupply, error) {
	s, err := suppliesOf(ctx, t, ev.ID)
	if err != nil {
		return wb.CombatStats{}, nil, err
	}
	return wb.Combat(progressOf(s), decodeBossSettings(ev.Settings)), s, nil
}

// BossRankRow 個人貢獻排行的一列。
type BossRankRow struct {
	Rank       int       `db:"-" json:"rank"`
	PlayerID   int64     `db:"player_id" json:"player_id"`
	PlayerName string    `db:"player_name" json:"player_name"`
	Points     int       `db:"points" json:"points"`
	Quantity   int       `db:"quantity" json:"quantity"`
	FirstAt    time.Time `db:"first_at" json:"first_at"`
	Donations  int       `db:"donations" json:"donations"`
}

// BossRanking 個人貢獻排行（積分高者在前；同分時先捐獻者在前），名次不並列。
func BossRanking(ctx context.Context, t *db.Tx, eventID int64) ([]BossRankRow, error) {
	rows, err := db.QueryStructs[BossRankRow](ctx, t, `SELECT c.player_id, p.display_name AS player_name,
		sum(c.points)::bigint AS points, sum(c.quantity)::bigint AS quantity, min(c.created_at) AS first_at, count(*) AS donations
		FROM world_boss_contributions c JOIN players p ON p.id = c.player_id WHERE c.event_id = $1
		GROUP BY c.player_id, p.display_name ORDER BY sum(c.points) DESC, min(c.created_at), c.player_id`, eventID)
	for i := range rows {
		rows[i].Rank = i + 1
	}
	if rows == nil {
		rows = []BossRankRow{}
	}
	return rows, err
}

type projection struct {
	HP         int
	DefeatedAt *time.Time
	EndedAt    *time.Time
	EndReason  *string
	State      string
}

// project 不寫入資料庫，推算目前的 HP、狀態與結束資訊（與 advance 相同公式，寫回後顯示結果一致）。
func project(ev *models.WorldBossEvent, stats wb.CombatStats, now time.Time) projection {
	out := projection{HP: ev.HP, DefeatedAt: ev.DefeatedAt, EndedAt: ev.EndedAt, EndReason: ev.EndReason}
	reason := func(s string) *string { return &s }
	switch {
	case ev.Status == "active":
		r := wb.Simulate(ev.HP, ev.LastTickAt, ev.StartsAt, ev.EndsAt, stats.DamagePerHour, now)
		out.HP = r.HP
		if r.DefeatedAt != nil {
			out.HP, out.DefeatedAt, out.EndedAt, out.EndReason = 0, r.DefeatedAt, r.DefeatedAt, reason("defeated")
		} else if r.Expired {
			ends := ev.EndsAt
			out.EndedAt, out.EndReason = &ends, reason("timeout")
		}
	case ev.Status == "paused" && !now.Before(ev.EndsAt):
		ends := ev.EndsAt
		out.EndedAt, out.EndReason = &ends, reason("timeout")
	}
	if out.EndReason != nil {
		out.State = "ended"
		if out.DefeatedAt != nil {
			out.State = "defeated"
		}
	} else {
		out.State = wb.DisplayState(ev.Status, false, ev.StartsAt, now)
	}
	return out
}

type BossSupplyView struct {
	Code        string  `json:"code"`
	Emoji       string  `json:"emoji"`
	Name        string  `json:"name"`
	Effect      string  `json:"effect"`
	Description string  `json:"description"`
	Progress    int     `json:"progress"`
	Target      int     `json:"target"`
	RatioPct    float64 `json:"ratio_pct"`
}

// BossView 一場世界 Boss 活動（Python event_view）；BossCurrent 另外填入 Me／Top／Claimable／Choices。
type BossView struct {
	EventID          int64            `json:"event_id"`
	GroupID          *int64           `json:"group_id"`
	GroupName        string           `json:"group_name"`
	ScopeLabel       string           `json:"scope_label"`
	ScheduleID       *int64           `json:"schedule_id"`
	SlotAt           *time.Time       `json:"slot_at"`
	Name             string           `json:"name"`
	MaxHP            int              `json:"max_hp"`
	HP               int              `json:"hp"`
	HPPct            float64          `json:"hp_pct"`
	Status           string           `json:"status"`
	State            string           `json:"state"`
	StateLabel       string           `json:"state_label"`
	StartsAt         time.Time        `json:"starts_at"`
	EndsAt           time.Time        `json:"ends_at"`
	EndedAt          *time.Time       `json:"ended_at"`
	EndReason        *string          `json:"end_reason"`
	DefeatedAt       *time.Time       `json:"defeated_at"`
	SettledAt        *time.Time       `json:"settled_at"`
	PausedAt         *time.Time       `json:"paused_at"`
	RemainingSeconds int              `json:"remaining_seconds"`
	Supplies         []BossSupplyView `json:"supplies"`
	AttackMult       float64          `json:"attack_mult"`
	UptimePct        float64          `json:"uptime_pct"`
	DamagePerHour    int              `json:"damage_per_hour"`
	Participants     int              `json:"participants"`
	TotalQuantity    int              `json:"total_quantity"`
	TotalPoints      int              `json:"total_points"`
	Donations        int              `json:"donations"`
	Claims           int              `json:"claims"`
	Settings         wb.Settings      `json:"-"`
	Rewards          wb.Rewards       `json:"-"`
	RawSettings      json.RawMessage  `json:"settings"`          // 資料庫中的原始設定 JSON（Python 直接輸出 ev.settings，鍵順序不變）
	RawRewards       json.RawMessage  `json:"rewards,omitempty"` // 同上；監控列表會拿掉（nil 時不輸出）
	RewardsText      *BossRewardsText `json:"rewards_text"`      // names 為 nil 時不產生
	CreatedBy        string           `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`

	Me        *BossMyStatus   `json:"-"`
	Top       []BossRankRow   `json:"-"`
	Claimable []BossClaimable `json:"-"`
	Choices   []BossChoice    `json:"-"`

	// DetailView（管理網站）
	Ranking       []BossRankRow         `json:"-"`
	Contributions []BossContributionRow `json:"-"`
	ClaimRows     []BossClaimRow        `json:"-"`
}

// BossDetail 管理網站活動明細的 JSON（Python detail_view：event_view 加上 ranking、contributions、claim_rows）。
type BossDetail struct {
	BossView
	Ranking       []BossRankRow         `json:"ranking"`
	Contributions []BossContributionRow `json:"contributions"`
	ClaimRows     []BossClaimRow        `json:"claim_rows"`
}

// Detail 轉成明細 JSON（nil 切片輸出 []）。
func (v BossView) Detail() BossDetail {
	return BossDetail{v, nonNil(v.Ranking), nonNil(v.Contributions), nonNil(v.ClaimRows)}
}

// EventView 活動資訊（不寫入資料庫）；names 為 nil 時不產生獎勵文字。
func EventView(ctx context.Context, t *db.Tx, ev *models.WorldBossEvent, now time.Time, names map[string]string) (BossView, error) {
	stats, supplies, err := combatOf(ctx, t, ev)
	if err != nil {
		return BossView{}, err
	}
	settings, rewards := decodeBossSettings(ev.Settings), decodeBossRewards(ev.Rewards)
	p := project(ev, stats, now)
	v := BossView{EventID: ev.ID, GroupID: ev.GroupID, GroupName: "全服", ScopeLabel: globalLabel, ScheduleID: ev.ScheduleID,
		SlotAt: ev.SlotAt, Name: ev.Name, MaxHP: ev.MaxHP, HP: p.HP, HPPct: numfmt.RoundN(float64(p.HP)*100/float64(ev.MaxHP), 1),
		Status: ev.Status, State: p.State, StateLabel: wb.StateLabels[p.State], StartsAt: ev.StartsAt, EndsAt: ev.EndsAt,
		EndedAt: p.EndedAt, EndReason: p.EndReason, DefeatedAt: p.DefeatedAt, SettledAt: ev.SettledAt, PausedAt: ev.PausedAt,
		AttackMult: numfmt.RoundN(stats.AttackMult, 2), UptimePct: numfmt.RoundN(stats.Uptime*100, 1),
		DamagePerHour: int(stats.DamagePerHour), Settings: settings, Rewards: rewards, RawSettings: ev.Settings,
		RawRewards: ev.Rewards, CreatedBy: ev.CreatedBy, CreatedAt: ev.CreatedAt}
	if ev.GroupID != nil {
		g, err := groups.Get(ctx, t, *ev.GroupID)
		if err != nil {
			return BossView{}, err
		}
		if g != nil {
			v.GroupName, v.ScopeLabel = g.Name, scopeLabel(g)
		}
	}
	if p.State != "ended" && p.State != "defeated" {
		v.RemainingSeconds = max(0, int(ev.EndsAt.Sub(now).Seconds()))
	}
	for _, code := range wb.Supplies {
		info := wb.SupplyInfos[code]
		row := BossSupplyView{Code: code, Emoji: info.Emoji, Name: info.Name, Effect: info.Effect, Description: info.Desc,
			Target: settings.SupplyTargets[code], RatioPct: numfmt.RoundN(stats.Ratios[code]*100, 1)}
		if s, ok := supplies[code]; ok {
			row.Progress, row.Target = s.Progress, s.Target
		}
		v.Supplies = append(v.Supplies, row)
	}
	if err := t.QueryRow(ctx, `SELECT count(DISTINCT player_id), coalesce(sum(quantity), 0), coalesce(sum(points), 0), count(*)
		FROM world_boss_contributions WHERE event_id = $1`, ev.ID).Scan(&v.Participants, &v.TotalQuantity, &v.TotalPoints, &v.Donations); err != nil {
		return BossView{}, err
	}
	if v.Claims, _, err = db.QueryValue[int](ctx, t, "SELECT count(*) FROM world_boss_reward_claims WHERE event_id = $1", ev.ID); err != nil {
		return BossView{}, err
	}
	if names != nil {
		rt := NewBossRewardsText(rewards, names)
		v.RewardsText = &rt
	}
	return v, nil
}

type BossContributionRow struct {
	ID           int64     `json:"id"`
	PlayerName   string    `json:"player_name"`
	Category     string    `json:"category"`
	CategoryName string    `json:"category_name"`
	Supply       string    `json:"supply"`
	ItemCode     string    `json:"item_code"`
	ItemName     string    `json:"item_name"`
	Quantity     int       `json:"quantity"`
	Points       int       `json:"points"`
	Damage       int       `json:"damage"`
	CreatedAt    time.Time `json:"created_at"`
}

type BossClaimRow struct {
	PlayerName string    `json:"player_name"`
	Rank       int       `json:"rank"`
	Points     int       `json:"points"`
	Outcome    string    `json:"outcome"`
	Gold       int       `json:"gold"`
	ItemsText  string    `json:"items_text"`
	Title      *string   `json:"title"`
	ClaimedAt  time.Time `json:"claimed_at"`
}

// bossNames 物品與魚的顯示名稱。
func bossNames(ctx context.Context, t *db.Tx) (map[string]string, error) {
	names, err := catalog.ItemNames(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(names))
	for k, v := range names {
		out[k] = v
	}
	fish, err := fishrepo.ListFishTypes(ctx, t, false)
	for _, f := range fish {
		out[f.Code] = f.Name
	}
	return out, err
}

// DetailView 管理網站用：活動資訊、完整排行、捐獻紀錄與獎勵發放紀錄。
func DetailView(ctx context.Context, t *db.Tx, ev *models.WorldBossEvent, now time.Time) (BossView, error) {
	names, err := bossNames(ctx, t)
	if err != nil {
		return BossView{}, err
	}
	v, err := EventView(ctx, t, ev, now, names)
	if err != nil {
		return BossView{}, err
	}
	if v.Ranking, err = BossRanking(ctx, t, ev.ID); err != nil {
		return BossView{}, err
	}
	type contribRow struct {
		models.WorldBossContribution
		PlayerName string `db:"player_name"`
	}
	rows, err := db.QueryStructs[contribRow](ctx, t, `SELECT c.*, p.display_name AS player_name FROM world_boss_contributions c
		JOIN players p ON p.id = c.player_id WHERE c.event_id = $1 ORDER BY c.created_at DESC, c.id DESC LIMIT $2`,
		ev.ID, contributionLogLimit)
	if err != nil {
		return BossView{}, err
	}
	v.Contributions = []BossContributionRow{}
	for _, c := range rows {
		n, ok := names[c.ItemCode]
		if !ok {
			n = c.ItemCode
		}
		v.Contributions = append(v.Contributions, BossContributionRow{c.ID, c.PlayerName, c.Category,
			wb.CategoryInfos[c.Category].Name, wb.SupplyInfos[c.SupplyCode].Name, c.ItemCode,
			n + GeneticsLabel(deref(c.Quality), deref(c.Mutation)), c.Quantity, c.Points, c.Damage, c.CreatedAt})
	}
	claims, err := db.QueryStructs[models.WorldBossRewardClaim](ctx, t,
		"SELECT * FROM world_boss_reward_claims WHERE event_id = $1 ORDER BY claimed_at DESC", ev.ID)
	if err != nil {
		return BossView{}, err
	}
	v.ClaimRows = []BossClaimRow{}
	for _, c := range claims {
		v.ClaimRows = append(v.ClaimRows, BossClaimRow{c.PlayerName, c.Rank, c.Points, c.Outcome, c.Gold,
			BossRewardText(wb.Reward{Items: c.Items}, names), c.Title, c.ClaimedAt})
	}
	return v, nil
}

func lastSlot(ctx context.Context, t *db.Tx, scheduleID int64) (*time.Time, error) {
	v, _, err := db.QueryValue[*time.Time](ctx, t, "SELECT max(slot_at) FROM world_boss_events WHERE schedule_id = $1", scheduleID)
	return v, err
}

func scheduleTimes(s models.WorldBossSchedule) wb.ScheduleTimes {
	return wb.ScheduleTimes{AnchorDate: db.TaipeiDate(s.AnchorDate), IntervalDays: s.IntervalDays, SpawnHour: s.SpawnHour,
		SpawnMinute: s.SpawnMinute, DurationMinutes: s.DurationMinutes, MaxHP: s.MaxHP}
}

type BossNextEvent struct {
	EventID  int64      `json:"event_id"`
	Name     string     `json:"name"`
	StartsAt time.Time  `json:"starts_at"`
	EndsAt   time.Time  `json:"ends_at"`
	SlotAt   *time.Time `json:"slot_at"`
}

// BossScheduleView 排程：時間規則、下一場（已建立的活動）與之後幾場的預計時間。
type BossScheduleView struct {
	ScheduleID int64            `json:"schedule_id"`
	Name       string           `json:"name"`
	GroupID    *int64           `json:"group_id"`
	GroupName  string           `json:"group_name"`
	ScopeLabel string           `json:"scope_label"`
	Times      wb.ScheduleTimes `json:"-"`
	// Python **schedule_values(s)：時間欄位攤平在排程中（anchor_date 為台灣日期字串）
	AnchorDate      string          `json:"anchor_date"`
	IntervalDays    int             `json:"interval_days"`
	SpawnHour       int             `json:"spawn_hour"`
	SpawnMinute     int             `json:"spawn_minute"`
	DurationMinutes int             `json:"duration_minutes"`
	MaxHP           int             `json:"max_hp"`
	Enabled         bool            `json:"enabled"`
	TimeText        string          `json:"time_text"`
	Settings        wb.Settings     `json:"-"`
	Rewards         wb.Rewards      `json:"-"`
	RawSettings     json.RawMessage `json:"settings"` // 資料庫中的原始 JSON（Python 直接輸出 s.settings／s.rewards）
	RawRewards      json.RawMessage `json:"rewards"`
	RewardsText     BossRewardsText `json:"rewards_text"`
	NextEvent       *BossNextEvent  `json:"next_event"`
	UpcomingSlots   []time.Time     `json:"upcoming_slots"`
	CreatedBy       string          `json:"created_by"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func ScheduleView(ctx context.Context, t *db.Tx, s *models.WorldBossSchedule, now time.Time, names map[string]string) (BossScheduleView, error) {
	rewards := decodeBossRewards(s.Rewards)
	v := BossScheduleView{ScheduleID: s.ID, Name: s.Name, GroupID: s.GroupID, GroupName: "全服", ScopeLabel: globalLabel,
		Times: scheduleTimes(*s), Enabled: s.Enabled, Settings: decodeBossSettings(s.Settings), Rewards: rewards,
		RawSettings: s.Settings, RawRewards: s.Rewards,
		RewardsText: NewBossRewardsText(rewards, names), UpcomingSlots: []time.Time{}, CreatedBy: s.CreatedBy, UpdatedAt: s.UpdatedAt}
	if s.GroupID != nil {
		g, err := groups.Get(ctx, t, *s.GroupID)
		if err != nil {
			return v, err
		}
		if g != nil {
			v.GroupName, v.ScopeLabel = g.Name, scopeLabel(g)
		}
	}
	v.AnchorDate, v.IntervalDays, v.SpawnHour = v.Times.AnchorDate.Format(time.DateOnly), s.IntervalDays, s.SpawnHour
	v.SpawnMinute, v.DurationMinutes, v.MaxHP = s.SpawnMinute, s.DurationMinutes, s.MaxHP
	hours, minutes := s.DurationMinutes/60, s.DurationMinutes%60
	v.TimeText = fmt.Sprintf("每 %d 天 %02d:%02d（台灣時間）出現，持續 %d 小時", s.IntervalDays, s.SpawnHour, s.SpawnMinute, hours)
	if minutes != 0 {
		v.TimeText += fmt.Sprintf(" %d 分", minutes)
	}
	up, err := db.QueryStruct[models.WorldBossEvent](ctx, t,
		"SELECT * FROM world_boss_events WHERE schedule_id = $1 AND status <> 'ended' ORDER BY slot_at LIMIT 1", s.ID)
	if err != nil {
		return v, err
	}
	if up != nil {
		v.NextEvent = &BossNextEvent{up.ID, up.Name, up.StartsAt, up.EndsAt, up.SlotAt}
	}
	if s.Enabled {
		after, err := lastSlot(ctx, t, s.ID)
		if err != nil {
			return v, err
		}
		v.UpcomingSlots = wb.UpcomingSlots(v.Times, now, after, wb.SchedulePreview)
	}
	return v, nil
}

// ---------- 共用 ----------

func bossForUpdate(ctx context.Context, t *db.Tx, eventID int64) (*models.WorldBossEvent, error) {
	ev, err := db.QueryStruct[models.WorldBossEvent](ctx, t, "SELECT * FROM world_boss_events WHERE id = $1 FOR UPDATE", eventID)
	if err != nil {
		return nil, err
	}
	if ev == nil {
		return nil, gameerr.NotFoundf("找不到世界 Boss 活動 #%d。", eventID)
	}
	return ev, nil
}

func saveEvent(ctx context.Context, t *db.Tx, ev *models.WorldBossEvent) error {
	_, err := t.Exec(ctx, `UPDATE world_boss_events SET name = $2, level = $3, max_hp = $4, hp = $5, status = $6,
		starts_at = $7, ends_at = $8, last_tick_at = $9, paused_at = $10, defeated_at = $11, ended_at = $12, end_reason = $13,
		settled_at = $14, settings = $15::json, rewards = $16::json, updated_at = $17 WHERE id = $1`,
		ev.ID, ev.Name, ev.Level, ev.MaxHP, ev.HP, ev.Status, ev.StartsAt, ev.EndsAt, ev.LastTickAt, ev.PausedAt, ev.DefeatedAt,
		ev.EndedAt, ev.EndReason, ev.SettledAt, string(ev.Settings), string(ev.Rewards), ev.UpdatedAt)
	return err
}

func bossFinish(ev *models.WorldBossEvent, reason string, at, now time.Time) {
	ev.Status, ev.EndReason, ev.EndedAt, ev.PausedAt = "ended", &reason, &at, nil
	if reason == "defeated" {
		ev.DefeatedAt, ev.HP = &at, 0
	}
	ev.SettledAt, ev.UpdatedAt = &now, now
}

// advance 把戰鬥推進到現在並寫回（只有進行中的活動會扣 HP）；到期或擊敗時結束活動。
func advance(ctx context.Context, t *db.Tx, ev *models.WorldBossEvent, now time.Time) error {
	if ev.Status == "ended" {
		return nil
	}
	changed := false
	if ev.Status == "paused" {
		if !now.Before(ev.EndsAt) {
			bossFinish(ev, "timeout", ev.EndsAt, now)
			changed = true
		}
	} else {
		stats, _, err := combatOf(ctx, t, ev)
		if err != nil {
			return err
		}
		r := wb.Simulate(ev.HP, ev.LastTickAt, ev.StartsAt, ev.EndsAt, stats.DamagePerHour, now)
		if r.HP != ev.HP || !r.LastTickAt.Equal(ev.LastTickAt) {
			ev.HP, ev.LastTickAt, ev.UpdatedAt = r.HP, r.LastTickAt, now
			changed = true
		}
		if r.DefeatedAt != nil {
			bossFinish(ev, "defeated", *r.DefeatedAt, now)
			changed = true
		} else if r.Expired {
			bossFinish(ev, "timeout", ev.EndsAt, now)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return saveEvent(ctx, t, ev)
}

func bossClaim(ctx context.Context, t *db.Tx, eventID, playerID int64) (*models.WorldBossRewardClaim, error) {
	return db.QueryStruct[models.WorldBossRewardClaim](ctx, t,
		"SELECT * FROM world_boss_reward_claims WHERE event_id = $1 AND player_id = $2", eventID, playerID)
}

// memberEvent 確認玩家可以參加（lock 時鎖定活動列）：全服活動所有玩家都可以；群組活動必須在該群組中操作且是成員
// （其他群組的活動一律視為不存在）。只讀取的查詢用 lock=false，不會和捐獻搶同一列的鎖。
func memberEvent(ctx context.Context, t *db.Tx, groupID *int64, playerID, eventID int64, lock bool) (*models.WorldBossEvent, error) {
	if groupID != nil {
		if err := requireMember(ctx, t, *groupID, playerID); err != nil {
			return nil, err
		}
	}
	var ev *models.WorldBossEvent
	var err error
	if lock {
		ev, err = bossForUpdate(ctx, t, eventID)
	} else {
		ev, err = db.QueryStruct[models.WorldBossEvent](ctx, t, "SELECT * FROM world_boss_events WHERE id = $1", eventID)
	}
	if err != nil {
		return nil, err
	}
	if ev == nil || (ev.GroupID != nil && (groupID == nil || *ev.GroupID != *groupID)) {
		return nil, gameerr.NotFoundf("找不到世界 Boss 活動 #%d。", eventID)
	}
	return ev, nil
}

// scopeSQL 玩家在目前位置看得到的活動（全服活動，加上所在群組的活動）；參數 $n 為群組 ID（可為 NULL）。
func scopeSQL(param string) string {
	return "(group_id IS NULL OR group_id = " + param + "::bigint)"
}

func cropsByProduce(ctx context.Context, t *db.Tx) (map[string]models.CropType, error) {
	m, err := crops.ProduceMap(ctx, t)
	out := map[string]models.CropType{}
	for code, p := range m {
		out[code] = p.Crop
	}
	return out, err
}

// pointsPerUnit 每個物品的積分：指定物品用固定積分；作物依售價（含品質與突變）換算；其他物品不能捐（0）。
func pointsPerUnit(ctx context.Context, t *db.Tx, s wb.Settings, category, code, quality, mutation string, crop *models.CropType) (int, error) {
	if fixed, ok := s.Items[code]; ok {
		return fixed.Points, nil
	}
	if category == "crop" && crop != nil {
		_, gs, err := generepo.Current(ctx, t)
		if err != nil {
			return 0, err
		}
		return wb.PricePoints(genetics.UnitPrice(crop.SellPrice, quality, mutation, gs), s.GoldPerPoint), nil
	}
	return 0, nil
}

func supplyOf(s wb.Settings, category, code string) string {
	if fixed, ok := s.Items[code]; ok {
		return fixed.Supply
	}
	if category == "fish" {
		return s.FishSupply
	}
	return s.CropSupply
}

func donatedByItem(ctx context.Context, t *db.Tx, eventID, playerID int64) (map[string]int, error) {
	rows, err := t.Query(ctx, `SELECT item_code, sum(quantity) FROM world_boss_contributions WHERE event_id = $1 AND player_id = $2
		GROUP BY item_code`, eventID, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var code string
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		out[code] = n
	}
	return out, rows.Err()
}

func itemKinds(ctx context.Context, t *db.Tx) (map[string]catalog.Entry, error) {
	items, err := catalog.ListItems(ctx, t)
	if err != nil {
		return nil, err
	}
	return items.Map, nil
}

// ---------- 玩家 ----------

type BossMyStatus struct {
	PlayerName   string
	Points       int
	Quantity     int
	Rank         *int
	Participants int
	ClaimState   string // claimed／pending／none／below_min／ready
	Title        *string
}

type BossClaimable struct {
	EventID  int64
	Name     string
	EndedAt  *time.Time
	Defeated bool
}

type BossChoice struct {
	EventID    int64
	Name       string
	ScopeLabel string
	StateLabel string
	StartsAt   time.Time
}

// BossCurrent 玩家看得到的世界 Boss（全服活動與所在群組的活動）：指定 eventID 時顯示那一場，否則顯示進行中的活動
// （沒有時為即將開始、再沒有時為最近結束的一場），並附上自己的貢獻與可切換的其他活動；沒有任何活動時回傳 nil。
// groupID 為 nil 表示私訊（只看得到全服活動）；eventID 為 nil 表示自動選擇。
func (w *WorldBossService) BossCurrent(ctx context.Context, groupID *int64, playerID int64, eventID *int64) (*BossView, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (*BossView, error) {
		if _, err := spawnDue(ctx, t, now, nil); err != nil {
			return nil, err
		}
		if groupID != nil {
			if err := requireMember(ctx, t, *groupID, playerID); err != nil {
				return nil, err
			}
		}
		openIDs, err := db.QueryValues[int64](ctx, t, "SELECT id FROM world_boss_events WHERE "+scopeSQL("$1")+
			" AND status <> 'ended' ORDER BY starts_at > $2, starts_at, id", groupID, now)
		if err != nil {
			return nil, err
		}
		var id *int64
		if eventID != nil {
			v, ok, err := db.QueryValue[int64](ctx, t, "SELECT id FROM world_boss_events WHERE "+scopeSQL("$1")+" AND id = $2", groupID, *eventID)
			if err != nil {
				return nil, err
			}
			if ok { // 看不到的活動（其他群組）一律忽略
				id = &v
			}
		}
		if id == nil {
			if len(openIDs) > 0 {
				id = &openIDs[0]
			} else {
				v, ok, err := db.QueryValue[int64](ctx, t, "SELECT id FROM world_boss_events WHERE "+scopeSQL("$1")+
					" ORDER BY ended_at DESC NULLS LAST, id DESC LIMIT 1", groupID)
				if err != nil || !ok {
					return nil, err
				}
				id = &v
			}
		}
		// 只讀取：HP 與狀態依公式推算，不鎖活動列，大量玩家同時查看不會互相等待
		ev, err := db.QueryStruct[models.WorldBossEvent](ctx, t, "SELECT * FROM world_boss_events WHERE id = $1", *id)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		v, err := EventView(ctx, t, ev, now, names)
		if err != nil {
			return nil, err
		}
		rows, err := BossRanking(ctx, t, ev.ID)
		if err != nil {
			return nil, err
		}
		if v.Me, err = myBossStatus(ctx, t, ev, playerID, rows, v.State); err != nil {
			return nil, err
		}
		v.Top = rows[:min(3, len(rows))]
		if v.Claimable, err = claimable(ctx, t, groupID, playerID, ev.ID); err != nil {
			return nil, err
		}
		v.Choices = []BossChoice{}
		for _, oid := range openIDs {
			c, err := db.QueryStruct[models.WorldBossEvent](ctx, t, "SELECT * FROM world_boss_events WHERE id = $1", oid)
			if err != nil {
				return nil, err
			}
			var g *models.Group
			if c.GroupID != nil {
				if g, err = groups.Get(ctx, t, *c.GroupID); err != nil {
					return nil, err
				}
			}
			state := wb.DisplayState(c.Status, false, c.StartsAt, now)
			v.Choices = append(v.Choices, BossChoice{c.ID, c.Name, scopeLabel(g), wb.StateLabels[state], c.StartsAt})
		}
		return &v, nil
	})
}

func myBossStatus(ctx context.Context, t *db.Tx, ev *models.WorldBossEvent, playerID int64, rows []BossRankRow, state string) (*BossMyStatus, error) {
	var mine *BossRankRow
	for i := range rows {
		if rows[i].PlayerID == playerID {
			mine = &rows[i]
		}
	}
	claim, err := bossClaim(ctx, t, ev.ID, playerID)
	if err != nil {
		return nil, err
	}
	st := &BossMyStatus{Participants: len(rows)}
	switch {
	case claim != nil:
		st.ClaimState, st.Title = "claimed", claim.Title
	case state != "ended" && state != "defeated":
		st.ClaimState = "pending" // 活動結束後才能領
	case mine == nil:
		st.ClaimState = "none" // 沒有貢獻
	case mine.Points < wb.MinPoints(decodeBossRewards(ev.Rewards)):
		st.ClaimState = "below_min" // 積分未達參與門檻
	default:
		st.ClaimState = "ready"
	}
	p, err := players.Get(ctx, t, playerID, false)
	if err != nil {
		return nil, err
	}
	if p != nil {
		st.PlayerName = p.DisplayName
	}
	if mine != nil {
		rank := mine.Rank
		st.Points, st.Quantity, st.Rank = mine.Points, mine.Quantity, &rank
	}
	return st, nil
}

// claimable 看得到的活動中已結束、有貢獻（達參與門檻）但還沒領獎的其他活動。
func claimable(ctx context.Context, t *db.Tx, groupID *int64, playerID, exclude int64) ([]BossClaimable, error) {
	type row struct {
		ID         int64           `db:"id"`
		Name       string          `db:"name"`
		EndedAt    *time.Time      `db:"ended_at"`
		DefeatedAt *time.Time      `db:"defeated_at"`
		Rewards    json.RawMessage `db:"rewards"`
		Points     int             `db:"points"`
	}
	rows, err := db.QueryStructs[row](ctx, t, `SELECT e.id, e.name, e.ended_at, e.defeated_at, e.rewards,
		(SELECT coalesce(sum(c.points), 0) FROM world_boss_contributions c WHERE c.event_id = e.id AND c.player_id = $2)::bigint AS points
		FROM world_boss_events e WHERE (e.group_id IS NULL OR e.group_id = $1::bigint) AND e.status = 'ended' AND e.id <> $3
		AND EXISTS (SELECT 1 FROM world_boss_contributions c WHERE c.event_id = e.id AND c.player_id = $2)
		AND NOT EXISTS (SELECT 1 FROM world_boss_reward_claims r WHERE r.event_id = e.id AND r.player_id = $2)
		ORDER BY e.ended_at DESC LIMIT 10`, groupID, playerID, exclude)
	if err != nil {
		return nil, err
	}
	out := []BossClaimable{}
	for _, r := range rows {
		if r.Points >= wb.MinPoints(decodeBossRewards(r.Rewards)) {
			out = append(out, BossClaimable{r.ID, r.Name, r.EndedAt, r.DefeatedAt != nil})
		}
	}
	return out, nil
}

type BossDonationOption struct {
	Category  string
	Code      string
	Quality   string
	Mutation  string
	Name      string
	Held      int
	PointsMin int
	PointsMax int
	Supply    string
	Remaining int
}

// BossDonationOptions 玩家背包中這個類別可以捐獻的物品：名稱、持有數量、每個積分、補給類型與本場剩餘可捐數量。
func (w *WorldBossService) BossDonationOptions(ctx context.Context, groupID *int64, playerID, eventID int64, category string) ([]BossDonationOption, error) {
	if !slices.Contains(wb.Categories, category) {
		return nil, gameerr.Validationf("捐獻類別不正確。")
	}
	return inTx(ctx, w.GameService, func(t *db.Tx) ([]BossDonationOption, error) {
		ev, err := memberEvent(ctx, t, groupID, playerID, eventID, false)
		if err != nil {
			return nil, err
		}
		farm, err := w.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		s := decodeBossSettings(ev.Settings)
		donated, err := donatedByItem(ctx, t, ev.ID, playerID)
		if err != nil {
			return nil, err
		}
		limit := s.Limits.PerItemMax
		out := []BossDonationOption{}
		if category == "fish" {
			type fishRow struct {
				Code  string `db:"fish_code"`
				Name  string `db:"name"`
				Emoji string `db:"emoji"`
				Count int    `db:"count"`
				Low   int    `db:"low"`
				High  int    `db:"high"`
			}
			rows, err := db.QueryStructs[fishRow](ctx, t, `SELECT f.fish_code, ft.name, ft.emoji, count(*) AS count,
				min(f.price) AS low, max(f.price) AS high FROM farm_fish f JOIN fish_types ft ON ft.code = f.fish_code
				WHERE f.farm_id = $1 AND `+fishrepo.InBasket("f")+`
				GROUP BY f.fish_code, ft.name, ft.emoji, ft.sort_order ORDER BY ft.sort_order, f.fish_code`, farm.ID)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				fixed, isFixed := s.Items[r.Code]
				if isFixed && fixed.Points <= 0 {
					continue
				}
				lo, hi := wb.PricePoints(r.Low, s.GoldPerPoint), wb.PricePoints(r.High, s.GoldPerPoint)
				if isFixed {
					lo, hi = fixed.Points, fixed.Points
				}
				out = append(out, BossDonationOption{"fish", r.Code, "normal", "none", r.Emoji + " " + r.Name, r.Count, lo, hi,
					supplyOf(s, "fish", r.Code), max(0, limit-donated[r.Code])})
			}
			return out, nil
		}
		kinds, err := itemKinds(ctx, t)
		if err != nil {
			return nil, err
		}
		cropMap, err := cropsByProduce(ctx, t)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		stacks, err := inventory.ListItems(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		for _, st := range stacks {
			if ItemCategory(kinds[st.ItemCode].Kind, st.ItemCode) != category {
				continue
			}
			var crop *models.CropType
			if c, ok := cropMap[st.ItemCode]; ok {
				crop = &c
			}
			pts, err := pointsPerUnit(ctx, t, s, category, st.ItemCode, st.Quality, st.Mutation, crop)
			if err != nil {
				return nil, err
			}
			if pts <= 0 {
				continue
			}
			name, ok := names[st.ItemCode]
			if !ok {
				name = st.ItemCode
			}
			if crop != nil {
				name = genetics.ProduceName(crop.Name, crop.VariantName, st.Mutation)
			}
			out = append(out, BossDonationOption{category, st.ItemCode, st.Quality, st.Mutation, name + GeneticsLabel(st.Quality, st.Mutation),
				st.Quantity, pts, pts, supplyOf(s, category, st.ItemCode), max(0, limit-donated[st.ItemCode])})
		}
		return out, nil
	})
}

type BossDonateResult struct {
	Message   string
	Duplicate bool
	Points    int
	Quantity  int
	Damage    int
	Defeated  bool
}

// BossDonate 捐獻物資：扣除背包物品（或魚簍的魚）、增加補給進度與個人積分，並立即造成一次傷害。
// requestID 相同的請求只處理一次（重複點擊回傳 Duplicate=true，不再扣物品）。quality 預設 "normal"，mutation 預設 "none"。
func (w *WorldBossService) BossDonate(ctx context.Context, groupID *int64, playerID, eventID int64, category, itemCode string,
	quantity int, requestID, quality, mutation string) (BossDonateResult, error) {
	if !slices.Contains(wb.Categories, category) {
		return BossDonateResult{}, gameerr.Validationf("捐獻類別不正確。")
	}
	if !requestIDRe.MatchString(requestID) {
		return BossDonateResult{}, gameerr.Validationf("捐獻請求編號不正確，請重新開啟捐獻畫面。")
	}
	if n := utf8.RuneCountInString(itemCode); n < 1 || n > 64 {
		return BossDonateResult{}, gameerr.Validationf("物品代碼不正確。")
	}
	if !slices.Contains(balance.QualityOrder, quality) || !slices.Contains(balance.MutationOrder, mutation) {
		return BossDonateResult{}, gameerr.Validationf("物品品質不正確。")
	}
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return BossDonateResult{}, err
	}
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossDonateResult, error) {
		ev, err := memberEvent(ctx, t, groupID, playerID, eventID, true)
		if err != nil {
			return BossDonateResult{}, err
		}
		farm, err := w.lockedFarm(ctx, t, playerID)
		if err != nil {
			return BossDonateResult{}, err
		}
		done, err := db.QueryStruct[models.WorldBossContribution](ctx, t, `SELECT * FROM world_boss_contributions
			WHERE event_id = $1 AND player_id = $2 AND request_id = $3`, ev.ID, playerID, requestID)
		if err != nil {
			return BossDonateResult{}, err
		}
		if done != nil {
			return BossDonateResult{Message: "這筆捐獻已經完成，不會重複扣除物品。", Duplicate: true, Points: done.Points, Quantity: done.Quantity}, nil
		}
		if err := advance(ctx, t, ev, now); err != nil {
			return BossDonateResult{}, err
		}
		if state := wb.DisplayState(ev.Status, ev.DefeatedAt != nil, ev.StartsAt, now); state != "active" {
			return BossDonateResult{}, gameerr.Validationf("世界 Boss 活動%s，現在不能捐獻。", wb.StateLabels[state])
		}
		s := decodeBossSettings(ev.Settings)
		if quantity > s.Limits.PerActionMax {
			return BossDonateResult{}, gameerr.Validationf("單次最多捐獻 %d 個。", s.Limits.PerActionMax)
		}
		donated, err := donatedByItem(ctx, t, ev.ID, playerID)
		if err != nil {
			return BossDonateResult{}, err
		}
		if already := donated[itemCode]; already+quantity > s.Limits.PerItemMax {
			return BossDonateResult{}, gameerr.Validationf("每種物品每場活動最多捐獻 %d 個，你這項還可以捐 %d 個。",
				s.Limits.PerItemMax, max(0, s.Limits.PerItemMax-already))
		}
		var points int
		var name string
		var q, m string // "" = None（魚）
		if category == "fish" {
			if points, name, err = takeFish(ctx, t, farm.ID, itemCode, quantity, s, now); err != nil {
				return BossDonateResult{}, err
			}
		} else {
			kinds, err := itemKinds(ctx, t)
			if err != nil {
				return BossDonateResult{}, err
			}
			if ItemCategory(kinds[itemCode].Kind, itemCode) != category {
				return BossDonateResult{}, gameerr.Validationf("這個物品不能用這個類別捐獻。")
			}
			cropMap, err := cropsByProduce(ctx, t)
			if err != nil {
				return BossDonateResult{}, err
			}
			var crop *models.CropType
			if c, ok := cropMap[itemCode]; ok {
				crop = &c
			}
			per, err := pointsPerUnit(ctx, t, s, category, itemCode, quality, mutation, crop)
			if err != nil {
				return BossDonateResult{}, err
			}
			if per <= 0 {
				return BossDonateResult{}, gameerr.Validationf("這個物品不在本次活動的捐獻清單中。")
			}
			ok, err := inventory.Remove(ctx, t, farm.ID, itemCode, quantity, quality, mutation)
			if err != nil {
				return BossDonateResult{}, err
			}
			if !ok {
				held, err := inventory.GetQuantity(ctx, t, farm.ID, itemCode, quality, mutation)
				if err != nil {
					return BossDonateResult{}, err
				}
				return BossDonateResult{}, gameerr.New(gameerr.InsufficientItems, "背包數量不足：只有 %d 個，不能捐獻 %d 個。", held, quantity)
			}
			points = per * quantity
			if crop != nil {
				name = genetics.ProduceName(crop.Name, crop.VariantName, mutation)
			} else {
				names, err := catalog.ItemNames(ctx, t)
				if err != nil {
					return BossDonateResult{}, err
				}
				if name = names[itemCode]; name == "" {
					name = itemCode
				}
			}
			name += GeneticsLabel(quality, mutation)
			q, m = quality, mutation
		}
		supplyCode := supplyOf(s, category, itemCode)
		supplies, err := suppliesOf(ctx, t, ev.ID)
		if err != nil {
			return BossDonateResult{}, err
		}
		sup := supplies[supplyCode]
		sup.Progress += points
		supplies[supplyCode] = sup
		if _, err := t.Exec(ctx, "UPDATE world_boss_supplies SET progress = $3 WHERE event_id = $1 AND supply_code = $2",
			ev.ID, supplyCode, sup.Progress); err != nil {
			return BossDonateResult{}, err
		}
		stats := wb.Combat(progressOf(supplies), s)
		damage := min(ev.HP, wb.InstantDamage(points, s, stats.AttackMult))
		ev.HP -= damage
		ev.UpdatedAt = now
		err = t.Savepoint(ctx, func() error {
			return db.Insert(ctx, t, "world_boss_contributions", db.C("event_id", ev.ID), db.C("player_id", playerID),
				db.C("farm_id", farm.ID), db.C("request_id", requestID), db.C("category", category), db.C("supply_code", supplyCode),
				db.C("item_code", itemCode), db.C("quality", strPtr(q)), db.C("mutation", strPtr(m)), db.C("quantity", quantity),
				db.C("points", points), db.C("damage", damage), db.C("created_at", now))
		})
		if isIntegrity(err) { // 已被活動列鎖擋住；保險起見由唯一限制再擋一次
			return BossDonateResult{}, gameerr.Validationf("這筆捐獻已經處理過了。")
		}
		if err != nil {
			return BossDonateResult{}, err
		}
		supplyName := wb.SupplyInfos[supplyCode].Name
		defeated := ev.HP <= 0
		if defeated {
			bossFinish(ev, "defeated", now, now)
		}
		if err := saveEvent(ctx, t, ev); err != nil {
			return BossDonateResult{}, err
		}
		if err := w.record(ctx, t, farm, rules.EvBossDonate,
			truncRunes(fmt.Sprintf("世界 Boss「%s」捐獻 %s ×%d（%s +%d 積分）", ev.Name, name, quantity, supplyName, points), 200),
			rec{ItemCode: itemCode, Quantity: quantity, Quality: q, Mutation: m}); err != nil {
			return BossDonateResult{}, err
		}
		msg := fmt.Sprintf("📦 捐獻 %s ×%d，獲得 %d 積分（%s +%d），前線立即造成 %s 點傷害。", name, quantity, points, supplyName, points,
			numfmt.Comma(damage))
		if defeated {
			msg += fmt.Sprintf("\n🎉 %s 被擊敗了！可以領取勝利獎勵。", ev.Name)
		}
		return BossDonateResult{Message: msg, Points: points, Quantity: quantity, Damage: damage, Defeated: defeated}, nil
	})
}

// takeFish 捐出魚簍中這種魚價值最低的 quantity 隻（保留高價值的魚）。
func takeFish(ctx context.Context, t *db.Tx, farmID int64, code string, quantity int, s wb.Settings, now time.Time) (int, string, error) {
	fixed, isFixed := s.Items[code]
	if isFixed && fixed.Points <= 0 {
		return 0, "", gameerr.Validationf("這種魚不在本次活動的捐獻清單中。")
	}
	fish, err := db.QueryStructs[models.FarmFish](ctx, t, `SELECT * FROM farm_fish WHERE farm_id = $1 AND fish_code = $2 AND `+
		fishrepo.InBasket("")+` ORDER BY price, weight_g, id LIMIT $3 FOR UPDATE`, farmID, code, quantity)
	if err != nil {
		return 0, "", err
	}
	if len(fish) < quantity {
		return 0, "", gameerr.New(gameerr.InsufficientItems, "魚簍裡這種魚只有 %d 隻，不能捐獻 %d 隻。", len(fish), quantity)
	}
	points := 0
	ids := make([]int64, len(fish))
	for i, f := range fish {
		ids[i] = f.ID
		if isFixed {
			points += fixed.Points
		} else {
			points += wb.PricePoints(f.Price, s.GoldPerPoint)
		}
	}
	if _, err := t.Exec(ctx, "UPDATE farm_fish SET donated_at = $2 WHERE id = ANY($1)", ids, now); err != nil {
		return 0, "", err
	}
	ft, err := fishrepo.GetFishType(ctx, t, code, false)
	if err != nil || ft == nil {
		return points, code, err
	}
	return points, ft.Emoji + " " + ft.Name, nil
}

type BossLeaderboard struct {
	EventID      int64
	Name         string
	Rows         []BossRankRow
	Me           *BossRankRow
	Participants int
	EndedAt      *time.Time // 只有 BossLastLeaderboard 會填
	Defeated     bool
	IsCurrent    bool
}

func boardOf(ev *models.WorldBossEvent, rows []BossRankRow, playerID int64, limit int) BossLeaderboard {
	b := BossLeaderboard{EventID: ev.ID, Name: ev.Name, Rows: rows[:min(limit, len(rows))], Participants: len(rows)}
	for i := range rows {
		if rows[i].PlayerID == playerID {
			r := rows[i]
			b.Me = &r
		}
	}
	return b
}

// BossLeaderboard limit 預設 wb.LeaderboardSize。
func (w *WorldBossService) BossLeaderboard(ctx context.Context, groupID *int64, playerID, eventID int64, limit int) (BossLeaderboard, error) {
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossLeaderboard, error) {
		ev, err := memberEvent(ctx, t, groupID, playerID, eventID, false) // 排行與 HP 無關，只讀取
		if err != nil {
			return BossLeaderboard{}, err
		}
		rows, err := BossRanking(ctx, t, ev.ID)
		return boardOf(ev, rows, playerID, limit), err
	})
}

// BossLastLeaderboard 介紹區「查看排行榜」：最近一場已結束活動的排行。目前這場已結束就是它自己；否則找同一個排程
// （手動活動則是同一個範圍）中、在它之前開始的最近一場已結束活動。沒有任何已結束的活動時回傳 nil。只讀取不鎖。
func (w *WorldBossService) BossLastLeaderboard(ctx context.Context, groupID *int64, playerID, eventID int64, limit int) (*BossLeaderboard, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (*BossLeaderboard, error) {
		ev, err := memberEvent(ctx, t, groupID, playerID, eventID, false)
		if err != nil {
			return nil, err
		}
		projOf := func(e *models.WorldBossEvent) (projection, error) {
			stats, _, err := combatOf(ctx, t, e)
			return project(e, stats, now), err
		}
		finished := func(e *models.WorldBossEvent) (bool, error) {
			p, err := projOf(e)
			return p.State == "ended" || p.State == "defeated", err
		}
		var last *models.WorldBossEvent
		ok, err := finished(ev)
		if err != nil {
			return nil, err
		}
		if ok {
			last = ev
		} else {
			sql := "SELECT * FROM world_boss_events WHERE " + scopeSQL("$1") + " AND id <> $2 AND starts_at <= $3"
			args := []any{groupID, ev.ID, ev.StartsAt}
			switch {
			case ev.ScheduleID != nil:
				sql += " AND schedule_id = $4"
				args = append(args, *ev.ScheduleID)
			case ev.GroupID == nil:
				sql += " AND group_id IS NULL AND schedule_id IS NULL"
			default:
				sql += " AND group_id = $4 AND schedule_id IS NULL"
				args = append(args, *ev.GroupID)
			}
			cands, err := db.QueryStructs[models.WorldBossEvent](ctx, t, sql+" ORDER BY starts_at DESC, id DESC LIMIT 5", args...)
			if err != nil {
				return nil, err
			}
			for i := range cands {
				ok, err := finished(&cands[i])
				if err != nil {
					return nil, err
				}
				if ok {
					last = &cands[i]
					break
				}
			}
		}
		if last == nil {
			return nil, nil
		}
		p, err := projOf(last)
		if err != nil {
			return nil, err
		}
		rows, err := BossRanking(ctx, t, last.ID)
		if err != nil {
			return nil, err
		}
		b := boardOf(last, rows, playerID, limit)
		b.EndedAt, b.Defeated, b.IsCurrent = p.EndedAt, p.DefeatedAt != nil, last.ID == ev.ID
		return &b, nil
	})
}

// BossClaim 領取活動獎勵（活動結束後）：依結果、個人積分與名次發放；每人每場只能領一次。
func (w *WorldBossService) BossClaim(ctx context.Context, groupID *int64, playerID, eventID int64) (ActionResult, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (ActionResult, error) {
		ev, err := memberEvent(ctx, t, groupID, playerID, eventID, true)
		if err != nil {
			return ActionResult{}, err
		}
		if err := advance(ctx, t, ev, now); err != nil {
			return ActionResult{}, err
		}
		if ev.Status != "ended" {
			return ActionResult{}, gameerr.Validationf("活動還沒結束，結束後才能領取獎勵。")
		}
		claim, err := bossClaim(ctx, t, ev.ID, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if claim != nil {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經領過這場活動的獎勵了。")
		}
		rows, err := BossRanking(ctx, t, ev.ID)
		if err != nil {
			return ActionResult{}, err
		}
		var mine *BossRankRow
		for i := range rows {
			if rows[i].PlayerID == playerID {
				mine = &rows[i]
			}
		}
		if mine == nil {
			return ActionResult{}, gameerr.Validationf("你沒有參與這場活動的捐獻，沒有獎勵可以領。")
		}
		rewards := decodeBossRewards(ev.Rewards)
		if need := wb.MinPoints(rewards); mine.Points < need {
			return ActionResult{}, gameerr.Validationf("你的貢獻積分 %s 未達參與門檻 %s 分，沒有獎勵可以領。",
				numfmt.Comma(mine.Points), numfmt.Comma(need))
		}
		farm, err := w.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		defeated := ev.DefeatedAt != nil
		reward := wb.ComposeRewards(rewards, defeated, mine.Points, mine.Rank)
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		codes := sortedCodes(reward.Items)
		for _, c := range codes {
			if err := w.ensureItemSpace(ctx, t, farm, c, reward.Items[c]); err != nil {
				return ActionResult{}, err
			}
		}
		outcome := "participation"
		if defeated {
			outcome = "victory"
		}
		items, err := json.Marshal(reward.Items)
		if err != nil {
			return ActionResult{}, err
		}
		err = t.Savepoint(ctx, func() error {
			_, err := t.Exec(ctx, `INSERT INTO world_boss_reward_claims (event_id, player_id, player_name, rank, points, outcome,
				gold, items, title, claimed_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::json, $9, $10)`,
				ev.ID, playerID, mine.PlayerName, mine.Rank, mine.Points, outcome, reward.Gold, string(items), strPtr(reward.Title), now)
			return err
		})
		if isIntegrity(err) {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經領過這場活動的獎勵了。")
		}
		if err != nil {
			return ActionResult{}, err
		}
		label := "參與"
		if defeated {
			label = "勝利"
		}
		if reward.Gold != 0 {
			farm.Gold += reward.Gold
			if err := w.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
				return ActionResult{}, err
			}
			if err := w.record(ctx, t, farm, rules.EvBossReward,
				truncRunes(fmt.Sprintf("世界 Boss「%s」%s獎勵（第 %d 名）%d 金幣", ev.Name, label, mine.Rank, reward.Gold), 200),
				rec{GoldDelta: reward.Gold}); err != nil {
				return ActionResult{}, err
			}
		}
		total := 0
		for _, c := range codes {
			qty := reward.Items[c]
			total += qty
			if err := inventory.Add(ctx, t, farm.ID, c, qty, "normal", "none"); err != nil {
				return ActionResult{}, err
			}
			n, ok := names[c]
			if !ok {
				n = c
			}
			if err := w.record(ctx, t, farm, rules.EvBossReward,
				truncRunes(fmt.Sprintf("世界 Boss「%s」%s獎勵：%s ×%d", ev.Name, label, n, qty), 200),
				rec{ItemCode: c, Quantity: qty}); err != nil {
				return ActionResult{}, err
			}
		}
		got := BossRewardText(wb.Reward{Gold: reward.Gold, Items: reward.Items}, names)
		parts := strings.Join(reward.Parts, "、")
		if parts == "" {
			parts = label + "獎勵"
		}
		msg := fmt.Sprintf("🎁 領取「%s」%s：%s。", ev.Name, parts, got)
		if reward.Title != "" {
			if err := titles.Grant(ctx, t, farm.ID, reward.Title, worldBossTitleSource, now); err != nil { // 可在「成就圖鑑」選用
				return ActionResult{}, err
			}
			msg += fmt.Sprintf("\n🏅 獲得稱號「%s」！可到「🏅 成就圖鑑」設定顯示。", reward.Title)
		}
		return result(msg, farm.Gold, reward.Gold, "", total), nil
	})
}

// ---------- 背景結算 ----------

// FinalizeDueWorldBosses 推進所有未結束的活動；已過結束時間或 HP 歸零的活動在此結束（開放領獎）。回傳本次結束的活動 ID。
func (w *WorldBossService) FinalizeDueWorldBosses(ctx context.Context) ([]int64, error) {
	now := w.now()
	ids, err := inTx(ctx, w.GameService, func(t *db.Tx) ([]int64, error) {
		return db.QueryValues[int64](ctx, t, "SELECT id FROM world_boss_events WHERE status <> 'ended' ORDER BY id")
	})
	if err != nil {
		return nil, err
	}
	done := []int64{}
	for _, id := range ids {
		ended, err := inTx(ctx, w.GameService, func(t *db.Tx) (bool, error) {
			ev, err := bossForUpdate(ctx, t, id)
			if err != nil {
				return false, err
			}
			if err := advance(ctx, t, ev, now); err != nil {
				return false, err
			}
			return ev.Status == "ended", nil
		})
		if err != nil {
			return nil, err
		}
		if ended {
			done = append(done, id)
		}
	}
	return done, nil
}

// SpawnDueWorldBosses 依啟用中的排程建立下一場世界 Boss（上一場結束後立即建立，出現前管理員可個別修改）；回傳新建立的活動 ID。
func (w *WorldBossService) SpawnDueWorldBosses(ctx context.Context) ([]int64, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) ([]int64, error) { return spawnDue(ctx, t, now, nil) })
}

func insertSupplies(ctx context.Context, t *db.Tx, eventID int64, s wb.Settings) error {
	for _, code := range wb.Supplies {
		if err := db.Insert(ctx, t, "world_boss_supplies", db.C("event_id", eventID), db.C("supply_code", code),
			db.C("target", s.SupplyTargets[code]), db.C("progress", 0)); err != nil {
			return err
		}
	}
	return nil
}

func spawnDue(ctx context.Context, t *db.Tx, now time.Time, scheduleID *int64) ([]int64, error) {
	sql := `SELECT * FROM world_boss_schedules s WHERE s.enabled
		AND NOT EXISTS (SELECT 1 FROM world_boss_events e WHERE e.schedule_id = s.id AND e.status <> 'ended')`
	args := []any{}
	if scheduleID != nil {
		sql += " AND s.id = $1"
		args = append(args, *scheduleID)
	}
	scheds, err := db.QueryStructs[models.WorldBossSchedule](ctx, t, sql+" ORDER BY s.id", args...)
	if err != nil {
		return nil, err
	}
	created := []int64{}
	for _, sc := range scheds {
		after, err := lastSlot(ctx, t, sc.ID)
		if err != nil {
			return nil, err
		}
		tm := scheduleTimes(sc)
		slot := wb.SlotAfter(tm.AnchorDate, sc.SpawnHour, sc.SpawnMinute, sc.IntervalDays, sc.DurationMinutes, now, after)
		lastTick := slot
		if now.After(slot) {
			lastTick = now
		}
		var id int64
		// 多個程序同時建立同一場時由唯一限制擋住，略過即可
		err = t.Savepoint(ctx, func() error {
			if err := t.QueryRow(ctx, `INSERT INTO world_boss_events (group_id, schedule_id, slot_at, name, level, max_hp, hp,
				status, starts_at, ends_at, last_tick_at, settings, rewards, created_by, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 1, $5, $5, 'active', $3, $6, $7, $8::json, $9::json, $10, $11, $11) RETURNING id`,
				sc.GroupID, sc.ID, slot, sc.Name, sc.MaxHP, slot.Add(time.Duration(sc.DurationMinutes)*time.Minute), lastTick,
				string(sc.Settings), string(sc.Rewards), fmt.Sprintf("schedule#%d", sc.ID), now).Scan(&id); err != nil {
				return err
			}
			return insertSupplies(ctx, t, id, decodeBossSettings(sc.Settings))
		})
		if isIntegrity(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		created = append(created, id)
	}
	return created, nil
}

// WorldBossSettlement 背景結算的結果（排名、競賽與世界 Boss）。
type WorldBossSettlement struct {
	BackgroundSettlement
	WorldBoss        []int64
	WorldBossSpawned []int64
}

// RunBackgroundSettlement 排名獎勵、競賽與世界 Boss 的背景結算；取不到鎖時回傳 nil。
func (w *WorldBossService) RunBackgroundSettlement(ctx context.Context, actor string) (*WorldBossSettlement, error) {
	base, err := w.RankingService.RunBackgroundSettlement(ctx, actor)
	if err != nil || base == nil {
		return nil, err
	}
	out := &WorldBossSettlement{BackgroundSettlement: *base}
	if out.WorldBoss, err = w.FinalizeDueWorldBosses(ctx); err != nil {
		return nil, err
	}
	out.WorldBossSpawned, err = w.SpawnDueWorldBosses(ctx)
	return out, err
}

// ---------- 管理員 ----------

func (w *WorldBossService) bossAudit(ctx context.Context, t *db.Tx, actor, action string, eventID int64, details map[string]any) error {
	return community.AddAdminAction(ctx, t, actor, action, worldBossTarget, &eventID, details, w.now())
}

// bossValidators 物品類別判斷（含魚）與物品是否存在（獎勵物品）。
func bossValidators(ctx context.Context, t *db.Tx) (func(string) (string, bool), func(string) bool, error) {
	kinds, err := itemKinds(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	fish, err := db.QueryValues[string](ctx, t, "SELECT code FROM fish_types")
	if err != nil {
		return nil, nil, err
	}
	category := func(code string) (string, bool) {
		if slices.Contains(fish, code) {
			return "fish", true
		}
		c := ItemCategory(kinds[code].Kind, code)
		return c, c != ""
	}
	valid := func(code string) bool { _, ok := kinds[code]; return ok }
	return category, valid, nil
}

func checkBossTimes(startsAt, endsAt, now time.Time) error {
	if !endsAt.After(startsAt) {
		return gameerr.Validationf("結束時間必須晚於開始時間。")
	}
	seconds := int(endsAt.Sub(startsAt).Seconds())
	low, high := wb.DurationRangeSeconds[0], wb.DurationRangeSeconds[1]
	if seconds < low || seconds > high {
		return gameerr.Validationf("活動期間必須介於 %d 分鐘到 %d 天。", low/60, high/86400)
	}
	if !endsAt.After(now) {
		return gameerr.Validationf("結束時間必須晚於現在。")
	}
	return nil
}

// BossInput 建立／修改世界 Boss 活動的資料（管理 API）。Level、MaxHP 為 JSON 值（由 IntValue 驗證）；
// GroupID 為 nil 表示全服；Settings、Rewards 為 nil 時使用預設值。
type BossInput struct {
	Name     string
	Level    any
	MaxHP    any
	StartsAt *time.Time
	EndsAt   *time.Time
	GroupID  *int64
	Settings map[string]any
	Rewards  map[string]any
}

type bossBasic struct {
	Name         string
	Level, MaxHP int
}

func bossBasicValues(in BossInput) (bossBasic, error) {
	name, err := rules.ValidateName(in.Name, "Boss 名稱", wb.NameMaxLen)
	if err != nil {
		return bossBasic{}, err
	}
	level := in.Level
	if level == nil || level == 0 {
		level = 1 // 介面不顯示，預設 1
	}
	lv, err := wb.IntValue(level, "Boss 等級", wb.LevelRange[0], wb.LevelRange[1])
	if err != nil {
		return bossBasic{}, err
	}
	hp, err := wb.IntValue(in.MaxHP, "Boss HP", wb.MaxHPRange[0], wb.MaxHPRange[1])
	return bossBasic{name, lv, hp}, err
}

func bossGroupID(ctx context.Context, t *db.Tx, groupID *int64) (*int64, error) {
	if groupID == nil {
		return nil, nil
	}
	g, err := groups.Get(ctx, t, *groupID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, gameerr.NotFoundf("找不到群組 #%d。", *groupID)
	}
	return groupID, nil
}

// isoTime Python datetime.isoformat()：「2026-01-01T00:00:00+00:00」；有微秒時固定 6 位（.500000），不會省略尾端的 0。
func isoTime(t time.Time) string {
	layout := "2006-01-02T15:04:05"
	if t.Nanosecond()/1000 != 0 {
		layout += ".000000"
	}
	return t.Format(layout + "-07:00")
}

func (w *WorldBossService) CreateWorldBoss(ctx context.Context, actor string, in BossInput) (BossView, error) {
	now := w.now()
	v, err := bossBasicValues(in)
	if err != nil {
		return BossView{}, err
	}
	if in.StartsAt == nil || in.EndsAt == nil {
		return BossView{}, gameerr.Validationf("請設定開始與結束時間。")
	}
	startsAt, endsAt := *in.StartsAt, *in.EndsAt
	if err := checkBossTimes(startsAt, endsAt, now); err != nil {
		return BossView{}, err
	}
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossView, error) {
		gid, err := bossGroupID(ctx, t, in.GroupID)
		if err != nil {
			return BossView{}, err
		}
		category, valid, err := bossValidators(ctx, t)
		if err != nil {
			return BossView{}, err
		}
		rawS, rawR := in.Settings, in.Rewards
		if len(rawS) == 0 {
			rawS = toRawValue(wb.DefaultSettings())
		}
		if len(rawR) == 0 {
			rawR = toRawValue(wb.DefaultRewards())
		}
		settings, err := wb.ValidateSettings(rawS, category)
		if err != nil {
			return BossView{}, err
		}
		rewards, err := wb.ValidateRewards(rawR, valid)
		if err != nil {
			return BossView{}, err
		}
		sj, _ := json.Marshal(settings)
		rj, _ := json.Marshal(rewards)
		var id int64
		err = t.Savepoint(ctx, func() error {
			return t.QueryRow(ctx, `INSERT INTO world_boss_events (group_id, name, level, max_hp, hp, status, starts_at, ends_at,
				last_tick_at, settings, rewards, created_by, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $4, 'active', $5, $6, $5, $7::json, $8::json, $9, $10, $10) RETURNING id`,
				gid, v.Name, v.Level, v.MaxHP, startsAt, endsAt, string(sj), string(rj), actor, now).Scan(&id)
		})
		if isIntegrity(err) {
			where := "這個群組"
			if gid == nil {
				where = "全服"
			}
			return BossView{}, gameerr.New(gameerr.AlreadyExists, "%s已經有尚未結束的手動世界 Boss 活動，請先結束它。", where)
		}
		if err != nil {
			return BossView{}, err
		}
		if err := insertSupplies(ctx, t, id, settings); err != nil {
			return BossView{}, err
		}
		if err := w.bossAudit(ctx, t, actor, "world_boss_create", id, map[string]any{"name": v.Name, "group_id": gid,
			"max_hp": v.MaxHP, "level": v.Level, "starts_at": isoTime(startsAt), "ends_at": isoTime(endsAt)}); err != nil {
			return BossView{}, err
		}
		ev, err := bossForUpdate(ctx, t, id)
		if err != nil {
			return BossView{}, err
		}
		return EventView(ctx, t, ev, now, nil)
	})
}

func openEvent(ctx context.Context, t *db.Tx, eventID int64, now time.Time) (*models.WorldBossEvent, error) {
	ev, err := bossForUpdate(ctx, t, eventID)
	if err != nil {
		return nil, err
	}
	if err := advance(ctx, t, ev, now); err != nil {
		return nil, err
	}
	if ev.Status == "ended" {
		return nil, gameerr.Validationf("活動已經結束，不能再修改（歷史紀錄會保留）。")
	}
	return ev, nil
}

// UpdateWorldBoss 修改名稱、等級、HP 與期間（活動結束前）。已開始的活動不能改開始時間；HP 上限改變時剩餘 HP 依差額調整。
// StartsAt／EndsAt 為 nil 時不變。
func (w *WorldBossService) UpdateWorldBoss(ctx context.Context, actor string, eventID int64, in BossInput) (BossView, error) {
	now := w.now()
	v, err := bossBasicValues(in)
	if err != nil {
		return BossView{}, err
	}
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossView, error) {
		ev, err := openEvent(ctx, t, eventID, now)
		if err != nil {
			return BossView{}, err
		}
		before := map[string]any{"name": ev.Name, "level": ev.Level, "max_hp": ev.MaxHP, "hp": ev.HP,
			"starts_at": isoTime(ev.StartsAt), "ends_at": isoTime(ev.EndsAt)}
		startsAt, endsAt := ev.StartsAt, ev.EndsAt
		if in.StartsAt != nil {
			startsAt = *in.StartsAt
		}
		if in.EndsAt != nil {
			endsAt = *in.EndsAt
		}
		started := !now.Before(ev.StartsAt)
		if started && !startsAt.Equal(ev.StartsAt) {
			return BossView{}, gameerr.Validationf("活動已經開始，不能修改開始時間。")
		}
		if err := checkBossTimes(startsAt, endsAt, now); err != nil {
			return BossView{}, err
		}
		if v.MaxHP != ev.MaxHP {
			if started {
				ev.HP = max(1, min(v.MaxHP, ev.HP+v.MaxHP-ev.MaxHP))
			} else {
				ev.HP = v.MaxHP
			}
		}
		ev.Name, ev.Level, ev.MaxHP = v.Name, v.Level, v.MaxHP
		if !started {
			ev.LastTickAt = startsAt
		}
		ev.StartsAt, ev.EndsAt, ev.UpdatedAt = startsAt, endsAt, now
		if err := saveEvent(ctx, t, ev); err != nil {
			return BossView{}, err
		}
		if err := w.bossAudit(ctx, t, actor, "world_boss_update", ev.ID, map[string]any{"before": before, "after": map[string]any{
			"name": ev.Name, "level": ev.Level, "max_hp": ev.MaxHP, "hp": ev.HP, "starts_at": isoTime(startsAt),
			"ends_at": isoTime(endsAt)}}); err != nil {
			return BossView{}, err
		}
		return EventView(ctx, t, ev, now, nil)
	})
}

// UpdateWorldBossSettings 修改補給目標、效果、捐獻限制與可捐獻物品（活動結束前）；先以舊設定把傷害結算到現在。
func (w *WorldBossService) UpdateWorldBossSettings(ctx context.Context, actor string, eventID int64, raw map[string]any) (BossView, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossView, error) {
		ev, err := openEvent(ctx, t, eventID, now)
		if err != nil {
			return BossView{}, err
		}
		category, _, err := bossValidators(ctx, t)
		if err != nil {
			return BossView{}, err
		}
		settings, err := wb.ValidateSettings(raw, category)
		if err != nil {
			return BossView{}, err
		}
		if _, err := t.Exec(ctx, `UPDATE world_boss_supplies SET target = (($2::json)->>supply_code)::int WHERE event_id = $1`,
			ev.ID, toJSON(settings.SupplyTargets)); err != nil {
			return BossView{}, err
		}
		ev.Settings, ev.UpdatedAt = []byte(toJSON(settings)), now
		if err := saveEvent(ctx, t, ev); err != nil {
			return BossView{}, err
		}
		if err := w.bossAudit(ctx, t, actor, "world_boss_settings", ev.ID, map[string]any{"name": ev.Name, "settings": toRawValue(settings)}); err != nil {
			return BossView{}, err
		}
		return EventView(ctx, t, ev, now, nil)
	})
}

func toJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// UpdateWorldBossRewards 修改獎勵設定；已經有人領獎後不能再改（避免同一場活動獎勵不一致）。
func (w *WorldBossService) UpdateWorldBossRewards(ctx context.Context, actor string, eventID int64, raw map[string]any) (BossView, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossView, error) {
		ev, err := bossForUpdate(ctx, t, eventID)
		if err != nil {
			return BossView{}, err
		}
		claimed, _, err := db.QueryValue[int](ctx, t, "SELECT count(*) FROM world_boss_reward_claims WHERE event_id = $1", ev.ID)
		if err != nil {
			return BossView{}, err
		}
		if claimed > 0 {
			return BossView{}, gameerr.Validationf("已經有玩家領取獎勵，不能再修改這場活動的獎勵。")
		}
		_, valid, err := bossValidators(ctx, t)
		if err != nil {
			return BossView{}, err
		}
		rewards, err := wb.ValidateRewards(raw, valid)
		if err != nil {
			return BossView{}, err
		}
		ev.Rewards, ev.UpdatedAt = []byte(toJSON(rewards)), now
		if err := saveEvent(ctx, t, ev); err != nil {
			return BossView{}, err
		}
		if err := w.bossAudit(ctx, t, actor, "world_boss_rewards", ev.ID, map[string]any{"name": ev.Name, "rewards": toRawValue(rewards)}); err != nil {
			return BossView{}, err
		}
		return EventView(ctx, t, ev, now, nil)
	})
}

// WorldBossAction start：立即開始（尚未開始的活動）；pause／resume：暫停與繼續（暫停期間不扣 HP、不能捐獻）；end：提前結束。
func (w *WorldBossService) WorldBossAction(ctx context.Context, actor string, eventID int64, action string) (BossView, error) {
	if !slices.Contains([]string{"start", "pause", "resume", "end"}, action) {
		return BossView{}, gameerr.Validationf("不支援的操作。")
	}
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossView, error) {
		ev, err := openEvent(ctx, t, eventID, now)
		if err != nil {
			return BossView{}, err
		}
		switch action {
		case "start":
			if ev.Status != "active" || !now.Before(ev.StartsAt) {
				return BossView{}, gameerr.Validationf("只有尚未開始的活動可以立即開始。")
			}
			ev.StartsAt, ev.LastTickAt = now, now
		case "pause":
			if ev.Status != "active" || now.Before(ev.StartsAt) {
				return BossView{}, gameerr.Validationf("只有進行中的活動可以暫停。")
			}
			ev.Status, ev.PausedAt = "paused", &now
		case "resume":
			if ev.Status != "paused" {
				return BossView{}, gameerr.Validationf("活動沒有暫停。")
			}
			ev.Status, ev.PausedAt = "active", nil
			ev.LastTickAt = now
			if ev.StartsAt.After(now) {
				ev.LastTickAt = ev.StartsAt
			}
		default:
			bossFinish(ev, "admin", now, now)
		}
		ev.UpdatedAt = now
		if err := saveEvent(ctx, t, ev); err != nil {
			return BossView{}, err
		}
		if err := w.bossAudit(ctx, t, actor, "world_boss_"+action, ev.ID, map[string]any{"name": ev.Name}); err != nil {
			return BossView{}, err
		}
		return EventView(ctx, t, ev, now, nil)
	})
}

// ---------- 固定週期的世界 Boss（排程） ----------

type scheduleValues struct {
	Times    wb.ScheduleTimes
	Name     string
	GroupID  *int64
	Settings wb.Settings
	Rewards  wb.Rewards
	Enabled  bool
}

// bossScheduleValues raw 為管理 API 的 JSON：時間欄位、name、group_id（null 表示全服）、settings、rewards、enabled（預設 true）。
func bossScheduleValues(ctx context.Context, t *db.Tx, raw map[string]any) (scheduleValues, error) {
	times, err := wb.ValidateSchedule(raw)
	if err != nil {
		return scheduleValues{}, err
	}
	category, valid, err := bossValidators(ctx, t)
	if err != nil {
		return scheduleValues{}, err
	}
	base := wb.AbyssDragon()
	name, _ := raw["name"].(string)
	if name, err = rules.ValidateName(name, "Boss 名稱", wb.NameMaxLen); err != nil {
		return scheduleValues{}, err
	}
	var gid *int64
	if g, ok := raw["group_id"]; ok && g != nil {
		n, isInt := featurecfg.AsInt(g)
		id := int64(n)
		if _, isBool := g.(bool); !isInt || isBool {
			return scheduleValues{}, gameerr.NotFoundf("找不到群組 #%v。", g)
		}
		if gid, err = bossGroupID(ctx, t, &id); err != nil {
			return scheduleValues{}, err
		}
	}
	rawS, _ := raw["settings"].(map[string]any)
	if len(rawS) == 0 {
		rawS = toRawValue(base.Settings)
	}
	rawR, _ := raw["rewards"].(map[string]any)
	if len(rawR) == 0 {
		rawR = toRawValue(base.Rewards)
	}
	settings, err := wb.ValidateSettings(rawS, category)
	if err != nil {
		return scheduleValues{}, err
	}
	rewards, err := wb.ValidateRewards(rawR, valid)
	if err != nil {
		return scheduleValues{}, err
	}
	enabled := true
	if e, ok := raw["enabled"]; ok && e != nil {
		enabled = truthy(e)
	}
	return scheduleValues{times, name, gid, settings, rewards, enabled}, nil
}

// truthy Python bool(value)。
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		return x.String() != "0"
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return v != nil
}

func scheduleForUpdate(ctx context.Context, t *db.Tx, id int64) (*models.WorldBossSchedule, error) {
	s, err := db.QueryStruct[models.WorldBossSchedule](ctx, t, "SELECT * FROM world_boss_schedules WHERE id = $1 FOR UPDATE", id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, gameerr.NotFoundf("找不到世界 Boss 排程 #%d。", id)
	}
	return s, nil
}

func (w *WorldBossService) scheduleAudit(ctx context.Context, t *db.Tx, actor, action string, s *models.WorldBossSchedule) error {
	return community.AddAdminAction(ctx, t, actor, action, worldBossScheduleKind, &s.ID, map[string]any{
		"name": s.Name, "group_id": s.GroupID, "interval_days": s.IntervalDays,
		"time": fmt.Sprintf("%02d:%02d", s.SpawnHour, s.SpawnMinute), "anchor_date": db.TaipeiDate(s.AnchorDate).Format("2006-01-02"),
		"duration_minutes": s.DurationMinutes, "max_hp": s.MaxHP, "enabled": s.Enabled,
	}, w.now())
}

func (w *WorldBossService) scheduleResult(ctx context.Context, t *db.Tx, id int64, now time.Time) (BossScheduleView, error) {
	s, err := db.QueryStruct[models.WorldBossSchedule](ctx, t, "SELECT * FROM world_boss_schedules WHERE id = $1", id)
	if err != nil {
		return BossScheduleView{}, err
	}
	names, err := catalog.ItemNames(ctx, t)
	if err != nil {
		return BossScheduleView{}, err
	}
	return ScheduleView(ctx, t, s, now, names)
}

// CreateWorldBossSchedule 新增固定週期的世界 Boss；建立後立即產生下一場（出現前可在活動列表個別修改時間與獎勵）。
func (w *WorldBossService) CreateWorldBossSchedule(ctx context.Context, actor string, raw map[string]any) (BossScheduleView, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossScheduleView, error) {
		v, err := bossScheduleValues(ctx, t, raw)
		if err != nil {
			return BossScheduleView{}, err
		}
		s, err := db.QueryStruct[models.WorldBossSchedule](ctx, t, `INSERT INTO world_boss_schedules (name, group_id, interval_days,
			spawn_hour, spawn_minute, anchor_date, duration_minutes, max_hp, settings, rewards, enabled, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::json, $10::json, $11, $12, $13, $13) RETURNING *`,
			v.Name, v.GroupID, v.Times.IntervalDays, v.Times.SpawnHour, v.Times.SpawnMinute, db.DateParam(v.Times.AnchorDate),
			v.Times.DurationMinutes, v.Times.MaxHP, toJSON(v.Settings), toJSON(v.Rewards), v.Enabled, actor, now)
		if err != nil {
			return BossScheduleView{}, err
		}
		if err := w.scheduleAudit(ctx, t, actor, "world_boss_schedule_create", s); err != nil {
			return BossScheduleView{}, err
		}
		if _, err := spawnDue(ctx, t, now, &s.ID); err != nil {
			return BossScheduleView{}, err
		}
		return w.scheduleResult(ctx, t, s.ID, now)
	})
}

// UpdateWorldBossSchedule 修改排程（之後產生的每一場都套用）；已經建立的下一場不受影響，要改那一場請在活動列表修改。
func (w *WorldBossService) UpdateWorldBossSchedule(ctx context.Context, actor string, scheduleID int64, raw map[string]any) (BossScheduleView, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossScheduleView, error) {
		if _, err := scheduleForUpdate(ctx, t, scheduleID); err != nil {
			return BossScheduleView{}, err
		}
		v, err := bossScheduleValues(ctx, t, raw)
		if err != nil {
			return BossScheduleView{}, err
		}
		s, err := db.QueryStruct[models.WorldBossSchedule](ctx, t, `UPDATE world_boss_schedules SET name = $2, group_id = $3,
			interval_days = $4, spawn_hour = $5, spawn_minute = $6, anchor_date = $7, duration_minutes = $8, max_hp = $9,
			settings = $10::json, rewards = $11::json, enabled = $12, updated_at = $13 WHERE id = $1 RETURNING *`,
			scheduleID, v.Name, v.GroupID, v.Times.IntervalDays, v.Times.SpawnHour, v.Times.SpawnMinute, db.DateParam(v.Times.AnchorDate),
			v.Times.DurationMinutes, v.Times.MaxHP, toJSON(v.Settings), toJSON(v.Rewards), v.Enabled, now)
		if err != nil {
			return BossScheduleView{}, err
		}
		if err := w.scheduleAudit(ctx, t, actor, "world_boss_schedule_update", s); err != nil {
			return BossScheduleView{}, err
		}
		if _, err := spawnDue(ctx, t, now, &s.ID); err != nil {
			return BossScheduleView{}, err
		}
		return w.scheduleResult(ctx, t, s.ID, now)
	})
}

// SetWorldBossScheduleEnabled 停用後不再產生新的一場（已建立的下一場保留，可在活動列表結束）。
func (w *WorldBossService) SetWorldBossScheduleEnabled(ctx context.Context, actor string, scheduleID int64, enabled bool) (BossScheduleView, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (BossScheduleView, error) {
		s, err := scheduleForUpdate(ctx, t, scheduleID)
		if err != nil {
			return BossScheduleView{}, err
		}
		s.Enabled, s.UpdatedAt = enabled, now
		if _, err := db.UpdateByID(ctx, t, "world_boss_schedules", s.ID, db.C("enabled", enabled), db.C("updated_at", now)); err != nil {
			return BossScheduleView{}, err
		}
		action := "world_boss_schedule_disable"
		if enabled {
			action = "world_boss_schedule_enable"
		}
		if err := w.scheduleAudit(ctx, t, actor, action, s); err != nil {
			return BossScheduleView{}, err
		}
		if enabled {
			if _, err := spawnDue(ctx, t, now, &s.ID); err != nil {
				return BossScheduleView{}, err
			}
		}
		return w.scheduleResult(ctx, t, s.ID, now)
	})
}

// DeleteWorldBossSchedule 刪除排程；尚未結束的那一場一併結束（管理員結束），已產生的活動與紀錄保留（schedule_id 設為 NULL）。
func (w *WorldBossService) DeleteWorldBossSchedule(ctx context.Context, actor string, scheduleID int64) (int64, error) {
	now := w.now()
	return inTx(ctx, w.GameService, func(t *db.Tx) (int64, error) {
		s, err := scheduleForUpdate(ctx, t, scheduleID)
		if err != nil {
			return 0, err
		}
		open, err := db.QueryStructs[models.WorldBossEvent](ctx, t,
			"SELECT * FROM world_boss_events WHERE schedule_id = $1 AND status <> 'ended' FOR UPDATE", s.ID)
		if err != nil {
			return 0, err
		}
		for i := range open {
			ev := &open[i]
			if err := advance(ctx, t, ev, now); err != nil {
				return 0, err
			}
			if ev.Status != "ended" {
				bossFinish(ev, "admin", now, now)
				if err := saveEvent(ctx, t, ev); err != nil {
					return 0, err
				}
			}
		}
		if err := w.scheduleAudit(ctx, t, actor, "world_boss_schedule_delete", s); err != nil {
			return 0, err
		}
		_, err = t.Exec(ctx, "DELETE FROM world_boss_schedules WHERE id = $1", s.ID)
		return scheduleID, err
	})
}
