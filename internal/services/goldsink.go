package services

// 金幣回收第二波（Go 版新增，2026-11-19）：名牌（game/cosmetics）與農場聲望（game/reputation）。
// 擁有的外觀存在 farm_cosmetics，裝備的名牌在 farms.badge，聲望在 farms.reputation（Migration 0046）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/cosmetics"
	"farmgm/internal/game/gameerr"
	petrules "farmgm/internal/game/pets"
	"farmgm/internal/game/ranking"
	"farmgm/internal/game/reputation"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
	petrepo "farmgm/internal/repositories/pets"
	"farmgm/internal/repositories/titles"
)

// ownedCosmetics 擁有的某一種外觀：代碼 → 取得方式。
func ownedCosmetics(ctx context.Context, t *db.Tx, farmID int64, kind string) (map[string]string, error) {
	rows, err := t.Query(ctx, "SELECT code, source FROM farm_cosmetics WHERE farm_id = $1 AND kind = $2", farmID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var code, source string
		if err := rows.Scan(&code, &source); err != nil {
			return nil, err
		}
		out[code] = source
	}
	return out, rows.Err()
}

// grantCosmetic 給予外觀（已經有的不重複）；回傳是不是新取得的。
func grantCosmetic(ctx context.Context, t *db.Tx, farmID int64, kind, code, source string, now time.Time) (bool, error) {
	tag, err := t.Exec(ctx, `INSERT INTO farm_cosmetics (farm_id, kind, code, source, obtained_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (farm_id, kind, code) DO NOTHING`, farmID, kind, code, source, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ---------- 名牌 ----------

// BadgeItem 一種名牌與玩家的擁有狀態。
type BadgeItem struct {
	cosmetics.Badge
	Price  int // 商店價格；0 = 不販售
	Owned  bool
	Source string // 取得方式（已擁有時）
}

// BadgeView 名牌頁。
type BadgeView struct {
	Gold     int
	Equipped string // 裝備中的名牌代碼（"" = 沒有）
	Items    []BadgeItem
}

// Badges 名牌頁的資料。
func (s *GameService) Badges(ctx context.Context, playerID int64) (BadgeView, error) {
	return inTx(ctx, s, func(t *db.Tx) (BadgeView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return BadgeView{}, err
		}
		owned, err := ownedCosmetics(ctx, t, farm.ID, cosmetics.KindBadge)
		if err != nil {
			return BadgeView{}, err
		}
		v := BadgeView{Gold: farm.Gold, Equipped: deref(farm.Badge)}
		for _, b := range cosmetics.Badges {
			src, has := owned[b.Code]
			v.Items = append(v.Items, BadgeItem{Badge: b, Price: cosmetics.BadgePrice(b.Code), Owned: has, Source: src})
		}
		return v, nil
	})
}

// BuyBadge 用金幣買名牌（每種只能買一次），買到後直接裝備。
func (s *GameService) BuyBadge(ctx context.Context, playerID int64, code string) (ActionResult, error) {
	b, ok := cosmetics.FindBadge(code)
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有這種名牌。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		owned, err := ownedCosmetics(ctx, t, farm.ID, cosmetics.KindBadge)
		if err != nil {
			return ActionResult{}, err
		}
		if _, has := owned[code]; has {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經有名牌 %s %s 了。", b.Emoji, b.Name)
		}
		price := cosmetics.BadgePrice(code)
		if price <= 0 {
			return ActionResult{}, gameerr.Validationf("名牌 %s %s 不在商店販售（取得方式：%s）。", b.Emoji, b.Name, b.How)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：名牌 %s 需要 %d，目前只有 %d。", b.Name, price, farm.Gold)
		}
		farm.Gold -= price
		farm.Badge = &b.Code
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("badge", b.Code)); err != nil {
			return ActionResult{}, err
		}
		if _, err := grantCosmetic(ctx, t, farm.ID, cosmetics.KindBadge, code, cosmetics.SourceShop, s.now()); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("購買名牌%s %s，金幣 -%d", b.Emoji, b.Name, price),
			rec{ItemCode: "badge_" + code, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("📛 買下名牌 %s %s，花費 %s 金幣，已經裝備（排行榜、日報與世界 Boss 排行的名字前面會顯示）。",
			b.Emoji, b.Name, numfmt.Comma(price)), farm.Gold, -price, "", 0), nil
	})
}

