package bot

// 面板的顯示工具（對應 Python app/discord_bot/panels.py 的「顯示工具」與 fishing_panel.fish_line）。
// 字串長度一律以字元計算（Python len）。

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
	"farmgm/internal/services"
)

// Embed 顏色。
const (
	Green  = 0x57F287
	Blue   = 0x5865F2
	Gold   = 0xF1C40F
	Red    = 0xED4245
	Grey   = 0x99AAB5
	Purple = 0x9B59B6 // 賭場
)

var (
	CropEmoji = map[string]string{"wheat": "🌾", "carrot": "🥕", "tomato": "🍅", "strawberry": "🍓", "amber_melon": "🍈",
		"moonflower": "🌙", "corn": "🌽", "pumpkin": "🎃", "grape": "🍇", "watermelon": "🍉"}
	AnimalEmoji   = map[string]string{"chicken": "🐔", "cow": "🐄", "sheep": "🐑"}
	MaterialEmoji = map[string]string{"wood": "🪵", "stone": "🪨", "copper_ore": "🟠", "iron_ore": "⛓️", "resin": "🍯",
		"herb": "🌿", "gear": "⚙️", "crystal": "💎", "ancient_core": "🌳", "machine_core": "🔩", "fake_coin": "🪙"}
	ItemEmoji = map[string]string{"fertilizer": "🧪", "earthworm": "🪱", "auto_waterer": "🚿", "patrol_ticket": "🔦",
		"auto_planter": "🤖", "auto_seller": "🏧", "thief_glove": "🧤"}
	PlotEmoji = map[rules.PlotState]string{rules.PlotEmpty: "⬜", rules.PlotWaitingWater: "💧", rules.PlotGrowing: "🌱",
		rules.PlotReady: "✅", rules.PlotWithered: "🥀"}
	// plotStateOrder Python PlotState 列舉的順序
	plotStateOrder = []rules.PlotState{rules.PlotEmpty, rules.PlotWaitingWater, rules.PlotGrowing, rules.PlotReady, rules.PlotWithered}
	Medals         = map[int]string{1: "🥇", 2: "🥈", 3: "🥉"}
	KindNames      = map[string]string{"money": "金幣", "sales": "農產品販售", "goods": "商品總販售"}
	PeriodNames    = map[string]string{"all": "全部期間", "day": "本日", "week": "本週", "month": "本月"}
	ScopeNames     = map[string]string{"global": "個人總排行", "group": "本伺服器排行", "groups": "群組總排行"}
)

// ts Discord 動態時間戳（玩家端依自己的時區顯示並自動倒數）；style 預設 "R"。
func ts(t *time.Time, style ...string) string {
	if t == nil {
		return "—"
	}
	s := "R"
	if len(style) > 0 {
		s = style[0]
	}
	return fmt.Sprintf("<t:%d:%s>", t.Unix(), s)
}

// cut 超過 limit 個字元時截斷並加上「…」。
func cut(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	r := []rune(text)
	return string(r[:limit-1]) + "…"
}

