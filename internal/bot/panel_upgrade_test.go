package bot

import "testing"

// TestBuildPageUpgradesFacility 建造設施頁升級加工設施（2026-11-17）：選設施後要按「確認升級」才扣錢，加工頁顯示等級。
func TestBuildPageUpgradesFacility(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.exec("UPDATE farms SET gold = 100000 WHERE player_id = $1", pid)
	p.goPage("build")
	if p.selectMenu("升級加工設施") != nil {
		t.Fatal("還沒有設施時不能升級")
	}
	p.choose("選擇要建造的設施", "mill")
	p.goPage("build")
	p.choose("升級加工設施", "mill")
	contains(t, p.Text, "磨坊升到 Lv.1 需要 15,000 金幣")
	p.click("確認升級磨坊")
	contains(t, p.Text, "磨坊升到 Lv.1", "Lv.1 → Lv.2：加工時間 -15% → -40%（50,000 金）")
	p.goPage("craft")
	contains(t, p.Text, "磨坊** Lv.1（加工時間 -15%）")
}
