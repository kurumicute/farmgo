package services

// 農場日報：產生群組前一天的日報內容，顯示在 /farm 總覽頁最下方（只讀取，Bot 不主動發訊息）
// （對應 Python app/services/news_service.py）。
//
// 規則與文字在 game/news。過去某一天的交易紀錄不會再變，所以同一個 (群組, 日期) 的結果快取在程序內，
// 不必每次打開總覽都重新彙總一天的交易紀錄（農場改名最多晚一天才反映在日報上）。

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"farmgm/internal/db"
	"farmgm/internal/game/cosmetics"
	"farmgm/internal/game/fishing"
	newsrules "farmgm/internal/game/news"
	"farmgm/internal/game/reputation"
	"farmgm/internal/game/ruins"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/catalog"
	newsrepo "farmgm/internal/repositories/news"
	ruinsrepo "farmgm/internal/repositories/ruins"
)

// NewsCacheMax 超過時整個清空（每個群組每天一筆，很少達到）。
const NewsCacheMax = 1024

type newsKey struct {
	groupID int64
	day     string // 台灣日期 YYYY-MM-DD
}

var (
	newsCache   = map[newsKey]*DailyNews{}
	newsCacheMu sync.Mutex
	// makeRumor 測試可替換（Python 測試以 monkeypatch 替換 news.rumor）；日報與偷菜挖寶共用。
	makeRumor = newsrules.MakeRumor
)

// ClearNewsCache 測試重設資料庫後呼叫。
func ClearNewsCache() {
	newsCacheMu.Lock()
	defer newsCacheMu.Unlock()
	clear(newsCache)
}

// DailyNews 群組某一天的日報（Python 回傳 {"day", "empty", "text"}）。
// 快取的結果會被多個呼叫端共用，呼叫端不可修改。
type DailyNews struct {
	Day   time.Time // 台灣日期 00:00
	Empty bool
	Text  string
}

type NewsService struct {
	*WorldBossService
}

func NewNewsService(d *db.DB, opt Options) *NewsService {
	return &NewsService{NewWorldBossService(d, opt)}
}

func (n *NewsService) InGroupContext(groupID *int64) *NewsService {
	return &NewsService{n.WorldBossService.InGroupContext(groupID)}
}

