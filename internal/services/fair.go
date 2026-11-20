package services

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/fair"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/treasure"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
)

// 品評會（Go 版新增，2026-11-20，Migration 0048；規則見 game/fair）。

// fairPiece 庫存物品在品評會的分類與價值。
type fairPiece struct {
	Category string
	Item     fair.Item
	Value    int
	Name     string
}

func (s *GameService) fairClassify(produce map[string]crops.Produce, settings balance.GeneticsSettings,
	code, quality, mutation string) (fairPiece, bool) {
	if p, ok := produce[code]; ok {
		c := p.Crop
		cat := fair.CatCrop
		if !c.ShopBuyable {
			cat = fair.CatLab
		}
		return fairPiece{cat, fair.Item{Code: code, CropCode: c.Code, Quality: quality, Mutation: mutation},
			genetics.UnitPrice(c.SellPrice, quality, mutation, settings),
			genetics.ProduceName(c.Name, c.VariantName, mutation) + GeneticsLabel(quality, mutation)}, true
	}
	if g, ok := balance.Goods[code]; ok && quality == "normal" && mutation == "none" {
		return fairPiece{fair.CatGoods, fair.Item{Code: code, Quality: quality, Mutation: mutation}, g.SellPrice, g.Name}, true
	}
	return fairPiece{}, false
}

func fairEntries(ctx context.Context, t *db.Tx, groupID int64, week string, forUpdate bool) ([]models.FairEntry, error) {
	sql := "SELECT * FROM fair_entries WHERE group_id = $1 AND week_start = $2::date ORDER BY id"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStructs[models.FairEntry](ctx, t, sql, groupID, week)
}

type playerName struct {
	ID   int64  `db:"id"`
	Name string `db:"display_name"`
}

func playerNames(ctx context.Context, t *db.Tx, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.QueryStructs[playerName](ctx, t, "SELECT id, display_name FROM players WHERE id = ANY($1)", ids)
	for _, r := range rows {
		out[r.ID] = r.Name
	}
	return out, err
}

// FairEntryView 一件參賽作品。
type FairEntryView struct {
	ID         int64
	PlayerName string
	ItemName   string
	Votes      int
	Mine       bool
	Voted      bool // 我投過這件
	Rank       int  // 0 = 還沒結算
	Reward     int
	JudgeScore int
	Status     string
}

// FairOption 可以報名的作品。
type FairOption struct {
	Code, Quality, Mutation, Name string
	Value, Have                   int
	Liked                         bool // 符合本週評審偏好
}

// FairView 品評會頁。
type FairView struct {
	Now       time.Time
	HasGroup  bool
	GroupName string
	Category  string
	Pref      fair.Pref
	Open      bool
	CloseAt   time.Time
	ResultsAt time.Time
	Entries   []FairEntryView // 本週，依票數排序
	Mine      *FairEntryView
	VotesLeft int
	Options   []FairOption    // 依價值排序，最多 ui 上限由呼叫端截斷
	Last      []FairEntryView // 上週（已結算時依名次排序）
	LastState string          // ""＝上週沒有比賽／settled／refunded／pending（等待結算）
	Gold      int
}

func entryViews(entries []models.FairEntry, names map[int64]string, me int64, voted map[int64]bool) []FairEntryView {
	out := make([]FairEntryView, 0, len(entries))
	for _, e := range entries {
		v := FairEntryView{ID: e.ID, PlayerName: names[e.PlayerID], ItemName: e.ItemName, Votes: e.Votes, Mine: e.PlayerID == me,
			Voted: voted[e.ID], Reward: e.Reward, JudgeScore: e.JudgeScore, Status: e.Status}
		if e.Rank != nil {
			v.Rank = *e.Rank
		}
		out = append(out, v)
	}
	return out
}

