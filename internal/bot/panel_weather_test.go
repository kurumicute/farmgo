package bot

import (
	"testing"

	"farmgm/internal/game/weather"
)

// 總覽頁最底部（footer 第一行）顯示今日天氣與效果（2026-11-17）。
func TestMainShowsWeather(t *testing.T) {
	e := setup(t)
	defer func() { weather.Fixed = weather.Cloudy }()
	p := e.makePlayer(alice, guildA, "阿明")
	p.goPage("main")
	contains(t, p.Footer, "☁️ 今日天氣：陰天｜沒有特別影響")
	weather.Fixed = weather.Storm
	p.goPage("main")
	contains(t, p.Footer, "⛈️ 今日天氣：暴風｜收成時每格被吹壞 1 個")
}
