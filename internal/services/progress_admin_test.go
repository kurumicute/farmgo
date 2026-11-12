package services

// 對應 Python tests/test_progress_admin.py 的服務層測試（網站 API 測試改寫為服務層；只經規則套件的部分在 game/achievements、
// game/checkin）。

import (
	"context"
	"maps"
	"strings"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/checkin"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/specialshop"
	"farmgm/internal/repositories/featuresettings"
	"farmgm/internal/repositories/repotest"
)

// progressFarmer Python 夾具 pid：主群組的「測試農夫」，金幣 500,000。
func (e *env) progressFarmer() int64 {
	e.t.Helper()
	pid := e.farmer("測試農夫", e.group("主群組"))
	e.setGoldRecorded(pid, 500_000)
	return pid
}

func (e *env) updateFeature(key string, raw featurecfg.Value) error {
	_, err := e.a.UpdateFeatureSettings(e.ctx, "root", key, raw)
	return err
}

func (e *env) mustFeature(key string, raw featurecfg.Value) {
	e.t.Helper()
	if err := e.updateFeature(key, raw); err != nil {
		e.t.Fatal(err)
	}
}

// ---------- 規則設定 ----------

func TestPetsSettingsApplyAndReset(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	e.mustFeature("pets", featurecfg.Value{"buy_price": 1_000, "upgrade_prices": []any{2_000, 3_000}, "base_pct": 10, "pct_per_level": 20})
	if pets.BuyPrice() != 1_000 || pets.MaxLevel() != 2 || pets.BonusPct(intPtr(2)) != 50 {
		t.Fatal(pets.BuyPrice(), pets.MaxLevel())
	}
	before := e.gold(pid)
	e.ok(e.r.BuyPet(e.ctx, pid, pets.Cat))
	if e.gold(pid) != before-1_000 {
		t.Fatal(e.gold(pid))
	}
	e.ok(e.r.UpgradePet(e.ctx, pid, pets.Cat))
	e.ok(e.r.UpgradePet(e.ctx, pid, pets.Cat))
	_, err := e.r.UpgradePet(e.ctx, pid, pets.Cat)
	wantMsg(t, err, gameerr.Validation, "最高等級")
	if e.scalar("SELECT (details->'after'->>'base_pct')::int FROM admin_actions WHERE action = 'update_feature_settings'") != 10 {
		t.Fatal("管理紀錄不對")
	}
	err = e.updateFeature("pets", featurecfg.Value{"buy_price": 1, "upgrade_prices": []any{1, 1, 1}, "base_pct": 50, "pct_per_level": 20})
	wantMsg(t, err, gameerr.Validation, "不可超過 100%")
	must[featurecfg.Value](t)(e.a.ResetFeatureSettings(e.ctx, "root", "pets"))
	if pets.BuyPrice() != 5_000 || pets.BonusPct(intPtr(4)) != 25 {
		t.Fatal(pets.BuyPrice())
	}
	if e.scalar("SELECT count(*) FROM feature_settings WHERE key = 'pets'") != 0 {
		t.Fatal("資料列沒有刪除")
	}
}

// 另一個程序（Bot）重新讀取資料庫的設定：清掉程序內快取後，下一筆交易就套用。
func TestSettingsSurviveRestartOfOtherProcess(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	e.mustFeature("checkin", featurecfg.Value{"rewards": []any{
		map[string]any{"gold": 7, "items": map[string]any{}},
		map[string]any{"gold": 0, "items": map[string]any{balance.ItemFertilizer: 2}},
	}})
	featurecfg.Reset()
	featuresettings.Invalidate()
	if checkin.CycleDays() != 7 { // 還沒讀取：預設值
		t.Fatal(checkin.CycleDays())
	}
	e.status(pid)
	if checkin.CycleDays() != 2 || checkin.RewardFor(3).Gold != 7 {
		t.Fatal(checkin.CycleDays())
	}
}

