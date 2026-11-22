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

// 蜂箱入口與「收蜂蜜」在農田頁（2026-11-22 從總覽移過來）；總覽只提示有蜂蜜。
func TestPlotsCollectsHoney(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setLevel(pid, 3)
	e.setGold(pid, 100_000)
	p.goPage("plots")
	p.click("蜂箱")
	p.choose("選一格空地放蜂箱", "3")
	e.clock.Advance(2 * 12 * 3600)
	p.goPage("main")
	contains(t, p.Text, "蜂箱裡有 4／10 罐蜂蜜")
	hasLabel := func(s string) bool {
		return slices.ContainsFunc(p.labels(), func(l string) bool { return strings.Contains(l, s) })
	}
	if hasLabel("收蜂蜜") {
		t.Fatal("總覽不再有收蜂蜜按鈕")
	}
	p.goPage("plots")
	if !hasLabel("收蜂蜜（4 罐）") || !hasLabel("蜂箱") {
		t.Fatalf("農田頁有蜂蜜時要有收蜂蜜按鈕和蜂箱入口：%v", p.labels())
	}
	p.click("收蜂蜜")
	contains(t, p.Text, "收了 4 罐蜂蜜")
	if !hasLabel("蜂箱") || hasLabel("收蜂蜜") {
		t.Fatalf("收完蜂蜜後只剩蜂箱入口：%v", p.labels())
	}
	p.click("蜂箱")
	contains(t, p.Text, "蜂箱在第 **3** 格")
}
