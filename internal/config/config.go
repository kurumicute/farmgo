// Package config 集中讀取環境設定（.env 或系統環境變數），對應 Python app/config.py（只保留農場相關設定，不含音樂）。
//
// 規則與 Python 版相同：
//   - 系統環境變數優先於 .env；
//   - .env 中留空的項目（例如 DISCORD_DEV_GUILD_ID=）視為未設定，使用預設值；
//   - 未知的設定（例如 LAVALINK_*）會被忽略；音樂仍由 Python 版執行，Go 只讀取介紹網站顯示用的 MUSIC_* 設定；
//   - 設定只在程序啟動時讀取。
//
// 密碼、Token 不可寫進日誌：Settings 的 String() 會遮蔽機密欄位。
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// bom UTF-8 BOM（Windows 記事本可能加上）。
var bom = string(rune(0xFEFF))

// OfficialGuildURL 官方 Discord 群組（公開邀請連結）。
const OfficialGuildURL = "https://discord.gg/gsd823SQtP"

// Settings 執行設定。
type Settings struct {
	DatabaseURL           string
	AppHost               string
	AppPort               int
	MonitorRefreshSeconds float64
	AllowedHosts          string
	AdminUsername         string
	AdminPasswordHash     string
	AdminSessionHours     float64

	RankingSettleIntervalSeconds float64
	BackgroundTasksRunner        string // web／bot／none
	AppEnv                       string // development／production

	DBPoolSize    int
	DBMaxOverflow int
	DBPoolTimeout float64

	// RedisURL 選用（Go 版新增）：設定時以 Redis 通知各程序同步清空目錄與規則設定的快取（internal/cachebus）
	RedisURL string

	DiscordBotToken         string
	DiscordApplicationID    int64
	DiscordDevGuildID       int64
	DiscordSyncCommands     bool
	DiscordAdvancedCommands bool
	BotWorkerThreads        int

	SiteHost       string
	SitePort       int
	SiteSupportURL string

	// 音樂（Python 版執行；介紹網站只顯示這些設定值）。MUSIC_BOT_TOKEN 只記錄有沒有設定，不保存 Token。
	MusicEnabled        bool
	MusicBotConfigured  bool
	MusicApplicationID  int64
	MusicPrefixCommands bool
	MusicPrefix         string
	MusicPlaylistLimit  int
	MusicIdleTimeout    int
	MusicVolume         float64
	// Lavalink 音樂伺服器（Go 版的音樂在農場 Bot 程序內執行；沒有設定 MUSIC_BOT_TOKEN 時）。密碼不出現在 String()。
	LavalinkURI       string
	LavalinkPassword  string
	MusicSettingsFile string // 各伺服器的音量與前綴（與 Python 相同的 JSON 檔）
}

func (s Settings) String() string {
	mask := func(v string) string {
		if v == "" {
			return ""
		}
		return "***"
	}
	return fmt.Sprintf("Settings{DatabaseURL:%s AppHost:%s AppPort:%d AppEnv:%s DiscordBotToken:%s AdminPasswordHash:%s}",
		mask(s.DatabaseURL), s.AppHost, s.AppPort, s.AppEnv, mask(s.DiscordBotToken), mask(s.AdminPasswordHash))
}

