// Package ui Discord 互動介面的抽象層（對應 discord.py 的 Embed、ui.View、Button、Select、Modal 與 Interaction）。
//
// 面板與指令只依賴這個套件，不直接使用 Discord 函式庫：正式環境由 discordgo 轉接（internal/bot/discordio），
// 測試用 uitest.Interaction 模擬（與 Python 測試的 FakeInteraction 相同，不連到真實 Discord）。
//
// 與 discord.py 的對應：
//   - View：元件（按鈕、下拉選單）的集合，有逾時；每次重繪先 Clear 再加入新元件。元件的 custom_id 由 Registry 產生，
//     每次加入都是新的 ID（不重複使用），舊訊息上的元件被點擊時找不到處理函式，與 discord.py 相同（Discord 顯示互動失敗）。
//   - 同一個 View 的回呼依序執行（Go 的處理函式在不同 goroutine，View 的狀態不能同時修改；discord.py 在單一事件迴圈中）。
//     等待前一個回呼的時間計入「直接回覆」的時限：超過時先 defer（Python _in_time 的行為）。
package ui

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"
)

// Discord 的限制。
const (
	MaxRowItems   = 5  // 一列最多 5 個按鈕（下拉選單獨佔一列）
	MaxRows       = 5  // 一則訊息最多 5 列
	MaxOptions    = 25 // 下拉選單最多 25 個選項
	EmbedLimit    = 6000
	MaxTitle      = 256
	MaxDesc       = 4096
	MaxFieldName  = 256
	MaxFieldValue = 1024
	MaxFooter     = 2048
)

// Style 按鈕樣式（數值與 Discord API 相同）。
type Style int

const (
	Primary   Style = 1
	Secondary Style = 2
	Success   Style = 3
	Danger    Style = 4
	Link      Style = 5
)

// Field Embed 欄位。
type Field struct {
	Name, Value string
	Inline      bool
}

// Embed 訊息卡片。Image 為圖片網址（附件用 "attachment://檔名"）。
type Embed struct {
	Title       string
	Description string
	URL         string
	Color       int
	Fields      []Field
	Footer      string
	FooterIcon  string
	Image       string
	Thumbnail   string
	AuthorName  string
	AuthorIcon  string
}

// AddField 加入欄位（Python embed.add_field）。
func (e *Embed) AddField(name, value string, inline bool) *Embed {
	e.Fields = append(e.Fields, Field{name, value, inline})
	return e
}

// Len Python len(embed)：標題、說明、欄位名稱與內容、頁尾、作者名稱的字元數總和（Discord 限制 6000）。
func (e *Embed) Len() int {
	n := utf8.RuneCountInString(e.Title) + utf8.RuneCountInString(e.Description) +
		utf8.RuneCountInString(e.Footer) + utf8.RuneCountInString(e.AuthorName)
	for _, f := range e.Fields {
		n += utf8.RuneCountInString(f.Name) + utf8.RuneCountInString(f.Value)
	}
	return n
}

// File 要上傳的附件。
type File struct {
	Name string
	Data []byte
}

// Handler 元件被點擊時的處理函式。
type Handler func(ctx context.Context, it Interaction) error

// Item 按鈕或下拉選單。
type Item interface {
	ItemRow() int
	isItem()
}

// Button 按鈕。URL 不為空時是連結按鈕（沒有回呼）。
type Button struct {
	Label    string
	Emoji    string
	Style    Style
	Row      int
	Disabled bool
	URL      string
	OnClick  Handler
	customID string
}

func (b *Button) ItemRow() int { return b.Row }
func (*Button) isItem()        {}

// CustomID Registry 指定的 ID（連結按鈕為空）。
func (b *Button) CustomID() string { return b.customID }

// SelectOption 下拉選單選項。
type SelectOption struct {
	Label, Value, Description, Emoji string
	Default                          bool
}

// Select 下拉選單。單選（MaxValues ≤ 1）時 OnSelect 收到選取的值；可複選（discord.py min_values／max_values）時
// OnSelectMany 收到所有選取的值（依 Discord 傳來的順序）。
type Select struct {
	Placeholder  string
	Row          int
	Options      []SelectOption
	Disabled     bool
	MinValues    int // 0 視為 1
	MaxValues    int // 0 視為 1
	OnSelect     func(ctx context.Context, it Interaction, value string) error
	OnSelectMany func(ctx context.Context, it Interaction, values []string) error
	customID     string
}