func TestInvalidStoredSettingsFallBackToDefaults(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		return featuresettings.Save(ctx, tx, "pets", featurecfg.Value{"buy_price": -1}, "root", e.clock.Now())
	})
	featuresettings.Invalidate()
	e.status(pid)
	if pets.BuyPrice() != 5_000 {
		t.Fatal(pets.BuyPrice())
	}
}

func TestCheckinRewardsFollowSettings(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	e.mustFeature("checkin", featurecfg.Value{"rewards": []any{
		map[string]any{"gold": 11, "items": map[string]any{}},
		map[string]any{"gold": 22, "items": map[string]any{balance.ItemPatrolTicket: 1}},
	}})
	var got []int
	for range 3 {
		before := e.gold(pid)
		if !e.checkin(pid).New {
			t.Fatal("沒有簽到")
		}
		got = append(got, e.gold(pid)-before)
		e.clock.Advance(dayS)
	}
	if got[0] != 11 || got[1] != 22 || got[2] != 11 { // 兩天一輪
		t.Fatal(got)
	}
}

func shopConfig(kv ...any) featurecfg.Value {
	raw := maps.Clone(specialshop.Config())
	for i := 0; i < len(kv); i += 2 {
		raw[kv[i].(string)] = kv[i+1]
	}
	return raw
}

func TestSpecialShopSettingsAndTitleClash(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	titles := []any{map[string]any{"title": "測試大亨", "price": 123}}
	e.mustFeature("special_shop", shopConfig("greenhouse_prices", []any{100, 200}, "greenhouse_pct_per_level", 10, "titles", titles))
	if p, ok := specialshop.TitlePrice("測試大亨"); specialshop.GreenhouseMaxLevel() != 2 || !ok || p != 123 {
		t.Fatal(specialshop.GreenhouseMaxLevel(), p)
	}
	e.ok(e.p.BuyTitle(e.ctx, pid, "測試大亨"))
	e.ok(e.s.UpgradeGreenhouse(e.ctx, pid))
	e.ok(e.s.UpgradeGreenhouse(e.ctx, pid))
	_, err := e.s.UpgradeGreenhouse(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "最高等級")
	err = e.updateFeature("special_shop", shopConfig("greenhouse_prices", []any{100, 200}, "greenhouse_pct_per_level", 10,
		"titles", []any{map[string]any{"title": "神偷", "price": 1}}))
	wantMsg(t, err, gameerr.Validation, "不可重複")
	err = e.updateFeature("special_shop", shopConfig("greenhouse_prices", []any{100, 200}, "greenhouse_pct_per_level", 50,
		"titles", titles))
	wantMsg(t, err, gameerr.Validation, "不可超過 90%")
}

func TestAchievementOverrides(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	e.mustFeature("achievements", featurecfg.Value{"items": map[string]any{
		"crops_100": map[string]any{"target": 1, "gold": 77, "items": map[string]any{}, "title": "第一顆"},
		"steal_20":  achievements.Editable(achievements.BaseByCode["steal_20"]), // 和預設一樣：不存
	}})
	stored, _ := featurecfg.AsMap(featurecfg.Get("achievements")["items"])
	if len(stored) != 1 || stored["crops_100"] == nil {
		t.Fatal(stored)
	}
	if a := achievements.ByCode()["crops_100"]; a.Target != 1 || a.Reward().Gold != 77 || a.Title != "第一顆" {
		t.Fatalf("%+v", a)
	}
	e.grow(pid, 1, "wheat")
	e.clock.Advance(600)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	if r := e.ok(e.p.ClaimAchievements(e.ctx, pid)); !strings.Contains(r.Message, "初出茅廬") || !strings.Contains(r.Message, "第一顆") {
		t.Fatal(r.Message)
	}
	err := e.updateFeature("achievements", featurecfg.Value{"items": map[string]any{"crops_100": map[string]any{"title": "神偷"}}})
	wantMsg(t, err, gameerr.Validation, "不可重複")
	err = e.updateFeature("achievements", featurecfg.Value{"items": map[string]any{"crops_100": map[string]any{"title": "黃金地主"}}})
	wantMsg(t, err, gameerr.Validation, "不可重複") // 和稱號商店重複
	err = e.updateFeature("achievements", featurecfg.Value{"items": map[string]any{"codex_all": map[string]any{"target": 101}}})
	wantKind(t, err, gameerr.Validation)
}

