// Package discordio 把 internal/bot/ui 的互動抽象層接到 Discord（github.com/bwmarrin/discordgo）。
//
// 面板與指令只依賴 ui；這裡負責：
//   - Interaction：ui.Interaction 的實作（回應、defer、編輯原訊息、followup、對話框）；
//   - 訊息內容轉成 Discord API 的 JSON（Embed、元件、附件、對話框）；
//   - Bot 本體（bot.go）：連線、同步指令、伺服器對應群組、背景結算與延遲彙總。
//
// Discord API 呼叫用 discordgo.Session 的 RequestRaw（連線、速率限制、重試沿用 discordgo），但 JSON 由本套件產生：
// discordgo 的元件型別會送出空字串的 label／description，附件也採舊版（v9）語意；這裡依 discord.py 的行為
// 只送有值的欄位，並以 attachments 陣列明確指定訊息上要保留或上傳的附件。
package discordio

import (
	"strconv"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot/ui"
)

// 互動回應類型（Discord API interaction callback type）。
const (
	respMessage        = 4 // CHANNEL_MESSAGE_WITH_SOURCE
	respDeferMessage   = 5 // DEFERRED_CHANNEL_MESSAGE_WITH_SOURCE（指令：顯示思考中）
	respDeferUpdate    = 6 // DEFERRED_UPDATE_MESSAGE（元件：之後編輯原訊息）
	respUpdateMessage  = 7 // UPDATE_MESSAGE
	respModal          = 9 // MODAL
	flagEphemeral      = 1 << 6
	componentRow       = 1
	componentButton    = 2
	componentSelect    = 3
	componentTextInput = 4
)

// wireMessage 訊息內容（新訊息、編輯、互動回應的 data 共用）。指標欄位為 nil 時不送（編輯時表示不變更）。
type wireMessage struct {
	Content         *string                    `json:"content,omitempty"`
	Embeds          *[]*discordgo.MessageEmbed `json:"embeds,omitempty"`
	Components      *[]wireComponent           `json:"components,omitempty"`
	Attachments     *[]wireAttachment          `json:"attachments,omitempty"`
	Flags           int                        `json:"flags,omitempty"`
	AllowedMentions *wireMentions              `json:"allowed_mentions,omitempty"`
	// 對話框
	CustomID string `json:"custom_id,omitempty"`
	Title    string `json:"title,omitempty"`
}

// wireMentions Python 的 Bot 設定 allowed_mentions=AllowedMentions.none()：所有訊息都不 @ 任何人。
type wireMentions struct {
	Parse []string `json:"parse"`
}

var noMentions = &wireMentions{Parse: []string{}}

// wireAttachment 訊息上要有的附件：新上傳的檔案以 multipart 的索引為 id。
type wireAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename,omitempty"`
}

// wireResponse 互動回應。
type wireResponse struct {
	Type int          `json:"type"`
	Data *wireMessage `json:"data,omitempty"`
}

// wireComponent 元件（列、按鈕、下拉選單、輸入框）；只送有值的欄位。
type wireComponent struct {
	Type        int             `json:"type"`
	Components  []wireComponent `json:"components,omitempty"`
	CustomID    string          `json:"custom_id,omitempty"`
	Style       int             `json:"style,omitempty"`
	Label       string          `json:"label,omitempty"`
	Emoji       *wireEmoji      `json:"emoji,omitempty"`
	URL         string          `json:"url,omitempty"`
	Disabled    bool            `json:"disabled,omitempty"`
	Placeholder string          `json:"placeholder,omitempty"`
	MinValues   *int            `json:"min_values,omitempty"`
	MaxValues   int             `json:"max_values,omitempty"`
	Options     []wireOption    `json:"options,omitempty"`
	// 輸入框
	Value     string `json:"value,omitempty"`
	Required  *bool  `json:"required,omitempty"`
	MaxLength int    `json:"max_length,omitempty"`
}

type wireEmoji struct {
	Name string `json:"name"`
}

type wireOption struct {
	Label       string     `json:"label"`
	Value       string     `json:"value"`
	Description string     `json:"description,omitempty"`
	Emoji       *wireEmoji `json:"emoji,omitempty"`
	Default     bool       `json:"default,omitempty"`
}

func emojiOf(s string) *wireEmoji {
	if s == "" {
		return nil
	}
	return &wireEmoji{Name: s}
}

