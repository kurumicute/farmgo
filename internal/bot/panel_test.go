package bot

// 對應 Python tests/test_discord_panel.py（互動式農場面板）：以模擬的互動物件點按鈕、選選單、填對話框（未連到真實 Discord）。
// 本檔是第一批頁面（歡迎、總覽、農田、背包、說明）與面板框架的測試；其他頁面的測試隨頁面移植。

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/bot/ui/uitest"
	"farmgm/internal/clock"
	"farmgm/internal/config"
	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/services"
	"farmgm/internal/services/svctest"

	"testing/fstest"
)

const (
	guildA = "111111111111111111"
	guildB = "222222222222222222"
	alice  = "900000000000000001"
	bob    = "900000000000000002"
)

type botEnv struct {
	t     *testing.T
	ctx   context.Context
	d     *db.DB
	clock *clock.Manual
	admin *services.AdminService
	cmds  *Commands
	last  *Panel
}

func setup(t *testing.T) *botEnv {
	t.Helper()
	d := svctest.Prepare(t, services.ClearNewsCache)
	c := clock.NewManual(time.Time{})
	admin := services.NewAdminService(d, services.Options{Clock: c})
	reg := ui.NewRegistry()
	reg.DirectReply = -1 // 測試預設不自動 defer（需要時個別設定）
	e := &botEnv{t: t, ctx: context.Background(), d: d, clock: c, admin: admin}
	e.cmds = NewCommands(admin, NewRunner(4), reg)
	e.cmds.AutoCheckin = false // Python conftest no_auto_checkin
	e.cmds.onPanel = func(p *Panel) { e.last = p }
	return e
}

// player 一位玩家開著的面板（Python Player）；Text 為目前面板內容（含頂端的操作結果）。
type player struct {
	e      *botEnv
	user   string
	guild  string
	name   string
	panel  *Panel
	Text   string
	opened *uitest.Interaction
}

func (e *botEnv) open(user, guild, name string) *player {
	e.t.Helper()
	pl := &player{e: e, user: user, guild: guild, name: name}
	first := pl.interaction()
	e.cmds.OpenPanel(e.ctx, first)
	var sent *uitest.Entry
	for _, en := range first.Entries() {
		if en.Kind == "followup" {
			sent = &en
		}
	}
	if sent == nil || !sent.Message.Ephemeral {
		e.t.Fatalf("沒有送出私密面板：%v", first.Kinds())
	}
	pl.panel, pl.opened = e.last, first
	pl.Text = uitest.EmbedText(sent.Message.Embed)
	return pl
}

// makePlayer 開面板並按「建立農場」（Python make_player）。
func (e *botEnv) makePlayer(user, guild, name string) *player {
	e.t.Helper()
	p := e.open(user, guild, name)
	if !strings.Contains(p.Text, "歡迎來到農場") {
		e.t.Fatal(p.Text)
	}
	p.click("建立農場")
	for _, want := range []string{"農場建立完成", "Lv 1", "農田（4 格）"} {
		if !strings.Contains(p.Text, want) {
			e.t.Fatalf("缺少 %q：%s", want, p.Text)
		}
	}
	return p
}

func (p *player) interaction() *uitest.Interaction {
	it := uitest.New(p.e.t, p.e.cmds.Registry, p.user, p.guild)
	it.Name = p.name
	return it
}

func (p *player) update(it *uitest.Interaction) *uitest.Interaction {
	for _, en := range it.Entries() {
		if en.Kind == "edit" && en.Message.Embed != nil {
			p.Text = uitest.EmbedText(en.Message.Embed)
		}
	}
	return it
}

func (p *player) labels() []string {
	var out []string
	for _, b := range p.panel.View.Buttons() {
		out = append(out, b.Label)
	}
	return out
}

