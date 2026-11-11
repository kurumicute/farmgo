package services

// 對應 Python tests/test_home_group.py 與 tests/test_group_transfer.py
// （CMD、Migration 降版、任務計分、管理員轉移、群組排行等，對應服務移植時再補）。

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/repositories/repotest"
)

const cooldown = balance.GroupTransferCooldownSeconds

func (e *env) board(kind string, g, p *int64) []string {
	e.t.Helper()
	var out []string
	for _, r := range must[[]leaderboard.Row](e.t)(e.so.Leaderboard(e.ctx, kind, g, p, 10)) {
		out = append(out, r.PlayerName)
	}
	sort.Strings(out)
	return out
}

func (e *env) home(pid int64) *HomeGroup {
	e.t.Helper()
	return must[*HomeGroup](e.t)(e.s.GetHomeGroup(e.ctx, pid))
}

// legacyFarm 模擬 Migration 前就存在、尚未綁定群組的舊農場。
func (e *env) legacyFarm(name string) int64 {
	e.t.Helper()
	pid := must[PlayerInfo](e.t)(e.s.CreatePlayer(e.ctx, name, nil, nil)).ID
	e.exec(`WITH f AS (INSERT INTO farms (player_id, name, level, gold, warehouse_capacity) VALUES ($1, $2, 1, 300, 50) RETURNING id)
		INSERT INTO farm_plots (farm_id, plot_number) SELECT f.id, n FROM f, generate_series(1, $3::int) AS n`,
		pid, name+"的舊農場", balance.InitialPlots)
	return pid
}

// sqlErr 直接執行 SQL，回傳錯誤（測試資料庫觸發器）。
func (e *env) sqlErr(sql string, args ...any) error {
	return e.d.InTx(e.ctx, db.TxOptions{}, func(tx *db.Tx) error {
		_, err := tx.Exec(e.ctx, sql, args...)
		return err
	})
}

func TestFarmBelongsToCreationGroupOnly(t *testing.T) {
	e := setup(t)
	a, b := e.group("A 群"), e.group("B 群")
	alice := must[PlayerInfo](t)(e.s.CreatePlayer(e.ctx, "Alice", nil, nil)).ID
	if st := must[FarmStatus](t)(e.s.CreateFarm(e.ctx, alice, "", &a)); *st.HomeGroupID != a {
		t.Fatal(st.HomeGroupID)
	}
	if !must[bool](t)(e.so.IsMember(e.ctx, a, alice)) { // 建立時自動成為群組 A 成員
		t.Fatal("沒有加入群組")
	}
	bob := e.farmer("Bob", b)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice)) // 同一玩家加入群組 B
	if h := e.home(alice); h.ID != a || h.Name != "A 群" {
		t.Fatal(h)
	}
	e.grow(alice, 1, "wheat")
	e.clock.Advance(60)
	e.ok(e.s.HarvestCrop(e.ctx, alice, 1))
	e.ok(e.s.SellCrop(e.ctx, alice, "wheat", 2, "normal", "none"))
	for _, kind := range []string{"money", "sales"} {
		if !slices.Equal(e.board(kind, &a, &alice), []string{"Alice"}) || !slices.Equal(e.board(kind, &b, &bob), []string{"Bob"}) ||
			!slices.Equal(e.board(kind, nil, nil), []string{"Alice", "Bob"}) {
			t.Fatal(kind)
		}
	}
	if rows := must[[]leaderboard.Row](t)(e.so.Leaderboard(e.ctx, "sales", nil, nil, 10)); rows[0].SoldQuantity != 2 {
		t.Fatalf("%+v", rows[0])
	}
}

func TestCreateFarmRequiresExistingGroup(t *testing.T) {
	e := setup(t)
	pid := must[PlayerInfo](t)(e.s.CreatePlayer(e.ctx, "x", nil, nil)).ID
	_, err := e.s.CreateFarm(e.ctx, pid, "", nil)
	wantKind(t, err, gameerr.Validation)
	missing := int64(999)
	_, err = e.s.CreateFarm(e.ctx, pid, "", &missing)
	wantKind(t, err, gameerr.NotFound)
	if must[PlayerInfo](t)(e.s.GetPlayer(e.ctx, pid)).HasFarm {
		t.Fatal("建立了農場")
	}
}