// EquipBadge 裝備擁有的名牌；code 為 "" 時不顯示名牌。
func (s *GameService) EquipBadge(ctx context.Context, playerID int64, code string) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if code == "" {
			farm.Badge = nil
			if err := s.saveFarm(ctx, t, farm, db.C("badge", nil)); err != nil {
				return ActionResult{}, err
			}
			return result("📛 已取消顯示名牌。", farm.Gold, 0, "", 0), nil
		}
		b, ok := cosmetics.FindBadge(code)
		if !ok {
			return ActionResult{}, gameerr.NotFoundf("沒有這種名牌。")
		}
		owned, err := ownedCosmetics(ctx, t, farm.ID, cosmetics.KindBadge)
		if err != nil {
			return ActionResult{}, err
		}
		if _, has := owned[code]; !has {
			return ActionResult{}, gameerr.Validationf("你還沒有名牌 %s %s。", b.Emoji, b.Name)
		}
		farm.Badge = &b.Code
		if err := s.saveFarm(ctx, t, farm, db.C("badge", b.Code)); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("📛 改為顯示名牌 %s %s。", b.Emoji, b.Name), farm.Gold, 0, "", 0), nil
	})
}

// PlayerBadges 排行榜顯示用：玩家 → 裝備中名牌的 emoji。
func (s *GameService) PlayerBadges(ctx context.Context, playerIDs []int64) (map[int64]string, error) {
	_, badges, err := s.PlayerNameTags(ctx, playerIDs)
	return badges, err
}

// PlayerNameTags 排行榜顯示用（一次查詢）：玩家 → 選用的稱號、裝備中名牌的 emoji。
func (s *GameService) PlayerNameTags(ctx context.Context, playerIDs []int64) (titles, badges map[int64]string, err error) {
	titles, badges = map[int64]string{}, map[int64]string{}
	if len(playerIDs) == 0 {
		return titles, badges, nil
	}
	err = s.transaction(ctx, func(t *db.Tx) error {
		rows, err := t.Query(ctx, `SELECT player_id, title, badge FROM farms
			WHERE player_id = ANY($1) AND (title IS NOT NULL OR badge IS NOT NULL)`, playerIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var title, badge *string
			if err := rows.Scan(&id, &title, &badge); err != nil {
				return err
			}
			if title != nil {
				titles[id] = *title
			}
			if e := cosmetics.BadgeEmoji(deref(badge)); e != "" {
				badges[id] = e
			}
		}
		return rows.Err()
	})
	return titles, badges, err
}

// ---------- 農場聲望（農會） ----------

// ReputationRank 農會排行的一列。
type ReputationRank struct {
	Rank     int
	FarmName string
	Badge    string // emoji
	Points   int
	Level    int
}

// ReputationView 農會頁。
type ReputationView struct {
	Gold         int
	Points       int
	Level        int
	NextNeed     int // 下一級需要的累積聲望
	LevelStart   int // 這一級的起點（進度條用）
	DonatedToday int
	DailyCap     int // 0 = 不限
	MinDonation  int
	NextReward   *reputation.Reward
	Board        []ReputationRank // 本群組前 10 名（不在群組中查看時是全部玩家）
}

// donatedToday 今天（台灣日期）已經捐了多少。
func (s *GameService) donatedToday(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	start, end, err := ranking.PeriodBounds("daily", s.now())
	if err != nil {
		return 0, err
	}
	v, _, err := db.QueryValue[int](ctx, t, `SELECT coalesce(-sum(gold_delta), 0)::int FROM game_transactions
		WHERE farm_id = $1 AND event_type = $2 AND created_at >= $3 AND created_at < $4`,
		farmID, string(rules.EvReputationDonate), start, end)
	return v, err
}

