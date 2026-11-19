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
	"farmgm/i18n"
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
	LabelFor   func(q int) string // 數量按鈕的標籤（預設「動詞 數量」），例如種子製造「用 5 個 → 10 顆」
	Short      string             // 買不起時的說明（例如「金幣不足（需 50，你有 20）」）：數量按鈕改成一顆停用的說明按鈕
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
	sellPage int      // 販售頁選單的頁碼（超過 25 項時分頁）
	bet      *casinoBet
	board    boardState // 排行榜條件
	// 偷菜：作物與玩家選單的頁數、正在查看農田的玩家
	stealPage       int
	stealPlayerPage int
	stealVictim     *int64
	// 送禮與互助（2026-11-17）：選中的對象與兩個選單的頁碼
	aidTarget     *int64
	aidTargetPage int
	aidItemPage   int
	competitionID *int64 // 競賽頁（Python self.detail）
	// 山洞：目前查看的區域、最近一次探索結果
	caveArea   string
	caveResult *services.ExploreReward
	// 成就與圖鑑：目前查看的分類
	achCategory   string
	codexCategory string
	usageItem     string // 背包頁「🔍 查詢物品用途」選中的物品（"c|代碼" 作物、"i|代碼" 其他；2026-11-18）
	// 世界 Boss：目前活動、玩家切換查看的活動、捐獻流程（類別、選項、已選物品、待確認）
	bossEventID      *int64
	bossSelected     *int64
	bossCategory     string
	bossOptions      []services.BossDonationOption
	bossItem         *services.BossDonationOption
	bossPending      *bossPending
	bossLastCategory string // 上次選的捐獻類別（再次開啟捐獻頁時沿用）
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
	helpTopic         string       // 說明頁選取的主題（HelpEmbed 欄位索引；"" = 主題清單）
	recentPages       []string     // 總覽「更多功能」最近用過的頁面（最新的在前，最多 3 個）
	lastEmbed         *ui.Embed    // 最近一次送出的畫面（逾時時改 footer 用）
	lab               labState     // 種子實驗室：選取的種子與雜交設定
	backButtons       []*ui.Button // 這次建頁面時標記為返回類的按鈕（placeBackButtons 使用）
	// 文字清單的頁碼：作物價格、操作紀錄、培育紀錄（2026-11-14 改成分頁，避免資料一多就被截掉或洗掉）
	pricePage, logPage, labRecordPage, seedMakerPage int
	plotSeedPage                                     int    // 農田頁特殊種子選單的頁碼（超過 25 顆時選單內翻頁）
	plotSeedCrop                                     string // 農田頁特殊種子的作物篩選（"" / cropFilterAll = 全部，cropFilterPick = 正在選作物；2026-11-17）
	// 農場商店的分類與種子頁碼（2026-11-15）
	shopTab  string
	shopPage int
	// 庭院擺設頁（2026-11-15）：要購買的裝飾、選取的位置
	decorBuy      string
	decorSlot     int
	decorPage     int // 購買選單的頁碼（2026-11-19 裝飾超過 25 種）
	decorPutPage  int // 擺放選單的頁碼
	badgeBuy      string
	looksBuy      string // 外觀頁確認購買的商品（theme:snow／hat:crown／color:pink）
	looksPet      string // 外觀頁正在打扮的寵物
	checkinNextAt time.Time
	lastShareAt   time.Time // 最近一次公開分享（農場、魚卡）的時間，用來限制洗版
	welcomed      bool
	lastActiveAt  time.Time // 最後一次操作面板的時間：逾時時寫回「最後看到面板」（2026-11-18 離線摘要）
	marks         *services.ProgressMarks
	lang          string // 介面語言（i18n：玩家選的，或跟著 Discord 用戶端）

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
	p.lang = i18n.Resolve(ctx.Language, w.Locale)
	if ctx.HasFarm {
		p.Page = "main"
	}
	p.recentPages = c.recentPages(w.UserID)
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
		_ = it.SendMessage(ctx, ui.Message{Content: ptr(p.s("這是別人的農場面板，請用 /farm 開啟自己的面板。")), Ephemeral: true})
		return false
	}
	p.lastActiveAt = p.cmds.now()
	return true
}

