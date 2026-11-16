package bot

import "testing"

// 玩家名稱放進 markdown 前跳脫；放進 footer（不支援 markdown）時要拿掉跳脫符號（2026-11-16）。
func TestEscapeMDAndPlainText(t *testing.T) {
	for _, name := range []string{"my_farm", "a*b_c~d|e`f", "[點我](https://x)", `back\slash`, "<b>", "#標題"} {
		esc := escapeMD(name)
		if got := plainText("「" + esc + "」"); got != "「"+name+"」" {
			t.Errorf("plainText(escapeMD(%q)) = %q", name, got)
		}
	}
	if got := escapeMD("<@123>"); got != `\<@​123\>` {
		t.Errorf("提及語法要跳脫：%q", got)
	}
}
