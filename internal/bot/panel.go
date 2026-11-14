package bot

// 互動式農場面板的框架（對應 Python app/discord_bot/panels.py 的 FarmPanel：互動基礎、重繪、操作、錯誤畫面、圖片、
// 元件工具與對話框）。各頁面在 pages_*.go。
//
// 與 Python 相同：
//   - /farm 開出只有自己看得到的面板；同一則訊息依按鈕切換頁面。只有開啟面板的玩家可以操作。
//   - 身分與群組在開啟時由互動來源決定；所有操作都呼叫遊戲服務，交易完成後才更新 Discord 訊息。
//   - 一鍵操作依序呼叫服務方法，每一步各自是一筆完整交易（每個呼叫各自死結重試，見 Runner）。
//   - 頁面資料每次都重新從資料庫讀取（自動機器＋整頁在同一筆交易：RunBatch），網站或其他介面的修改會立即反映。
//
// 與 Python 不同：同一面板的回呼由 ui.Registry 依序執行，所以面板狀態不需要另外加鎖（Python 的 _render_lock）；
// 1.5 秒內沒有回應就先 defer 也由 ui.Registry 處理（Python _in_time）。

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/services"
)

const (
	// PanelTimeout Discord 私密訊息的互動權杖有效 15 分鐘。
	PanelTimeout = 15 * time.Minute
	// FarmImage 面板圖片的附件檔名（Embed 以 attachment:// 引用）。
	FarmImage = "farm.png"
)

// Picker 數量選擇（Python self.picker dict）：買、賣、兌換時顯示 1／5／10／全部／自訂。
type Picker struct {
	Key     string
	Label   string
	Verb    string // 按鈕前綴，預設「×」
	Max     int
	Presets []int
	Run     func(ctx context.Context, q int) (string, error)
	// After 操作成功後附在結果後面的文字（例如購買後的持有數量）；在重新讀取頁面的同一筆交易中讀取，失敗時省略
	After      func(ctx context.Context) string
	Keep       bool // 完成後保留已選商品與數量按鈕（方便加購）
	NoProgress bool // Python track_progress=False
	Confirm    bool // 高價物品：只有一個確認按鈕
	Most       int  // 加工：現有原料最多能做幾份（每次重繪重算）
	PriceText  string
}

// prepared 操作後預先讀好的頁面（Python act 傳給 _render 的 (頁面, _prepare 的結果)）。
type prepared struct {
	page  string
	notes []string
	data  any
	err   error
}

// pageDef 一個頁面：在 RunBatch 中讀取資料，再依資料建立 Embed 與元件。
type pageDef struct {
	load  func(ctx context.Context) (any, error)
	build func(ctx context.Context, data any) (*ui.Embed, error)
}

// Panel 一個玩家的私密農場面板。
type Panel struct {
	cmds      *Commands
	who       Who
	ownerID   string
	playerID  int64 // 0 = 還沒有農場（welcome）
	groupID   *int64
	groupName string
	Page      string
	View      *ui.View
	AvatarURL string
	message   ui.SentMessage
	pages     map[string]pageDef

	// 頁面狀態
	picker   *Picker
	sellKeys []string // 販售頁勾選的項目
	bet      *casinoBet
	board    boardState // 排行榜條件
	// 偷菜：作物與玩家選單的頁數、正在查看農田的玩家
	stealPage       int
	stealPlayerPage int
	stealVictim     *int64
	competitionID   *int64 // 競賽頁（Python self.detail）
	// 山洞：目前查看的區域、最近一次探索結果
	caveArea   string
	caveResult *services.ExploreReward
	// 成就與圖鑑：目前查看的分類
	achCategory   string
	codexCategory string
	// 世界 Boss：目前活動、玩家切換查看的活動、捐獻流程（類別、選項、已選物品、待確認）
	bossEventID  *int64
	bossSelected *int64
	bossCategory string
	bossOptions  []services.BossDonationOption
	bossItem     *services.BossDonationOption
	bossPending  *bossPending
	// 釣魚：操作結果（直接顯示，不再查詢）、自動更新計時器與期限、選取的魚、勾選出售、魚簍頁數、排行範圍
	fishCache         *services.FishingView
	fishTimer         *time.Timer
	fishDue           *fishDue
	fishLastDue       time.Time
	fishTimersEnabled bool
	fishSelected      *int64
	fishSellIDs       []int64
	fishPage          int
	fishRankGlobal    bool
	confirm           string
	lab               labState // 種子實驗室：選取的種子與雜交設定
	// 文字清單的頁碼：作物價格、操作紀錄、培育紀錄（2026-11-14 改成分頁，避免資料一多就被截掉或洗掉）
	pricePage, logPage, labRecordPage, seedMakerPage int
	checkinNextAt                                    time.Time
	welcomed                                         bool
	marks                                            *services.ProgressMarks

	// 圖片：本次畫面要附上的檔案與內容雜湊；面板訊息上目前掛著的圖的雜湊（相同時沿用，不重新上傳）
	files       []ui.File
	imageDigest []byte
	sentDigest  []byte
	imageSize   int
}