func (p *player) button(label string) *ui.Button {
	p.e.t.Helper()
	for _, b := range p.panel.View.Buttons() {
		if strings.Contains(b.Label, label) {
			return b
		}
	}
	p.e.t.Fatalf("找不到按鈕「%s」：%v", label, p.labels())
	return nil
}

func (p *player) click(label string) *uitest.Interaction {
	p.e.t.Helper()
	b := p.button(label)
	it := p.interaction()
	if !p.e.cmds.Registry.DispatchComponent(p.e.ctx, it, b.CustomID(), nil) {
		p.e.t.Fatalf("按鈕「%s」沒有登記", label)
	}
	return p.update(it)
}

func (p *player) selectMenu(placeholder string) *ui.Select {
	for _, s := range p.panel.View.Selects() {
		if strings.Contains(s.Placeholder, placeholder) {
			return s
		}
	}
	return nil
}

func (p *player) choose(placeholder, value string) *uitest.Interaction {
	p.e.t.Helper()
	s := p.selectMenu(placeholder)
	if s == nil {
		p.e.t.Fatalf("找不到選單「%s」", placeholder)
	}
	it := p.interaction()
	if !p.e.cmds.Registry.DispatchComponent(p.e.ctx, it, s.CustomID(), []string{value}) {
		p.e.t.Fatalf("選單「%s」沒有登記", placeholder)
	}
	return p.update(it)
}

func (p *player) options(placeholder string) []string {
	s := p.selectMenu(placeholder)
	if s == nil {
		return nil
	}
	var out []string
	for _, o := range s.Options {
		out = append(out, o.Value)
	}
	return out
}

// modal 按下會開啟輸入框的按鈕，填入數值並送出。
func (p *player) modal(label, value string) *uitest.Interaction {
	p.e.t.Helper()
	opener := p.click(label)
	var m *ui.Modal
	for _, en := range opener.Entries() {
		if en.Kind == "modal" {
			m = en.Modal
		}
	}
	if m == nil {
		p.e.t.Fatalf("「%s」沒有開啟輸入框：%v", label, opener.Kinds())
	}
	it := p.interaction()
	p.e.cmds.Registry.DispatchModal(p.e.ctx, it, m.CustomID(), []string{value})
	return p.update(it)
}

// goPage 直接換頁（Python p.go；經 Registry 依序執行，與點擊相同會自動 defer）。
func (p *player) goPage(page string) *uitest.Interaction {
	return p.goWith(page, nil)
}

func (p *player) goWith(page string, attached []string) *uitest.Interaction {
	it := p.interaction()
	it.Attached = attached
	p.e.cmds.Registry.RunFor(p.e.ctx, it, p.panel.View, func(ctx context.Context) error {
		p.panel.goTo(ctx, it, page, "")
		return nil
	})
	return p.update(it)
}

func lastEdit(t *testing.T, it *uitest.Interaction) uitest.Entry {
	t.Helper()
	var out *uitest.Entry
	for _, en := range it.Entries() {
		if en.Kind == "edit" {
			out = &en
		}
	}
	if out == nil {
		t.Fatalf("沒有 edit：%v", it.Kinds())
	}
	return *out
}

func attachmentNames(en uitest.Entry) []string {
	if en.Message.Attachments == nil {
		return nil
	}
	var out []string
	for _, f := range en.Message.Attachments.Files {
		out = append(out, f.Name)
	}
	return out
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func contains(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Fatalf("缺少 %q：\n%s", w, text)
		}
	}
}

func (e *botEnv) exec(sql string, args ...any) {
	e.t.Helper()
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

func (e *botEnv) scalar(sql string, args ...any) any {
	e.t.Helper()
	var v any
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&v)
	})
	return v
}

// ---------- 測試 ----------

func TestDefaultRegistrationIsShort(t *testing.T) {
	var names []string
	for _, s := range Specs() {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"farm", "help"}) {
		t.Fatal(names)
	}
}

