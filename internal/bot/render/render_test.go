package render

// 對應 Python tests/test_discord_bot.py 的 test_long_output_is_split_or_attached（指令回覆的附件部分在指令那一批）。

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLongOutputIsSplitOrAttached(t *testing.T) {
	if got := ToMessages(Reply{Text: strings.Repeat("x", 30) + "\n", Code: true}); !slices.Equal(got, []string{"```\n" + strings.Repeat("x", 30) + "\n```"}) {
		t.Fatal(got)
	}
	var lines []string
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("第 %d 行 ", i)+strings.Repeat("資料", 20))
	}
	text := strings.Join(lines, "\n")
	chunks := ToMessages(Reply{Text: text, Code: true})
	if len(chunks) <= 1 {
		t.Fatal(len(chunks))
	}
	for _, c := range chunks {
		if utf8.RuneCountInString(c) > DiscordLimit {
			t.Fatal(utf8.RuneCountInString(c))
		}
	}
	if ToMessages(Reply{Text: strings.Repeat(text, 10), Code: true}) != nil {
		t.Fatal("太長應改用附件")
	}
}

func TestEscapeAndSplit(t *testing.T) {
	// Go 版：``` 不會提前結束程式碼區塊；單行過長硬切；空白內容
	got := ToMessages(Reply{Text: "a```b", Code: true})
	if len(got) != 1 || strings.Count(got[0], "```") != 2 {
		t.Fatal(got)
	}
	if c := SplitLines(strings.Repeat("字", 25), 10); !slices.Equal(c, []string{strings.Repeat("字", 10), strings.Repeat("字", 10), strings.Repeat("字", 5)}) {
		t.Fatal(c)
	}
	if got := ToMessages(Reply{Text: "  ", Code: false}); !slices.Equal(got, []string{"（沒有內容）"}) {
		t.Fatal(got)
	}
	if c := SplitLines("a\r\nb\rc\n", 100); !slices.Equal(c, []string{"a\nb\nc"}) {
		t.Fatal(c)
	}
}
