package services

// 加速沙漏、種子庫與研究槽擴充、農場裝飾（Go 版新增，2026-11-15）。

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/decor"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/seedlab"
)

func TestHourglassOnLabAndCellar(t *testing.T) {
	e := setup(t)
	pid := e.labOwner("阿明", e.group("主群組"))
	e.s.LabRng = func() float64 { return 0.9 }
	if balance.ItemPrice(balance.ItemHourglass) != 500 {
		t.Fatal(balance.ItemPrice(balance.ItemHourglass))
	}
	gold := e.gold(pid)
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ItemHourglass, 3))
	if e.gold(pid) != gold-1500 || e.qty(pid, balance.ItemHourglass) != 3 {
		t.Fatal(e.gold(pid))
	}
	wheat := e.identify(pid, "wheat")
	_, err := e.s.UseHourglass(e.ctx, pid, "lab:1")
	wantMsg(t, err, gameerr.Validation, "是空的")
	e.ok(e.s.StartClone(e.ctx, pid, wheat.ID, 1, false)) // 60 分鐘
	e.clock.Advance((20 * time.Minute).Seconds())
	targets, have, err := e.s.HourglassTargets(e.ctx, pid)
	if err != nil || have != 3 || len(targets) != 1 || targets[0].Key != "lab:1" || targets[0].Remaining != 40*time.Minute {
		t.Fatalf("%+v %d %v", targets, have, err)
	}
	r := e.ok(e.s.UseHourglass(e.ctx, pid, "lab:1"))
	if !strings.Contains(r.Message, "剩餘 40 分 0 秒 → 20 分 0 秒") || e.qty(pid, balance.ItemHourglass) != 2 {
		t.Fatal(r.Message)
	}
	e.clock.Advance((20 * time.Minute).Seconds())
	if r := e.ok(e.s.CollectLab(e.ctx, pid)); !strings.Contains(r.Message, "育苗完成") {
		t.Fatal(r.Message)
	}
	_, err = e.s.UseHourglass(e.ctx, pid, "lab:1")
	wantMsg(t, err, gameerr.Validation, "是空的")
	if e.qty(pid, balance.ItemHourglass) != 2 {
		t.Fatal("失敗時沙漏不扣")
	}

	// 地下菇房：所有生長中的菇床剩餘時間減半
	e.exec("UPDATE farms SET level = 5 WHERE player_id = $1", pid)
	v := e.cellar(pid)
	if v.Hourglasses != 2 || v.NextReadyAt == nil {
		t.Fatalf("%+v", v)
	}
	left := v.NextReadyAt.Sub(e.clock.Now())
	e.ok(e.s.UseHourglass(e.ctx, pid, "cellar"))
	v = e.cellar(pid)
	if got := v.NextReadyAt.Sub(e.clock.Now()); got != (left / 2).Round(time.Second) {
		t.Fatal(left, got)
	}
	e.clock.Advance(float64(mushroom.Grow()))
	_, err = e.s.UseHourglass(e.ctx, pid, "cellar")
	wantMsg(t, err, gameerr.Validation, "不需要加速沙漏")
	e.invariant()
}