// DailyNews 群組某一天（day 為零值時：台灣時間昨天）的日報。
func (n *NewsService) DailyNews(ctx context.Context, groupID int64, day time.Time) (*DailyNews, error) {
	now := n.now()
	if day.IsZero() {
		day = newsrules.NewsDay(now)
	}
	start, end := newsrules.DayRange(day)
	key := newsKey{groupID, start.Format(time.DateOnly)}
	newsCacheMu.Lock()
	cached := newsCache[key]
	newsCacheMu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var data newsrules.DayData
	err := n.transaction(ctx, func(t *db.Tx) error {
		gid := groupID
		if err := n.settleGuardian(ctx, t, &gid); err != nil { // 先結算守護者，崩壞的消息才不會被快取漏掉
			return err
		}
		events, err := newsrepo.FarmEvents(ctx, t, groupID, start, end)
		if err != nil {
			return err
		}
		special, err := newsrepo.SpecialHarvests(ctx, t, groupID, start, end)
		if err != nil {
			return err
		}
		fish, err := newsrepo.BestFishOf(ctx, t, groupID, start, end)
		if err != nil {
			return err
		}
		farmIDs := map[int64]bool{}
		if data.Auction, err = auctionWinOn(ctx, t, groupID, start, end); err != nil {
			return err
		}
		if data.Auction != nil {
			farmIDs[data.Auction.FarmID] = true
		}
		var active []int64
		donated := 0
		for _, e := range events {
			if !farmIDs[e.FarmID] {
				active = append(active, e.FarmID)
			}
			farmIDs[e.FarmID] = true
			data.Events = append(data.Events, newsrules.Event{FarmID: e.FarmID, EventType: e.EventType, Count: e.Count, Qty: e.Quantity, Gold: e.Gold})
			if e.EventType == "ruins_donate" {
				donated += e.Quantity
			}
		}
		for _, sp := range special {
			farmIDs[sp.FarmID] = true
			data.Special = append(data.Special, newsrules.Special{FarmID: sp.FarmID, ItemCode: deref(sp.ItemCode), Quality: deref(sp.Quality), Mutation: deref(sp.Mutation), Qty: sp.Quantity})
		}
		if fish != nil {
			farmIDs[fish.FarmID] = true
			rarity, ok := fishing.RarityNames[fish.Rarity]
			if !ok {
				rarity = fish.Rarity
			}
			data.Fish = &newsrules.Fish{FarmID: fish.FarmID, Name: fish.Name, Rarity: rarity, WeightG: fish.WeightG}
		}
		data.Rumor = makeRumor(groupID, start, active) // MakeRumor 自行排序候選農場
		if data.Rumor != nil {
			farmIDs[data.Rumor.Target] = true
		}
		ids := make([]int64, 0, len(farmIDs))
		for id := range farmIDs {
			ids = append(ids, id)
		}
		if data.Names, err = newsrepo.FarmNames(ctx, t, ids); err != nil {
			return err
		}
		if data.Badges, err = newsBadges(ctx, t, ids); err != nil {
			return err
		}
		if data.Reputation, err = reputationUps(ctx, t, events, end); err != nil {
			return err
		}
		if data.ItemNames, err = catalog.ItemNames(ctx, t); err != nil {
			return err
		}
		row, err := ruinsrepo.Get(ctx, t, groupID, false)
		if err != nil || row == nil {
			return err
		}
		guardian, err := ruinsrepo.GetGuardian(ctx, t, groupID, ruins.WeekStart(start), false)
		if err != nil {
			return err
		}
		within := func(at *time.Time) bool { return at != nil && !at.Before(start) && at.Before(end) }
		data.Ruins = &newsrules.Ruins{
			Donated:               donated,
			Percent:               ruins.Percent(row.Stage, row.Progress),
			CompletedToday:        within(row.CompletedAt),
			CollapsedToday:        within(row.CollapsedAt),
			GuardianDefeatedToday: guardian != nil && within(guardian.DefeatedAt),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := newsrules.Build(start, data, strconv.FormatInt(groupID, 10))
	out := &DailyNews{Day: start, Empty: result.Empty(), Text: newsrules.Render(result)}
	// 只快取已經結束的日子；管理網站的 preview 交易最後會回滾，算出來的結果不快取（2026-11-16）
	if !inPreview(ctx) && start.Before(newsrules.NewsDay(now).AddDate(0, 0, 1)) {
		newsCacheMu.Lock()
		if len(newsCache) >= NewsCacheMax {
			clear(newsCache)
		}
		newsCache[key] = out
		newsCacheMu.Unlock()
	}
	return out, nil
}

// newsBadges 日報顯示用：農場 → 裝備中名牌的 emoji（以產生日報當下的裝備為準）。
func newsBadges(ctx context.Context, t *db.Tx, farmIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(farmIDs) == 0 {
		return out, nil
	}
	rows, err := t.Query(ctx, "SELECT id, badge FROM farms WHERE id = ANY($1) AND badge IS NOT NULL", farmIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		if e := cosmetics.BadgeEmoji(code); e != "" {
			out[id] = e
		}
	}
	return out, rows.Err()
}

// reputationUps 當天聲望升級的農場：目前聲望扣掉 end 之後的捐款 = 當天結束時的聲望，再扣當天的捐款 = 當天開始時的聲望。
func reputationUps(ctx context.Context, t *db.Tx, events []newsrepo.FarmEvent, end time.Time) ([]newsrules.ReputationUp, error) {
	donated := map[int64]int{}
	var ids []int64
	for _, e := range events {
		if e.EventType == string(rules.EvReputationDonate) {
			if _, ok := donated[e.FarmID]; !ok {
				ids = append(ids, e.FarmID)
			}
			donated[e.FarmID] += -e.Gold
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := t.Query(ctx, `SELECT f.id, (f.reputation - coalesce((SELECT -sum(g.gold_delta) FROM game_transactions g
		WHERE g.farm_id = f.id AND g.event_type = $2 AND g.created_at >= $3), 0))::bigint
		FROM farms f WHERE f.id = ANY($1)`, ids, string(rules.EvReputationDonate), end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []newsrules.ReputationUp
	for rows.Next() {
		var id int64
		var atEnd int
		if err := rows.Scan(&id, &atEnd); err != nil {
			return nil, err
		}
		if after := reputation.Level(atEnd); after > reputation.Level(atEnd-donated[id]) {
			out = append(out, newsrules.ReputationUp{FarmID: id, Level: after})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b newsrules.ReputationUp) int {
		if a.Level != b.Level {
			return b.Level - a.Level
		}
		return int(a.FarmID - b.FarmID)
	})
	return out, nil
}

// auctionWinOn 當天結標、得標者屬於這個群組的每週拍賣（沒有時是 nil）。
func auctionWinOn(ctx context.Context, t *db.Tx, groupID int64, start, end time.Time) (*newsrules.AuctionWin, error) {
	var w newsrules.AuctionWin
	var kind, code string
	err := t.QueryRow(ctx, `SELECT a.top_farm_id, a.top_bid, a.item_kind, a.item_code FROM auctions a JOIN farms f ON f.id = a.top_farm_id
		WHERE a.status = 'settled' AND a.settled_at >= $2 AND a.settled_at < $3 AND f.home_group_id = $1
		ORDER BY a.settled_at DESC LIMIT 1`, groupID, start, end).Scan(&w.FarmID, &w.Price, &kind, &code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.Item, _ = auctionItemName(kind, code)
	return &w, nil
}