func TestLeavingAndRejoiningHomeGroup(t *testing.T) {
	e := setup(t)
	a := e.group("A 群")
	alice, carol := e.farmer("Alice", a), e.farmer("Carol", a)
	must[bool](t)(e.so.RemoveMember(e.ctx, a, alice))
	if !slices.Equal(e.board("money", &a, &carol), []string{"Carol"}) || !slices.Contains(e.board("money", nil, nil), "Alice") ||
		e.gold(alice) != balance.InitialGold { // 總排行不受影響、農場與金幣保留
		t.Fatal("離開群組後的排行不對")
	}
	must[bool](t)(e.so.AddMember(e.ctx, a, alice))
	if !slices.Equal(e.board("money", &a, &carol), []string{"Alice", "Carol"}) {
		t.Fatal("重新加入後不在排行")
	}
}

func TestLegacyFarmOneTimeBinding(t *testing.T) {
	e := setup(t)
	a, b := e.group("A 群"), e.group("B 群")
	legacy := e.legacyFarm("Old")
	must[bool](t)(e.so.AddMember(e.ctx, a, legacy))
	anna := e.farmer("Anna", a)
	if e.home(legacy) != nil || !slices.Contains(e.board("money", nil, nil), "Old") ||
		!slices.Equal(e.board("money", &a, &anna), []string{"Anna"}) {
		t.Fatal("舊農場的狀態不對")
	}
	if r := e.ok(e.s.SetHomeGroup(e.ctx, legacy, a)); !strings.Contains(r.Message, "群組轉移") {
		t.Fatal(r.Message)
	}
	if h := e.home(legacy); h.ID != a || !slices.Equal(e.board("money", &a, &anna), []string{"Anna", "Old"}) {
		t.Fatal(h)
	}
	_, err := e.s.SetHomeGroup(e.ctx, legacy, b)
	wantMsg(t, err, gameerr.HomeGroupLocked, "操縱排行")
	if e.home(legacy).ID != a || e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'bind_group'") != 1 {
		t.Fatal("綁定被改")
	}
}

func TestConcurrentOneTimeBindingOnlyOnce(t *testing.T) {
	e := setup(t)
	a, b := e.group("A 群"), e.group("B 群")
	legacy := e.legacyFarm("Old")
	gs := []int64{a, b}
	res := runConcurrentlyIdx(2, func(i int) (ActionResult, error) { return e.s.SetHomeGroup(e.ctx, legacy, gs[i]) })
	if n := countOK(t, res, gameerr.HomeGroupLocked); n != 1 {
		t.Fatal(n)
	}
	winner := a
	if res[1].err == nil {
		winner = b
	}
	if e.home(legacy).ID != winner {
		t.Fatal("綁定的群組不對")
	}
}

func TestDatabaseTriggerBlocksChangingHomeGroup(t *testing.T) {
	e := setup(t)
	a, b := e.group("A 群"), e.group("B 群")
	pid := e.farmer("Alice", a)
	if err := e.sqlErr("UPDATE farms SET home_group_id = $2 WHERE player_id = $1", pid, b); !isIntegrity(err) ||
		!strings.Contains(err.Error(), "must record last_group_transfer_at") {
		t.Fatal(err)
	}
	if err := e.sqlErr("UPDATE farms SET home_group_id = NULL WHERE player_id = $1", pid); !isIntegrity(err) ||
		!strings.Contains(err.Error(), "cannot be cleared") {
		t.Fatal(err)
	}
	e.setGold(pid, 777) // 其他欄位照常可更新
	if e.home(pid).ID != a {
		t.Fatal("所屬群組被改")
	}
}

