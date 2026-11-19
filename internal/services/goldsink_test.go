package services

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/achievements"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/reputation"
	"farmgm/internal/game/specialshop"
)

// 名牌（2026-11-19）：商店名牌買一次就裝備；不販售的買不到；只能裝備擁有的。
func TestBadges(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.s.BuyBadge(e.ctx, pid, "clover")
	wantKind(t, err, gameerr.InsufficientGold)
	e.setGoldRecorded(pid, 100_000)
	if r := e.ok(e.s.BuyBadge(e.ctx, pid, "clover")); r.GoldDelta != -30_000 || !strings.Contains(r.Message, "🍀") {
		t.Fatal(r.Message)
	}
	_, err = e.s.BuyBadge(e.ctx, pid, "clover")
	wantKind(t, err, gameerr.AlreadyExists)
	_, err = e.s.BuyBadge(e.ctx, pid, "dragon")
	wantMsg(t, err, gameerr.Validation, "不在商店販售")
	_, err = e.s.EquipBadge(e.ctx, pid, "crown")
	wantMsg(t, err, gameerr.Validation, "還沒有")
	if b := must[map[int64]string](t)(e.s.PlayerBadges(e.ctx, []int64{pid})); b[pid] != "🍀" {
		t.Fatal(b)
	}
	e.ok(e.s.EquipBadge(e.ctx, pid, ""))
	if b := must[map[int64]string](t)(e.s.PlayerBadges(e.ctx, []int64{pid})); len(b) != 0 {
		t.Fatal(b)
	}
	v := must[BadgeView](t)(e.s.Badges(e.ctx, pid))
	if v.Equipped != "" || !v.Items[0].Owned || v.Items[0].Source != "shop" {
		t.Fatalf("%+v", v)
	}
	e.invariant()
}

// 農會聲望：捐款扣金幣、升級發獎勵、每日上限、不影響淨賺金幣；日報出現升級新聞與名牌。
func TestReputationDonate(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	e.setGoldRecorded(pid, 2_000_000)
	_, err := e.s.DonateReputation(e.ctx, pid, 999)
	wantMsg(t, err, gameerr.Validation, "最少")
	netBefore := e.scalar("SELECT f.gold - s.non_operating_gold FROM farms f JOIN farm_stats s ON s.farm_id = f.id WHERE f.player_id = $1", pid)
	// Lv.5 需要 10000 × 5^1.5 ≈ 111,803；Lv.10 ≈ 316,228
	r := e.ok(e.s.DonateReputation(e.ctx, pid, 320_000))
	if r.GoldDelta != -320_000 || !strings.Contains(r.Message, "Lv.10") || !strings.Contains(r.Message, "農會會員") ||
		!strings.Contains(r.Message, "金穗") {
		t.Fatal(r.Message)
	}
	if n := e.scalar("SELECT f.gold - s.non_operating_gold FROM farms f JOIN farm_stats s ON s.farm_id = f.id WHERE f.player_id = $1", pid); n != netBefore {
		t.Fatal("捐款不應影響淨賺金幣", netBefore, n)
	}
	if !slicesHasTitle(must[TitlesView](t)(e.p.Titles(e.ctx, pid)).Owned, "農會會員") {
		t.Fatal("要拿到稱號")
	}
	e.ok(e.s.EquipBadge(e.ctx, pid, "wheat")) // 聲望拿到的名牌可以裝備
	v := must[ReputationView](t)(e.s.Reputation(e.ctx, &home, pid))
	if v.Level != 10 || v.Points != 320_000 || v.DonatedToday != 320_000 || len(v.Board) != 1 || v.Board[0].Badge != "🌾" ||
		v.NextReward == nil || v.NextReward.Level != 15 {
		t.Fatalf("%+v", v)
	}
	_, err = e.s.DonateReputation(e.ctx, pid, 700_000) // 超過每天 100 萬
	wantMsg(t, err, gameerr.Validation, "每天最多捐")
	e.ok(e.s.DonateReputation(e.ctx, pid, 680_000))
	// 隔天的日報：升級新聞＋名牌
	e.clock.Advance(24 * 3600)
	d := e.news(home, time.Time{})
	if !strings.Contains(d.Text, "聲望升上 **Lv.") || !strings.Contains(d.Text, "🌾 **阿明的農場**") {
		t.Fatal(d.Text)
	}
	if v := must[ReputationView](t)(e.s.Reputation(e.ctx, &home, pid)); v.DonatedToday != 0 {
		t.Fatal("每日上限隔天重算", v.DonatedToday)
	}
	e.invariant()
}