func (p *Panel) onTimeout(ctx context.Context) {
	p.cancelFishTimer()
	p.View.DisableAll()
	if p.playerID != 0 && !p.lastActiveAt.IsZero() { // 下次「你不在的時候」從最後一次操作算起，不把自己的操作算進去
		if err := p.svc().MarkSeen(ctx, p.playerID, p.lastActiveAt); err != nil {
			logUnexpected(err, "記錄最後操作時間失敗", "player", p.playerID)
		}
	}
	if p.message != nil {
		msg := ui.Message{Content: ptr(p.s("面板已逾時，請重新輸入 /farm。")), View: p.View}
		// 2026-11-16 按鈕停用後，按鈕正上方的 footer 也寫逾時說明（Content 在畫面最上方，玩家看按鈕時看不到）；附件不變
		if p.lastEmbed != nil {
			e := *p.lastEmbed
			e.Footer = p.s("⏰ 面板已逾時，請重新輸入 /farm")
			msg.Embed = &e
		}
		_ = p.message.Edit(ctx, msg)
	}
}

func (p *Panel) onError(ctx context.Context, it ui.Interaction, err error) {
	slog.Error("面板操作失敗", "user", it.UserID(), "error", err.Error())
	_, _ = it.Followup(ctx, ui.Message{Content: ptr("❌ " + p.msg(err)), Ephemeral: true})
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
		built, berr := def.build(ctx, res.data)
		if berr == nil {
			p.placeBackButtons()
		}
		return built, berr
	}()
	if err != nil {
		logUnexpected(err, "面板讀取失敗", "page", p.Page)
		p.View.Clear()
		p.files, p.imageDigest = nil, nil
		embed = &ui.Embed{Title: p.s("⚠️ 無法顯示"), Color: Red, Description: "### " + cleanMessage(p.msg(err))}
		p.navBack(0)
	}
	if notice != "" {
		placeNotice(p.lang, embed, notice)
		for _, mark := range HighlightMarks {
			if strings.Contains(notice, mark) {
				embed.Color = Gold // 稀有收穫、新紀錄、成就達成等：整個面板換成金色
				break
			}
		}
	}
	p.localize(embed)
	// Discord 限制單一 Embed 總長度 6000 字：超過時縮短主要內容（例如農田非常多時）
	// 2026-11-16：說明最後一段是「貼近按鈕」的關鍵資訊（狀態、金幣、菇床…），保留最後一段、從前面的內容截短
	if overflow := embed.Len() - 5900; overflow > 0 && embed.Description != "" {
		head, tail := p.s(embed.Description), ""
		if i := strings.LastIndex(head, "\n\n"); i > 0 && utf8.RuneCountInString(head[i:]) < 1000 {
			head, tail = head[:i], head[i:]
		}
		embed.Description = cut(head, max(200, utf8.RuneCountInString(head)-overflow)) + tail
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
			data, err := def.load(i18n.WithLang(ctx, p.lang))
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
	return progressDiff(p.lang, *old, *m)
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
	// 2026-11-16 縮短成一行：每次換頁都可能出現在畫面上，原本附帶的說明（簽到紀錄在哪裡）移到說明頁
	note := p.f("📅 每日簽到成功！連續第 **%d** 天 +%s", r.Streak, r.RewardText)
	if r.Converted {
		note += p.s("（倉庫滿，改發金幣）")
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
	p.lastEmbed = embed
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
	p.refresh(ctx, it, notice, nil, p.s("換頁"), time.Time{})
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

// act 執行一個操作。成功時結果以 ✅ 顯示在畫面最下方（貼近按鈕）並重新整理；失敗時改顯示獨立的錯誤畫面，只有錯誤原因與「返回」按鈕，
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
		p.showError(ctx, it, cleanMessage(p.msg(werr)))
		p.cmds.Latency.Record(p.s("操作失敗 ")+target, msSince(started), deferred)
		return
	}
	if !o.keep { // 購買後保留已選商品與數量按鈕，方便確認與加購
		p.picker = nil
	}
	if o.page != "" {
		p.Page = o.page
	}
	p.refresh(ctx, it, cut(prefix+cleanMessage(text), 1500), &pre, p.s("操作"), started)
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
		r, err := Call(i18n.WithLang(ctx, p.lang), p.cmds.Runner, name, fn)
		return r.Message, err
	}
}

