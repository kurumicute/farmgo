package bot

// 自動販售機設定頁（2026-11-15 販售項目；2026-11-22 移到獨立頁面、加保留條件）。

import (
	"slices"
	"strings"
	"testing"
)

func TestAutoSellerItemsFromBackpack(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	p.goPage("backpack")
	if slices.ContainsFunc(p.labels(), func(l string) bool { return strings.Contains(l, "自動販售機設定") }) {
		t.Fatal("沒有自動販售機時不顯示")
	}
	e.exec("INSERT INTO farm_tools (farm_id, tool_code, enabled, acquired_at) SELECT id, 'auto_seller', true, now() FROM farms WHERE player_id = $1", pid)
	p.goPage("backpack")
	contains(t, p.Text, "🏧 自動販售機", "賣：全部")
	p.click("自動販售機設定")
	contains(t, p.Text, "自動販售機設定", "所有作物的保留條件", "不保留")
	s := p.selectMenu("勾選自動販售機要賣的東西")
	if s == nil || s.MaxValues != len(s.Options) || !s.Options[0].Default {
		t.Fatalf("%+v", s)
	}
	p.chooseMany("勾選自動販售機要賣的東西", []string{"wheat", "carrot", "fish"})
	contains(t, p.Text, "自動販售機只賣：小麥、胡蘿蔔、自動釣魚機的魚")
	p.goPage("auto_seller")
	contains(t, p.Text, "賣：小麥、胡蘿蔔、自動釣魚機的魚")
	for _, o := range p.selectMenu("勾選自動販售機要賣的東西").Options {
		if o.Default != (o.Value == "wheat" || o.Value == "carrot" || o.Value == "fish") {
			t.Fatalf("%+v", o)
		}
	}
	// 選單列有勾選販售的作物與魚（魚在第 2 項）
	pick := p.selectMenu("選擇要設定保留條件的作物")
	if pick == nil || len(pick.Options) != 4 || pick.Options[1].Value != "fish" {
		t.Fatalf("%+v", pick)
	}
	// 魚依稀有度保留（2026-11-25）：沒有「跟著所有作物」，選項是稀有度
	p.choose("選擇要設定保留條件的作物", "fish")
	contains(t, p.Text, "自動釣魚機的魚的保留條件", "不保留")
	keep := p.selectMenu("自動釣魚機的魚要保留哪些")
	if keep == nil || len(keep.Options) != 4 || keep.Options[1].Value != "r:rare" {
		t.Fatalf("%+v", keep)
	}
	p.chooseMany("自動釣魚機的魚要保留哪些", []string{"r:epic", "r:legendary"})
	contains(t, p.Text, "自動釣魚機的魚保留：史詩魚、傳說魚")
	p.goPage("auto_seller")
	contains(t, p.Text, "保留：自動釣魚機的魚：史詩魚、傳說魚")
	// 小麥只保留可以加工的
	p.choose("選擇要設定保留條件的作物", "wheat")
	contains(t, p.Text, "小麥的保留條件", "跟著所有作物")
	p.chooseMany("小麥要保留哪些", []string{"craftable"})
	contains(t, p.Text, "小麥收成時保留：可以加工的（普通品質）")
	p.goPage("auto_seller")
	contains(t, p.Text, "保留：小麥：可以加工的（普通品質）")
	p.goPage("backpack")
	contains(t, p.Text, "保留：小麥：可以加工的（普通品質）；自動釣魚機的魚：史詩魚、傳說魚")
}