// NewPanel 建立面板（Python FarmPanel.__init__）。
func NewPanel(c *Commands, w Who, ctx services.ExternalContext) *Panel {
	p := &Panel{cmds: c, who: w, ownerID: w.UserID, groupID: ctx.GroupID, groupName: deref(ctx.GroupName), Page: "welcome",
		board: boardState{scope: "global", kind: "money", period: "all"}, achCategory: "farm", codexCategory: "crop", fishTimersEnabled: true}
	if ctx.PlayerID != nil {
		p.playerID = *ctx.PlayerID
	}
	if ctx.HasFarm {
		p.Page = "main"
	}
	p.View = ui.NewView(PanelTimeout)
	p.View.Check = p.interactionCheck
	p.View.OnTimeout = p.onTimeout
	p.View.OnError = p.onError
	p.registerPages()
	return p
}

// svc 操作所在的群組 = 開啟面板的伺服器（只做紀錄與任務計分判斷，不影響農場歸屬）。
func (p *Panel) svc() *services.AdminService { return p.cmds.Service.InGroupContext(p.groupID) }

// ---------- 互動基礎 ----------

func (p *Panel) interactionCheck(ctx context.Context, it ui.Interaction) bool {
	if it.UserID() != p.ownerID {
		_ = it.SendMessage(ctx, ui.Message{Content: ptr("這是別人的農場面板，請用 /farm 開啟自己的面板。"), Ephemeral: true})
		return false
	}
	return true
}

func (p *Panel) onTimeout(ctx context.Context) {
	p.cancelFishTimer()
	p.View.DisableAll()
	if p.message != nil {
		_ = p.message.Edit(ctx, ui.Message{Content: ptr("面板已逾時，請重新輸入 /farm。"), View: p.View})
	}
}

func (p *Panel) onError(ctx context.Context, it ui.Interaction, err error) {
	slog.Error("面板操作失敗", "user", it.UserID(), "error", err.Error())
	_, _ = it.Followup(ctx, ui.Message{Content: ptr("❌ " + PlayerMessage(err)), Ephemeral: true})
}

func ptr[T any](v T) *T { return &v }

// logUnexpected 非遊戲規則的錯誤才記錄（Python「if not isinstance(exc, GameError): logger.exception」）。
func logUnexpected(err error, msg string, args ...any) {
	if _, ok := gameerr.As(err); ok {
		return
	}
	slog.Error(msg, append(args, "error", err.Error())...)
}

// ---------- 讀取與重繪 ----------