// callQty 依數量執行的服務呼叫（數量選擇用）。
func (p *Panel) callQty(name string, fn func(ctx context.Context, q int) (services.ActionResult, error)) func(ctx context.Context, q int) (string, error) {
	return func(ctx context.Context, q int) (string, error) {
		r, err := Call(i18n.WithLang(ctx, p.lang), p.cmds.Runner, name, func(ctx context.Context) (services.ActionResult, error) { return fn(ctx, q) })
		return r.Message, err
	}
}

// showError 錯誤畫面：只有錯誤原因與「返回」按鈕。
func (p *Panel) showError(ctx context.Context, it ui.Interaction, message string) {
	p.View.Clear()
	embed := &ui.Embed{Title: p.t("ui.error.title"), Color: Red, Description: "### " + cut(message, 1500), Footer: p.t("ui.error.footer")}
	p.button(p.t("ui.error.back"), func(ctx context.Context, it ui.Interaction) error {
		p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
		return nil
	}, btn{emoji: "↩️", style: ui.Primary})
	p.localize(embed)
	if err := it.Show(ctx, ui.Message{Embed: embed, View: p.View, Attachments: &ui.Attachments{}}); err != nil {
		slog.Warn("更新面板失敗", "type", fmt.Sprintf("%T", err))
	} else {
		p.lastEmbed = embed // 停在錯誤畫面逾時時，改的是這個畫面（不會蓋回上一頁、引用已刪除的圖片）
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
	return p.image(p.s("農田"), func() ([]byte, error) { return farmimage.FarmPNGFromStatus(st) })
}

func (p *Panel) yardImage(ws services.WorkshopStatus, animals []services.AnimalView) []byte {
	return p.image(p.s("建築畫面"), func() ([]byte, error) { return farmimage.YardPNG(ws, animals) })
}

// ---------- 元件工具 ----------

type btn struct {
	emoji    string
	style    ui.Style
	row      int
	disabled bool
	back     bool // 返回類按鈕（回總覽、回上一層）：建好頁面後由 placeBackButtons 移到最後一列右邊
}

// button 加入按鈕（標籤最多 80 字）。
func (p *Panel) button(label string, handler ui.Handler, o btn) *ui.Button {
	style := o.style
	if style == 0 {
		style = ui.Secondary
	}
	b := &ui.Button{Label: cut(label, 80), Emoji: o.emoji, Style: style, Row: o.row, Disabled: o.disabled, OnClick: handler}
	p.add(b)
	if o.back {
		p.backButtons = append(p.backButtons, b)
	}
	return b
}

// add 加入元件。指定的列放不下時（例如之後在已有 5 顆按鈕的列再加一顆），改放到後面第一個放得下的列，
// 再不行就往前找；都放不下時略過這個元件並記錄警告，不讓整頁變成「⚠️ 無法顯示」（ui.View.Add 放不下會 panic）。
func (p *Panel) add(it ui.Item) {
	weight := 1
	if _, ok := it.(*ui.Select); ok {
		weight = ui.MaxRowItems // 下拉選單獨佔一列
	}
	fits := func(row int) bool {
		used := 0
		for _, x := range p.View.Items() {
			if x.ItemRow() == row {
				if _, ok := x.(*ui.Select); ok {
					used += ui.MaxRowItems
				} else {
					used++
				}
			}
		}
		return used+weight <= ui.MaxRowItems
	}
	want := max(0, min(it.ItemRow(), ui.MaxRows-1))
	row := -1
	for r := want; r < ui.MaxRows && row < 0; r++ {
		if fits(r) {
			row = r
		}
	}
	for r := want - 1; r >= 0 && row < 0; r-- {
		if fits(r) {
			row = r
		}
	}
	if row < 0 {
		slog.Warn("面板元件放不下，已略過", "page", p.Page, "row", it.ItemRow())
		return
	}
	if row != it.ItemRow() {
		slog.Warn("面板元件的列已滿，改放其他列", "page", p.Page, "row", it.ItemRow(), "to", row)
		switch x := it.(type) {
		case *ui.Button:
			x.Row = row
		case *ui.Select:
			x.Row = row
		}
	}
	p.View.Add(it)
}

// nav 換頁按鈕。
func (p *Panel) nav(label, page string, o btn) *ui.Button {
	o.back = o.back || isBackLabel(label) // 只有換頁按鈕會依標籤判斷；動作按鈕（例如「回收」）不受影響
	return p.button(label, func(ctx context.Context, it ui.Interaction) error {
		p.goTo(ctx, it, page, "")
		return nil
	}, o)
}

func (p *Panel) navBack(row int) { p.nav(p.t("ui.nav.home"), "main", btn{emoji: "🏠", row: row}) }

// localize 送出前的保險翻譯（非繁中時）：畫面與按鈕、選單裡還留著的中文詞句查 i18n 文字表換掉（i18n.Fallback）。
func (p *Panel) localize(e *ui.Embed) {
	if p.lang == i18n.Default {
		return
	}
	// 伺服器名稱、玩家名稱是玩家自己取的，先換成記號，翻完再放回去（不會被當成詞句翻掉）
	var keep []string
	for _, name := range []string{p.groupName, p.who.UserName} {
		if strings.TrimSpace(name) != "" {
			keep = append(keep, name)
		}
	}
	f := func(s string) string {
		for i, name := range keep {
			s = strings.ReplaceAll(s, name, fmt.Sprintf("\x00%d\x00", i))
		}
		s = i18n.Fallback(p.lang, s)
		for i, name := range keep {
			s = strings.ReplaceAll(s, fmt.Sprintf("\x00%d\x00", i), name)
		}
		return s
	}
	if e != nil {
		e.Title, e.Description, e.Footer, e.AuthorName = f(e.Title), f(e.Description), f(e.Footer), f(e.AuthorName)
		for i := range e.Fields {
			e.Fields[i].Name, e.Fields[i].Value = f(e.Fields[i].Name), f(e.Fields[i].Value)
		}
	}
	for _, b := range p.View.Buttons() {
		b.Label = cut(f(b.Label), 80)
	}
	for _, sel := range p.View.Selects() {
		sel.Placeholder = cut(f(sel.Placeholder), 150)
		for i := range sel.Options {
			sel.Options[i].Label = cut(f(sel.Options[i].Label), 100)
			sel.Options[i].Description = cut(f(sel.Options[i].Description), 100)
		}
	}
}

// dur 時間長度（long = 較長時間的格式，例如「1 天 3 小時」）。
func (p *Panel) dur(long bool, seconds int) string {
	return services.DurationLang(p.lang, seconds, long)
}

// msg 錯誤在面板語言的說明。
func (p *Panel) msg(err error) string { return PlayerMessageLang(p.lang, err) }

// s 中文原文在面板語言的翻譯（i18n/<語言>.text.json；繁中或沒有翻譯時回傳原文）。
func (p *Panel) s(zh string) string { return i18n.Text(p.lang, zh) }

// f 翻譯格式字串後代入參數（fmt.Sprintf 的多語版本）。
func (p *Panel) f(format string, args ...any) string { return i18n.Sprintf(p.lang, format, args...) }

// t 依面板的語言取文字（i18n/<語言>.json）。
func (p *Panel) t(key string, args ...any) string { return i18n.T(p.lang, key, args...) }

// isBackLabel 換頁按鈕的標籤以「回」或「返回」開頭時視為返回類按鈕（2026-11-16 只套用在 nav 建立的換頁按鈕）。
func isBackLabel(label string) bool {
	return strings.HasPrefix(label, "回") || strings.HasPrefix(label, "返回") ||
		strings.HasPrefix(label, "Back") || label == i18n.T(i18n.English, "ui.nav.home")
}

// placeBackButtons 2026-11-16 統一返回按鈕的位置：各頁建好元件後，把返回類按鈕移到最後一列的最右邊
// （「回上一層」在前、「🏠 回總覽」在最右）。最後一列是選單或放不下時改放下一列；已經是第 5 列放不下時維持原位。
func (p *Panel) placeBackButtons() {
	marked := p.backButtons
	p.backButtons = nil
	var back []*ui.Button
	for _, b := range p.View.Buttons() {
		if slices.Contains(marked, b) {
			back = append(back, b)
		}
	}
	if len(back) == 0 {
		return
	}
	slices.SortStableFunc(back, func(a, b *ui.Button) int { // 回總覽放最右邊
		ah, bh := a.Label == "回總覽", b.Label == "回總覽"
		switch {
		case ah == bh:
			return 0
		case ah:
			return 1
		default:
			return -1
		}
	})
	// 其他元件佔用的列與數量
	// action：該列有操作按鈕（綠色主要操作、紅色確認、藍色數量／選取），返回鍵不和它們擠在同一列，避免按錯
	last, used, hasSelect, action := -1, map[int]int{}, map[int]bool{}, map[int]bool{}
	for _, it := range p.View.Items() {
		if b, ok := it.(*ui.Button); ok && slices.Contains(back, b) {
			continue
		}
		r := it.ItemRow()
		last = max(last, r)
		switch x := it.(type) {
		case *ui.Select:
			hasSelect[r] = true
			used[r] += ui.MaxRowItems
		case *ui.Button:
			used[r]++
			if x.Style == ui.Success || x.Style == ui.Danger || x.Style == ui.Primary {
				action[r] = true
			}
		}
	}
	target := -1
	switch {
	case last >= 0 && !hasSelect[last] && !action[last] && used[last]+len(back) <= ui.MaxRowItems:
		target = last
	case last+1 < ui.MaxRows && len(back) <= ui.MaxRowItems:
		target = last + 1
	}
	if target < 0 {
		return
	}
	for _, b := range back {
		p.View.MoveLast(b, target)
	}
}

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
	p.add(s)
	return s
}

