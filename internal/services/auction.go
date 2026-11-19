package services

// 每週系統拍賣（Go 版新增，2026-11-19，金幣回收第二波；規則在 game/auction，資料在 auctions、auction_bids，Migration 0046）。
//
// 鎖的順序：一律先鎖拍賣（SELECT … FOR UPDATE），再鎖農場，避免出價與結算互相等待。
// 出價時金幣先從出價者扣除（auction_bid）；被超過時前一位最高出價者的金幣全數退回（auction_refund）；
// 結標時只記一筆 auction_win（金額 0），凍結的金幣就此回收。結算以「status = 'open' 的那一列」為準，重複呼叫不會重複發獎。

import (
	"context"
	"fmt"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/auction"
	"farmgm/internal/game/cosmetics"
	"farmgm/internal/game/decor"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/titles"
)

func init() {
	auction.ItemValidator = auctionItemName
}

// auctionItemName 拍賣品的名稱（含 emoji）；不存在或不能拍賣時 ok = false。
func auctionItemName(kind, code string) (string, bool) {
	switch kind {
	case auction.KindDecor:
		it, ok := decor.Find(code)
		if !ok || it.Tier != decor.Auction {
			return "", false
		}
		return it.Emoji + " " + it.Name, true
	case auction.KindBadge:
		b, ok := cosmetics.FindBadge(code)
		return "名牌 " + b.Emoji + " " + b.Name, ok
	case auction.KindTitle:
		return "稱號「" + code + "」", code != ""
	}
	return "", false
}

type auctionRow struct {
	ID              int64      `db:"id"`
	WeekStart       time.Time  `db:"week_start"`
	ItemKind        string     `db:"item_kind"`
	ItemCode        string     `db:"item_code"`
	StartPrice      int        `db:"start_price"`
	MinIncrementPct int        `db:"min_increment_pct"`
	StartsAt        time.Time  `db:"starts_at"`
	EndsAt          time.Time  `db:"ends_at"`
	Status          string     `db:"status"`
	TopBid          *int       `db:"top_bid"`
	TopFarmID       *int64     `db:"top_farm_id"`
	SettledAt       *time.Time `db:"settled_at"`
}

const auctionCols = `id, week_start, item_kind, item_code, start_price, min_increment_pct, starts_at, ends_at, status,
	top_bid, top_farm_id, settled_at`

// ensureAuction 這一週還沒有拍賣時開一場（已經過了結標時間就不開）。
func (s *GameService) ensureAuction(ctx context.Context, t *db.Tx, now time.Time) error {
	week := auction.WeekStart(now)
	ends := auction.EndsAt(week)
	if !now.Before(ends) {
		return nil
	}
	c := auction.Current()
	item := auction.ItemOf(week)
	_, err := t.Exec(ctx, `INSERT INTO auctions (week_start, item_kind, item_code, start_price, min_increment_pct, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (week_start) DO NOTHING`,
		db.DateParam(week), item.Kind, item.Code, c.StartPrice, c.MinIncrementPct, week, ends)
	return err
}

// ownsAuctionItem 這座農場是不是已經擁有這件拍賣品。
func ownsAuctionItem(ctx context.Context, t *db.Tx, farmID int64, kind, code string) (bool, error) {
	var sql string
	switch kind {
	case auction.KindDecor:
		sql = "SELECT EXISTS (SELECT 1 FROM farm_decorations WHERE farm_id = $1 AND decor_code = $2)"
	case auction.KindBadge:
		sql = "SELECT EXISTS (SELECT 1 FROM farm_cosmetics WHERE farm_id = $1 AND kind = 'badge' AND code = $2)"
	default:
		sql = "SELECT EXISTS (SELECT 1 FROM farm_titles WHERE farm_id = $1 AND title = $2)"
	}
	var has bool
	err := t.QueryRow(ctx, sql, farmID, code).Scan(&has)
	return has, err
}

// AuctionView 拍賣頁。
type AuctionView struct {
	Gold      int
	Open      bool // 有進行中的拍賣
	ItemKind  string
	ItemName  string
	EndsAt    time.Time
	TopBid    *int
	TopName   string // 最高出價者的農場名稱
	TopBadge  string
	Mine      bool // 自己是最高出價者
	Owned     bool // 已經擁有這件拍賣品（不能出價）
	MinBid    int
	Outbid    int // 這一場被超過、已經退回的出價總額（自己目前不是最高出價者時）
	BidCount  int
	LastName  string // 上一場的商品
	LastWin   string // 上一場得標者（"" = 流標）
	LastPrice int
}