// render 讀取目前頁面的資料並重建元件；讀取失敗時顯示錯誤並提供回總覽按鈕。pre 不為 nil 且頁面相同時使用預先讀好的資料。
func (p *Panel) render(ctx context.Context, notice string, pre *prepared) *ui.Embed {
	p.View.Clear()
	p.files, p.imageDigest = nil, nil // 換頁時舊圖片也要移除
	embed, err := func() (e *ui.Embed, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("面板建立失敗：%v", r)
			}
		}()
		var res prepared
		if pre != nil && pre.page == p.Page {
			res = *pre
		} else {
			res = p.prepareRun(ctx, p.Page)
		}
		for _, n := range res.notes {
			if n != "" {
				if notice != "" {
					notice += "\n" + n
				} else {
					notice = n
				}
			}
		}
		if res.err != nil {
			return nil, res.err
		}
		def, ok := p.pages[p.Page]
		if !ok {
			return nil, gameerr.Validationf("這個頁面還沒有完成。")
		}
		return def.build(ctx, res.data)
	}()
	if err != nil {
		logUnexpected(err, "面板讀取失敗", "page", p.Page)
		p.View.Clear()
		p.files, p.imageDigest = nil, nil
		embed = &ui.Embed{Title: "⚠️ 無法顯示", Color: Red, Description: "### " + cleanMessage(PlayerMessage(err))}
		p.navBack(0)
	}
	if notice != "" {
		embed.Description = cut(strings.TrimSpace(quote(notice)+"\n\n"+embed.Description), 4096)
		for _, mark := range HighlightMarks {
			if strings.Contains(notice, mark) {
				embed.Color = Gold // 稀有收穫、新紀錄、成就達成等：整個面板換成金色
				break
			}
		}
	}
	// Discord 限制單一 Embed 總長度 6000 字：超過時縮短主要內容（例如農田非常多時）
	if overflow := embed.Len() - 5900; overflow > 0 && embed.Description != "" {
		embed.Description = cut(embed.Description, max(200, utf8.RuneCountInString(embed.Description)-overflow))
	}
	return embed
}

// prepareRun 在 Runner 的空位中執行 prepare。
func (p *Panel) prepareRun(ctx context.Context, page string) prepared {
	var res prepared
	_ = p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
		res = p.prepare(ctx, page)
		return nil
	})
	return res
}

// prepare 依序：每日簽到 → 自動機器＋讀取整頁（同一筆交易）。回傳頂端提示、頁面資料與讀取錯誤。
// 機器與簽到各自是一筆交易，失敗不影響面板顯示；讀取失敗時仍回傳前兩步的提示（例如剛簽到的獎勵）。
func (p *Panel) prepare(ctx context.Context, page string) prepared {
	return p.prepareWith(ctx, page, nil)
}

// prepareWith 同 prepare；before 在讀取整頁的同一筆交易中、自動機器之前執行（操作後的附加讀取，與頁面共用讀取快取，
// 讀到的是機器運作前的狀態，與原本各自一筆交易相同）。死結重試時會重新執行。
func (p *Panel) prepareWith(ctx context.Context, page string, before func(ctx context.Context)) prepared {
	res := prepared{page: page}
	farmPage := page != "welcome"
	if farmPage {
		res.notes = append(res.notes, p.welcomeNote(ctx), p.checkinNote(ctx))
	}
	def, ok := p.pages[page]
	if !ok {
		return res // render 會顯示「還沒有完成」
	}
	var machines string
	_, err := Call(ctx, p.cmds.Runner, "run_batch", func(ctx context.Context) (struct{}, error) {
		machines = ""
		return struct{}{}, p.svc().RunBatch(ctx, func(ctx context.Context) error {
			if before != nil {
				// SAVEPOINT：附加讀取失敗只回滾這一段，頁面照常讀取（原本是各自一筆交易，失敗不影響頁面）
				if err := p.svc().Isolated(ctx, func(ctx context.Context) error { before(ctx); return nil }); err != nil {
					logUnexpected(err, "操作後的附加讀取失敗", "player", p.playerID)
				}
			}
			if farmPage {
				machines = p.machinesNote(ctx)
			}
			data, err := def.load(ctx)
			res.data = data
			return err
		})
	})
	res.notes = append(res.notes, machines)
	res.err = err
	return res
}

// welcomeNote 面板第一次讀取農場頁面時：「你不在的時候」摘要，同時記下圖鑑與成就標記。
func (p *Panel) welcomeNote(ctx context.Context) string {
	if p.welcomed {
		return ""
	}
	p.welcomed = true
	r, err := Call(ctx, p.cmds.Runner, "welcome_back", func(ctx context.Context) (services.WelcomeBack, error) {
		return p.svc().WelcomeBack(ctx, p.playerID)
	})
	if err != nil {
		logUnexpected(err, "歡迎回來摘要失敗", "player", p.playerID)
		return ""
	}
	p.marks = &r.Marks
	return strings.Join(r.Lines, "\n")
}

