// Package render Discord 文字訊息格式（對應 Python app/discord_bot/render.py）：放進程式碼區塊並依 Discord 長度限制分段。
// 長度以字元計算（Python len）。
package render

import (
	"strings"
	"unicode/utf8"
)

const (
	DiscordLimit = 2000
	ChunkLimit   = 1900 // 保留程式碼區塊標記的空間
	MaxMessages  = 4    // 超過時改以文字檔附件回覆，避免洗版與觸發速率限制
)

// Reply 服務層執行結果；Code 為 true 時以等寬程式碼區塊顯示（表格對齊）。
type Reply struct {
	Text string
	Code bool
}

// zwsp 零寬空格：放在 ``` 中間，防止內容提前結束程式碼區塊。
var zwsp = string(rune(0x200B))

func escape(text string) string { return strings.ReplaceAll(text, "```", "`"+zwsp+"``") }

// SplitLines 依行分段，每段最多 limit 個字元；單行過長時硬切。
func SplitLines(text string, limit int) []string {
	var chunks []string
	current := ""
	lines := splitLinesPy(text)
	if len(lines) == 0 {
		lines = []string{""}
	}
	for _, line := range lines {
		for utf8.RuneCountInString(line) > limit {
			if current != "" {
				chunks = append(chunks, current)
				current = ""
			}
			r := []rune(line)
			chunks = append(chunks, string(r[:limit]))
			line = string(r[limit:])
		}
		candidate := line
		if current != "" {
			candidate = current + "\n" + line
		}
		if utf8.RuneCountInString(candidate) > limit {
			chunks = append(chunks, current)
			current = line
		} else {
			current = candidate
		}
	}
	if current != "" || len(chunks) == 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// splitLinesPy Python str.splitlines()（\n、\r\n、\r 都是換行；結尾的換行不產生空行）。
func splitLinesPy(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// ToMessages 要依序送出的訊息內容；太長時回傳 nil（改用附件）。
func ToMessages(r Reply) []string {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		text = "（沒有內容）"
	}
	var chunks []string
	if r.Code {
		for _, c := range SplitLines(escape(text), ChunkLimit) {
			chunks = append(chunks, "```\n"+c+"\n```")
		}
	} else {
		chunks = SplitLines(text, DiscordLimit-50)
	}
	if len(chunks) > MaxMessages {
		return nil
	}
	return chunks
}