// Auction 拍賣頁的資料（順便結算到期的拍賣、開這一週的拍賣）。
func (s *GameService) Auction(ctx context.Context, playerID int64) (AuctionView, error) {
	if _, err := s.SettleAuctions(ctx); err != nil {
		return AuctionView{}, err
	}
	return inTx(ctx, s, func(t *db.Tx) (AuctionView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return AuctionView{}, err
		}
		now := s.now()
		v := AuctionView{Gold: farm.Gold}
		a, err := db.QueryStruct[auctionRow](ctx, t, "SELECT "+auctionCols+
			" FROM auctions WHERE status = 'open' AND starts_at <= $1 ORDER BY id DESC LIMIT 1", now)
		if err != nil {
			return v, err
		}
		if a != nil {
			v.Open, v.ItemKind, v.EndsAt, v.TopBid = true, a.ItemKind, a.EndsAt, a.TopBid
			v.ItemName, _ = auctionItemName(a.ItemKind, a.ItemCode)
			v.MinBid = auction.MinNextBid(a.StartPrice, a.TopBid, a.MinIncrementPct)
			v.Mine = a.TopFarmID != nil && *a.TopFarmID == farm.ID
			if a.TopFarmID != nil {
				if top, err := farms.GetByID(ctx, t, *a.TopFarmID, false); err != nil {
					return v, err
				} else if top != nil {
					v.TopName, v.TopBadge = top.Name, cosmetics.BadgeEmoji(deref(top.Badge))
				}
			}
			if v.Owned, err = ownsAuctionItem(ctx, t, farm.ID, a.ItemKind, a.ItemCode); err != nil {
				return v, err
			}
			if err := t.QueryRow(ctx, `SELECT count(*), coalesce(sum(amount) FILTER (WHERE farm_id = $2 AND refunded_at IS NOT NULL), 0)
				FROM auction_bids WHERE auction_id = $1`, a.ID, farm.ID).Scan(&v.BidCount, &v.Outbid); err != nil {
				return v, err
			}
			if v.Mine {
				v.Outbid = 0
			}
		}
		last, err := db.QueryStruct[auctionRow](ctx, t, "SELECT "+auctionCols+
			" FROM auctions WHERE status <> 'open' ORDER BY ends_at DESC LIMIT 1")
		if err != nil {
			return v, err
		}
		if last != nil {
			v.LastName, _ = auctionItemName(last.ItemKind, last.ItemCode)
			if last.Status == "settled" && last.TopFarmID != nil {
				if w, err := farms.GetByID(ctx, t, *last.TopFarmID, false); err != nil {
					return v, err
				} else if w != nil {
					v.LastWin, v.LastPrice = w.Name, deref0(last.TopBid)
				}
			}
		}
		return v, nil
	})
}

// PlaceBid 對目前的拍賣出價：amount 是這次出價後的總額（自己已經是最高出價者時只扣差額）。
func (s *GameService) PlaceBid(ctx context.Context, playerID int64, amount int) (ActionResult, error) {
	if amount <= 0 {
		return ActionResult{}, gameerr.Validationf("出價必須是正整數。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		now := s.now()
		if err := s.ensureAuction(ctx, t, now); err != nil {
			return ActionResult{}, err
		}
		a, err := db.QueryStruct[auctionRow](ctx, t, "SELECT "+auctionCols+
			" FROM auctions WHERE status = 'open' AND starts_at <= $1 ORDER BY id DESC LIMIT 1 FOR UPDATE", now)
		if err != nil {
			return ActionResult{}, err
		}
		if a == nil || !now.Before(a.EndsAt) {
			return ActionResult{}, gameerr.Validationf("目前沒有進行中的拍賣（每週一 00:00 開新的一場）。")
		}
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		name, _ := auctionItemName(a.ItemKind, a.ItemCode)
		if owned, err := ownsAuctionItem(ctx, t, farm.ID, a.ItemKind, a.ItemCode); err != nil {
			return ActionResult{}, err
		} else if owned {
			return ActionResult{}, gameerr.Validationf("你已經擁有%s，不能再出價。", name)
		}
		minBid := auction.MinNextBid(a.StartPrice, a.TopBid, a.MinIncrementPct)
		if amount < minBid {
			return ActionResult{}, gameerr.Validationf("出價至少要 %s 金幣。", numfmt.Comma(minBid))
		}
		mine := a.TopFarmID != nil && *a.TopFarmID == farm.ID
		already := 0
		if mine {
			already = deref0(a.TopBid)
		}
		need := amount - already
		if farm.Gold < need {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：這次要凍結 %d，目前只有 %d。", need, farm.Gold)
		}
		farm.Gold -= need
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvAuctionBid, fmt.Sprintf("拍賣出價 %d 金（%s），凍結 %d", amount, name, need),
			rec{Quantity: 1, GoldDelta: -need}); err != nil {
			return ActionResult{}, err
		}
		if a.TopFarmID != nil && !mine { // 前一位最高出價者：凍結的金幣全數退回
			prev, err := farms.GetByID(ctx, t, *a.TopFarmID, true)
			if err != nil {
				return ActionResult{}, err
			}
			if prev != nil {
				back := deref0(a.TopBid)
				prev.Gold += back
				if err := s.saveFarm(ctx, t, prev, db.C("gold", prev.Gold)); err != nil {
					return ActionResult{}, err
				}
				if err := s.record(ctx, t, prev, rules.EvAuctionRefund, fmt.Sprintf("拍賣出價被超過（%s），退回 %d 金", name, back),
					rec{Quantity: 1, GoldDelta: back}); err != nil {
					return ActionResult{}, err
				}
			}
			if _, err := t.Exec(ctx, "UPDATE auction_bids SET refunded_at = $3 WHERE auction_id = $1 AND farm_id = $2 AND refunded_at IS NULL",
				a.ID, *a.TopFarmID, now); err != nil {
				return ActionResult{}, err
			}
		}
		if _, err := t.Exec(ctx, "INSERT INTO auction_bids (auction_id, farm_id, amount, created_at) VALUES ($1, $2, $3, $4)",
			a.ID, farm.ID, amount, now); err != nil {
			return ActionResult{}, err
		}
		ends := a.EndsAt
		extend := time.Duration(auction.Current().ExtendMinutes) * time.Minute
		extended := ""
		if ends.Sub(now) < extend {
			ends = ends.Add(extend)
			extended = txf(ctx, "\n⏰ 結標前 %d 分鐘內有人出價，結標時間延後到 %s。", auction.Current().ExtendMinutes, taipeiFmt(ends, "01-02 15:04"))
		}
		if _, err := t.Exec(ctx, "UPDATE auctions SET top_bid = $2, top_farm_id = $3, ends_at = $4 WHERE id = $1",
			a.ID, amount, farm.ID, ends); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "🔨 出價 %s 金幣（%s），目前最高價！這次凍結 %s 金幣；被超過時會全數退回。%s",
			numfmt.Comma(amount), name, numfmt.Comma(need), extended), farm.Gold, -need, "", 0), nil
	})
}