// readProgressMarks 操作後讀取圖鑑與成就標記（與 applyProgress 比較操作前後）；失敗時回傳 nil，不影響操作結果。
func (p *Panel) readProgressMarks(ctx context.Context) *services.ProgressMarks {
	m, err := Call(ctx, p.cmds.Runner, "progress_marks", func(ctx context.Context) (services.ProgressMarks, error) {
		return p.svc().ProgressMarks(ctx, p.playerID)
	})
	if err != nil {
		logUnexpected(err, "圖鑑與成就標記讀取失敗", "player", p.playerID)
		return nil
	}
	return &m
}

// applyProgress 比較圖鑑與成就標記：新登記的圖鑑、新達成的成就。
func (p *Panel) applyProgress(m *services.ProgressMarks) string {
	if m == nil {
		return ""
	}
	old := p.marks
	p.marks = m
	if old == nil {
		return ""
	}
	return progressDiff(*old, *m)
}

// machinesNote 自動收割機／自動加工機在開啟或切換頁面時運作；失敗不影響面板顯示。
func (p *Panel) machinesNote(ctx context.Context) string {
	note, err := p.svc().RunMachines(ctx, p.playerID)
	if err != nil {
		logUnexpected(err, "自動機器執行失敗", "player", p.playerID)
		return ""
	}
	return note
}

// checkinNote 開啟或切換頁面時自動每日簽到；今天已簽到後到台灣隔天 00:00 前不再查詢。
func (p *Panel) checkinNote(ctx context.Context) string {
	if !p.cmds.AutoCheckin || (!p.checkinNextAt.IsZero() && p.cmds.now().Before(p.checkinNextAt)) {
		return ""
	}
	r, err := Call(ctx, p.cmds.Runner, "daily_checkin", func(ctx context.Context) (*services.CheckinResult, error) {
		return p.svc().DailyCheckin(ctx, p.playerID)
	})
	if err != nil {
		logUnexpected(err, "每日簽到失敗", "player", p.playerID)
		return ""
	}
	if r == nil {
		return ""
	}
	p.checkinNextAt = r.NextAt
	if !r.New {
		return ""
	}
	note := fmt.Sprintf("📅 每日簽到成功！連續第 **%d** 天，獲得 %s。（簽到紀錄在「🏅 成就圖鑑」）", r.Streak, r.RewardText)
	if r.Converted {
		note += "（倉庫已滿，道具改發等值金幣）"
	}
	return note
}

// refresh 重繪並送出畫面（尚未回應時直接回應，已 defer 則編輯原訊息）。metric：延遲統計的項目
// （「換頁」「操作」…，加上頁面名稱；"" 不記錄）；started：計時起點。
func (p *Panel) refresh(ctx context.Context, it ui.Interaction, notice string, pre *prepared, metric string, started time.Time) {
	if started.IsZero() {
		started = time.Now()
	}
	defer p.scheduleFishTimer(it) // 釣魚畫面：上鉤、反應期限、拉扯逾時時自動更新（送出失敗也照常排定，與 Python 相同）
	embed := p.render(ctx, notice, pre)
	att := p.attachments(it)
	deferred := it.Responded()
	uploadStarted := time.Now()
	if err := it.Show(ctx, ui.Message{Embed: embed, View: p.View, Attachments: att}); err != nil {
		p.sentDigest = nil // 不確定訊息上現在是哪張圖，下次重新上傳
		slog.Warn("更新面板失敗", "type", fmt.Sprintf("%T", err))
		return
	}
	p.sentDigest = p.imageDigest
	if !att.Keep && len(att.Files) > 0 { // 真的上傳了新圖片（沿用既有附件時不記錄）
		slog.Info("面板上傳圖片", "player", p.playerID, "page", p.Page, "kb", p.imageSize/1024,
			"ms", fmt.Sprintf("%.0f", msSince(uploadStarted)))
	}
	if metric != "" {
		p.cmds.Latency.Record(metric+" "+p.Page, msSince(started), deferred)
	}
}

// attachments 圖片和面板訊息上現有的一樣時沿用既有附件，否則上傳新檔案（沒有圖片時移除附件）。
func (p *Panel) attachments(it ui.Interaction) *ui.Attachments {
	if len(p.files) > 0 && p.imageDigest != nil && string(p.imageDigest) == string(p.sentDigest) &&
		slices.Contains(it.MessageAttachments(), FarmImage) {
		return &ui.Attachments{Keep: true}
	}
	return &ui.Attachments{Files: p.files}
}

