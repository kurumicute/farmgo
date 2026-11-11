package music

// Spotify 連結解析（對應 Python app/music/spotify.py）：讀取 open.spotify.com/embed 頁面內的 __NEXT_DATA__，
// 取得歌曲、專輯、播放清單或歌手熱門歌曲；播放前才到 YouTube 搜尋同一首歌。不需要 Spotify API 金鑰。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	spotifyRE    = regexp.MustCompile(`(?:open\.spotify\.com/(?:intl-[\w-]+/)?(?:embed/)?|spotify:)(track|album|playlist|artist)[/:]([A-Za-z0-9]+)`)
	anySpotifyRE = regexp.MustCompile(`^https?://open\.spotify\.com/`)
	shortLinkRE  = regexp.MustCompile(`^https?://(?:spotify\.link|spotify\.app\.link)/\S+`)
	nextDataRE   = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.+?)</script>`)
)

const spotifyUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

// SpotifyBase embed 頁面的網址（測試改成假伺服器）。
var SpotifyBase = "https://open.spotify.com"

// SpotifyError 給玩家看的錯誤。
type SpotifyError string

func (e SpotifyError) Error() string { return string(e) }

// IsSpotify 是否為 Spotify 連結（其他 Spotify 網址也當作 Spotify，回覆不支援）。
func IsSpotify(q string) bool {
	return spotifyRE.MatchString(q) || shortLinkRE.MatchString(q) || anySpotifyRE.MatchString(q)
}

// SpotifyTrack 一首 Spotify 歌曲。
type SpotifyTrack struct {
	Title, Artist, URL, Thumbnail string
	Duration                      *int // 秒；沒有時 nil
}

// SpotifyResult 解析結果。
type SpotifyResult struct {
	Kind, Name, PageURL, Cover string
	Tracks                     []SpotifyTrack
}

func cleanText(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, " ", " ")) }

type spotifyImage struct {
	URL      string `json:"url"`
	MaxWidth int    `json:"maxWidth"`
	Width    int    `json:"width"`
}

type spotifyEntity struct {
	Name           string `json:"name"`
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle"`
	Duration       int    `json:"duration"`
	VisualIdentity *struct {
		Image []spotifyImage `json:"image"`
	} `json:"visualIdentity"`
	CoverArt *struct {
		Sources []spotifyImage `json:"sources"`
	} `json:"coverArt"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	TrackList []struct {
		Title      string `json:"title"`
		Subtitle   string `json:"subtitle"`
		Duration   int    `json:"duration"`
		URI        string `json:"uri"`
		EntityType string `json:"entityType"`
	} `json:"trackList"`
}

func bestImage(e spotifyEntity) string {
	var imgs []spotifyImage
	if e.VisualIdentity != nil {
		imgs = e.VisualIdentity.Image
	}
	if len(imgs) == 0 && e.CoverArt != nil {
		imgs = e.CoverArt.Sources
	}
	best, bestW := "", -1
	for _, i := range imgs {
		w := i.MaxWidth
		if w == 0 {
			w = i.Width
		}
		if w > bestW {
			best, bestW = i.URL, w
		}
	}
	return best
}

func secondsOrNil(ms int) *int {
	if s := ms / 1000; s != 0 {
		return &s
	}
	return nil
}

func spotifyGet(ctx context.Context, c *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", spotifyUA)
	return c.Do(req)
}

// ResolveSpotify 解析 Spotify 連結；limit 為最多幾首（MUSIC_PLAYLIST_LIMIT）。
func ResolveSpotify(ctx context.Context, link string, limit int) (SpotifyResult, error) {
	c := &http.Client{Timeout: 20 * time.Second}
	if shortLinkRE.MatchString(link) {
		resp, err := spotifyGet(ctx, c, link)
		if err != nil {
			return SpotifyResult{}, SpotifyError("無法解析 Spotify 短網址")
		}
		final := resp.Request.URL.String()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if !spotifyRE.MatchString(final) {
			m := spotifyRE.FindStringSubmatch(string(body))
			if m == nil {
				return SpotifyResult{}, SpotifyError("無法解析 Spotify 短網址")
			}
			final = fmt.Sprintf("https://open.spotify.com/%s/%s", m[1], m[2])
		}
		link = final
	}
	m := spotifyRE.FindStringSubmatch(link)
	if m == nil {
		return SpotifyResult{}, SpotifyError("不支援這種 Spotify 連結（只支援歌曲、專輯、播放清單、歌手）")
	}
	kind, sid := m[1], m[2]
	resp, err := spotifyGet(ctx, c, fmt.Sprintf("%s/embed/%s/%s", SpotifyBase, kind, sid))
	if err != nil {
		return SpotifyResult{}, SpotifyError("無法連線到 Spotify，請稍後再試")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SpotifyResult{}, SpotifyError(fmt.Sprintf("Spotify 回應錯誤（HTTP %d），連結可能是私人或已失效", resp.StatusCode))
	}
	dm := nextDataRE.FindSubmatch(body)
	if dm == nil {
		return SpotifyResult{}, SpotifyError("無法讀取 Spotify 頁面資料")
	}
	var page struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity *spotifyEntity `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(dm[1], &page); err != nil || page.Props.PageProps.State.Data.Entity == nil {
		return SpotifyResult{}, SpotifyError("找不到這個 Spotify 內容（可能是私人、已刪除，或網址有誤）")
	}
	e := *page.Props.PageProps.State.Data.Entity
	cover := bestImage(e)
	name := e.Name
	if name == "" {
		name = e.Title
	}
	name = cleanText(name)
	if kind == "artist" { // 歌手頁只提供熱門歌曲（約 10 首）
		name += " 熱門歌曲"
	}
	pageURL := fmt.Sprintf("https://open.spotify.com/%s/%s", kind, sid)
	res := SpotifyResult{Kind: kind, Name: name, PageURL: pageURL, Cover: cover}
	if kind == "track" {
		artists := make([]string, len(e.Artists))
		for i, a := range e.Artists {
			artists[i] = cleanText(a.Name)
		}
		artist := strings.Join(artists, ", ")
		if artist == "" {
			artist = cleanText(e.Subtitle)
		}
		res.Tracks = append(res.Tracks, SpotifyTrack{Title: name, Artist: artist, Duration: secondsOrNil(e.Duration), URL: pageURL, Thumbnail: cover})
	} else {
		for _, t := range e.TrackList {
			if len(res.Tracks) >= limit {
				break
			}
			et := t.EntityType
			if et == "" {
				et = "track"
			}
			if et != "track" || t.Title == "" {
				continue
			}
			u := pageURL
			if i := strings.LastIndex(t.URI, ":"); i >= 0 && t.URI[i+1:] != "" {
				u = "https://open.spotify.com/track/" + t.URI[i+1:]
			} else if t.URI != "" && !strings.Contains(t.URI, ":") {
				u = "https://open.spotify.com/track/" + t.URI
			}
			res.Tracks = append(res.Tracks, SpotifyTrack{Title: cleanText(t.Title), Artist: cleanText(t.Subtitle),
				Duration: secondsOrNil(t.Duration), URL: u, Thumbnail: cover})
		}
	}
	if len(res.Tracks) == 0 {
		return SpotifyResult{}, SpotifyError("這個 Spotify 連結裡沒有可播放的歌曲")
	}
	return res, nil
}