// Reputation 農會頁的資料（groupID 不為 nil 時排行只算這個群組的成員）。
func (s *GameService) Reputation(ctx context.Context, groupID *int64, playerID int64) (ReputationView, error) {
	return inTx(ctx, s, func(t *db.Tx) (ReputationView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return ReputationView{}, err
		}
		c := reputation.Current()
		lv := reputation.Level(farm.Reputation)
		v := ReputationView{Gold: farm.Gold, Points: farm.Reputation, Level: lv, NextNeed: reputation.Need(lv + 1),
			LevelStart: reputation.Need(lv), DailyCap: c.DailyCap, MinDonation: c.MinDonation}
		if r, ok := reputation.NextReward(lv); ok {
			v.NextReward = &r
		}
		if v.DonatedToday, err = s.donatedToday(ctx, t, farm.ID); err != nil {
			return ReputationView{}, err
		}
		sql := "SELECT f.name, f.badge, f.reputation FROM farms f WHERE f.reputation > 0"
		args := []any{}
		if groupID != nil {
			sql += " AND f.player_id IN (SELECT player_id FROM group_members WHERE group_id = $1)"
			args = append(args, *groupID)
		}
		rows, err := t.Query(ctx, sql+" ORDER BY f.reputation DESC, f.id LIMIT 10", args...)
		if err != nil {
			return ReputationView{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var r ReputationRank
			var badge *string
			if err := rows.Scan(&r.FarmName, &badge, &r.Points); err != nil {
				return ReputationView{}, err
			}
			r.Rank, r.Badge, r.Level = len(v.Board)+1, cosmetics.BadgeEmoji(deref(badge)), reputation.Level(r.Points)
			v.Board = append(v.Board, r)
		}
		return v, rows.Err()
	})
}

// RewardText 聲望獎勵的說明文字。
func RewardText(r reputation.Reward) string {
	if r.Title != "" {
		return fmt.Sprintf("稱號「%s」", r.Title)
	}
	b, _ := cosmetics.FindBadge(r.Badge)
	return fmt.Sprintf("名牌 %s %s", b.Emoji, b.Name)
}

// DonateReputation 捐金幣給農會：1 金 = 1 聲望點，金幣直接消失。升級時發放等級獎勵（稱號、名牌）。
func (s *GameService) DonateReputation(ctx context.Context, playerID int64, amount int) (ActionResult, error) {
	c := reputation.Current()
	if amount < c.MinDonation {
		return ActionResult{}, gameerr.Validationf("一次最少要捐 %s 金幣。", numfmt.Comma(c.MinDonation))
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		today, err := s.donatedToday(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if c.DailyCap > 0 && today+amount > c.DailyCap {
			return ActionResult{}, gameerr.Validationf("每天最多捐 %s 金幣，今天已經捐了 %s，還可以捐 %s。",
				numfmt.Comma(c.DailyCap), numfmt.Comma(today), numfmt.Comma(max(0, c.DailyCap-today)))
		}
		if farm.Gold < amount {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：要捐 %d，目前只有 %d。", amount, farm.Gold)
		}
		before := reputation.Level(farm.Reputation)
		farm.Gold -= amount
		farm.Reputation += amount
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold), db.C("reputation", farm.Reputation)); err != nil {
			return ActionResult{}, err
		}
		after := reputation.Level(farm.Reputation)
		desc := fmt.Sprintf("捐給農會 %d 金幣，聲望 %d（Lv.%d）", amount, farm.Reputation, after)
		if err := s.record(ctx, t, farm, rules.EvReputationDonate, desc, rec{Quantity: amount, GoldDelta: -amount}); err != nil {
			return ActionResult{}, err
		}
		msg := fmt.Sprintf("🏛️ 捐給農會 %s 金幣，聲望 +%s（共 %s）。", numfmt.Comma(amount), numfmt.Comma(amount), numfmt.Comma(farm.Reputation))
		if after > before {
			msg += fmt.Sprintf("\n🎉 聲望升到 **Lv.%d**！", after)
			var got []string
			for _, r := range reputation.RewardsBetween(before, after) {
				if err := s.grantReputationReward(ctx, t, farm, r); err != nil {
					return ActionResult{}, err
				}
				got = append(got, RewardText(r))
			}
			if len(got) > 0 {
				msg += "\n🎁 獲得 " + strings.Join(got, "、") + "（稱號到「🎖️ 稱號」選用，名牌到「📛 名牌」裝備）"
			}
		}
		msg += fmt.Sprintf("\n下一級 Lv.%d 還差 %s 聲望。", after+1, numfmt.Comma(reputation.Need(after+1)-farm.Reputation))
		return result(msg, farm.Gold, -amount, "", 0), nil
	})
}