func TestWelcomeInDMAndNamedCreation(t *testing.T) {
	e := setup(t)
	dm := e.open(alice, "", "玩家")
	if !strings.Contains(dm.Text, "請到 Discord 伺服器") || slices.Contains(dm.labels(), "建立農場") {
		t.Fatal(dm.Text, dm.labels())
	}
	p := e.open(alice, guildA, "玩家")
	p.modal("自訂名稱建立", "開心農場") // NameModal 只有一個欄位
	contains(t, p.Text, "開心農場")
	if name := e.scalar("SELECT name FROM farms"); name != "開心農場" {
		t.Fatal(name)
	}
}

// expiredInteraction defer 失敗（互動已超過 Discord 的 3 秒期限：10062 Unknown interaction）。
type expiredInteraction struct{ *uitest.Interaction }

func (expiredInteraction) Defer(context.Context, bool) error {
	return errors.New("10062 Unknown interaction")
}

func TestOpenPanelExpiredInteractionIsIgnored(t *testing.T) {
	e := setup(t)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	it := expiredInteraction{uitest.New(t, e.cmds.Registry, alice, guildA)}
	e.cmds.OpenPanel(e.ctx, it)
	if !strings.Contains(buf.String(), "開啟面板逾時") || slices.Contains(it.Kinds(), "followup") {
		t.Fatal(buf.String(), it.Kinds())
	}
}

func TestRenameFarmFromHelpPage(t *testing.T) {
	// 說明頁的「更改農場名稱」：每次 300 金幣、記錄交易；金幣不足時按鈕停用。
	e := setup(t)
	cost := balance.RenameFarmCost
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	gold := 1000
	must[services.FarmValues](t)(e.admin.SetFarmValues(e.ctx, "test", pid, &gold, nil))
	shown := p.click("說明")
	embed := lastEdit(t, shown).Message.Embed
	if embed.Len() > 6000 || len(embed.Fields) > 25 { // Discord Embed 上限
		t.Fatal(embed.Len(), len(embed.Fields))
	}
	contains(t, p.Text, "怎麼玩", "目前：**阿明的農場**")
	contains(t, strings.Join(p.labels(), " "), "回總覽")
	p.modal("更改農場名稱", "  星空牧場  ")
	contains(t, p.Text, "已從「阿明的農場」改為「星空牧場」", "星空牧場")
	if n := e.scalar("SELECT count(*) FROM farms WHERE name = '星空牧場' AND gold = $1", 1000-cost); n != int64(1) {
		t.Fatal(n)
	}
	if n := e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'rename_farm' AND gold_delta = $1 AND description LIKE '%星空牧場%'", -cost); n != int64(1) {
		t.Fatal(n)
	}
	p.click("回總覽")
	contains(t, p.Text, "星空牧場")
	low := cost - 1
	must[services.FarmValues](t)(e.admin.SetFarmValues(e.ctx, "test", pid, &low, nil))
	p.click("說明")
	if !p.button("更改農場名稱").Disabled {
		t.Fatal("金幣不足時應停用")
	}
}

func TestOnlyOwnerCanUsePanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	intruder := uitest.New(t, e.cmds.Registry, bob, guildA)
	e.cmds.Registry.DispatchComponent(e.ctx, intruder, p.button("重新整理").CustomID(), nil)
	contains(t, intruder.Text(), "別人的農場面板")
	if !intruder.Last().Message.Ephemeral {
		t.Fatal("應為私密訊息")
	}
}

type fakeMessage struct{ edits []ui.Message }

func (m *fakeMessage) Edit(_ context.Context, msg ui.Message) error {
	m.edits = append(m.edits, msg)
	return nil
}

func TestTimeoutDisablesPanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	m := &fakeMessage{}
	p.panel.message = m
	p.panel.onTimeout(e.ctx)
	for _, it := range p.panel.View.Items() {
		switch x := it.(type) {
		case *ui.Button:
			if !x.Disabled {
				t.Fatal(x.Label)
			}
		case *ui.Select:
			if !x.Disabled {
				t.Fatal(x.Placeholder)
			}
		}
	}
	if len(m.edits) == 0 || !strings.Contains(*m.edits[len(m.edits)-1].Content, "逾時") {
		t.Fatal(m.edits)
	}
}