// goto 換頁（清除數量選擇與二次確認）。
func (p *Panel) goTo(ctx context.Context, it ui.Interaction, page, notice string) {
	p.Page, p.picker, p.confirm = page, nil, ""
	p.resetPageState()
	p.refresh(ctx, it, notice, nil, "換頁", time.Time{})
}

// resetPageState 換頁時清除的其他頁面狀態（Python goto 中的 fish_sell_ids、sell_keys）。
func (p *Panel) resetPageState() { p.sellKeys, p.fishSellIDs = nil, nil }

// actOpts act 的選項。
type actOpts struct {
	page       string // 完成後切換到的頁面（"" = 目前頁面）
	keep       bool
	prefix     string                           // 預設 "✅ "
	noProgress bool                             // Python track_progress=False：這個操作不可能讓圖鑑或成就有進展
	after      func(ctx context.Context) string // Picker.After
	plain      bool                             // Python prefix=""：結果前面不加「✅ 」（賭場）
}

// act 執行一個操作。成功時結果以 ✅ 顯示在頁面頂端並重新整理；失敗時改顯示獨立的錯誤畫面，只有錯誤原因與「返回」按鈕，
// 返回後保留剛才的選擇。操作與讀取新畫面在同一次排隊中完成。
func (p *Panel) act(ctx context.Context, it ui.Interaction, work func(ctx context.Context) (string, error), o actOpts) {
	p.confirm = ""
	started := time.Now()
	target := o.page
	if target == "" {
		target = p.Page
	}
	prefix := o.prefix
	if prefix == "" && !o.plain {
		prefix = "✅ "
	}
	var text string
	var pre prepared
	var werr error
	_ = p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
		text, werr = work(ctx)
		if werr != nil {
			return nil
		}
		// 附加文字與圖鑑成就標記在重新讀取頁面的同一筆交易中讀取（原本各自一筆交易，讀的資料大多與頁面重複）
		track := target != "welcome" && !o.noProgress
		var after string
		var marks *services.ProgressMarks
		var before func(ctx context.Context)
		if o.after != nil || track {
			before = func(ctx context.Context) {
				after, marks = "", nil
				if o.after != nil {
					after = o.after(ctx)
				}
				if track {
					marks = p.readProgressMarks(ctx)
				}
			}
		}
		pre = p.prepareWith(ctx, target, before)
		text += after + p.applyProgress(marks)
		return nil
	})
	if werr != nil {
		logUnexpected(werr, "面板操作失敗", "player", p.playerID)
		deferred := it.Responded()
		p.showError(ctx, it, cleanMessage(PlayerMessage(werr)))
		p.cmds.Latency.Record("操作失敗 "+target, msSince(started), deferred)
		return
	}
	if !o.keep { // 購買後保留已選商品與數量按鈕，方便確認與加購
		p.picker = nil
	}
	if o.page != "" {
		p.Page = o.page
	}
	p.refresh(ctx, it, cut(prefix+cleanMessage(text), 1500), &pre, "操作", started)
}

// actMsg 只需要 ActionResult.Message 的操作。
func actMsg(fn func(ctx context.Context) (services.ActionResult, error)) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		r, err := fn(ctx)
		return r.Message, err
	}
}

// call 包一個服務呼叫（死結重試）。
func (p *Panel) call(name string, fn func(ctx context.Context) (services.ActionResult, error)) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		r, err := Call(ctx, p.cmds.Runner, name, fn)
		return r.Message, err
	}
}

// callQty 依數量執行的服務呼叫（數量選擇用）。
func (p *Panel) callQty(name string, fn func(ctx context.Context, q int) (services.ActionResult, error)) func(ctx context.Context, q int) (string, error) {
	return func(ctx context.Context, q int) (string, error) {
		r, err := Call(ctx, p.cmds.Runner, name, func(ctx context.Context) (services.ActionResult, error) { return fn(ctx, q) })
		return r.Message, err
	}
}

