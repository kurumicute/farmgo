package i18n

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// 每個語言的 key 要和繁體中文一模一樣，參數 {名稱} 也要相同，文字不可空白（改 JSON 後跑這個測試就知道有沒有漏）。
func TestLanguagesMatchDefault(t *testing.T) {
	param := regexp.MustCompile(`\{[a-z_]+\}`)
	base := Keys(Default)
	if len(base) == 0 {
		t.Fatal("繁體中文沒有任何字串")
	}
	for _, lang := range Langs() {
		keys := Keys(lang)
		for _, k := range base {
			if !slices.Contains(keys, k) {
				t.Errorf("%s.json 缺少 %s", lang, k)
			}
		}
		for _, k := range keys {
			if !slices.Contains(base, k) {
				t.Errorf("%s.json 多了 %s（繁體中文沒有）", lang, k)
			}
			v := T(lang, k)
			if v == "" {
				t.Errorf("%s.json 的 %s 是空白", lang, k)
			}
			if a, b := param.FindAllString(T(Default, k), -1), param.FindAllString(v, -1); !slices.Equal(sortCopy(a), sortCopy(b)) {
				t.Errorf("%s.json 的 %s 參數不同：%v ↔ %v", lang, k, a, b)
			}
		}
	}
}

func sortCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func TestT(t *testing.T) {
	if T("en", "core.gold") != "Gold" || T("zh-TW", "core.gold") != "金幣" {
		t.Fatal(T("en", "core.gold"))
	}
	if got := T("en", "core.plot_number", "n", 3); got != "Plot #3" {
		t.Fatal(got)
	}
	if got := T("fr", "core.gold"); got != "金幣" { // 不支援的語言退回繁體中文
		t.Fatal(got)
	}
	if got := T("en", "no.such_key"); got != "no.such_key" {
		t.Fatal(got)
	}
	if !Supported("en") || !Supported("zh-TW") || Supported("fr") {
		t.Fatal(Langs())
	}
}

func TestResolve(t *testing.T) {
	en := "en"
	for _, c := range []struct {
		chosen *string
		locale string
		want   string
	}{{nil, "zh-TW", "zh-TW"}, {nil, "zh-CN", "zh-TW"}, {nil, "en-US", "en"}, {nil, "ja", "en"}, {nil, "", "zh-TW"}, {&en, "zh-TW", "en"}} {
		if got := Resolve(c.chosen, c.locale); got != c.want {
			t.Errorf("%v %s → %s", c.chosen, c.locale, got)
		}
	}
	if LangOf(WithLang(context.Background(), "en")) != "en" || LangOf(context.Background()) != Default {
		t.Fatal("ctx")
	}
}

// 文字表：翻譯要保留和原文一樣的格式符號（%d、%s…，%[2]d 這種指定位置的也算），否則代入參數會錯。
func TestTextVerbs(t *testing.T) {
	verbs := regexp.MustCompile(`%(\[\d+\])?[-+#0]*\d*(\.\d+)?[a-zA-Z%]`)
	kinds := func(s string) []string {
		var out []string
		for _, v := range verbs.FindAllString(s, -1) {
			if v != "%%" {
				out = append(out, v[len(v)-1:])
			}
		}
		slices.Sort(out)
		return out
	}
	for lang, m := range texts {
		for zh, tr := range m {
			if a, b := kinds(zh), kinds(tr); !slices.Equal(a, b) {
				t.Errorf("%s.text.json「%s」的格式符號不同：%v ↔ %v", lang, zh, a, b)
			}
			if strings.TrimSpace(tr) == "" && len([]rune(strings.TrimSpace(zh))) > 1 { // 單一個字的單位可以是空字串
				t.Errorf("%s.text.json「%s」的翻譯是空白", lang, zh)
			}
		}
	}
	if Text("en", "不存在的句子") != "不存在的句子" || Text("zh-TW", "x") != "x" {
		t.Fatal("沒有翻譯時要回傳原文")
	}
}

func TestFallback(t *testing.T) {
	if got := Fallback("en", "🎁 化肥 ×5、人工養殖蚯蚓 ×2"); got != "🎁 Fertilizer ×5, Farm-Raised Earthworms ×2" {
		t.Fatal(got)
	}
	if got := Fallback("zh-TW", "化肥"); got != "化肥" {
		t.Fatal(got)
	}
}

func TestFallbackFormats(t *testing.T) {
	// 「%s 金幣、約 %s、得到 %d–%d 顆」是文字表裡的樣板：組好的中文句子也能換成英文
	if got := Fallback("en", "1,000 金幣、約 2h、得到 1–3 顆"); got != "1,000 Gold, about 2h, gets 1–3 seeds" {
		t.Fatal(got)
	}
}