func TestFeatureSettingsView(t *testing.T) {
	e := setup(t)
	view := must[FeatureSettingsView](t)(e.a.FeatureSettings(e.ctx))
	var keys []string
	for _, k := range view.Keys {
		keys = append(keys, k.Key)
		if !k.IsDefault {
			t.Fatal(k.Key)
		}
	}
	if strings.Join(keys, ",") != "checkin,pets,special_shop,achievements,exploration,mushroom" {
		t.Fatal(keys)
	}
	if len(view.Achievements) != len(achievements.BaseAchievements) || len(view.Items) != len(balance.Items) {
		t.Fatal(len(view.Achievements), len(view.Items))
	}
	if len(view.Exploration.Areas) != len(exploration.AreaOrder) || len(view.Exploration.DropItems) == 0 ||
		len(view.Mushroom.Nutrients) != len(mushroom.NutrientOrder) || len(view.Mushroom.Crops) == 0 {
		t.Fatal(view.Exploration, view.Mushroom)
	}
	for _, it := range view.Items {
		if _, ok := balance.Items[it.Code]; !ok {
			t.Fatal(it.Code)
		}
	}
	// 網站 API 測試的服務層部分：不存在的設定與錯誤訊息
	_, err := e.a.UpdateFeatureSettings(e.ctx, "root", "nope", featurecfg.Value{})
	wantKind(t, err, gameerr.NotFound)
	cfg := maps.Clone(pets.Config())
	cfg["base_pct"] = 999
	_, err = e.a.UpdateFeatureSettings(e.ctx, "root", "pets", cfg)
	wantMsg(t, err, gameerr.Validation, "Lv0 加成")
}

// ---------- 單一玩家 ----------

func TestPlayerPetsGreenhouseTitles(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	must[bool](t)(e.a.SetGreenhouseLevel(e.ctx, "root", pid, 3))
	must[bool](t)(e.a.SetPetLevel(e.ctx, "root", pid, pets.Dog, intPtr(2)))
	must[bool](t)(e.a.SetPetLevel(e.ctx, "root", pid, pets.Cat, intPtr(0)))
	view := must[AdminProgressView](t)(e.a.AdminProgress(e.ctx, pid))
	levels := map[string]int{}
	for _, p := range view.Pets {
		levels[p.Code] = *p.Level
	}
	if view.Greenhouse.Level != 3 || !maps.Equal(levels, map[string]int{"cat": 0, "dog": 2}) {
		t.Fatalf("%+v %v", view.Greenhouse, levels)
	}
	must[bool](t)(e.a.SetPetLevel(e.ctx, "root", pid, pets.Cat, nil))
	if p := e.status(pid).Pets; !maps.Equal(p, map[string]int{"dog": 2}) {
		t.Fatal(p)
	}
	_, err := e.a.SetGreenhouseLevel(e.ctx, "root", pid, specialshop.GreenhouseMaxLevel()+1)
	wantKind(t, err, gameerr.Validation)

	must[string](t)(e.a.GrantTitle(e.ctx, "root", pid, "管理員之友"))
	_, err = e.a.GrantTitle(e.ctx, "root", pid, "管理員之友")
	wantKind(t, err, gameerr.AlreadyExists)
	must[bool](t)(e.a.SetActiveTitle(e.ctx, "root", pid, "管理員之友"))
	ts := must[TitlesView](t)(e.p.Titles(e.ctx, pid))
	if deref(ts.Current) != "管理員之友" || ts.Owned[0].Source != "管理員" {
		t.Fatalf("%+v", ts)
	}
	_, err = e.a.SetActiveTitle(e.ctx, "root", pid, "沒有的稱號")
	wantKind(t, err, gameerr.NotFound)
	if err := e.a.RevokeTitle(e.ctx, "root", pid, "管理員之友"); err != nil {
		t.Fatal(err)
	}
	if ts := must[TitlesView](t)(e.p.Titles(e.ctx, pid)); ts.Current != nil || len(ts.Owned) != 0 {
		t.Fatalf("%+v", ts)
	}
	if e.scalar("SELECT count(*) FROM admin_actions WHERE action = 'set_pet_level'") != 3 {
		t.Fatal("管理紀錄數量不對")
	}
}