// showError 錯誤畫面：只有錯誤原因與「返回」按鈕。
func (p *Panel) showError(ctx context.Context, it ui.Interaction, message string) {
	p.View.Clear()
	embed := &ui.Embed{Title: "⚠️ 無法完成", Color: Red, Description: "### " + cut(message, 1500), Footer: "按「返回」回到剛才的畫面"}
	p.button("返回", func(ctx context.Context, it ui.Interaction) error {
		p.refresh(ctx, it, "", nil, "重繪", time.Time{})
		return nil
	}, btn{emoji: "↩️", style: ui.Primary})
	if err := it.Show(ctx, ui.Message{Embed: embed, View: p.View, Attachments: &ui.Attachments{}}); err != nil {
		slog.Warn("更新面板失敗", "type", fmt.Sprintf("%T", err))
	}
	p.sentDigest = nil
}

// ---------- 圖片 ----------

// attachImage 附上圖片（png 為 nil 時不附）。
func (p *Panel) attachImage(e *ui.Embed, png []byte) {
	if len(png) == 0 {
		return
	}
	p.files = []ui.File{{Name: FarmImage, Data: png}}
	sum := sha256.Sum256(png)
	p.imageDigest = sum[:16]
	p.imageSize = len(png)
	e.Image = "attachment://" + FarmImage
}

// image 產生圖片；素材缺漏或拼圖失敗不能讓面板壞掉，退回純文字。
// 動態圖片在組畫面（build）時才產生，不佔資料庫交易與 Runner 的名額；同時產生的張數以 CPU 核心數為上限，
// 避免大量玩家同時操作時一次配置太多張畫布。
func (p *Panel) image(what string, make func() ([]byte, error)) []byte {
	if s := p.cmds.images; s != nil {
		s <- struct{}{}
		defer func() { <-s }()
	}
	png, err := make()
	if err != nil {
		slog.Error(what+"圖片產生失敗", "player", p.playerID, "error", err.Error())
		return nil
	}
	return png
}

func (p *Panel) farmImage(st services.FarmStatus) []byte {
	return p.image("農田", func() ([]byte, error) { return farmimage.FarmPNGFromStatus(st) })
}

func (p *Panel) yardImage(ws services.WorkshopStatus, animals []services.AnimalView) []byte {
	return p.image("建築畫面", func() ([]byte, error) { return farmimage.YardPNG(ws, animals) })
}

// ---------- 元件工具 ----------

type btn struct {
	emoji    string
	style    ui.Style
	row      int
	disabled bool
}

// button 加入按鈕（標籤最多 80 字）。
func (p *Panel) button(label string, handler ui.Handler, o btn) *ui.Button {
	style := o.style
	if style == 0 {
		style = ui.Secondary
	}
	b := &ui.Button{Label: cut(label, 80), Emoji: o.emoji, Style: style, Row: o.row, Disabled: o.disabled, OnClick: handler}
	p.View.Add(b)
	return b
}

// nav 換頁按鈕。
func (p *Panel) nav(label, page string, o btn) *ui.Button {
	return p.button(label, func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, page, "")
		return nil
	}, o)
}

func (p *Panel) navBack(row int) { p.nav("回總覽", "main", btn{emoji: "🏠", row: row}) }

// option 下拉選單選項：(標籤, 值, 說明, emoji)。
type option struct{ label, value, desc, emoji string }

// selectMenu 下拉選單（最多 25 項）；selected 是重新整理後仍顯示為已選取的選項。沒有選項時不加入。
func (p *Panel) selectMenu(placeholder string, options []option, handler func(ctx context.Context, it ui.Interaction, value string) error,
	row int, selected string) *ui.Select {
	if len(options) == 0 {
		return nil
	}
	s := &ui.Select{Placeholder: cut(placeholder, 150), Row: row, OnSelect: handler}
	for _, o := range options[:min(len(options), ui.MaxOptions)] {
		desc := ""
		if o.desc != "" {
			desc = cut(o.desc, 100)
		}
		s.Options = append(s.Options, ui.SelectOption{Label: cut(o.label, 100), Value: o.value, Description: desc,
			Emoji: o.emoji, Default: o.value == selected})
	}
	p.View.Add(s)
	return s
}

