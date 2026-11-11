package music

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// fmtDuration Python fmt_duration：秒 → m:ss 或 h:mm:ss；nil 為「直播/未知」。
func fmtDuration(seconds *int) string {
	if seconds == nil {
		return "直播/未知"
	}
	s := *seconds
	h, rem := s/3600, s%3600
	m, sec := rem/60, rem%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

func intPtr(v int) *int { return &v }

// progressBar Python progress_bar（長度 16）。
func progressBar(elapsed int, total *int) string {
	const length = 16
	if total == nil || *total == 0 {
		return "🔴 LIVE"
	}
	filled := min(length-1, length*elapsed / *total)
	return strings.Repeat("▬", filled) + "🔘" + strings.Repeat("▬", length-filled-1)
}

// short Python short：超過 limit 個字元截斷並加「…」。
func short(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

// discord.py utils.escape_markdown（as_needed=False、ignore_links=True）。
var (
	markdownCommon = `^>(?:>>)?\s|\[.+\]\(.+\)|^#{1,3}|^\s*-`
	urlRegex       = `(?P<url><[^: >]+:\/[^ >]+>|(?:https?|steam):\/\/[^\s<]+[^<.,:;\"\'\]\s])`
	escapeRE       = regexp.MustCompile(`(?m)(?:` + urlRegex + `|(?P<markdown>[_\\~|\*` + "`" + `]|` + markdownCommon + `))`)
)

func escape(text string) string {
	urlIdx := escapeRE.SubexpIndex("url")
	var b strings.Builder
	last := 0
	for _, loc := range escapeRE.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(text[last:loc[0]])
		if loc[2*urlIdx] >= 0 { // 網址保持原樣
			b.WriteString(text[loc[0]:loc[1]])
		} else {
			b.WriteString(`\` + text[loc[0]:loc[1]])
		}
		last = loc[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// ---------- 各伺服器的設定（音量、前綴；與 Python 相同的 data/music_guild_settings.json） ----------

// Settings 各伺服器設定檔。
type Settings struct {
	path string
	mu   sync.Mutex
	data map[string]map[string]any
}

// LoadSettings 讀取設定檔（不存在或格式錯誤時為空）。
func LoadSettings(path string) *Settings {
	s := &Settings{path: path, data: map[string]map[string]any{}}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &s.data)
		if s.data == nil {
			s.data = map[string]map[string]any{}
		}
	}
	return s
}

// Float 讀取數值設定。
func (s *Settings) Float(guildID, key string, def float64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.data[guildID][key].(float64); ok {
		return v
	}
	return def
}

// String 讀取文字設定。
func (s *Settings) String(guildID, key, def string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.data[guildID][key].(string); ok {
		return v
	}
	return def
}

// Set 寫入設定並存檔（先寫暫存檔再取代，避免寫到一半）。
func (s *Settings) Set(guildID, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[guildID] == nil {
		s.data[guildID] = map[string]any{}
	}
	s.data[guildID][key] = value
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