func TestPlayerCheckinStreakRestore(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	e.checkin(pid)
	e.clock.Advance(3 * dayS)                                           // 斷簽兩天
	if err := e.a.SetCheckinStreak(e.ctx, "root", pid, 5); err != nil { // 補登昨天
		t.Fatal(err)
	}
	if r := e.checkin(pid); r.Streak != 6 {
		t.Fatalf("%+v", r)
	}
	if err := e.a.SetCheckinStreak(e.ctx, "root", pid, 9); err != nil { // 今天已簽到：改今天那一筆
		t.Fatal(err)
	}
	if s := e.checkinStatus(pid); s.Streak != 9 {
		t.Fatalf("%+v", s)
	}
	if e.scalar("SELECT gold FROM daily_checkins WHERE streak = 5") != 0 { // 補登的不發獎
		t.Fatal("補登的簽到有發獎")
	}
}

func achievementStates(t *testing.T, e *env, pid int64) map[string]string {
	out := map[string]string{}
	for _, r := range must[AdminProgressView](t)(e.a.AdminProgress(e.ctx, pid)).Achievements {
		out[r.Code] = r.State
	}
	return out
}

func TestPlayerAchievementAndCodex(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	must[bool](t)(e.a.SetAchievementClaimed(e.ctx, "root", pid, "crops_100", true))
	if s := achievementStates(t, e, pid)["crops_100"]; s != "claimed" {
		t.Fatal(s)
	}
	must[bool](t)(e.a.SetAchievementClaimed(e.ctx, "root", pid, "crops_100", false))
	if s := achievementStates(t, e, pid)["crops_100"]; s != "progress" {
		t.Fatal(s)
	}
	must[bool](t)(e.a.SetCodexEntry(e.ctx, "root", pid, "wheat", 150, 2)) // 收成 150 個小麥 → 成就「初出茅廬」可領
	view := must[AdminProgressView](t)(e.a.AdminProgress(e.ctx, pid))
	var wheat *AdminCodexEntry
	for _, c := range view.Codex {
		for i, en := range c.Entries {
			if c.Key == "crop" && en.Code == "wheat" {
				wheat = &c.Entries[i]
			}
		}
	}
	if wheat == nil || !wheat.Got || wheat.Quantity != 150 || wheat.BestQuality != 2 {
		t.Fatalf("%+v", wheat)
	}
	if s := achievementStates(t, e, pid)["crops_100"]; s != "ready" {
		t.Fatal(s)
	}
	must[bool](t)(e.a.SetCodexEntry(e.ctx, "root", pid, "wheat", 0, 0))
	if e.scalar("SELECT count(*) FROM farm_codex WHERE item_code = 'wheat'") != 0 {
		t.Fatal("圖鑑沒有刪除")
	}
	_, err := e.a.SetCodexEntry(e.ctx, "root", pid, "fish_nope", 1, 0)
	wantKind(t, err, gameerr.NotFound)
}