// quantityButtons 依 picker 顯示數量按鈕（1／5／10／全部／自訂）。
func (p *Panel) quantityButtons(row int) {
	pk := p.picker
	if pk == nil {
		return
	}
	if pk.Short != "" { // 買不起：不顯示「買 1」讓玩家點了才看到錯誤，直接寫出差多少
		p.button(pk.Short, p.noop, btn{emoji: "💸", row: row, disabled: true})
		return
	}
	if pk.Confirm { // 高價物品：只有一個確認按鈕
		p.button(p.f("確認購買（%s）", pk.PriceText), func(ctx context.Context, it ui.Interaction) error {
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
		label := fmt.Sprintf("%s %d", verb, q)
		if pk.LabelFor != nil {
			label = pk.LabelFor(q)
		}
		p.button(label, func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, func(ctx context.Context) (string, error) { return pk.Run(ctx, q) }, actOpts{keep: pk.Keep, noProgress: pk.NoProgress, after: pk.After})
			return nil
		}, btn{row: row, style: ui.Primary})
	}
	p.button(p.s("自訂數量…"), func(ctx context.Context, it ui.Interaction) error {
		return it.SendModal(ctx, quantityModal(pk.Label, p.s("數量"), pk.Max, func(ctx context.Context, mit ui.Interaction, q int) {
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

// renameCostText 改名價格的文字（0 = 免費）。
func renameCostText(cost int) string {
	if cost == 0 {
		return "免費"
	}
	return money(cost) + " 金幣"
}

// renameModal 更改農場名稱：預先填入目前名稱，送出後才扣金幣。
func renameModal(current string, cost int, onSubmit func(ctx context.Context, it ui.Interaction, name string)) *ui.Modal {
	return &ui.Modal{
		Title: cut(fmt.Sprintf("更改農場名稱（%s）", renameCostText(cost)), 45),
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