func (s *GameService) Fair(ctx context.Context, playerID int64) (FairView, error) {
	return inTx(ctx, s, func(t *db.Tx) (FairView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return FairView{}, err
		}
		now := s.now()
		week := fair.WeekStart(now)
		v := FairView{Now: now, Category: fair.Category(now), Pref: fair.WeekPref(now), Open: fair.Open(now),
			CloseAt: week.Add(fair.CloseAfter), ResultsAt: fair.ResultsAt(week), Gold: farm.Gold}
		if farm.HomeGroupID == nil {
			return v, nil
		}
		gid := *farm.HomeGroupID
		g, err := groups.Get(ctx, t, gid)
		if err != nil || g == nil {
			return v, err
		}
		v.HasGroup, v.GroupName = true, g.Name
		cur, err := fairEntries(ctx, t, gid, fair.WeekKey(week), false)
		if err != nil {
			return v, err
		}
		last, err := fairEntries(ctx, t, gid, fair.WeekKey(week.AddDate(0, 0, -7)), false)
		if err != nil {
			return v, err
		}
		var ids []int64
		for _, e := range append(slices.Clone(cur), last...) {
			ids = append(ids, e.PlayerID)
		}
		names, err := playerNames(ctx, t, ids)
		if err != nil {
			return v, err
		}
		votedIDs, err := db.QueryValues[int64](ctx, t, `SELECT entry_id FROM fair_votes
			WHERE voter_player_id = $1 AND group_id = $2 AND week_start = $3::date`, playerID, gid, fair.WeekKey(week))
		if err != nil {
			return v, err
		}
		voted := map[int64]bool{}
		for _, id := range votedIDs {
			voted[id] = true
		}
		v.VotesLeft = max(0, fair.VotesPerWeek-len(votedIDs))
		v.Entries = entryViews(cur, names, playerID, voted)
		sort.SliceStable(v.Entries, func(i, j int) bool { return v.Entries[i].Votes > v.Entries[j].Votes })
		for i := range v.Entries {
			if v.Entries[i].Mine {
				v.Mine = &v.Entries[i]
			}
		}
		if len(last) > 0 {
			v.Last = entryViews(last, names, playerID, nil)
			v.LastState = last[0].Status
			if v.LastState == "active" {
				v.LastState = "pending"
			}
			sort.SliceStable(v.Last, func(i, j int) bool {
				a, b := v.Last[i].Rank, v.Last[j].Rank
				return a != 0 && (b == 0 || a < b)
			})
		}
		if v.Open && v.Mine == nil {
			if v.Options, err = s.fairOptions(ctx, t, farm, v.Category, v.Pref); err != nil {
				return v, err
			}
		}
		return v, nil
	})
}

func (s *GameService) fairOptions(ctx context.Context, t *db.Tx, farm *models.Farm, category string, pref fair.Pref) ([]FairOption, error) {
	list, err := inventory.ListItems(ctx, t, farm.ID)
	if err != nil {
		return nil, err
	}
	produce, err := crops.ProduceMap(ctx, t)
	if err != nil {
		return nil, err
	}
	settings, err := s.geneticsSettings(ctx, t)
	if err != nil {
		return nil, err
	}
	var out []FairOption
	for _, it := range list {
		if it.Quantity <= 0 {
			continue
		}
		p, ok := s.fairClassify(produce, settings, it.ItemCode, it.Quality, it.Mutation)
		if !ok || p.Category != category {
			continue
		}
		out = append(out, FairOption{Code: it.ItemCode, Quality: it.Quality, Mutation: it.Mutation, Name: p.Name,
			Value: fair.JudgeScore(p.Value, p.Item, pref), Have: it.Quantity, Liked: pref.Matches(p.Item)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out, nil
}

// EnterFair 報名本週的品評會：付報名費，作品（1 個）被收走。
func (s *GameService) EnterFair(ctx context.Context, playerID int64, code, quality, mutation string) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.HomeGroupID == nil {
			return ActionResult{}, gameerr.Validationf("要先加入伺服器的群組才能參加品評會。")
		}
		now := s.now()
		if !fair.Open(now) {
			return ActionResult{}, gameerr.Validationf("本週的報名已經截止，結果在週日 20:00 公布。")
		}
		gid, week := *farm.HomeGroupID, fair.WeekKey(fair.WeekStart(now))
		if n, _, err := db.QueryValue[int](ctx, t, `SELECT count(*)::int FROM fair_entries
			WHERE group_id = $1 AND week_start = $2::date AND player_id = $3`, gid, week, playerID); err != nil {
			return ActionResult{}, err
		} else if n > 0 {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "本週已經報名過了，每週只能報名一件作品。")
		}
		produce, err := crops.ProduceMap(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		settings, err := s.geneticsSettings(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		p, ok := s.fairClassify(produce, settings, code, quality, mutation)
		category := fair.Category(now)
		if !ok || p.Category != category {
			return ActionResult{}, gameerr.Validationf("本週是%s，這件作品不能參賽。", txt(ctx, fair.CategoryNames[category]))
		}
		if farm.Gold < fair.EntryFee {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：報名費 %d，目前只有 %d。", fair.EntryFee, farm.Gold)
		}
		removed, err := inventory.Remove(ctx, t, farm.ID, code, 1, quality, mutation)
		if err != nil {
			return ActionResult{}, err
		}
		if !removed {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "倉庫裡沒有%s。", p.Name)
		}
		pref := fair.WeekPref(now)
		judge := fair.JudgeScore(p.Value, p.Item, pref)
		if _, err := t.Exec(ctx, `INSERT INTO fair_entries (group_id, week_start, player_id, farm_id, category, item_code, item_name,
			quality, mutation, base_value, judge_score, votes, fee, reward, status, created_at)
			VALUES ($1, $2::date, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0, $12, 0, 'active', $13)`,
			gid, week, playerID, farm.ID, category, code, truncRunes(p.Name, 64), quality, mutation, p.Value, judge,
			fair.EntryFee, now); err != nil {
			return ActionResult{}, err
		}
		farm.Gold -= fair.EntryFee
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvFairEntry, fmt.Sprintf("報名品評會：%s，金幣 -%d", p.Name, fair.EntryFee),
			rec{ItemCode: code, Quantity: 1, Quality: quality, Mutation: mutation, GoldDelta: -fair.EntryFee}); err != nil {
			return ActionResult{}, err
		}
		msg := txf(ctx, "🏆 報名成功！作品：%s（報名費 %d 金幣）。週日 20:00 公布結果。", p.Name, fair.EntryFee)
		if pref.Matches(p.Item) {
			msg += txt(ctx, "\n✨ 這件作品正好符合本週評審的偏好！")
		}
		return result(msg, farm.Gold, -fair.EntryFee, code, 1), nil
	})
}