// quantityButtons 依 picker 顯示數量按鈕（1／5／10／全部／自訂）。
func (p *Panel) quantityButtons(row int) {
	pk := p.picker
	if pk == nil {
		return
	}
	if pk.Confirm { // 高價物品：只有一個確認按鈕
		p.button(fmt.Sprintf("確認購買（%s）", pk.PriceText), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, func(ctx context.Context) (string, error) { return pk.Run(ctx, 1) }, actOpts{})
			return nil
		}, btn{emoji: "✅", row: row, style: ui.Success})
		return
	}
	verb := pk.Verb
	if verb == "" {
		verb = "×"
	}
	for _, q := range pk.Presets {
		if q > pk.Max {
			continue
		}
		p.button(fmt.Sprintf("%s %d", verb, q), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, func(ctx context.Context) (string, error) { return pk.Run(ctx, q) }, actOpts{keep: pk.Keep, noProgress: pk.NoProgress, after: pk.After})
			return nil
		}, btn{row: row, style: ui.Primary})
	}
	p.button("自訂數量…", func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, quantityModal(pk.Label, "數量", pk.Max, func(ctx context.Context, mit ui.Interaction, q int) {
			p.act(ctx, mit, func(ctx context.Context) (string, error) { return pk.Run(ctx, q) }, actOpts{keep: pk.Keep, noProgress: pk.NoProgress, after: pk.After})
		}))
	}, btn{emoji: "✏️", row: row})
}

// ---------- 對話框 ----------

// parseDigits Python str.isdigit() 後 int()：只接受十進位數字（含全形數字）。
func parseDigits(s string) (int, bool) {
	if s == "" || utf8.RuneCountInString(s) > 9 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if !unicode.Is(unicode.Nd, r) {
			return 0, false
		}
		n = n*10 + digitValue(r)
	}
	return n, true
}

// digitValue Unicode 十進位數字的值：Nd 字元都是從 0 開始、每 10 個一組的連續區段，往前找到區段起點後取餘數。
func digitValue(r rune) int {
	start := r
	for unicode.Is(unicode.Nd, start-1) {
		start--
	}
	return int(r-start) % 10
}

// quantityModal 數量輸入框（Python QuantityModal）：1–maximum 的整數，否則回覆錯誤（不執行）。
func quantityModal(title, label string, maximum int, onSubmit func(ctx context.Context, it ui.Interaction, q int)) *ui.Modal {
	return &ui.Modal{
		Title:  cut(title, 45),
		Inputs: []ui.TextInput{{Label: cut(label, 45), Placeholder: fmt.Sprintf("1–%d", maximum), MaxLength: 6, Required: true}},
		OnSubmit: func(ctx context.Context, it ui.Interaction, values []string) error {
			raw := strings.TrimSpace(first(values))
			n, ok := parseDigits(raw)
			if !ok || n < 1 || n > maximum {
				return it.SendMessage(ctx, ui.Message{Content: ptr(fmt.Sprintf("請輸入 1–%d 的整數。", maximum)), Ephemeral: true})
			}
			onSubmit(ctx, it, n)
			return nil
		},
	}
}

// nameModal 為農場取名（空白表示用預設名稱）。
func nameModal(onSubmit func(ctx context.Context, it ui.Interaction, name string)) *ui.Modal {
	return &ui.Modal{
		Title:  "為農場取名",
		Inputs: []ui.TextInput{{Label: "農場名稱", MaxLength: balance.FarmNameMaxLen}},
		OnSubmit: func(ctx context.Context, it ui.Interaction, values []string) error {
			onSubmit(ctx, it, strings.TrimSpace(first(values)))
			return nil
		},
	}
}

// renameModal 更改農場名稱：預先填入目前名稱，送出後才扣金幣。
func renameModal(current string, onSubmit func(ctx context.Context, it ui.Interaction, name string)) *ui.Modal {
	return &ui.Modal{
		Title: cut(fmt.Sprintf("更改農場名稱（%d 金幣）", balance.RenameFarmCost), 45),
		Inputs: []ui.TextInput{{Label: "新的農場名稱", Default: string([]rune(current)[:min(utf8.RuneCountInString(current), balance.FarmNameMaxLen)]),
			MaxLength: balance.FarmNameMaxLen, Required: true}},
		OnSubmit: func(ctx context.Context, it ui.Interaction, values []string) error {
			onSubmit(ctx, it, strings.TrimSpace(first(values)))
			return nil
		},
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