func TestContextOnlyRecordedForMemberGroups(t *testing.T) {
	e := setup(t)
	a, b := e.group("A 群"), e.group("B 群")
	alice := e.farmer("Alice", a)
	e.ok(e.s.InGroupContext(&b).BuySeeds(e.ctx, alice, "wheat", 1)) // 不是 B 成員
	e.ok(e.s.InGroupContext(&a).BuySeeds(e.ctx, alice, "wheat", 1))
	var ctxs []string
	repotestQuery(t, e, "SELECT coalesce(context_group_id::text, '-') FROM game_transactions WHERE event_type = 'buy_seed' ORDER BY id", &ctxs)
	if !slices.Equal(ctxs, []string{"-", strconvItoa64(a)}) {
		t.Fatal(ctxs)
	}
}

// ---------- 群組轉移（test_group_transfer.py） ----------

func (e *env) groups3() (int64, int64, int64) {
	return e.group("A 群"), e.group("B 群"), e.group("C 群")
}

func TestFirstTransferFreeThenSevenDayCooldown(t *testing.T) {
	e := setup(t)
	a, b, c := e.groups3()
	alice := e.farmer("Alice", a)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice))
	must[bool](t)(e.so.AddMember(e.ctx, c, alice))
	st := must[TransferView](t)(e.s.GroupTransferStatus(e.ctx, alice, &b))
	if !st.CanTransfer || !st.IsMember || *st.HomeGroupName != "A 群" {
		t.Fatalf("%+v", st)
	}
	if r := e.ok(e.s.TransferHomeGroup(e.ctx, alice, b)); !strings.Contains(r.Message, "轉移到「B 群」") || e.home(alice).ID != b {
		t.Fatal(r.Message)
	}
	e.clock.Advance(cooldown - 1)
	_, err := e.s.TransferHomeGroup(e.ctx, alice, c)
	wantMsg(t, err, gameerr.HomeGroupLocked, "冷卻中")
	if !strings.Contains(err.Error(), "下次可轉移時間") {
		t.Fatal(err)
	}
	if st := must[TransferView](t)(e.s.GroupTransferStatus(e.ctx, alice, &c)); st.CanTransfer || st.RemainingSeconds != 1 {
		t.Fatalf("%+v", st)
	}
	e.clock.Advance(1)
	e.ok(e.s.TransferHomeGroup(e.ctx, alice, c))
	var ctxs []string
	repotestQuery(t, e, "SELECT context_group_id::text FROM game_transactions WHERE event_type = 'transfer_group' ORDER BY id", &ctxs)
	if e.home(alice).ID != c || !slices.Equal(ctxs, []string{strconvItoa64(b), strconvItoa64(c)}) {
		t.Fatal(ctxs)
	}
}

func TestTransferRequiresMembershipAndDifferentGroup(t *testing.T) {
	e := setup(t)
	a, b, _ := e.groups3()
	alice := e.farmer("Alice", a)
	_, err := e.s.TransferHomeGroup(e.ctx, alice, b)
	wantKind(t, err, gameerr.NotGroupMember)
	_, err = e.s.TransferHomeGroup(e.ctx, alice, a)
	wantMsg(t, err, gameerr.Validation, "已經屬於")
	if e.home(alice).ID != a {
		t.Fatal("所屬群組被改")
	}
}

func TestTransferKeepsAssetsAndUpdatesRankings(t *testing.T) {
	e := setup(t)
	a, b, _ := e.groups3()
	alice, anna, bob := e.farmer("Alice", a), e.farmer("Anna", a), e.farmer("Bob", b)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice))
	e.grow(alice, 1, "wheat")
	e.ok(e.s.BuySeeds(e.ctx, alice, "carrot", 3))
	snap := func() string {
		return strings.Join([]string{
			strconvItoa64(int64(e.scalar("SELECT id FROM farms WHERE player_id = $1", alice))),
			strconvItoa64(int64(e.gold(alice))), strconvItoa64(int64(e.status(alice).Level)),
			strconvItoa64(int64(e.scalar("SELECT count(*) FROM farm_plots p JOIN farms f ON f.id = p.farm_id WHERE f.player_id = $1", alice))),
			strconvItoa64(int64(e.scalar("SELECT sum(i.quantity) FROM inventory_items i JOIN farms f ON f.id = i.farm_id WHERE f.player_id = $1", alice))),
		}, ",")
	}
	before := snap()
	if !slices.Equal(e.board("money", &a, &anna), []string{"Alice", "Anna"}) {
		t.Fatal("轉移前的排行不對")
	}
	e.ok(e.s.TransferHomeGroup(e.ctx, alice, b))
	if snap() != before || e.scalar("SELECT count(*) FROM farms") != 3 {
		t.Fatal("轉移改變了資產")
	}
	if !slices.Equal(e.board("money", &a, &anna), []string{"Anna"}) || !slices.Equal(e.board("money", &b, &bob), []string{"Alice", "Bob"}) ||
		!e.goldMatchesTransactions() {
		t.Fatal("轉移後的排行不對")
	}
}

