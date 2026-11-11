// Package numfmt 提供與 Python 行為一致的數值工具：千分位格式（f"{n:,}"）、
// 銀行家捨入（Python round()）與向下取整除法（Python //、%）。
package numfmt

import (
	"math"
	"strconv"
	"strings"
)

// Comma 等同 Python f"{n:,}"。
func Comma(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Round 等同 Python round(x)（四捨六入五成雙），回傳 int。
func Round(x float64) int {
	return int(math.RoundToEven(x))
}

// RoundN 等同 Python round(x, n)（以十進位近似，n 位小數）。
func RoundN(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.RoundToEven(x*p) / p
}

// FloorDiv 等同 Python a // b（向負無限大取整）。
func FloorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Mod 等同 Python a % b（結果與 b 同號）。
func Mod(a, b int) int {
	m := a % b
	if m != 0 && ((m < 0) != (b < 0)) {
		m += b
	}
	return m
}