// embedOf ui.Embed → Discord embed。
func embedOf(e *ui.Embed) *discordgo.MessageEmbed {
	out := &discordgo.MessageEmbed{Title: e.Title, Description: e.Description, URL: e.URL, Color: e.Color}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, &discordgo.MessageEmbedField{Name: f.Name, Value: f.Value, Inline: f.Inline})
	}
	if e.Footer != "" {
		out.Footer = &discordgo.MessageEmbedFooter{Text: e.Footer}
	}
	if e.Image != "" {
		out.Image = &discordgo.MessageEmbedImage{URL: e.Image}
	}
	if e.Thumbnail != "" {
		out.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: e.Thumbnail}
	}
	if e.AuthorName != "" {
		out.Author = &discordgo.MessageEmbedAuthor{Name: e.AuthorName, IconURL: e.AuthorIcon}
	}
	return out
}

// componentsOf View → 元件列（依 Row 分組，與 discord.py 相同）。
func componentsOf(v *ui.View) []wireComponent {
	rows := []wireComponent{}
	for _, row := range v.Rows() {
		if len(row) == 0 {
			continue
		}
		r := wireComponent{Type: componentRow}
		for _, it := range row {
			switch x := it.(type) {
			case *ui.Button:
				b := wireComponent{Type: componentButton, Style: int(x.Style), Label: x.Label, Emoji: emojiOf(x.Emoji), Disabled: x.Disabled}
				if x.URL != "" {
					b.Style, b.URL = int(ui.Link), x.URL
				} else {
					b.CustomID = x.CustomID()
				}
				r.Components = append(r.Components, b)
			case *ui.Select:
				minV, maxV := max(1, x.MinValues), max(1, x.MaxValues)
				s := wireComponent{Type: componentSelect, CustomID: x.CustomID(), Placeholder: x.Placeholder, MinValues: &minV,
					MaxValues: maxV, Disabled: x.Disabled}
				for _, o := range x.Options {
					s.Options = append(s.Options, wireOption{Label: o.Label, Value: o.Value, Description: o.Description,
						Emoji: emojiOf(o.Emoji), Default: o.Default})
				}
				r.Components = append(r.Components, s)
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// modalOf 對話框（每個輸入框一列；custom_id 為 f0、f1…，送出時依序取回）。
func modalOf(m *ui.Modal) *wireMessage {
	rows := make([]wireComponent, len(m.Inputs))
	for i, in := range m.Inputs {
		style, required := 1, in.Required
		if in.Paragraph {
			style = 2
		}
		rows[i] = wireComponent{Type: componentRow, Components: []wireComponent{{Type: componentTextInput, CustomID: "f" + strconv.Itoa(i),
			Style: style, Label: in.Label, Placeholder: in.Placeholder, Value: in.Default, Required: &required, MaxLength: in.MaxLength}}}
	}
	return &wireMessage{CustomID: m.CustomID(), Title: m.Title, Components: &rows}
}

// encoded 一則要送出的訊息：JSON 內容與要上傳的檔案。
type encoded struct {
	msg   *wireMessage
	files []ui.File
}

// encode ui.Message → Discord 訊息。edit 為 true 時，nil 的欄位不送（不變更）；新訊息時 Attachments 不使用。
//
// 附件（Python discord.py 的 attachments 參數）：Files 有檔案時上傳並只保留這些檔案；Keep 時不送附件欄位（保留原有）；
// Attachments 不為 nil 且沒有檔案、也不是 Keep 時移除所有附件。新訊息的 Files 與 Attachments.Files 都會上傳。
func encode(m ui.Message, edit bool) encoded {
	w := &wireMessage{Content: m.Content, AllowedMentions: noMentions}
	if m.Embed != nil {
		w.Embeds = &[]*discordgo.MessageEmbed{embedOf(m.Embed)}
	}
	if m.View != nil {
		c := componentsOf(m.View)
		w.Components = &c
	}
	if m.Ephemeral && !edit {
		w.Flags = flagEphemeral
	}
	files := m.Files
	if m.Attachments != nil && !m.Attachments.Keep {
		files = append(append([]ui.File{}, files...), m.Attachments.Files...)
		if len(files) == 0 {
			w.Attachments = &[]wireAttachment{}
		}
	}
	if len(files) > 0 {
		refs := make([]wireAttachment, len(files))
		for i, f := range files {
			refs[i] = wireAttachment{ID: strconv.Itoa(i), Filename: f.Name}
		}
		w.Attachments = &refs
	}
	return encoded{w, files}
}