func (s *GameService) grantReputationReward(ctx context.Context, t *db.Tx, farm *models.Farm, r reputation.Reward) error {
	if r.Title != "" {
		return titles.Grant(ctx, t, farm.ID, r.Title, cosmetics.SourceReputation, s.now())
	}
	_, err := grantCosmetic(ctx, t, farm.ID, cosmetics.KindBadge, r.Badge, cosmetics.SourceReputation, s.now())
	return err
}

// ---------- 農場主題、寵物帽子與顏色 ----------

// CosmeticItem 一種主題／帽子／顏色與玩家的擁有狀態。
type CosmeticItem struct {
	cosmetics.Item
	Price int // 0 = 不在商店販售
	Owned bool
}

// PetLook 一隻寵物目前的造型。
type PetLook struct {
	Code, Name, Emoji string
	Hat, Color        string
}

// LooksView 外觀頁（主題與寵物造型）。
type LooksView struct {
	Gold   int
	Theme  string
	Themes []CosmeticItem
	Hats   []CosmeticItem
	Colors []CosmeticItem
	Pets   []PetLook // 擁有的寵物（依 petrules.PetOrder）
}

func cosmeticKindName(kind string) string {
	return map[string]string{cosmetics.KindTheme: "主題", cosmetics.KindHat: "帽子", cosmetics.KindColor: "顏色",
		cosmetics.KindBadge: "名牌"}[kind]
}

// Looks 外觀頁的資料。
func (s *GameService) Looks(ctx context.Context, playerID int64) (LooksView, error) {
	return inTx(ctx, s, func(t *db.Tx) (LooksView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return LooksView{}, err
		}
		v := LooksView{Gold: farm.Gold, Theme: deref(farm.Theme)}
		for _, kind := range []string{cosmetics.KindTheme, cosmetics.KindHat, cosmetics.KindColor} {
			owned, err := ownedCosmetics(ctx, t, farm.ID, kind)
			if err != nil {
				return LooksView{}, err
			}
			var list []CosmeticItem
			for _, it := range cosmetics.Items(kind) {
				_, has := owned[it.Code]
				list = append(list, CosmeticItem{Item: it, Price: cosmetics.Price(kind, it.Code), Owned: has})
			}
			switch kind {
			case cosmetics.KindTheme:
				v.Themes = list
			case cosmetics.KindHat:
				v.Hats = list
			default:
				v.Colors = list
			}
		}
		levels, skins, err := petrepo.LevelsAndSkins(ctx, t, farm.ID)
		if err != nil {
			return LooksView{}, err
		}
		for _, code := range petrules.PetOrder {
			if _, ok := levels[code]; ok {
				p := petrules.Pets[code]
				v.Pets = append(v.Pets, PetLook{code, p.Name, p.Emoji, skins[code].Hat, skins[code].Color})
			}
		}
		return v, nil
	})
}

func findCosmetic(kind, code string) (cosmetics.Item, bool) {
	for _, it := range cosmetics.Items(kind) {
		if it.Code == code {
			return it, true
		}
	}
	return cosmetics.Item{}, false
}

// BuyCosmetic 用金幣買主題、帽子或顏色（每種只能買一次）；主題買下後直接套用。
func (s *GameService) BuyCosmetic(ctx context.Context, playerID int64, kind, code string) (ActionResult, error) {
	if kind != cosmetics.KindTheme && kind != cosmetics.KindHat && kind != cosmetics.KindColor {
		return ActionResult{}, gameerr.Validationf("沒有這種外觀。")
	}
	it, ok := findCosmetic(kind, code)
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有這種%s。", cosmeticKindName(kind))
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		owned, err := ownedCosmetics(ctx, t, farm.ID, kind)
		if err != nil {
			return ActionResult{}, err
		}
		what := cosmeticKindName(kind)
		if _, has := owned[code]; has {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經有%s %s %s 了。", what, it.Emoji, it.Name)
		}
		price := cosmetics.Price(kind, code)
		if price <= 0 {
			return ActionResult{}, gameerr.Validationf("%s %s 目前不在商店販售。", what, it.Name)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：%s %s 需要 %d，目前只有 %d。", what, it.Name, price, farm.Gold)
		}
		farm.Gold -= price
		cols := []db.Col{db.C("gold", farm.Gold)}
		tail := "到寵物頁選一隻寵物換上。"
		if kind == cosmetics.KindTheme {
			farm.Theme = &it.Code
			cols = append(cols, db.C("theme", it.Code))
			tail = "已經套用，回「🌱 農田」就能看到。"
		}
		if err := s.saveFarm(ctx, t, farm, cols...); err != nil {
			return ActionResult{}, err
		}
		if _, err := grantCosmetic(ctx, t, farm.ID, kind, code, cosmetics.SourceShop, s.now()); err != nil {
			return ActionResult{}, err
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("購買%s%s %s，金幣 -%d", what, it.Emoji, it.Name, price),
			rec{ItemCode: kind + "_" + code, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🎨 買下%s %s %s，花費 %s 金幣，%s", what, it.Emoji, it.Name, numfmt.Comma(price), tail),
			farm.Gold, -price, "", 0), nil
	})
}