func (s *Select) ItemRow() int { return s.Row }
func (*Select) isItem()        {}

// CustomID Registry 指定的 ID。
func (s *Select) CustomID() string { return s.customID }

// TextInput 輸入框（Modal 的欄位）。
type TextInput struct {
	Label       string
	Placeholder string
	Default     string
	MaxLength   int
	Required    bool
	Paragraph   bool
}

// Modal 對話框；OnSubmit 依欄位順序收到輸入值。
type Modal struct {
	Title    string
	Inputs   []TextInput
	Timeout  time.Duration // 0：5 分鐘（discord.py Modal timeout=300）
	OnSubmit func(ctx context.Context, it Interaction, values []string) error
	customID string
}

// CustomID Registry 指定的 ID。
func (m *Modal) CustomID() string { return m.customID }

// ErrRowFull 一列超過 5 個按鈕、下拉選單與按鈕同列或超過 5 列（discord.py add_item 的 ValueError）。
type ErrRowFull struct{ Row int }

func (e ErrRowFull) Error() string { return fmt.Sprintf("第 %d 列已滿", e.Row) }

// Attachments 編輯訊息時附件的處理方式（discord.py edit 的 attachments 參數）。
type Attachments struct {
	Files []File // 上傳新檔案並取代訊息上的附件；Files 與 Keep 都是零值時移除所有附件
	Keep  bool   // 保留訊息上現有的附件，不重新上傳
}

// Message 要送出或更新的訊息內容。Content／Embed 為 nil 表示不設定（編輯時不變更）。
type Message struct {
	Content     *string
	Embed       *Embed
	View        *View // 編輯時 nil 表示不變更元件；要移除元件請傳空的 View
	Files       []File
	Attachments *Attachments // 只用於編輯；nil 表示不變更
	Ephemeral   bool         // 只用於新訊息（send_message、followup）
}

// Text 只有文字的訊息。
func Text(s string) Message { return Message{Content: &s} }

// SentMessage 已送出的訊息（followup 回傳），可之後編輯（例如面板逾時時停用元件）。
type SentMessage interface {
	Edit(ctx context.Context, m Message) error
}

// Interaction 一次互動（斜線指令、元件點擊或對話框送出）。回應規則與 Discord 相同：
// 首次回應（SendMessage、SendModal、Defer、EditMessage）只能一次，之後用 EditOriginal、Followup。
type Interaction interface {
	UserID() string
	UserName() string // 顯示名稱（伺服器暱稱優先）
	AvatarURL() string
	GuildID() string // 私訊時為 ""
	GuildName() string
	// MessageAttachments 元件所在訊息目前的附件檔名（沿用既有圖片時使用）；指令互動為 nil。
	MessageAttachments() []string

	Responded() bool
	SendMessage(ctx context.Context, m Message) error
	SendModal(ctx context.Context, m *Modal) error
	// Defer 先回應「處理中」：指令互動顯示思考中（ephemeral 決定之後的訊息是否私密）；元件互動之後改編輯原訊息。
	Defer(ctx context.Context, ephemeral bool) error
	EditMessage(ctx context.Context, m Message) error // 元件互動：直接以新內容更新原訊息
	// Show 元件互動更新畫面：尚未回應時直接回應（EditMessage），已 defer 時編輯原訊息（EditOriginal）。
	// 判斷與送出是原子的：不會和 Registry 的自動 defer 互相搶（Python _show 在單一事件迴圈中不需要）。
	Show(ctx context.Context, m Message) error
	// DeferUpdateIfPending 尚未回應時先 defer（元件互動之後改編輯原訊息）；已回應時不做任何事。
	DeferUpdateIfPending(ctx context.Context) error
	EditOriginal(ctx context.Context, m Message) error
	DeleteOriginal(ctx context.Context) error
	Followup(ctx context.Context, m Message) (SentMessage, error)
}
