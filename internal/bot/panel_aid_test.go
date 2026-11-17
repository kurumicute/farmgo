package bot

import (
	"strconv"
	"testing"
)

// 送禮與互助頁（2026-11-17）：選對象 → 送禮、幫忙澆水；門檻不足時說明原因。
func TestAidPage(t *testing.T) {
	e := setup(t)
	a := e.makePlayer(alice, guildA, "阿明")
	b := e.makePlayer(bob, guildA, "小美")
	pa, pb := a.panel.playerID, b.panel.playerID

	a.goPage("aid")
	contains(t, a.Text, "Lv.5", "先用第一個選單選一位玩家")
	e.exec("UPDATE farms SET level = 5, created_at = '2025-01-01'")
	e.setInventory(pa, "wheat", 20)
	before := e.gold(pb)
	a.goPage("aid")
	a.choose("選擇要送禮或幫忙的玩家", fmtID(pb))
	contains(t, a.Text, "小美", "0 格待澆水")
	a.choose("選擇要送給 小美 的作物", "wheat|10")
	contains(t, a.Text, "已送 10 個小麥給 小美", "雙方各得 30 金幣")
	if e.produceQty(pb) != 10 || e.gold(pb) != before+30 {
		t.Fatal(e.produceQty(pb), e.gold(pb))
	}
	if a.selectMenu("選擇要送給") != nil {
		t.Fatal("送過禮後不應該再出現送禮選單")
	}
	if !a.button("幫 小美 澆水").Disabled {
		t.Fatal("對方沒有待澆水的土地，按鈕應該停用")
	}
	// 對方種下作物（還沒澆水）後，幫他澆水
	b.goPage("plots")
	b.choose("選擇作物", "wheat")
	a.click("重新整理")
	a.click("幫 小美 澆水")
	contains(t, a.Text, "已幫 小美 澆了", "雙方各得 20 金幣")
	if !a.button("幫 小美 澆水").Disabled {
		t.Fatal("今天已經幫過，按鈕應該停用")
	}
	e.checkLedger()
}

func fmtID(id int64) string { return strconv.FormatInt(id, 10) }
