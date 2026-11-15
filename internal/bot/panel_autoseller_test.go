package bot

// 背包頁的自動販售機販售項目選單（2026-11-15）。

import "testing"

func TestAutoSellerItemsFromBackpack(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	p.goPage("backpack")
	if p.selectMenu("勾選自動販售機要賣的東西") != nil {
		t.Fatal("沒有自動販售機時不顯示")
	}
	e.exec("INSERT INTO farm_tools (farm_id, tool_code, enabled, acquired_at) SELECT id, 'auto_seller', true, now() FROM farms WHERE player_id = $1", pid)
	p.goPage("backpack")
	contains(t, p.Text, "🏧 自動販售機販售項目", "全部都賣")
	s := p.selectMenu("勾選自動販售機要賣的東西")
	if s == nil || s.MaxValues != len(s.Options) || !s.Options[0].Default {
		t.Fatalf("%+v", s)
	}
	p.chooseMany("勾選自動販售機要賣的東西", []string{"carrot", "fish"})
	contains(t, p.Text, "自動販售機只賣：胡蘿蔔、自動釣魚機的魚")
	p.goPage("backpack")
	contains(t, p.Text, "只賣 胡蘿蔔、自動釣魚機的魚")
	for _, o := range p.selectMenu("勾選自動販售機要賣的東西").Options {
		if o.Default != (o.Value == "carrot" || o.Value == "fish") {
			t.Fatalf("%+v", o)
		}
	}
}