func TestSeedLibraryAndLabSlotExpansion(t *testing.T) {
	e := setup(t)
	pid := e.labOwner("阿明", e.group("主群組"))
	base := seedlab.Current().LibraryCapacity
	for i, price := range []int{10_000, 20_000, 40_000, 80_000} {
		v := e.lab(pid)
		if v.NextLibraryPrice != price || v.Capacity != base+10*i {
			t.Fatalf("第 %d 次：%+v", i+1, v)
		}
		gold := e.gold(pid)
		e.ok(e.s.ExpandSeedLibrary(e.ctx, pid))
		if e.gold(pid) != gold-price {
			t.Fatal(e.gold(pid))
		}
	}
	if v := e.lab(pid); v.Capacity != base+40 || v.NextLibraryPrice != 0 {
		t.Fatalf("%+v", v)
	}
	_, err := e.s.ExpandSeedLibrary(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "最大")
	// 擴充後的容量真的能放：先放到 base+39 顆，再鑑定 1 顆剛好滿
	e.exec(`INSERT INTO lab_seeds (farm_id, breeder_farm_id, crop_code, gene_growth, gene_yield, gene_quality, gene_mutation)
		SELECT id, id, 'wheat', 1, 1, 1, 1 FROM farms, generate_series(1, $2) WHERE player_id = $1`, pid, base+39)
	e.identify(pid, "wheat")
	e.give(pid, "seed_wheat", 1)
	_, err = e.s.IdentifySeed(e.ctx, pid, "wheat")
	wantMsg(t, err, gameerr.Validation, "種子庫已滿")

	// 研究槽：研究 Lv.3 才能買，預設只有 1 個（10 萬）
	_, err = e.s.BuyLabSlot(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "Lv.3")
	if v := e.lab(pid); v.NextSlotPrice != 0 {
		t.Fatalf("%+v", v)
	}
	e.exec("UPDATE seed_labs SET level = 3 WHERE farm_id = (SELECT id FROM farms WHERE player_id = $1)", pid)
	if v := e.lab(pid); v.Slots != 3 || v.NextSlotPrice != 100_000 {
		t.Fatalf("%+v", v)
	}
	r := e.ok(e.s.BuyLabSlot(e.ctx, pid))
	if !strings.Contains(r.Message, "3 → **4** 個") {
		t.Fatal(r.Message)
	}
	if v := e.lab(pid); v.Slots != 4 || v.FreeSlots != 4 || v.NextSlotPrice != 0 {
		t.Fatalf("%+v", v)
	}
	_, err = e.s.BuyLabSlot(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "最多")
	e.invariant()
}

func TestDecorations(t *testing.T) {
	e := setup(t)
	pid := e.player()
	if st := e.status(pid); st.Decor != nil {
		t.Fatal(st.Decor)
	}
	v := must[DecorView](t)(e.s.Decorations(e.ctx, pid))
	if len(v.Items) != len(decor.Catalog) || v.Items[0].Owned {
		t.Fatalf("%+v", v)
	}
	_, err := e.s.BuyDecor(e.ctx, pid, "scarecrow")
	wantKind(t, err, gameerr.InsufficientGold)
	_, err = e.s.BuyDecor(e.ctx, pid, "ufo")
	wantKind(t, err, gameerr.NotFound)
	e.setGoldRecorded(pid, 500_000)
	r := e.ok(e.s.BuyDecor(e.ctx, pid, "scarecrow"))
	if !strings.Contains(r.Message, "稻草人") || !strings.Contains(r.Message, "擺在庭院的左上") || r.GoldDelta != -20_000 {
		t.Fatalf("%+v", r)
	}
	_, err = e.s.BuyDecor(e.ctx, pid, "scarecrow")
	wantKind(t, err, gameerr.AlreadyExists)
	e.ok(e.s.BuyDecor(e.ctx, pid, "fountain"))
	if st := e.status(pid); strings.Join(st.Decor, ",") != "scarecrow,fountain,,,," {
		t.Fatal(st.Decor)
	}
	// 噴水池搬到 1 號：原本在 1 號的稻草人收起來
	e.ok(e.s.PlaceDecor(e.ctx, pid, 1, "fountain"))
	if st := e.status(pid); strings.Join(st.Decor, ",") != "fountain,,,,," {
		t.Fatal(st.Decor)
	}
	e.ok(e.s.PlaceDecor(e.ctx, pid, 6, "scarecrow"))
	v = must[DecorView](t)(e.s.Decorations(e.ctx, pid))
	if v.Placed[0] != "fountain" || v.Placed[5] != "scarecrow" {
		t.Fatalf("%+v", v.Placed)
	}
	_, err = e.s.PlaceDecor(e.ctx, pid, 2, "gazebo")
	wantMsg(t, err, gameerr.Validation, "還沒有小涼亭")
	_, err = e.s.PlaceDecor(e.ctx, pid, 7, "scarecrow")
	wantKind(t, err, gameerr.Validation)
	e.ok(e.s.PlaceDecor(e.ctx, pid, 1, ""))
	e.ok(e.s.PlaceDecor(e.ctx, pid, 6, ""))
	if st := e.status(pid); st.Decor != nil {
		t.Fatal(st.Decor)
	}
	// 擺飾區 6 格都滿了：新買的收起來
	for _, c := range []string{"barrel", "crate", "bush", "tree", "pine", "lamp"} {
		e.ok(e.s.BuyDecor(e.ctx, pid, c))
	}
	if r := e.ok(e.s.BuyDecor(e.ctx, pid, "sakura")); !strings.Contains(r.Message, "擺飾區滿了") {
		t.Fatal(r.Message)
	}
	e.invariant()
}