func TestLargeFarmFitsDiscordLimits(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	// 上限 16 格之前建立的舊農場：直接加到 100 格（服務層不允許）
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		_, err := farms.AddPlots(ctx, tx, e.scalar("SELECT id FROM farms").(int64), 96)
		return err
	})
	for _, page := range []string{"plots", "main"} {
		embed := lastEdit(t, p.goPage(page)).Message.Embed
		if embed.Len() > 6000 || len([]rune(embed.Description)) > 4096 {
			t.Fatal(page, embed.Len())
		}
		for _, f := range embed.Fields {
			if len([]rune(f.Value)) > 1024 {
				t.Fatal(page, f.Name)
			}
		}
	}
}

func TestFarmingLoopWithButtons(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("plots")
	if !slices.Contains(p.options("選擇作物"), "wheat") {
		t.Fatal(p.options("選擇作物"))
	}
	p.choose("選擇作物", "wheat") // 種子不夠會自動購買，種滿 4 格空地
	contains(t, p.Text, "在 4 格種下小麥（購買 4 顆種子）", "💧")
	p.click("全部澆水")
	contains(t, p.Text, "已為 4 格澆水", "成熟 <t:")
	e.clock.Advance(61)
	p.goPage("main")
	contains(t, p.Text, "4 格可以收成")
	p.click("全部收成")
	contains(t, p.Text, "收成 4 格")
	p.goPage("sell")
	contains(t, p.Text, "**小麥**　×**8**")
	p.click("全部賣出")
	contains(t, p.Text, "再按一次確認")
	p.click("確認全部賣出")
	contains(t, p.Text, "賣出 8 個商品")
	if gold, delta := e.scalar("SELECT sum(gold)::bigint FROM farms"), e.scalar("SELECT sum(gold_delta)::bigint FROM game_transactions"); gold != delta {
		t.Fatal(gold, delta)
	}
}

func TestOverviewListsCropsAndTimes(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("plots")
	p.choose("選擇作物", "wheat")
	p.click("全部澆水")
	p.goPage("main")
	if strings.Count(p.Text, "小麥") < 4 {
		t.Fatal(p.Text)
	}
	contains(t, p.Text, "成熟 <t:", "📊 農田統計")
}

func TestPagesAttachImages(t *testing.T) {
	// Python test_pages_attach_images 中已移植的頁面（其他頁面隨後續批次加入）
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	for _, c := range []struct {
		page     string
		expected int
	}{{"main", 1}, {"plots", 1}, {"shop", 1}, {"animals", 1}, {"craft", 1}, {"build", 1}, {"casino", 1}, {"backpack", 0}, {"help", 0}} {
		en := lastEdit(t, p.goPage(c.page))
		names := attachmentNames(en)
		if len(names) != c.expected || (c.expected == 1 && names[0] != FarmImage) {
			t.Fatal(c.page, names)
		}
		if (en.Message.Embed.Image == "attachment://"+FarmImage) != (c.expected == 1) {
			t.Fatal(c.page, en.Message.Embed.Image)
		}
	}
}

func TestFastPagesReplyDirectlyWithoutDefer(t *testing.T) {
	// 準備得夠快時直接以新畫面回覆互動，不先 defer（少一次和 Discord 的往返）。
	e := setup(t)
	e.cmds.Registry.DirectReply = ui.DefaultDirectReply
	p := e.makePlayer(alice, guildA, "玩家")
	for _, action := range []func() *uitest.Interaction{func() *uitest.Interaction { return p.goPage("plots") },
		func() *uitest.Interaction { return p.goPage("main") }, func() *uitest.Interaction { return p.click("背包") }} {
		it := action()
		if k := it.Kinds(); !slices.Equal(k, []string{"edit"}) || !lastEdit(t, it).Direct {
			t.Fatal(k)
		}
	}
}

