package discordio

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/clock"
)

func TestNextTaipeiMidnight(t *testing.T) {
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 11, 19, 23, 59, 0, 0, clock.TaipeiTZ), time.Date(2026, 11, 20, 0, 0, 0, 0, clock.TaipeiTZ)},
		{time.Date(2026, 11, 20, 0, 0, 0, 0, clock.TaipeiTZ), time.Date(2026, 11, 21, 0, 0, 0, 0, clock.TaipeiTZ)},
		{time.Date(2026, 12, 31, 15, 30, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, clock.TaipeiTZ)}, // 台灣 12/31 23:30，跨年
	}
	for _, c := range cases {
		if got := nextTaipeiMidnight(c.now); !got.Equal(c.want) {
			t.Errorf("%v → %v，預期 %v", c.now, got, c.want)
		}
	}
}

func TestClassifyMemberErr(t *testing.T) {
	rest := func(status, code int) error {
		return &discordgo.RESTError{Response: &http.Response{StatusCode: status}, Message: &discordgo.APIErrorMessage{Code: code}}
	}
	cases := []struct {
		name string
		err  error
		want memberCheck
	}{
		{"仍在伺服器", nil, memberPresent},
		{"已離開", rest(404, discordgo.ErrCodeUnknownMember), memberGone},
		{"Bot 不在伺服器", rest(404, discordgo.ErrCodeUnknownGuild), guildSkip},
		{"沒有權限", rest(403, 0), guildSkip},
		{"伺服器錯誤", rest(500, 0), checkFailed},
		{"網路錯誤", errors.New("timeout"), checkFailed},
	}
	for _, c := range cases {
		if got := classifyMemberErr(c.err); got != c.want {
			t.Errorf("%s：%v，預期 %v", c.name, got, c.want)
		}
	}
}
