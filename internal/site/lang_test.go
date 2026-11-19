package site

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestLangFromAccept(t *testing.T) {
	for header, want := range map[string]string{
		"":                               "zh-TW",
		"zh-TW,zh;q=0.9,en;q=0.8":        "zh-TW",
		"zh-CN,zh;q=0.9":                 "zh-TW", // 簡體中文也顯示繁體中文
		"zh":                             "zh-TW",
		"en-US,en;q=0.9":                 "en",
		"ja-JP,ja;q=0.9,zh-TW;q=0.8":     "en", // 非中文一律英文
		"en;q=0.5,zh-TW;q=0.9":           "zh-TW",
		"fr-FR":                          "en",
		"*":                              "zh-TW",
		"ko-KR,ko;q=0.9,en-US;q=0.8,*;q": "en",
	} {
		if got := langFromAccept(header); got != want {
			t.Errorf("langFromAccept(%q) = %q，want %q", header, got, want)
		}
	}
}

func langGet(t *testing.T, a *App, target string, header map[string]string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	return w.Result()
}

func resBody(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSiteLanguageSwitch(t *testing.T) {
	a := New(Options{})
	// 英文瀏覽器 → 英文頁面
	res := langGet(t, a, "/", map[string]string{"Accept-Language": "en-US,en;q=0.9"})
	html := resBody(t, res)
	if !strings.Contains(html, `<html lang="en">`) || !strings.Contains(html, `/static/site.js?lang=en`) {
		t.Fatalf("英文瀏覽器應該拿到英文頁面")
	}
	if !strings.Contains(html, `translate="no">中文</a>`) || !strings.Contains(html, `>English</a>`) {
		t.Fatalf("語言切換要保留「中文」與 English")
	}
	if res.Header.Get("Vary") == "" {
		t.Fatalf("依語言回應不同內容，要帶 Vary")
	}
	// 中文瀏覽器 → 原本的頁面
	html = resBody(t, langGet(t, a, "/", map[string]string{"Accept-Language": "zh-TW"}))
	if !strings.Contains(html, `<html lang="zh-Hant">`) || !strings.Contains(html, `<script src="/static/site.js"></script>`) {
		t.Fatalf("中文瀏覽器應該拿到繁體中文頁面")
	}
	// ?lang= 優先，並記在 Cookie
	res = langGet(t, a, "/?lang=en", map[string]string{"Accept-Language": "zh-TW"})
	if !strings.Contains(resBody(t, res), `<html lang="en">`) {
		t.Fatalf("?lang=en 應該顯示英文")
	}
	var saved *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == LangCookie {
			saved = c
		}
	}
	if saved == nil || saved.Value != "en" || !saved.HttpOnly {
		t.Fatalf("應該把語言記在 HttpOnly Cookie：%+v", saved)
	}
	html = resBody(t, langGet(t, a, "/", map[string]string{"Accept-Language": "zh-TW"}, saved))
	if !strings.Contains(html, `<html lang="en">`) {
		t.Fatalf("Cookie 記住的語言優先於瀏覽器語言")
	}
	// 不認得的語言不寫 Cookie
	res = langGet(t, a, "/?lang=xx", nil)
	if len(res.Cookies()) != 0 {
		t.Fatalf("不支援的語言不應寫 Cookie")
	}
	// 英文 site.js
	js := resBody(t, langGet(t, a, "/static/site.js?lang=en", nil))
	if !strings.Contains(js, `"en-US"`) || !strings.Contains(js, "`/api/guide/${part}?lang=en`") {
		t.Fatalf("英文 site.js 應該用英文日期格式並以英文讀取手冊資料")
	}
}

var (
	cjkOnly      = regexp.MustCompile(`[\p{Han}]+`)
	htmlComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	allowedWords = map[string]bool{"中文": true, "油祥農場": true}
)

// TestSiteFullyTranslated 英文頁面（不含註解）不應該留下中文：新增網頁文字時，要在 i18n/site/en.json 補上翻譯。
func TestSiteFullyTranslated(t *testing.T) {
	src, _ := fs.Sub(staticFS, "static")
	data, err := fs.ReadFile(src, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := htmlComment.ReplaceAllString(string(translateHTML(data, "en")), "")
	report(t, "index.html", html)

	data, err = fs.ReadFile(src, "site.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(translateJS(data, "en"))
	var lits []string
	for i := 0; i < len(js); {
		switch {
		case strings.HasPrefix(js[i:], "//"):
			j := strings.IndexByte(js[i:], '\n')
			if j < 0 {
				j = len(js) - i
			}
			i += j
		case strings.HasPrefix(js[i:], "/*"):
			i += strings.Index(js[i:], "*/") + 2
		case js[i] == '"' || js[i] == '\'' || js[i] == '`':
			end := jsLiteralEnd(js, i)
			lits = append(lits, js[i+1:end])
			i = end + 1
		default:
			i++
		}
	}
	report(t, "site.js", strings.Join(lits, "\n"))
}

func report(t *testing.T, name, text string) {
	t.Helper()
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		for _, w := range cjkOnly.FindAllString(line, -1) {
			if allowedWords[w] {
				continue
			}
			line = strings.TrimSpace(line)
			if !seen[line] {
				seen[line] = true
				t.Errorf("%s 還有中文：%s", name, line)
			}
			break
		}
	}
}

// jsLiterals site.js 的字串字面值（跳過註解）。
func jsLiterals(js string) []string {
	var lits []string
	for i := 0; i < len(js); {
		switch {
		case strings.HasPrefix(js[i:], "//"):
			j := strings.IndexByte(js[i:], '\n')
			if j < 0 {
				j = len(js) - i
			}
			i += j
		case strings.HasPrefix(js[i:], "/*"):
			i += strings.Index(js[i:], "*/") + 2
		case js[i] == '"' || js[i] == '\'' || js[i] == '`':
			end := jsLiteralEnd(js, i)
			lits = append(lits, js[i+1:end])
			i = end + 1
		default:
			i++
		}
	}
	return lits
}

// TestTranslatedJSKeepsStructure 翻譯後的 site.js 字串數量不變（翻譯裡的引號有跳脫，不會把字串切斷）。
func TestTranslatedJSKeepsStructure(t *testing.T) {
	src, _ := fs.Sub(staticFS, "static")
	js, err := fs.ReadFile(src, "site.js")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := len(jsLiterals(string(js))), len(jsLiterals(string(translateJS(js, "en")))); a != b {
		t.Fatalf("翻譯後字串數量 %d ≠ 原本 %d：翻譯裡可能有沒跳脫的引號", b, a)
	}
	if got := escapeQuote(`saved as "Prize", ok \"x\"`, '"'); got != `saved as \"Prize\", ok \"x\"` {
		t.Fatalf("escapeQuote = %s", got)
	}
}

// TestTranslateJSON 中文的 key 與值都翻譯，順序與數字原樣保留。
func TestTranslateJSON(t *testing.T) {
	in := `{"damage":{"普通":3,"稀有":15,"極稀有":80},"list":["稀有",1.5,true,null],"big":12345678901234567,"code":"barn"}`
	out, err := translateJSON([]byte(in), "en")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"damage":{"Normal":3,"Rare":15,"Ultra Rare":80},"list":["Rare",1.5,true,null],"big":12345678901234567,"code":"barn"}`
	if string(out) != want {
		t.Fatalf("translateJSON =\n%s\nwant\n%s", out, want)
	}
}