func TestSlowPageDefersFirst(t *testing.T) {
	// 讀取超過直接回覆的時限時先 defer，避免超過 Discord 3 秒的首次回覆期限，再編輯原訊息。
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.cmds.Registry.DirectReply = 50 * time.Millisecond
	real := p.panel.pages["backpack"]
	p.panel.pages["backpack"] = pageDef{func(ctx context.Context) (any, error) {
		time.Sleep(300 * time.Millisecond)
		return real.load(ctx)
	}, real.build}
	it := p.goPage("backpack")
	if k := it.Kinds(); !slices.Equal(k, []string{"defer", "edit"}) || lastEdit(t, it).Direct {
		t.Fatal(k)
	}
	contains(t, p.Text, "背包")
}

func TestSlowActionSharesReplyBudget(t *testing.T) {
	// 操作本身用掉直接回覆的時間後，重新讀取頁面前就先 defer（兩段不各自重新計時）；失敗的錯誤畫面可以直接回覆。
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.cmds.Registry.DirectReply = 200 * time.Millisecond
	it := p.interaction()
	e.cmds.Registry.RunFor(e.ctx, it, p.panel.View, func(ctx context.Context) error {
		p.panel.act(ctx, it, func(context.Context) (string, error) { time.Sleep(250 * time.Millisecond); return "做完了", nil }, actOpts{})
		return nil
	})
	if k := it.Kinds(); !slices.Equal(k, []string{"defer", "edit"}) {
		t.Fatal(k)
	}
	p.update(it)
	contains(t, p.Text, "做完了")
	e.cmds.Registry.DirectReply = ui.DefaultDirectReply
	failing := p.interaction()
	e.cmds.Registry.RunFor(e.ctx, failing, p.panel.View, func(ctx context.Context) error {
		p.panel.act(ctx, failing, func(context.Context) (string, error) { return "", errorsValidation("不行") }, actOpts{})
		return nil
	})
	if k := failing.Kinds(); !slices.Equal(k, []string{"edit"}) || !strings.Contains(uitest.EmbedText(lastEdit(t, failing).Message.Embed), "不行") {
		t.Fatal(k)
	}
	// 錯誤畫面的「返回」回到剛才的頁面
	p.update(failing)
	p.click("返回")
	contains(t, p.Text, "農田（4 格）")
}

func TestUnchangedImageReusesExistingAttachment(t *testing.T) {
	// 同一張圖沿用訊息上已有的附件，不重新上傳；換到沒圖的頁面才移除，之後重新上傳。
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	onMessage := []string{FarmImage}
	att := func(page string) *ui.Attachments { return lastEdit(t, p.goWith(page, onMessage)).Message.Attachments }
	if a := att("backpack"); a.Keep || len(a.Files) != 0 { // 沒有圖的頁面：移除
		t.Fatalf("%+v", a)
	}
	if a := att("plots"); a.Keep || len(a.Files) != 1 { // 訊息上沒有圖：上傳新圖
		t.Fatalf("%+v", a)
	}
	if a := att("main"); !a.Keep { // 圖片相同（同一座農田）：沿用
		t.Fatalf("%+v", a)
	}
	if a := att("backpack"); a.Keep || len(a.Files) != 0 { // 沒有圖的頁面：移除
		t.Fatalf("%+v", a)
	}
	if a := att("main"); a.Keep || len(a.Files) != 1 { // 訊息上已經沒有圖：重新上傳
		t.Fatalf("%+v", a)
	}
}

func TestOpenPanelSendsFarmImage(t *testing.T) {
	e := setup(t)
	e.makePlayer(alice, guildA, "玩家")
	p := e.open(alice, guildA, "玩家")
	sent := p.opened.Last()
	if len(sent.Message.Files) != 1 || sent.Message.Files[0].Name != FarmImage {
		t.Fatal(sent.Message.Files)
	}
}

