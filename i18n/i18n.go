// Package i18n 介面語言（2026-11-19 起準備中英文切換）。各語言的字串放在同一個資料夾的 <語言>.json，
// 格式是「分類 → key → 文字」兩層，程式用「分類.key」取用，例如 T("en", "core.gold") → "Gold"。
//
//   - 預設語言 Default（繁體中文）；其他語言缺少的 key 會退回繁體中文，繁中也沒有時回傳 key 本身（方便發現漏翻）。
//   - 文字裡的 {名稱} 是參數，用 T 的 args 依序成對傳入：T(lang, "core.plot_number", "n", 3)。
//   - JSON 檔用 go:embed 打包進執行檔；改了 JSON 要重新編譯才會生效。
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Default 預設語言（繁體中文）。
const Default = "zh-TW"

//go:embed *.json site/*.json
var files embed.FS

var tables = mustLoad()

// textSuffix 以中文原文為 key 的文字表檔名結尾，例如 en.text.json。
const textSuffix = ".text.json"

// texts 語言 → 中文原文 → 翻譯（2026-11-19：Bot 大部分頁面用這種方式翻譯，見 Text／Sprintf）。
var texts = mustLoadTexts()

func mustLoadTexts() map[string]map[string]string {
	out, err := loadTexts(files)
	if err != nil {
		panic(err)
	}
	return out
}

// loadTexts 讀入每個 <語言>.text.json（平的「中文原文 → 翻譯」）。
func loadTexts(fsys fs.FS) (map[string]map[string]string, error) {
	names, err := fs.Glob(fsys, "*"+textSuffix)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("i18n/%s 格式錯誤：%w", name, err)
		}
		out[strings.TrimSuffix(name, textSuffix)] = m
	}
	return out, nil
}

// Text 中文原文在 lang 的翻譯：繁體中文或文字表沒有這句時回傳原文（資料庫名稱、玩家輸入也可以放心丟進來）。
func Text(lang, zh string) string {
	if lang == Default || zh == "" {
		return zh
	}
	if s, ok := texts[lang][zh]; ok {
		return s // 單一個字的單位（個、隻、格…）可以翻成空字串
	}
	return zh
}

// Sprintf 先把格式字串翻譯，再代入參數（翻譯要保留同樣的 %d、%s；英文語序不同時可以用 %[2]d 指定第幾個參數）。
func Sprintf(lang, format string, args ...any) string {
	return fmt.Sprintf(Text(lang, format), args...)
}

// Texts 某個語言的文字表（檢查用）。
func Texts(lang string) map[string]string { return texts[lang] }

// siteTexts 介紹網站專用的文字表 site/<語言>.json（中文原文 → 翻譯；網頁的段落與說明，Bot 用不到）。
var siteTexts = mustLoadSite()

func mustLoadSite() map[string]map[string]string {
	sub, err := fs.Sub(files, "site")
	if err != nil {
		panic(err)
	}
	names, err := fs.Glob(sub, "*.json")
	if err != nil {
		panic(err)
	}
	out := map[string]map[string]string{}
	for _, name := range names {
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			panic(err)
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			panic(fmt.Errorf("i18n/site/%s 格式錯誤：%w", name, err))
		}
		out[strings.TrimSuffix(name, ".json")] = m
	}
	return out
}

// SiteText 介紹網站的文字：先查 site/<語言>.json，再查 Bot 的文字表；都沒有時 ok = false。
func SiteText(lang, zh string) (string, bool) {
	if s, ok := siteTexts[lang][zh]; ok {
		return s, true
	}
	s, ok := texts[lang][zh]
	return s, ok
}

func mustLoad() map[string]map[string]string {
	out, err := load(files)
	if err != nil {
		panic(err)
	}
	return out
}