// AllowedHostList 允許的 Host 標頭清單。
func (s Settings) AllowedHostList() []string {
	var out []string
	for _, h := range strings.Split(s.AllowedHosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// PgxDatabaseURL 把 SQLAlchemy 形式的 URL（postgresql+psycopg://）轉成 pgx 可用的 postgres:// URL。
func (s Settings) PgxDatabaseURL() string {
	u := s.DatabaseURL
	if i := strings.Index(u, "://"); i >= 0 {
		scheme := u[:i]
		if p := strings.Index(scheme, "+"); p >= 0 {
			scheme = scheme[:p]
		}
		u = scheme + u[i:]
	}
	return u
}

// PoolSize 連線池上限（Bot 至少為 BotWorkerThreads + 2）。
func (s Settings) PoolSize(minPoolSize int) int {
	return max(s.DBPoolSize, minPoolSize) + s.DBMaxOverflow
}

// ParseDotEnv 解析 .env 內容（KEY=VALUE、# 註解、可加單／雙引號）。
func ParseDotEnv(content string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(content, bom)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out[strings.ToUpper(k)] = v
	}
	return out
}

// Load 讀取 envFile（不存在時略過）與系統環境變數。
func Load(envFile string) (Settings, error) {
	file := map[string]string{}
	if envFile != "" {
		data, err := os.ReadFile(envFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Settings{}, err
		}
		file = ParseDotEnv(string(data))
	}
	return FromLookup(func(key string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return file[key]
	})
}

// FromLookup 依查詢函式建立設定（空字串表示未設定），並驗證範圍。
func FromLookup(get func(string) string) (Settings, error) {
	p := parser{get: get}
	s := Settings{
		DatabaseURL:                  p.str("DATABASE_URL", ""),
		AppHost:                      p.str("APP_HOST", "127.0.0.1"),
		AppPort:                      p.int("APP_PORT", 8000, 1, 65535),
		MonitorRefreshSeconds:        p.float("MONITOR_REFRESH_SECONDS", 3.0, 1.0, 60.0, false),
		AllowedHosts:                 p.str("ALLOWED_HOSTS", "127.0.0.1,localhost"),
		AdminUsername:                p.str("ADMIN_USERNAME", ""),
		AdminPasswordHash:            p.str("ADMIN_PASSWORD_HASH", ""),
		AdminSessionHours:            p.float("ADMIN_SESSION_HOURS", 8.0, 0, 72, true),
		RankingSettleIntervalSeconds: p.float("RANKING_SETTLE_INTERVAL_SECONDS", 60, 10, 3600, false),
		BackgroundTasksRunner:        p.choice("BACKGROUND_TASKS_RUNNER", "web", "web", "bot", "none"),
		AppEnv:                       p.choice("APP_ENV", "development", "development", "production"),
		DBPoolSize:                   p.int("DB_POOL_SIZE", 10, 1, 100),
		DBMaxOverflow:                p.int("DB_MAX_OVERFLOW", 5, 0, 100),
		DBPoolTimeout:                p.float("DB_POOL_TIMEOUT", 10, 0, 120, true),
		RedisURL:                     p.str("REDIS_URL", ""),
		DiscordBotToken:              p.str("DISCORD_BOT_TOKEN", ""),
		DiscordApplicationID:         p.int64("DISCORD_APPLICATION_ID"),
		DiscordDevGuildID:            p.int64("DISCORD_DEV_GUILD_ID"),
		DiscordSyncCommands:          p.bool("DISCORD_SYNC_COMMANDS", true),
		DiscordAdvancedCommands:      p.bool("DISCORD_ADVANCED_COMMANDS", false),
		BotWorkerThreads:             p.int("BOT_WORKER_THREADS", 16, 1, 64),
		SiteHost:                     p.str("SITE_HOST", "127.0.0.1"),
		SitePort:                     p.int("SITE_PORT", 24444, 1, 65535),
		SiteSupportURL:               p.str("SITE_SUPPORT_URL", ""),
		MusicEnabled:                 p.bool("MUSIC_ENABLED", true),
		MusicBotConfigured:           p.get("MUSIC_BOT_TOKEN") != "",
		MusicApplicationID:           p.int64("MUSIC_APPLICATION_ID"),
		MusicPrefixCommands:          p.bool("MUSIC_PREFIX_COMMANDS", false),
		MusicPrefix:                  p.strLen("MUSIC_PREFIX", "!", 1, 5),
		MusicPlaylistLimit:           p.int("MUSIC_PLAYLIST_LIMIT", 20, 1, 200),
		MusicIdleTimeout:             p.int("MUSIC_IDLE_TIMEOUT", 300, 30, 3600),
		MusicVolume:                  p.float("MUSIC_VOLUME", 0.1, 0, 2, false),
		LavalinkURI:                  p.str("LAVALINK_URI", "http://127.0.0.1:2333"),
		LavalinkPassword:             p.str("LAVALINK_PASSWORD", ""),
		MusicSettingsFile:            p.str("MUSIC_SETTINGS_FILE", "data/music_guild_settings.json"),
	}
	if s.DatabaseURL == "" {
		p.errs = append(p.errs, "DATABASE_URL 未設定")
	}
	if len(p.errs) > 0 {
		return s, errors.New("設定錯誤：" + strings.Join(p.errs, "；"))
	}
	return s, nil
}

type parser struct {
	get  func(string) string
	errs []string
}

func (p *parser) str(key, def string) string {
	if v := p.get(key); v != "" {
		return v
	}
	return def
}

// strLen 字串長度（以字元計算）必須在 low–high 之間（Python Field(min_length, max_length)）。
func (p *parser) strLen(key, def string, low, high int) string {
	v := p.get(key)
	if v == "" {
		return def
	}
	if n := utf8.RuneCountInString(v); n < low || n > high {
		p.errs = append(p.errs, fmt.Sprintf("%s 長度必須是 %d–%d 個字元", key, low, high))
		return def
	}
	return v
}

func (p *parser) int(key string, def, low, high int) int {
	v := p.get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < low || n > high {
		p.errs = append(p.errs, fmt.Sprintf("%s 必須是 %d–%d 的整數", key, low, high))
		return def
	}
	return n
}

func (p *parser) int64(key string) int64 {
	v := p.get(key)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		p.errs = append(p.errs, key+" 必須是整數")
	}
	return n
}

// float exclusiveLow 為 true 時下限不含（Python Field(gt=…)）。
func (p *parser) float(key string, def, low, high float64, exclusiveLow bool) float64 {
	v := p.get(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	bad := err != nil || f > high || f < low || (exclusiveLow && f == low)
	if bad {
		p.errs = append(p.errs, fmt.Sprintf("%s 必須在 %v–%v 之間", key, low, high))
		return def
	}
	return f
}

func (p *parser) bool(key string, def bool) bool {
	switch strings.ToLower(p.get(key)) {
	case "":
		return def
	case "1", "true", "yes", "on", "y", "t":
		return true
	case "0", "false", "no", "off", "n", "f":
		return false
	}
	p.errs = append(p.errs, key+" 必須是 true／false")
	return def
}

func (p *parser) choice(key, def string, options ...string) string {
	v := p.get(key)
	if v == "" {
		return def
	}
	for _, o := range options {
		if v == o {
			return v
		}
	}
	p.errs = append(p.errs, fmt.Sprintf("%s 必須是 %s 之一", key, strings.Join(options, "／")))
	return def
}