// VoteFair 投一票給同群組本週的作品（每人每週 VotesPerWeek 票、每件一票、不能投自己）。
func (s *GameService) VoteFair(ctx context.Context, playerID, entryID int64) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID) // 鎖農場：同一位玩家的投票依序執行，票數上限不會被並行請求突破
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		week := fair.WeekKey(fair.WeekStart(now))
		e, err := db.QueryStruct[models.FairEntry](ctx, t, "SELECT * FROM fair_entries WHERE id = $1 FOR UPDATE", entryID)
		if err != nil {
			return ActionResult{}, err
		}
		if e == nil || farm.HomeGroupID == nil || e.GroupID != *farm.HomeGroupID || e.Status != "active" ||
			fair.WeekKey(e.WeekStart) != week {
			return ActionResult{}, gameerr.NotFoundf("找不到這件作品，可能已經結束投票。")
		}
		if !fair.Open(now) {
			return ActionResult{}, gameerr.Validationf("本週的投票已經截止，結果在週日 20:00 公布。")
		}
		if e.PlayerID == playerID {
			return ActionResult{}, gameerr.Validationf("不能投給自己的作品。")
		}
		used, _, err := db.QueryValue[int](ctx, t, `SELECT count(*)::int FROM fair_votes
			WHERE voter_player_id = $1 AND group_id = $2 AND week_start = $3::date`, playerID, e.GroupID, week)
		if err != nil {
			return ActionResult{}, err
		}
		if used >= fair.VotesPerWeek {
			return ActionResult{}, gameerr.Validationf("本週的 %d 票已經投完了。", fair.VotesPerWeek)
		}
		tag, err := t.Exec(ctx, `INSERT INTO fair_votes (entry_id, group_id, week_start, voter_player_id, created_at)
			VALUES ($1, $2, $3::date, $4, $5) ON CONFLICT ON CONSTRAINT uq_fair_votes_entry_voter DO NOTHING`,
			e.ID, e.GroupID, week, playerID, now)
		if err != nil {
			return ActionResult{}, err
		}
		if tag.RowsAffected() == 0 {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經投過這件作品了。")
		}
		if _, err := t.Exec(ctx, "UPDATE fair_entries SET votes = votes + 1 WHERE id = $1", e.ID); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "👍 投給了「%s」，本週還剩 %d 票。", e.ItemName, fair.VotesPerWeek-used-1), farm.Gold, 0, "", 0), nil
	})
}

type fairDue struct {
	GroupID   int64     `db:"group_id"`
	WeekStart time.Time `db:"week_start"`
}