// load 讀入每個 <語言>.json，攤平成「分類.key」→ 文字。
func load(fsys fs.FS) (map[string]map[string]string, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, name := range names {
		if strings.HasSuffix(name, textSuffix) {
			continue // 以中文原文為 key 的文字表另外讀（loadTexts）
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var groups map[string]map[string]string
		if err := json.Unmarshal(data, &groups); err != nil {
			return nil, fmt.Errorf("i18n/%s 格式錯誤：%w", name, err)
		}
		flat := map[string]string{}
		for cat, items := range groups {
			for k, v := range items {
				flat[cat+"."+k] = v
			}
		}
		out[strings.TrimSuffix(name, ".json")] = flat
	}
	if _, ok := out[Default]; !ok {
		return nil, fmt.Errorf("i18n 缺少預設語言 %s.json", Default)
	}
	return out, nil
}

// Langs 支援的語言（排序後）。
func Langs() []string {
	out := make([]string, 0, len(tables))
	for lang := range tables {
		out = append(out, lang)
	}
	sort.Strings(out)
	return out
}

// Supported 是否支援這個語言。
func Supported(lang string) bool {
	_, ok := tables[lang]
	return ok
}

// T 取得 key 在 lang 的文字；args 是成對的參數名稱與值，代入文字裡的 {名稱}。
func T(lang, key string, args ...any) string {
	s, ok := tables[lang][key]
	if !ok {
		if s, ok = tables[Default][key]; !ok {
			s = key
		}
	}
	for i := 0; i+1 < len(args); i += 2 {
		s = strings.ReplaceAll(s, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return s
}

// Keys 某個語言的所有 key（排序後；檢查漏翻用）。
func Keys(lang string) []string {
	out := make([]string, 0, len(tables[lang]))
	for k := range tables[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// English 英文的語言代碼。
const English = "en"

// FromDiscord Discord 用戶端語言（interaction.locale，例如 zh-TW、zh-CN、en-US、ja）→ 支援的語言：
// 中文（繁、簡）用繁體中文，其他語言一律英文；空白（取不到）時用預設語言。
func FromDiscord(locale string) string {
	switch {
	case locale == "":
		return Default
	case strings.HasPrefix(locale, "zh"):
		return Default
	default:
		return English
	}
}

// Resolve 玩家實際使用的語言：有自己選的就用它，否則跟著 Discord 用戶端。
func Resolve(chosen *string, discordLocale string) string {
	if chosen != nil && Supported(*chosen) {
		return *chosen
	}
	return FromDiscord(discordLocale)
}

type ctxKey struct{}

// WithLang 把語言放進 ctx（服務層組訊息時用 LangOf 取出）。
func WithLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, ctxKey{}, lang)
}

// LangOf ctx 裡的語言；沒有設定時是預設語言。
func LangOf(ctx context.Context) string {
	if lang, ok := ctx.Value(ctxKey{}).(string); ok && Supported(lang) {
		return lang
	}
	return Default
}

// Tc 用 ctx 的語言取文字（服務層用）。
func Tc(ctx context.Context, key string, args ...any) string { return T(LangOf(ctx), key, args...) }

// Lookup 只查 lang 本身（不退回繁中）；找不到時 ok = false。資料庫名稱（作物等）用：沒有翻譯時沿用資料庫裡的中文。
func Lookup(lang, key string) (string, bool) {
	s, ok := tables[lang][key]
	return s, ok
}

// Name 遊戲物件的顯示名稱：英文等其他語言有翻譯時用翻譯，否則用 fallback（資料庫或程式裡的中文名稱）。
func Name(lang, key, fallback string) string {
	if lang == Default {
		return fallback
	}
	if s, ok := Lookup(lang, key); ok {
		return s
	}
	return fallback
}

var (
	fallbackOnce sync.Once
	// 詞組替換用 strings.Replacer（字典樹）：原本把幾千個詞組成一個「a|b|c…」正規表示式，Go 的 regexp 對這種寫法
	// 每個字都要走過所有分支，英文玩家一多就吃滿 CPU（2026-11-19 壓測：佔 Bot CPU 約 8 成）。
	// 詞依長度由長到短傳入，Replacer 在同一個位置優先用前面的詞，結果與原本的正規表示式相同（最左、最長優先）。
	fallbackRe = map[string]*strings.Replacer{}
)

// Fallback 送出畫面前的保險：把文字裡整段出現的中文原文（文字表的 key，至少兩個字、不含格式符號）換成翻譯，
// 長的優先（例如「金針菇」先於「金」以外的較短詞）。用來處理資料或輔助函式組好才傳進面板、沒有經過 Text 的名稱與句子。
// 繁體中文時原樣回傳。
func Fallback(lang, text string) string {
	if lang == Default || text == "" {
		return text
	}
	fallbackOnce.Do(buildFallback)
	re := fallbackRe[lang]
	if re == nil {
		return text
	}
	phrases := func(r *strings.Replacer, text string) string {
		if r == nil {
			return text
		}
		return r.Replace(text)
	}
	// 先換長的完整句子（5 個字以上），再比對帶參數的樣板，最後才換短詞：短詞先換會讓樣板比對不到
	text = fallbackFormats(lang, text, false) // 開頭是固定文字的樣板：不會吃到前面的文字，最先比對
	text = phrases(fallbackLong[lang], text)
	text = fallbackFormats(lang, text, true) // 開頭是參數的樣板
	text = fallbackUnits(text)
	text = phrases(re, text)
	return punct.Replace(text)
}

// 時間長度與數量單位（服務層先組好、沒有經過翻譯的「45 分 0 秒」「100 個」「68 金」）。
var unitRules = []struct {
	re  *regexp.Regexp
	rep string
}{
	{regexp.MustCompile(`(\d+) ?天 ?(\d+) ?小時 ?(\d+) ?分`), "${1}d ${2}h ${3}m"},
	{regexp.MustCompile(`(\d+) ?小時 ?(\d+) ?分 ?(\d+) ?秒`), "${1}h ${2}m ${3}s"},
	{regexp.MustCompile(`(\d+) ?小時 ?(\d+) ?分(鐘)?`), "${1}h ${2}m"},
	{regexp.MustCompile(`(\d+) ?分 ?(\d+) ?秒`), "${1}m ${2}s"},
	{regexp.MustCompile(`(\d+) ?天 ?(\d+) ?小時`), "${1}d ${2}h"},
	{regexp.MustCompile(`(\d+)([hm]) (\d+) ?分(鐘)?`), "${1}${2} ${3}m"},
	{regexp.MustCompile(`(\d+)([hm]) (\d+) ?秒`), "${1}${2} ${3}s"},
	{regexp.MustCompile(`(\d+) ?天`), "${1}d"},
	{regexp.MustCompile(`(\d+) ?小時`), "${1}h"},
	{regexp.MustCompile(`(\d+) ?分鐘`), "${1}m"},
	{regexp.MustCompile(`(\d+) ?秒`), "${1}s"},
	{regexp.MustCompile(`([\d,]+) ?金幣`), "${1} Gold"},
	{regexp.MustCompile(`([\d,]+) ?金`), "${1} Gold"},
	{regexp.MustCompile(`([\d,]+) ?格`), "${1} plots"},
	{regexp.MustCompile(`([\d,]+) ?(個|隻|顆|份|朵|座|場|種|次|級|位|張|條|項)`), "${1}"},
}

func fallbackUnits(text string) string {
	for _, r := range unitRules {
		text = r.re.ReplaceAllString(text, r.rep)
	}
	return text
}

// punct 英文介面的中文標點（只在 Fallback 裡、非繁中時使用）。
var punct = strings.NewReplacer("、", ", ", "，", ", ", "。", ". ", "：", ": ", "；", "; ", "（", " (", "）", ")",
	"「", "\"", "」", "\"", "『", "\"", "』", "\"", "！", "!", "？", "?", "～", "–", "｜", " | ", "　", "  ", "〔", " [", "〕", "]")

var (
	fallbackPairs = map[string]map[string]string{}
	fallbackLong  = map[string]*strings.Replacer{}
)

func buildFallback() {
	verb := regexp.MustCompile(`%(?:\[\d+\])?[-+#0]*\d*(?:\.\d+)?[a-zA-Z%]|\{[a-z_]+\}`)
	han := regexp.MustCompile(`\p{Han}`)
	for lang, m := range texts {
		pairs := map[string]string{}
		for zh, tr := range m {
			k := strings.TrimSpace(zh)
			v := strings.TrimSpace(tr)
			if utf8.RuneCountInString(k) < 2 || v == "" || !han.MatchString(k) || verb.MatchString(k) {
				continue
			}
			pairs[k] = v
		}
		keys := make([]string, 0, len(pairs))
		for k := range pairs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if a, b := utf8.RuneCountInString(keys[i]), utf8.RuneCountInString(keys[j]); a != b {
				return a > b
			}
			return keys[i] < keys[j]
		})
		if len(keys) == 0 {
			continue
		}
		all := make([]string, 0, 2*len(keys))
		var long []string
		for _, k := range keys {
			all = append(all, k, pairs[k])
			if utf8.RuneCountInString(k) >= 5 {
				long = append(long, k, pairs[k])
			}
		}
		fallbackRe[lang] = strings.NewReplacer(all...)
		fallbackPairs[lang] = pairs
		if len(long) > 0 {
			fallbackLong[lang] = strings.NewReplacer(long...)
		}
	}
}

// fmtPattern 帶參數的中文原文（例如「🧭 探險中，%s回來」）反向比對用的樣板。
type fmtPattern struct {
	leading bool           // 開頭是參數（例如「%s 達到…」）
	anchor  string         // 最長的一段中文字面文字（先用 strings.Contains 篩選，比對才快）
	re      *regexp.Regexp // 把參數換成擷取群組的正規表示式
	en      string         // 翻譯，參數一律改成 %s（保留 %[n] 位置）
}

var (
	fmtOnce     sync.Once
	fmtPatterns = map[string][]fmtPattern{}
	zhVerb      = regexp.MustCompile(`%%|%[-+#0]*\d*(?:\.\d+)?[sdvfq]`)
	enVerb      = regexp.MustCompile(`%%|%(\[\d+\])?[-+#0]*\d*(?:\.\d+)?[sdvfq]`)
)

func buildFormats() {
	han := regexp.MustCompile(`\p{Han}`)
	for lang, m := range texts {
		var list []fmtPattern
		for zh, tr := range m {
			locs := zhVerb.FindAllStringIndex(zh, -1)
			verbs := 0
			for _, l := range locs {
				if zh[l[0]:l[1]] != "%%" {
					verbs++
				}
			}
			if verbs == 0 || strings.TrimSpace(tr) == "" {
				continue
			}
			var b strings.Builder
			b.WriteString("(?s)")
			anchor, last := "", 0
			lits := []string{}
			for i, l := range locs {
				lit := zh[last:l[0]]
				lits = append(lits, lit)
				b.WriteString(regexp.QuoteMeta(lit))
				v := zh[l[0]:l[1]]
				switch {
				case v == "%%":
					b.WriteString("%")
				case strings.HasSuffix(v, "d"):
					b.WriteString(`([-+]?[\d,]+)`)
				case strings.HasSuffix(v, "f"):
					b.WriteString(`([-+]?[\d,.]+)`)
				case i == len(locs)-1 && l[1] == len(zh):
					b.WriteString(`([^\n]*)`)
				default:
					b.WriteString(`([^\n]*?)`)
				}
				last = l[1]
			}
			lits = append(lits, zh[last:])
			b.WriteString(regexp.QuoteMeta(zh[last:]))
			for _, lit := range lits {
				t := strings.TrimSpace(lit)
				if han.MatchString(t) && utf8.RuneCountInString(t) > utf8.RuneCountInString(anchor) {
					anchor = t
				}
			}
			if utf8.RuneCountInString(anchor) < 2 {
				continue // 中文太少，容易誤判
			}
			re, err := regexp.Compile(b.String())
			if err != nil {
				continue
			}
			en := enVerb.ReplaceAllStringFunc(tr, func(v string) string {
				if v == "%%" {
					return v
				}
				if i := strings.Index(v, "]"); strings.HasPrefix(v, "%[") && i > 0 {
					return v[:i+1] + "s"
				}
				return "%s"
			})
			list = append(list, fmtPattern{len(locs) > 0 && locs[0][0] == 0 && zh[0:2] != "%%", anchor, re, en})
		}
		sort.Slice(list, func(i, j int) bool {
			a, b := utf8.RuneCountInString(list[i].anchor), utf8.RuneCountInString(list[j].anchor)
			if a != b {
				return a > b
			}
			return list[i].anchor < list[j].anchor
		})
		fmtPatterns[lang] = list
	}
}

// fallbackFormats 把用中文樣板組出來的句子換成翻譯（參數原樣帶過去，字串參數再查一次名稱）。
func fallbackFormats(lang, text string, leading bool) string {
	fmtOnce.Do(buildFormats)
	for _, p := range fmtPatterns[lang] {
		if p.leading != leading || !strings.Contains(text, p.anchor) {
			continue
		}
		text = p.re.ReplaceAllStringFunc(text, func(s string) string {
			sub := p.re.FindStringSubmatch(s)
			args := make([]any, len(sub)-1)
			for i, a := range sub[1:] {
				if tr, ok := fallbackPairs[lang][strings.TrimSpace(a)]; ok {
					a = strings.Replace(a, strings.TrimSpace(a), tr, 1)
				}
				args[i] = a
			}
			return fmt.Sprintf(p.en, args...)
		})
	}
	return text
}
