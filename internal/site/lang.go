package site

// 介紹網站的語言切換（2026-11-19）：
//   - 語言依序取自網址 ?lang=、Cookie farmgo_lang、瀏覽器的 Accept-Language；
//     繁體或簡體中文（zh*）顯示繁體中文，其他語言一律顯示英文，沒有 Accept-Language 時顯示繁體中文。
//   - 網址帶 ?lang= 時記在 Cookie（只存語言代碼，HttpOnly），之後換頁、重新整理都維持同一個語言。
//   - 英文版的 index.html 與 site.js 在啟動時依 i18n/site/en.json（找不到時查 Bot 的 i18n/en.text.json）翻好放在記憶體；
//     只換掉 HTML 文字節點、少數屬性與 JS 字串字面值裡的中文，不動標籤與程式碼。
//   - /api/guide 的遊戲資料（作物名稱、說明）以 i18n.Text／i18n.Fallback 翻譯，各語言分開快取。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"farmgm/i18n"
)

// LangCookie 記住網站語言的 Cookie 名稱。
const LangCookie = "farmgo_lang"

// siteLangs 網站支援的語言（第一個是預設）。
var siteLangs = []string{i18n.Default, "en"}

func normalizeLang(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, l := range siteLangs {
		if strings.EqualFold(s, l) {
			return l, true
		}
	}
	return "", false
}

// langOf 這個請求要顯示的語言。
func langOf(r *http.Request) string {
	if l, ok := normalizeLang(r.URL.Query().Get("lang")); ok {
		return l
	}
	if c, err := r.Cookie(LangCookie); err == nil {
		if l, ok := normalizeLang(c.Value); ok {
			return l
		}
	}
	return langFromAccept(r.Header.Get("Accept-Language"))
}

// langFromAccept 取 Accept-Language 權重最高的語言：中文（zh*）→ 繁體中文，其他 → 英文；沒有標頭 → 繁體中文。
func langFromAccept(header string) string {
	best, bestQ := "", -1.0
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(f), "q="); ok {
				if n, err := strconv.ParseFloat(v, 64); err == nil {
					q = n
				}
			}
		}
		if q > bestQ {
			best, bestQ = tag, q
		}
	}
	if best == "" || bestQ <= 0 {
		return i18n.Default
	}
	if best == "zh" || strings.HasPrefix(best, "zh-") {
		return i18n.Default
	}
	return "en"
}

var (
	cjkRe = regexp.MustCompile(`[\p{Han}\x{3000}-\x{303f}\x{ff00}-\x{ffef}]`)
	wsRe  = regexp.MustCompile(`[\s\p{Zs}]+`) // 與 Python 的 \s 相同，含全形空白
)

// siteText 中文片段的翻譯；沒有翻譯時回傳原文。
func siteText(lang, zh string) string {
	if s, ok := i18n.SiteText(lang, zh); ok {
		return s
	}
	return zh
}

// htmlToken 依序切出註解、script／style 區塊、標籤與文字。
var (
	htmlToken = regexp.MustCompile(`(?s)<!--.*?-->|<script\b.*?</script>|<style\b.*?</style>|<[^>]*>|[^<]+`)
	htmlAttr  = regexp.MustCompile(`\b(content|aria-label|title|placeholder|alt)="([^"]*)"`)
)

// translateHTML 把 index.html 換成 lang：文字節點（前後空白保留，中間空白壓成一個）與 content／aria-label／title 屬性。
// 標籤帶 translate="no" 時，緊接的文字不翻（例如語言切換的「中文」）。
func translateHTML(src []byte, lang string) []byte {
	var b strings.Builder
	skipNext := false
	for _, tok := range htmlToken.FindAllString(string(src), -1) {
		switch {
		case strings.HasPrefix(tok, "<!--"), strings.HasPrefix(tok, "<script"), strings.HasPrefix(tok, "<style"):
			b.WriteString(tok)
		case strings.HasPrefix(tok, "<"):
			skipNext = strings.Contains(tok, `translate="no"`)
			if strings.HasPrefix(tok, "<html") {
				tok = strings.Replace(tok, `lang="zh-Hant"`, `lang="`+lang+`"`, 1)
			}
			tok = htmlAttr.ReplaceAllStringFunc(tok, func(m string) string {
				sub := htmlAttr.FindStringSubmatch(m)
				if !cjkRe.MatchString(sub[2]) {
					return m
				}
				return sub[1] + `="` + escapeAttr(siteText(lang, normSpace(sub[2]))) + `"`
			})
			b.WriteString(tok)
		default:
			if skipNext || !cjkRe.MatchString(tok) {
				b.WriteString(tok)
				continue
			}
			core := strings.TrimSpace(tok)
			start := strings.Index(tok, core)
			b.WriteString(tok[:start])
			b.WriteString(escapeText(siteText(lang, normSpace(core))))
			b.WriteString(tok[start+len(core):])
		}
	}
	// <script src> 是獨立的 token（<script ...></script> 會被整段比對），另外換網址
	return []byte(strings.Replace(b.String(), `<script src="/static/site.js"></script>`,
		`<script src="/static/site.js?lang=`+lang+`"></script>`, 1))
}

func normSpace(s string) string { return strings.TrimSpace(wsRe.ReplaceAllString(s, " ")) }

