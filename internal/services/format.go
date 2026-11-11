package services

// 訊息格式化（對應 Python game_service.py 模組層級的函式）。

import (
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"

	eventrules "farmgm/internal/game/events"
)

var (
	RareQualities = []string{"rare", "legendary"}
	RareMutations = []string{"giant", "golden", "variant"}
)

type recordLabel struct{ label, unit string }

var recordLabels = map[string]recordLabel{"harvest_quantity": {"一次收成", "個作物"}, "sale_income": {"一次販售", "金"}}

func RecordNote(key string, value, old int) string {
	l := recordLabels[key]
	return fmt.Sprintf("\n🏆 新紀錄！%s %s %s（舊紀錄 %s）", l.label, numfmt.Comma(value), l.unit, numfmt.Comma(old))
}

// IsRare quality／mutation 為 "" 表示 None。
func IsRare(quality, mutation string) bool {
	return slices.Contains(RareQualities, quality) || slices.Contains(RareMutations, mutation)
}

// GoldChange 金幣變化，例如「💰 3,000 → 4,240」。
func GoldChange(goldAfter, delta int) string {
	return fmt.Sprintf("\n💰 金幣 %s → %s", numfmt.Comma(goldAfter-delta), numfmt.Comma(goldAfter))
}

type nameQty struct {
	name string
	qty  int
}

// groupNames 依名稱加總數量（保留第一次出現的順序）。
func groupNames(details []HarvestDetail) []nameQty {
	var out []nameQty
	idx := map[string]int{}
	for _, d := range details {
		if i, ok := idx[d.Name]; ok {
			out[i].qty += d.Quantity
			continue
		}
		idx[d.Name] = len(out)
		out = append(out, nameQty{d.Name, d.Quantity})
	}
	return out
}

func joinNameQty(groups []nameQty) string {
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = fmt.Sprintf("%s ×%d", g.name, g.qty)
	}
	return strings.Join(parts, "、")
}

// HarvestHighlight 有稀有品質或突變時的醒目第一行；沒有時為空字串。
func HarvestHighlight(details []HarvestDetail) string {
	var rare []HarvestDetail
	legendary := false
	for _, d := range details {
		if IsRare(d.Quality, d.Mutation) {
			rare = append(rare, d)
			if d.Quality == "legendary" || d.Mutation == "golden" || d.Mutation == "variant" {
				legendary = true
			}
		}
	}
	if len(rare) == 0 {
		return ""
	}
	head := "✨ 稀有收穫！"
	if legendary {
		head = "🎉 大豐收！"
	}
	g := groupNames(rare)
	return head + joinNameQty(g[:min(6, len(g))]) + "\n"
}

// HarvestValueNote 收成的價值與寵物加成（單格收成時接在訊息後面）。
func HarvestValueNote(details []HarvestDetail) string {
	sold, kept, cat := 0, 0, 0
	for _, d := range details {
		if d.Sold {
			sold += d.Value
		} else {
			kept += d.Value
		}
		cat += d.CatBonus
	}
	var b strings.Builder
	if kept != 0 {
		fmt.Fprintf(&b, "\n📦 約值 %s 金（到「販售」賣出）", numfmt.Comma(kept))
	}
	if sold != 0 {
		fmt.Fprintf(&b, "\n🏪 自動販售機賣出，金幣 +%s", numfmt.Comma(sold))
	}
	if cat != 0 {
		fmt.Fprintf(&b, "\n🐱 貓咪多收 +%d 個", cat)
	}
	return b.String()
}

// HarvestSummary 一鍵收成的結果：稀有收穫、各作物數量、價值、寵物加成與損失。
func HarvestSummary(details []HarvestDetail) string {
	total, stolen, lost := 0, 0, 0
	for _, d := range details {
		total += d.Quantity
		stolen += d.Stolen
		lost += d.Lost
	}
	groups := groupNames(details)
	shown := joinNameQty(groups[:min(8, len(groups))])
	if len(groups) > 8 {
		shown += fmt.Sprintf(" 等 %d 種", len(groups))
	}
	text := HarvestHighlight(details) + fmt.Sprintf("🧺 收成 %d 格，共 %d 個作物。", len(details), total) + "\n" + shown +
		HarvestValueNote(details)
	var losses []string
	if stolen != 0 {
		losses = append(losses, fmt.Sprintf("被偷走 %d 個", stolen))
	}
	if lost != 0 {
		losses = append(losses, fmt.Sprintf("大風颳走 %d 個", lost))
	}
	if len(losses) > 0 {
		text += "\n😢 " + strings.Join(losses, "、")
	}
	return text
}

// GeneticsLabel 顯示用標籤，例如「【稀有・黃金作物】」；普通且無突變時為空字串。"" 表示 None。
func GeneticsLabel(quality, mutation string) string {
	var parts []string
	if quality != "" && quality != "normal" {
		parts = append(parts, balance.Qualities[quality])
	}
	if mutation != "" && mutation != "none" && mutation != "variant" {
		parts = append(parts, balance.Mutations[mutation])
	}
	if len(parts) == 0 {
		return ""
	}
	return "【" + strings.Join(parts, "・") + "】"
}

// LongDuration 較長時間的顯示，例如「6 天 23 小時 59 分」；不足 1 小時時沿用 FormatDuration。
func LongDuration(seconds int) string {
	seconds = max(0, seconds)
	if seconds < 3600 {
		return rules.FormatDuration(seconds)
	}
	days, rest := seconds/86400, seconds%86400
	hours, rest := rest/3600, rest%3600
	var parts []string
	if days != 0 {
		parts = append(parts, fmt.Sprintf("%d 天", days))
	}
	if hours != 0 {
		parts = append(parts, fmt.Sprintf("%d 小時", hours))
	}
	parts = append(parts, fmt.Sprintf("%d 分", rest/60))
	var out []string
	for _, p := range parts {
		if p != "0 分" || len(parts) == 1 {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

func cooldownText() string { return LongDuration(balance.GroupTransferCooldownSeconds) }

func eventsNote(applied []eventrules.Applied) string {
	if len(applied) == 0 {
		return ""
	}
	names := make([]string, len(applied))
	for i, e := range applied {
		names[i] = e.Name
	}
	return "（活動：" + strings.Join(names, "、") + "）"
}

func joinStr(parts []string, sep string) string { return strings.Join(parts, sep) }

// truncRunes Python 的 s[:n]（以字元計）。
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// taipeiFmt 台灣時間；layout 為 Go 格式。
func taipeiFmt(t time.Time, layout string) string { return t.In(clock.TaipeiTZ).Format(layout) }

// pyG Python f"{x:g}"。
func pyG(x float64) string { return fmt.Sprintf("%.6g", x) }

// mulDuration Python timedelta * float：以精確的有理數相乘，再四捨六入五成雙到微秒。
func mulDuration(d time.Duration, f float64) time.Duration {
	us := new(big.Rat).SetInt64(d.Microseconds())
	r := new(big.Rat).SetFloat64(f)
	prod := new(big.Rat).Mul(us, r)
	num, den := prod.Num(), prod.Denom()
	q, m := new(big.Int).QuoRem(num, den, new(big.Int)) // 向零截斷
	// 調整為向下取整，再依餘數與一半比較
	if m.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
		m.Add(m, den)
	}
	twice := new(big.Int).Lsh(m, 1)
	if c := twice.Cmp(den); c > 0 || (c == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	return time.Duration(q.Int64()) * time.Microsecond
}