func TestMissingAssetsFallBackToText(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	old := farmimage.Assets
	farmimage.Assets = fstest.MapFS{}
	farmimage.ResetCache()
	defer func() { farmimage.Assets = old; farmimage.ResetCache() }()
	for _, page := range []string{"main", "plots"} {
		en := lastEdit(t, p.goPage(page))
		if strings.Contains(p.Text, "無法顯示") || len(en.Message.Attachments.Files) != 0 || en.Message.Attachments.Keep {
			t.Fatal(page, p.Text)
		}
	}
}

func TestUpgradeButtonDisabledAtMaxPlots(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	must[services.PlotsAdded](t)(e.admin.AddPlots(e.ctx, "root", p.panel.playerID, balance.MaxPlots-balance.InitialPlots))
	p.goPage("plots")
	if !p.button("已達上限").Disabled {
		t.Fatal("應停用")
	}
}

func TestUpgradeAddsOnePlotAndPlantSelectIsRegistered(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.exec("UPDATE farms SET gold = 10000")
	p.goPage("plots")
	p.choose("選擇作物", "wheat")
	if p.options("選擇作物") != nil { // 種滿後沒有種植選單
		t.Fatal(p.options("選擇作物"))
	}
	p.button("擴建（" + money(balance.UpgradePrices[0]) + " 金）")
	p.click("擴建")
	p.click("確認擴建")
	contains(t, p.Text, "農田（5 格）", "新增第 5 格")
	p.button("擴建（" + money(balance.UpgradePrices[1]) + " 金）")
	// 擴建後才出現的種植選單，在送出畫面時就已登記（不會「此交互失敗」）
	s := p.selectMenu("選擇作物")
	if s == nil || !e.cmds.Registry.Has(s.CustomID()) {
		t.Fatal("種植選單沒有登記")
	}
	p.choose("選擇作物", "wheat")
	contains(t, p.Text, "在 1 格種下小麥")
}

func TestHelpHasOfficialGuildLink(t *testing.T) {
	e := HelpEmbed()
	found := false
	for _, f := range e.Fields {
		if f.Name == "💬 官方群組" && strings.Contains(f.Value, config.OfficialGuildURL) {
			found = true
		}
	}
	if !found || e.Len() > 6000 || len(e.Fields) > 25 {
		t.Fatal(found, e.Len(), len(e.Fields))
	}
}

func TestBackpackFakeCoinExchange(t *testing.T) {
	// Go 版：背包的假幣兌換（數量按鈕與自訂數量；Python 在 test_machines_page_build_and_fake_coin_exchange 中測）
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.exec(`INSERT INTO inventory_items (farm_id, item_code, quality, mutation, quantity)
		SELECT id, 'fake_coin', 'normal', 'none', 7 FROM farms`)
	p.goPage("backpack")
	p.click("兌換假幣（持有 7）")
	contains(t, p.Text, "持有 **7** 個")
	if slices.Contains(p.labels(), "換 10") { // 超過持有數量的按鈕不顯示
		t.Fatal(p.labels())
	}
	p.click("換 5")
	contains(t, p.Text, "500")
	if slices.Contains(p.labels(), "換 1") { // 兌換後收起數量按鈕（Python keep=False）
		t.Fatal(p.labels())
	}
	p.click("兌換假幣（持有 2）")
	bad := p.modal("自訂數量", "9")
	contains(t, bad.Text(), "請輸入 1–2 的整數。")
	p.click("兌換假幣（持有 2）")
	p.modal("自訂數量", "２") // 全形數字（Python isdigit）
	if n := e.scalar("SELECT coalesce(sum(quantity), 0) FROM inventory_items WHERE item_code = 'fake_coin'"); n != int64(0) {
		t.Fatal(n)
	}
}

func errorsValidation(msg string) error { return gameerr.Validationf("%s", msg) }
