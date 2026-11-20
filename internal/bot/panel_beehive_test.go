package bot

// 蜂箱頁的面板測試（2026-11-20）：放置、拆除要按兩次確認。

import (
	"slices"
	"strings"
	"testing"
)

func TestBeehiveRemovePanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setLevel(pid, 3)
	e.setGold(pid, 100_000)
	p.goPage("beehive")
	p.choose("選一格空地放蜂箱", "3")
	contains(t, p.Text, "在第 3 格放好蜂箱", "蜂箱在第 **3** 格")

	p.click("拆除蜂箱")
	contains(t, p.Text, "拆除後第 3 格變回空地", "不退費")
	p.click("確定拆除？")
	contains(t, p.Text, "已拆除第 3 格的蜂箱", "選一格空地放蜂箱")
	if slices.ContainsFunc(p.labels(), func(l string) bool { return strings.Contains(l, "拆除蜂箱") }) {
		t.Fatal("沒有蜂箱時不顯示拆除按鈕")
	}
}
