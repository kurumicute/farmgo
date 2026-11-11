package fishing

import (
	"encoding/json"
	"testing"
	"time"

	"farmgm/internal/db"
	gfishing "farmgm/internal/game/fishing"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestSettingsVersions(t *testing.T) {
	d := setup(t)
	inTx(t, d, func(tx *db.Tx) error {
		v, raw, err := CurrentSettings(ctx, tx)
		if err != nil || v != 0 {
			t.Fatal(v, err)
		}
		var s gfishing.Settings
		if err := json.Unmarshal(raw, &s); err != nil || s.BasketCapacity != gfishing.DefaultSettings().BasketCapacity {
			t.Fatal(s, err)
		}
		s.BasketCapacity = 33
		if v, err = AddSettingsVersion(ctx, tx, s, "admin", t0); err != nil || v != 1 {
			t.Fatal(v, err)
		}
		v, raw, _ = CurrentSettings(ctx, tx)
		_ = json.Unmarshal(raw, &s)
		if v != 1 || s.BasketCapacity != 33 {
			t.Fatal(v, s)
		}
		if h, _ := SettingsHistory(ctx, tx, 10); len(h) != 1 {
			t.Fatal(h)
		}
		return nil
	})
}

func TestCatalogGearAndSessions(t *testing.T) {
	d := setup(t)
	inTx(t, d, func(tx *db.Tx) error {
		_, fid := mkFarm(t, tx, "甲", nil)
		all, err := ListFishTypes(ctx, tx, false)
		if err != nil || len(all) == 0 {
			t.Fatal(all, err)
		}
		active, _ := ListFishTypes(ctx, tx, true)
		if len(active) == 0 || len(active) > len(all) {
			t.Fatal(len(active))
		}
		ft, err := GetFishType(ctx, tx, all[0].Code, true)
		if err != nil || ft == nil || ft.Code != all[0].Code {
			t.Fatal(ft, err)
		}
		names, _ := FishTypeNames(ctx, tx)
		if !names[all[0].Name] {
			t.Fatal(names)
		}
		gear, err := ListGear(ctx, tx, true)
		if err != nil || len(gear) == 0 || gear[0].Kind != "rod" || gear[len(gear)-1].Kind != "bait" {
			t.Fatal(gear, err)
		}
		rod, _ := GetGear(ctx, tx, gear[0].Code, false)
		owned, err := AddOwned(ctx, tx, fid, *rod, 1, t0, true)
		if err != nil || owned.ID == 0 {
			t.Fatal(owned, err)
		}
		eq, err := EquippedGear(ctx, tx, fid)
		if err != nil || eq["rod"].Gear.Code != rod.Code || eq["rod"].Owned.FarmID != fid {
			t.Fatal(eq, err)
		}
		if err := UnequipKind(ctx, tx, fid, "rod"); err != nil {
			return err
		}
		if eq, _ := EquippedGear(ctx, tx, fid); len(eq) != 0 {
			t.Fatal(eq)
		}
		if o, _ := GetOwned(ctx, tx, fid, rod.Code, true); o == nil || o.Equipped {
			t.Fatal(o)
		}
		if o, _ := OwnedGear(ctx, tx, fid); len(o) != 1 {
			t.Fatal(o)
		}
		// Session：state 保留未知欄位
		state := `{"turn": 0, "future_field": "keep"}`
		var sid int64
		if err := tx.QueryRow(ctx, "INSERT INTO fishing_sessions (farm_id, status, settings_version, settings, gear, fish_code, "+
			"rarity, weight_g, price, state, started_at, bite_at, react_deadline) VALUES ($1, 'casting', 0, '{}', $2, $3, "+
			"'common', 100, 10, $4, $5, $6, $7) RETURNING id", fid, json.RawMessage(`{"rod": {"name": "竹竿"}}`), ft.Code,
			json.RawMessage(state), t0.Add(-time.Second), t0, t0.Add(time.Minute)).Scan(&sid); err != nil {
			return err
		}
		s, err := ActiveSession(ctx, tx, fid, true)
		if err != nil || s == nil || s.ID != sid {
			t.Fatal(s, err)
		}
		var m map[string]any
		if err := json.Unmarshal(s.State, &m); err != nil || m["future_field"] != "keep" {
			t.Fatal(string(s.State), err)
		}
		if last, _ := LastSession(ctx, tx, fid); last == nil || last.ID != sid {
			t.Fatal(last)
		}
		stale, err := StaleActiveSessions(ctx, tx, t0.Add(2*time.Minute), 500)
		if err != nil || len(stale) != 1 {
			t.Fatal(stale, err)
		}
		if stale, _ := StaleActiveSessions(ctx, tx, t0, 500); len(stale) != 0 {
			t.Fatal(stale)
		}
		if _, err := tx.Exec(ctx, "UPDATE fishing_sessions SET status = 'landed', ended_at = $2 WHERE id = $1", sid, t0); err != nil {
			return err
		}
		if st, _ := PlayerSessionStats(ctx, tx, fid); st.Sessions != 1 || st.Landed != 1 {
			t.Fatal(st)
		}
		// 魚簍
		var fishID int64
		if err := tx.QueryRow(ctx, "INSERT INTO farm_fish (farm_id, session_id, fish_code, rarity, weight_g, price, caught_at) "+
			"VALUES ($1, $2, $3, 'legendary', 1200, 500, $4) RETURNING id", fid, sid, ft.Code, t0).Scan(&fishID); err != nil {
			return err
		}
		if f, _ := SessionFish(ctx, tx, sid); f == nil || f.ID != fishID {
			t.Fatal(f)
		}
		b, err := Basket(ctx, tx, fid)
		if err != nil || len(b) != 1 || b[0].Type.Code != ft.Code {
			t.Fatal(b, err)
		}
		if n, _ := BasketCount(ctx, tx, fid); n != 1 {
			t.Fatal(n)
		}
		if f, _ := GetFarmFish(ctx, tx, fid+1, fishID); f != nil {
			t.Fatal("other farm can read fish")
		}
		if f, _ := GetFarmFish(ctx, tx, fid, fishID); f == nil {
			t.Fatal("own fish missing")
		}
		if l, _ := LockUnsold(ctx, tx, fid, nil); len(l) != 1 {
			t.Fatal(l)
		}
		if l, _ := LockUnsold(ctx, tx, fid, []int64{fishID + 100}); len(l) != 0 {
			t.Fatal(l)
		}
		if w, _ := BestWeights(ctx, tx, fid); w[ft.Code] != 1200 {
			t.Fatal(w)
		}
		if c, _ := Codex(ctx, tx, fid); c[ft.Code].Count != 1 || !c[ft.Code].FirstCaughtAt.Equal(t0) {
			t.Fatal(c)
		}
		if at, _ := LastShareAt(ctx, tx, fishID); at != nil {
			t.Fatal(at)
		}
		if _, err := AddShare(ctx, tx, fishID, fid, nil, t0); err != nil {
			return err
		}
		if at, _ := LastShareAt(ctx, tx, fishID); at == nil || !at.Equal(t0) {
			t.Fatal(at)
		}
		lb, err := LeaderboardOf(ctx, tx, nil, 10)
		if err != nil || len(lb.Catches) != 1 || lb.Catches[0].Legendary != 1 || len(lb.Best) != 1 || lb.Best[0].Price != 500 {
			t.Fatal(lb, err)
		}
		gid := mkGroup(t, tx, "群組")
		if lb, _ := LeaderboardOf(ctx, tx, &gid, 10); len(lb.Catches) != 0 || len(lb.Best) != 0 {
			t.Fatal(lb)
		}
		ov, err := OverviewOf(ctx, tx, t0)
		if err != nil || ov.Sessions != 1 || ov.SuccessRate == nil || *ov.SuccessRate != 100 || ov.InBaskets != 1 ||
			ov.Shares != 1 || ov.ByStatus["landed"] != 1 || ov.ByRarity["common"].Landed != 1 {
			t.Fatal(ov, err)
		}
		total, rows, err := PlayerRows(ctx, tx, "", 20, 0)
		if err != nil || total != 1 || len(rows) != 1 || rows[0].Sessions != 1 || rows[0].Equipped == nil {
			t.Fatal(total, rows, err)
		}
		if total, _, _ := PlayerRows(ctx, tx, "甲", 20, 0); total != 1 {
			t.Fatal(total)
		}
		if total, _, _ := PlayerRows(ctx, tx, "#1", 20, 0); total != 1 {
			t.Fatal(total)
		}
		if total, _, _ := PlayerRows(ctx, tx, "%", 20, 0); total != 0 {
			t.Fatal("LIKE not escaped")
		}
		det, err := PlayerDetail(ctx, tx, fid, 30)
		if err != nil || len(det.Catches) != 1 || len(det.Sessions) != 1 || det.Sessions[0].Rod == nil ||
			*det.Sessions[0].Rod != "竹竿" || det.Sessions[0].Line != nil || len(det.Shares) != 1 || len(det.Gear) != 1 {
			t.Fatal(det, err)
		}
		if u, _ := GearUsage(ctx, tx); u[rod.Code] != 1 {
			t.Fatal(u)
		}
		if c, _ := FishCounts(ctx, tx); c[ft.Code] != 1 {
			t.Fatal(c)
		}
		if g, _ := GetGuard(ctx, tx, fid); g != nil {
			t.Fatal(g)
		}
		return nil
	})
}