// SettleDueFairs 結算已經到公布時間的品評會（背景結算呼叫）；回傳結算的場數。
func (s *GameService) SettleDueFairs(ctx context.Context) (int, error) {
	now := s.now()
	cutoff := fair.WeekKey(fair.WeekStart(now.Add(-fair.ResultsAfter)))
	due, err := inTx(ctx, s, func(t *db.Tx) ([]fairDue, error) {
		return db.QueryStructs[fairDue](ctx, t, `SELECT DISTINCT group_id, week_start FROM fair_entries
			WHERE status = 'active' AND week_start <= $1::date ORDER BY week_start, group_id`, cutoff)
	})
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, d := range due {
		week := d.WeekStart.UTC().Format("2006-01-02") // DATE 讀回是 UTC 00:00
		if _, err := inTx(ctx, s, func(t *db.Tx) (bool, error) { return true, s.settleFair(ctx, t, d.GroupID, week) }); err != nil {
			return settled, err
		}
		settled++
	}
	return settled, nil
}

func (s *GameService) settleFair(ctx context.Context, t *db.Tx, groupID int64, week string) error {
	now := s.now()
	all, err := fairEntries(ctx, t, groupID, week, true)
	if err != nil {
		return err
	}
	var entries []models.FairEntry
	for _, e := range all {
		if e.Status == "active" {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	// 依農場 ID 順序鎖定所有參賽者（和其他操作一樣先鎖農場，避免死結）
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.FarmID)
	}
	slices.Sort(ids)
	locked := map[int64]*models.Farm{}
	for _, id := range slices.Compact(ids) {
		f, err := farms.GetByID(ctx, t, id, true)
		if err != nil {
			return err
		}
		locked[id] = f
	}
	if len(entries) < fair.MinEntries { // 流會：退回報名費與作品
		for _, e := range entries {
			f := locked[e.FarmID]
			if f == nil {
				continue // 農場已被刪除
			}
			if err := inventory.Add(ctx, t, f.ID, e.ItemCode, 1, e.Quality, e.Mutation); err != nil {
				return err
			}
			f.Gold += e.Fee
			if err := s.saveFarm(ctx, t, f, db.C("gold", f.Gold)); err != nil {
				return err
			}
			r := ctxGroup(groupID)
			r.GoldDelta, r.ItemCode, r.Quantity, r.Quality, r.Mutation = e.Fee, e.ItemCode, 1, e.Quality, e.Mutation
			if err := s.record(ctx, t, f, rules.EvFairReward,
				fmt.Sprintf("品評會報名不足 %d 件流會：退回報名費 %d 與作品 %s", fair.MinEntries, e.Fee, e.ItemName), r); err != nil {
				return err
			}
			if _, err := t.Exec(ctx, "UPDATE fair_entries SET status = 'refunded', settled_at = $2 WHERE id = $1", e.ID, now); err != nil {
				return err
			}
		}
		return nil
	}
	rankIn := make([]fair.Entry, len(entries))
	byID := map[int64]models.FairEntry{}
	fees := 0
	for i, e := range entries {
		rankIn[i] = fair.Entry{ID: e.ID, Judge: e.JudgeScore, Votes: e.Votes, CreatedAt: e.CreatedAt}
		byID[e.ID] = e
		fees += e.Fee
	}
	prizes := fair.Prizes(fees)
	for i, id := range fair.Rank(rankIn) {
		e, rank, reward := byID[id], i+1, 0
		if i < len(prizes) {
			reward = prizes[i]
		}
		if _, err := t.Exec(ctx, "UPDATE fair_entries SET status = 'settled', rank = $2, reward = $3, settled_at = $4 WHERE id = $1",
			id, rank, reward, now); err != nil {
			return err
		}
		f := locked[e.FarmID]
		if f == nil {
			continue
		}
		if reward > 0 {
			f.Gold += reward
			if err := s.saveFarm(ctx, t, f, db.C("gold", f.Gold)); err != nil {
				return err
			}
			r := ctxGroup(groupID)
			r.GoldDelta = reward
			if err := s.record(ctx, t, f, rules.EvFairReward,
				fmt.Sprintf("品評會第 %d 名（%s）：金幣 +%d", rank, e.ItemName, reward), r); err != nil {
				return err
			}
		} else if _, err := s.addPiece(ctx, t, f, treasure.RandomPiece(s.TreasureRng()), now); err != nil { // 參加獎：1 片碎片
			return err
		}
	}
	return nil
}