func TestPlayerWeeklyPurchasesReset(t *testing.T) {
	e := setup(t)
	pid := e.progressFarmer()
	e.mustFeature("special_shop", shopConfig("normal_limit", 3))
	offer := must[SpecialShopView](t)(e.p.SpecialShop(e.ctx, pid)).Weekly.Offers[0]
	if offer.Limit != 3 {
		t.Fatalf("%+v", offer)
	}
	e.ok(e.p.BuyWeeklyDeal(e.ctx, pid, offer.CropCode, offer.Limit))
	_, err := e.p.BuyWeeklyDeal(e.ctx, pid, offer.CropCode, 1)
	wantMsg(t, err, gameerr.Validation, "限購")
	if p := must[AdminProgressView](t)(e.a.AdminProgress(e.ctx, pid)).Weekly.Purchases; p[0].Quantity != offer.Limit {
		t.Fatalf("%+v", p)
	}
	if n := must[int](t)(e.a.ResetWeeklyPurchases(e.ctx, "root", pid)); n != 1 {
		t.Fatal(n)
	}
	e.ok(e.p.BuyWeeklyDeal(e.ctx, pid, offer.CropCode, 1))
	if n := must[int](t)(e.a.ResetWeeklyPurchases(e.ctx, "root", pid)); n != 1 {
		t.Fatal(n)
	}
	if n := must[int](t)(e.a.ResetWeeklyPurchases(e.ctx, "root", pid)); n != 0 {
		t.Fatal(n)
	}
}

// Go 版新增：探險與地下菇房的設定存到 feature_settings 後立即生效，還原後回到預設值。
func TestExplorationAndMushroomSettings(t *testing.T) {
	e := setup(t)
	t.Cleanup(featurecfg.Reset)
	cfg, err := featurecfg.DeepCopy(featurecfg.Defaults(exploration.ConfigKey))
	if err != nil {
		t.Fatal(err)
	}
	cfg["fake_coin_value"] = 300
	cfg["areas"].(map[string]any)["forest"].(map[string]any)["unlock_level"] = 3
	if _, err := e.a.UpdateFeatureSettings(e.ctx, "root", exploration.ConfigKey, cfg); err != nil {
		t.Fatal(err)
	}
	if exploration.FakeCoinRate() != 300 || exploration.AreaOf("forest").UnlockLevel != 3 {
		t.Fatal(exploration.FakeCoinRate(), exploration.AreaOf("forest").UnlockLevel)
	}
	bad, _ := featurecfg.DeepCopy(cfg)
	bad["reward_types"].(map[string]any)["normal"].(map[string]any)["event"] = 0
	if _, err := e.a.UpdateFeatureSettings(e.ctx, "root", exploration.ConfigKey, bad); !gameerr.Is(err, gameerr.Validation) {
		t.Fatal("機率合計不是 100% 應該失敗", err)
	}
	m, _ := featurecfg.DeepCopy(featurecfg.Defaults(mushroom.ConfigKey))
	m["unlock_level"] = 1
	if _, err := e.a.UpdateFeatureSettings(e.ctx, "root", mushroom.ConfigKey, m); err != nil {
		t.Fatal(err)
	}
	if mushroom.Unlock() != 1 {
		t.Fatal(mushroom.Unlock())
	}
	view := must[FeatureSettingsView](t)(e.a.FeatureSettings(e.ctx))
	for _, k := range view.Keys {
		if (k.Key == exploration.ConfigKey || k.Key == mushroom.ConfigKey) && (k.IsDefault || k.UpdatedBy == nil) {
			t.Fatal(k.Key)
		}
	}
	for _, key := range []string{exploration.ConfigKey, mushroom.ConfigKey} {
		if _, err := e.a.ResetFeatureSettings(e.ctx, "root", key); err != nil {
			t.Fatal(err)
		}
	}
	if exploration.FakeCoinRate() != exploration.FakeCoinValue || mushroom.Unlock() != mushroom.UnlockLevel {
		t.Fatal("還原後沒有回到預設值")
	}
}