// SettleAuctions 結算所有到期的拍賣並開這一週的拍賣；回傳這次結算的場數。可重複呼叫，不會重複發獎。
func (s *GameService) SettleAuctions(ctx context.Context) (int, error) {
	ids, err := inTx(ctx, s, func(t *db.Tx) ([]int64, error) {
		return db.QueryValues[int64](ctx, t, "SELECT id FROM auctions WHERE status = 'open' AND ends_at <= $1 ORDER BY id", s.now())
	})
	if err != nil {
		return 0, err
	}
	done := 0
	for _, id := range ids {
		settled, err := inTx(ctx, s, func(t *db.Tx) (bool, error) { return s.settleAuction(ctx, t, id) })
		if err != nil {
			return done, err
		}
		if settled {
			done++
		}
	}
	err = s.transaction(ctx, func(t *db.Tx) error { return s.ensureAuction(ctx, t, s.now()) })
	return done, err
}

func (s *GameService) settleAuction(ctx context.Context, t *db.Tx, id int64) (bool, error) {
	now := s.now()
	a, err := db.QueryStruct[auctionRow](ctx, t, "SELECT "+auctionCols+
		" FROM auctions WHERE id = $1 AND status = 'open' AND ends_at <= $2 FOR UPDATE", id, now)
	if err != nil || a == nil {
		return false, err // 已經被別的程序結算，或結標時間被延後了
	}
	var winner *int64
	if a.TopFarmID != nil {
		farm, err := farms.GetByID(ctx, t, *a.TopFarmID, true)
		if err != nil {
			return false, err
		}
		if farm != nil {
			if err := s.grantAuctionItem(ctx, t, farm.ID, a.ItemKind, a.ItemCode, now); err != nil {
				return false, err
			}
			name, _ := auctionItemName(a.ItemKind, a.ItemCode)
			if err := s.record(ctx, t, farm, rules.EvAuctionWin, fmt.Sprintf("拍賣得標：%s，得標價 %d 金", name, deref0(a.TopBid)),
				rec{Quantity: 1}); err != nil {
				return false, err
			}
			winner = &farm.ID
		}
	}
	status := "unsold"
	if winner != nil {
		status = "settled"
	}
	_, err = t.Exec(ctx, "UPDATE auctions SET status = $2, settled_at = $3 WHERE id = $1 AND status = 'open'", a.ID, status, now)
	return err == nil, err
}

func (s *GameService) grantAuctionItem(ctx context.Context, t *db.Tx, farmID int64, kind, code string, now time.Time) error {
	switch kind {
	case auction.KindDecor:
		_, err := t.Exec(ctx, `INSERT INTO farm_decorations (farm_id, decor_code, slot, acquired_at) VALUES ($1, $2, NULL, $3)
			ON CONFLICT (farm_id, decor_code) DO NOTHING`, farmID, code, now)
		return err
	case auction.KindBadge:
		_, err := grantCosmetic(ctx, t, farmID, cosmetics.KindBadge, code, cosmetics.SourceAuction, now)
		return err
	default:
		return titles.Grant(ctx, t, farmID, code, cosmetics.SourceAuction, now)
	}
}