func slicesHasTitle(list []TitleInfo, title string) bool {
	for _, x := range list {
		if x.Title == title {
			return true
		}
	}
	return false
}

// 聲望獎勵的稱號不可和成就、商店稱號重複。
func TestReputationTitlesUnique(t *testing.T) {
	used := map[string]bool{}
	for _, o := range specialshop.TitleShop() {
		used[o.Title] = true
	}
	for _, a := range achievements.BaseAchievements {
		used[a.Title] = true
	}
	for _, title := range reputation.Titles() {
		if used[title] || len([]rune(title)) > specialshop.TitleMaxLen {
			t.Fatal(title)
		}
	}
	if reputation.Level(0) != 0 || reputation.Level(10_000) != 1 || reputation.Level(316_228) != 10 || reputation.Level(316_227) != 9 {
		t.Fatal(reputation.Level(316_228))
	}
}

// 農場主題與寵物造型（2026-11-19）：主題買下就套用、可以換回預設；帽子與顏色要先買，每隻寵物各自設定。
func TestThemesAndPetLooks(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 1_000_000)
	e.ok(e.r.BuyPet(e.ctx, pid, "cat"))
	if r := e.ok(e.s.BuyCosmetic(e.ctx, pid, "theme", "snow")); r.GoldDelta != -100_000 || !strings.Contains(r.Message, "已經套用") {
		t.Fatal(r.Message)
	}
	if st := e.status(pid); st.Theme != "snow" {
		t.Fatal(st.Theme)
	}
	_, err := e.s.SetTheme(e.ctx, pid, "night")
	wantMsg(t, err, gameerr.Validation, "還沒有")
	e.ok(e.s.SetTheme(e.ctx, pid, ""))
	if st := e.status(pid); st.Theme != "" {
		t.Fatal(st.Theme)
	}
	_, err = e.s.SetPetLook(e.ctx, pid, "cat", "hat", "crown")
	wantMsg(t, err, gameerr.Validation, "還沒有")
	_, err = e.s.SetPetLook(e.ctx, pid, "dog", "hat", "")
	wantMsg(t, err, gameerr.Validation, "還沒有狗狗")
	e.ok(e.s.BuyCosmetic(e.ctx, pid, "hat", "crown"))
	e.ok(e.s.BuyCosmetic(e.ctx, pid, "color", "pink"))
	_, err = e.s.BuyCosmetic(e.ctx, pid, "hat", "crown")
	wantKind(t, err, gameerr.AlreadyExists)
	e.ok(e.s.SetPetLook(e.ctx, pid, "cat", "hat", "crown"))
	e.ok(e.s.SetPetLook(e.ctx, pid, "cat", "color", "pink"))
	if st := e.status(pid); st.PetSkins["cat"].Hat != "crown" || st.PetSkins["cat"].Color != "pink" {
		t.Fatalf("%+v", st.PetSkins)
	}
	e.ok(e.s.SetPetLook(e.ctx, pid, "cat", "hat", ""))
	v := must[LooksView](t)(e.s.Looks(e.ctx, pid))
	if len(v.Pets) != 1 || v.Pets[0].Hat != "" || v.Pets[0].Color != "pink" || !v.Themes[0].Owned || v.Hats[5].Code != "crown" || !v.Hats[5].Owned {
		t.Fatalf("%+v", v)
	}
	e.invariant()
}