// bar 進度條；width 預設 10。
func bar(value, total, width int) string {
	filled := 0
	if total > 0 {
		filled = min(width, numfmt.Round(float64(width)*float64(value)/float64(total)))
	}
	filled = max(0, filled)
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

func cropEmoji(code string) string {
	if e, ok := CropEmoji[code]; ok {
		return e
	}
	return "🌱"
}

func lookupOr(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}

func money(n int) string { return numfmt.Comma(n) }

// linesOr 把多行文字放進 Embed 欄位（上限 1024 字），超過時截斷並註明；沒有內容時回傳 empty。
func linesOr(lines []string, empty string, limit int) string {
	var out []string
	used := 0
	for i, line := range lines {
		n := utf8.RuneCountInString(line)
		if used+n+1 > limit-20 {
			out = append(out, fmt.Sprintf("…另外 %d 項", len(lines)-i))
			break
		}
		out = append(out, line)
		used += n + 1
	}
	if s := strings.Join(out, "\n"); s != "" {
		return s
	}
	return empty
}

// Section 頁面上的一個區塊（Python sections 的 tuple）：標題與行；Sep 預設換行。
type Section struct {
	Title string
	Lines []string
	Sep   string
}

// sections 把頁面內容排成有標題、有空行的區塊（Python sections）：string 為段落，Section 為區塊；空的略過。
func sections(parts ...any) string {
	var blocks []string
	for _, part := range parts {
		switch x := part.(type) {
		case Section:
			var lines []string
			for _, l := range x.Lines {
				if l != "" {
					lines = append(lines, l)
				}
			}
			sep := x.Sep
			if sep == "" {
				sep = "\n"
			}
			if len(lines) > 0 {
				if x.Title != "" {
					blocks = append(blocks, "**"+x.Title+"**\n"+strings.Join(lines, sep))
				} else {
					blocks = append(blocks, strings.Join(lines, sep))
				}
			}
		case string:
			if x != "" {
				blocks = append(blocks, x)
			}
		}
	}
	return cut(strings.Join(blocks, "\n\n"), 4000)
}

// cliHint CMD 介面的指令提示，例如「（workshop build 磨坊）」「（buy wheat 1）」，在 Discord 面板中沒有意義。
var cliHint = regexp.MustCompile(`（[a-z][a-z_]*(?: [^）]*)?）`)

func cleanMessage(text string) string { return cliHint.ReplaceAllString(text, "") }

// HighlightMarks 操作結果含這些字時面板換成金色（稀有收穫、新紀錄、成就達成、新圖鑑）。
var HighlightMarks = []string{"🎉", "✨ 稀有收穫", "✨ 偷到稀有貨", "🏆 新紀錄", "🏅 成就達成", "📖 新圖鑑"}

// progressDiff 操作前後的圖鑑與成就標記差異（新圖鑑、新達成的成就）。
func progressDiff(old, now services.ProgressMarks) string {
	var lines []string
	var found []string
	for _, key := range now.CodexOrder {
		if _, ok := old.Codex[key]; !ok {
			found = append(found, now.Codex[key])
		}
	}
	if len(found) > 0 {
		more := ""
		if len(found) > 5 {
			more = fmt.Sprintf(" 等 %d 項", len(found))
		}
		lines = append(lines, fmt.Sprintf("📖 新圖鑑：%s%s（圖鑑 %d/%d）", strings.Join(found[:min(5, len(found))], "、"), more,
			len(now.Codex), now.CodexTotal))
	}
	var reached []string
	for _, code := range now.ReadyOrder {
		if _, ok := old.Ready[code]; !ok {
			reached = append(reached, now.Ready[code])
		}
	}
	if len(reached) > 0 {
		lines = append(lines, fmt.Sprintf("🏅 成就達成：%s！到「🏅 成就圖鑑」領取獎勵", strings.Join(reached[:min(3, len(reached))], "、")))
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("\n" + l)
	}
	return b.String()
}

// quote 操作結果放在頁面頂端的引用區塊（Discord 顯示為左側有直線的區塊），和頁面內容分開。
func quote(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = "> " + l
		} else {
			lines[i] = ">"
		}
	}
	return strings.Join(lines, "\n")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// plotLine 一格土地的說明；owner=false 是查看別人的農田（偷菜），不顯示給主人的操作提示。
func plotLine(p services.PlotInfo, owner bool) string {
	emoji := PlotEmoji[p.State]
	if p.State == rules.PlotEmpty {
		return fmt.Sprintf("%s `#%2d` 空地", emoji, p.PlotNumber)
	}
	genes := services.GeneticsLabel(deref(p.Quality), deref(p.Mutation))
	if deref(p.Mutation) == "variant" {
		genes += "【珍稀變種】"
	}
	name := fmt.Sprintf("%s %s%s", cropEmoji(deref(p.CropCode)), deref(p.CropName), genes)
	var tail string
	switch p.State {
	case rules.PlotWaitingWater:
		tail = "等待澆水"
	case rules.PlotGrowing:
		tail = "成熟 " + ts(p.ReadyAt)
		if p.Fertilized {
			tail += " 🧪"
		}
	case rules.PlotReady:
		qty := 0
		if p.RemainingQuantity != nil {
			qty = *p.RemainingQuantity
		}
		var wither *time.Time
		if p.ReadyAt != nil {
			w := rules.WitherTime(*p.ReadyAt)
			wither = &w
		}
		tail = fmt.Sprintf("可收成 ×%d（%s乾枯）", qty, ts(wither))
	default:
		if owner {
			tail = "已乾枯，請清除"
		} else {
			tail = "已乾枯"
		}
	}
	return fmt.Sprintf("%s `#%2d` %s · %s", emoji, p.PlotNumber, name, tail)
}

// summaryCounts 各狀態的格數（依 PlotState 的順序，沒有的不列）。
func summaryCounts(plots []services.PlotInfo) string {
	var parts []string
	for _, s := range plotStateOrder {
		n := 0
		for _, p := range plots {
			if p.State == s {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s%s %d", PlotEmoji[s], rules.PlotStateLabels[s], n))
		}
	}
	return strings.Join(parts, "　")
}

// fishLine 魚簍中的一條魚（Python fishing_panel.fish_line）；n 為 0 時不顯示編號。
func fishLine(f services.BasketFish, n int) string {
	num := ""
	if n != 0 {
		num = fmt.Sprintf("`%2d` ", n)
	}
	record := ""
	if f.Record {
		record = "　🏆"
	}
	return fmt.Sprintf("%s%s %s **%s**　⚖️ %s　💰 %d 金%s", num, f.RarityEmoji, f.Emoji, f.Name, f.WeightText, f.Price, record)
}