// 翻譯只會有一般文字；仍跳脫 < > & "，避免翻譯檔被改壞時插入標籤。
var (
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

func escapeText(s string) string {
	if strings.Contains(s, "&") && !strings.ContainsAny(s, "<>") && htmlEntity.MatchString(s) {
		return s // 原文就帶 &nbsp; 之類的實體
	}
	return textEscaper.Replace(s)
}

func escapeAttr(s string) string { return attrEscaper.Replace(s) }

var htmlEntity = regexp.MustCompile(`&(#\d+|#x[0-9a-fA-F]+|[a-zA-Z]+);`)

// translateJS 把 site.js 的字串字面值（"…"、'…'、`…`）裡的中文換成 lang；註解不動。
// 樣板字串以整段原文（含 ${…}）查表，翻譯要保留同樣的 ${…}。
func translateJS(src []byte, lang string) []byte {
	s := string(src)
	var b strings.Builder
	n := len(s)
	last := 0
	for i := 0; i < n; {
		switch {
		case strings.HasPrefix(s[i:], "//"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				i = n
			} else {
				i += j
			}
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				i = n
			} else {
				i += j + 4
			}
		case s[i] == '"' || s[i] == '\'' || s[i] == '`':
			end := jsLiteralEnd(s, i)
			body := s[i+1 : end]
			if cjkRe.MatchString(body) {
				if tr := siteText(lang, body); tr != body {
					b.WriteString(s[last : i+1])
					b.WriteString(escapeQuote(tr, s[i]))
					last = end
				}
			}
			i = end + 1
		default:
			i++
		}
	}
	b.WriteString(s[last:])
	out := b.String()
	if lang == "en" {
		out = strings.ReplaceAll(out, `"zh-TW"`, `"en-US"`)
	}
	// 遊戲資料依網址的語言回傳（不依賴 Cookie）
	out = strings.ReplaceAll(out, "`/api/guide/${part}`", "`/api/guide/${part}?lang="+lang+"`")
	return []byte(out)
}

// jsLiteralEnd 從 s[i]（引號）開始的字串字面值的結尾引號位置。
func jsLiteralEnd(s string, i int) int {
	q := s[i]
	n := len(s)
	j := i + 1
	for j < n {
		switch {
		case s[j] == '\\':
			j += 2
			continue
		case q == '`' && strings.HasPrefix(s[j:], "${"):
			depth := 0
			for k := j + 1; k < n; k++ {
				if s[k] == '{' {
					depth++
				} else if s[k] == '}' {
					depth--
					if depth == 0 {
						j = k
						break
					}
				}
			}
		case s[j] == q:
			return j
		}
		j++
	}
	return n - 1
}

// translateJSON 把遊戲資料 JSON 裡的中文字串（含物件的 key，例如遺跡守護者的「稀有」傷害表）換成 lang。
// 逐個 token 改寫，維持原本的順序（部分資料是有順序的 OrderedMap，前端以 Object.entries 依序顯示）。
func translateJSON(body []byte, lang string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // 數字原樣保留
	type frame struct {
		obj, wantKey bool
		n            int
	}
	var (
		out   bytes.Buffer
		stack []*frame
	)
	// before 寫入一個值或 key 之前的分隔符號
	before := func() {
		if len(stack) == 0 {
			return
		}
		f := stack[len(stack)-1]
		switch {
		case f.obj && f.wantKey:
			if f.n > 0 {
				out.WriteByte(',')
			}
		case f.obj:
			out.WriteByte(':')
		case f.n > 0:
			out.WriteByte(',')
		}
	}
	// after 寫完一個 key 或完整的值
	after := func() {
		if len(stack) == 0 {
			return
		}
		f := stack[len(stack)-1]
		if f.obj && f.wantKey {
			f.wantKey = false
			return
		}
		f.wantKey = f.obj
		f.n++
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				before()
				out.WriteByte(byte(t))
				stack = append(stack, &frame{obj: t == '{', wantKey: t == '{'})
			default:
				out.WriteByte(byte(t))
				stack = stack[:len(stack)-1]
				after()
			}
		case string:
			before()
			s, err := marshal(translateString(t, lang))
			if err != nil {
				return nil, err
			}
			out.Write(s)
			after()
		default: // json.Number、bool、nil
			before()
			s, err := marshal(t)
			if err != nil {
				return nil, err
			}
			out.Write(s)
			after()
		}
	}
	return out.Bytes(), nil
}

var (
	// seasonRe Fallback 不翻單一個字；剩下的春夏秋冬（可能帶「季」）就是季節
	seasonRe    = regexp.MustCompile(`[春夏秋冬]季?`)
	seasonNames = map[string]string{"春": "Spring", "夏": "Summer", "秋": "Autumn", "冬": "Winter"}
	// pctX 寵物效果的說明把 {pct} 換成了 X（例如「收成數量 +X%」），用原本的 {pct} 查表
	pctX = regexp.MustCompile(`X%`)
)

// translateString 先查完整翻譯（網站文字表 → Bot 文字表），查不到再用 i18n.Fallback 逐詞翻。
func translateString(s, lang string) string {
	if !cjkRe.MatchString(s) {
		return s
	}
	if tr, ok := i18n.SiteText(lang, s); ok {
		return tr
	}
	if pctX.MatchString(s) {
		if tr, ok := i18n.SiteText(lang, pctX.ReplaceAllString(s, "{pct}%")); ok {
			return strings.ReplaceAll(tr, "{pct}", "X")
		}
	}
	tr := i18n.Fallback(lang, s)
	return seasonRe.ReplaceAllStringFunc(tr, func(m string) string {
		return seasonNames[string([]rune(m)[0])]
	})
}

// escapeQuote 翻譯裡出現與字串相同的引號（例如英文的 "🏆 Casino Prize"）時加上反斜線，避免把 JS 字串切斷。
// 樣板字串（`）的 ${…} 裡可能有合法的巢狀反引號，不處理。
func escapeQuote(s string, q byte) string {
	if q == '`' || strings.IndexByte(s, q) < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == q {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