func TestDatabaseTriggerEnforcesCooldown(t *testing.T) {
	e := setup(t)
	a, b, c := e.groups3()
	alice := e.farmer("Alice", a)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice))
	e.ok(e.s.TransferHomeGroup(e.ctx, alice, b))
	var last time.Time
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		return tx.QueryRow(ctx, "SELECT last_group_transfer_at FROM farms WHERE player_id = $1", alice).Scan(&last)
	})
	day := 24 * time.Hour
	cases := []struct {
		sql  string
		args []any
		want string
	}{
		{"UPDATE farms SET home_group_id = $2, last_group_transfer_at = $3 WHERE player_id = $1", []any{alice, c, last.Add(6 * day)}, "cooldown"},
		{"UPDATE farms SET home_group_id = $2, last_group_transfer_at = $3 WHERE player_id = $1", []any{alice, c, last.Add(3650 * day)}, "in the future"},
		{"UPDATE farms SET home_group_id = $2 WHERE player_id = $1", []any{alice, c}, "must record"},
		{"UPDATE farms SET last_group_transfer_at = NULL WHERE player_id = $1", []any{alice}, "without a transfer"},
		{"UPDATE farms SET last_group_transfer_at = $2 WHERE player_id = $1", []any{alice, last.Add(-30 * day)}, "without a transfer"},
	}
	for _, c := range cases {
		if err := e.sqlErr(c.sql, c.args...); !isIntegrity(err) || !strings.Contains(err.Error(), c.want) {
			t.Fatal(c.want, err)
		}
	}
	e.exec("UPDATE farms SET home_group_id = $2, last_group_transfer_at = $3 WHERE player_id = $1", alice, c, last.Add(7*day))
	if e.home(alice).ID != c {
		t.Fatal("冷卻後仍不能轉移")
	}
}

func TestConcurrentTransfersOnlyOneSucceeds(t *testing.T) {
	e := setup(t)
	a, b, c := e.groups3()
	alice := e.farmer("Alice", a)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice))
	must[bool](t)(e.so.AddMember(e.ctx, c, alice))
	gs := []int64{b, c}
	res := runConcurrentlyIdx(2, func(i int) (ActionResult, error) { return e.s.TransferHomeGroup(e.ctx, alice, gs[i]) })
	if n := countOK(t, res, gameerr.HomeGroupLocked); n != 1 {
		t.Fatal(n)
	}
	winner := b
	if res[1].err == nil {
		winner = c
	}
	if e.home(alice).ID != winner || e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'transfer_group'") != 1 {
		t.Fatal("轉移了兩次")
	}
}

func TestCooldownSurvivesRestart(t *testing.T) {
	e := setup(t)
	a, b, c := e.groups3()
	alice := e.farmer("Alice", a)
	must[bool](t)(e.so.AddMember(e.ctx, b, alice))
	must[bool](t)(e.so.AddMember(e.ctx, c, alice))
	e.ok(e.s.TransferHomeGroup(e.ctx, alice, b))
	restarted := restartedRanch(t, e)
	e.clock.Advance(3600)
	_, err := restarted.TransferHomeGroup(e.ctx, alice, c)
	wantKind(t, err, gameerr.HomeGroupLocked)
	if st := must[TransferView](t)(restarted.GroupTransferStatus(e.ctx, alice, nil)); st.RemainingSeconds != cooldown-3600 {
		t.Fatal(st.RemainingSeconds)
	}
}

func strconvItoa64(n int64) string { return strconv.FormatInt(n, 10) }