// SetTheme 套用擁有的主題；code 為 "" 時換回預設外觀。
func (s *GameService) SetTheme(ctx context.Context, playerID int64, code string) (ActionResult, error) {
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if code == "" {
			farm.Theme = nil
			if err := s.saveFarm(ctx, t, farm, db.C("theme", nil)); err != nil {
				return ActionResult{}, err
			}
			return result("🖼️ 換回預設外觀。", farm.Gold, 0, "", 0), nil
		}
		it, ok := findCosmetic(cosmetics.KindTheme, code)
		if !ok {
			return ActionResult{}, gameerr.NotFoundf("沒有這種主題。")
		}
		owned, err := ownedCosmetics(ctx, t, farm.ID, cosmetics.KindTheme)
		if err != nil {
			return ActionResult{}, err
		}
		if _, has := owned[code]; !has {
			return ActionResult{}, gameerr.Validationf("你還沒有主題 %s %s。", it.Emoji, it.Name)
		}
		farm.Theme = &it.Code
		if err := s.saveFarm(ctx, t, farm, db.C("theme", it.Code)); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🖼️ 套用主題 %s %s。", it.Emoji, it.Name), farm.Gold, 0, "", 0), nil
	})
}

// SetPetLook 幫一隻寵物戴帽子或換顏色（kind = hat／color；code 為 "" 時拿掉）。
func (s *GameService) SetPetLook(ctx context.Context, playerID int64, petCode, kind, code string) (ActionResult, error) {
	if kind != cosmetics.KindHat && kind != cosmetics.KindColor {
		return ActionResult{}, gameerr.Validationf("只能換帽子或顏色。")
	}
	pet, ok := petrules.Pets[petCode]
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有這種寵物。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		lv, err := petrepo.Level(ctx, t, farm.ID, petCode)
		if err != nil {
			return ActionResult{}, err
		}
		if lv == nil {
			return ActionResult{}, gameerr.Validationf("你還沒有%s。", pet.Name)
		}
		var value any // nil = 拿掉
		msg := fmt.Sprintf("%s %s 拿掉了%s。", pet.Emoji, pet.Name, cosmeticKindName(kind))
		if code != "" {
			it, ok := findCosmetic(kind, code)
			if !ok {
				return ActionResult{}, gameerr.NotFoundf("沒有這種%s。", cosmeticKindName(kind))
			}
			owned, err := ownedCosmetics(ctx, t, farm.ID, kind)
			if err != nil {
				return ActionResult{}, err
			}
			if _, has := owned[code]; !has {
				return ActionResult{}, gameerr.Validationf("你還沒有%s %s %s。", cosmeticKindName(kind), it.Emoji, it.Name)
			}
			value = code
			msg = fmt.Sprintf("%s %s 換上了%s %s %s。", pet.Emoji, pet.Name, cosmeticKindName(kind), it.Emoji, it.Name)
		}
		col := "hat"
		if kind == cosmetics.KindColor {
			col = "color"
		}
		if _, err := t.Exec(ctx, "UPDATE farm_pets SET "+col+" = $3 WHERE farm_id = $1 AND pet_code = $2", farm.ID, petCode, value); err != nil {
			return ActionResult{}, err
		}
		return result(msg+"回「🌱 農田」就能看到。", farm.Gold, 0, "", 0), nil
	})
}
