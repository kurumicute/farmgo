package bot

// 面板每個步驟的 SQL 次數（Go 版新增）：與壓力測試（stress_test.go）相同的流程，記錄每一步實際執行的 SQL。
// FARMGM_SQL_DUMP=1 時印出每一步的 SQL 明細（找重複查詢用）；FARMGM_SQL_DUMP=seq 依執行順序列出。

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
)

// sqlShape SQL 的前 90 個字（同一種查詢歸成一類）。
func sqlShape(s string) string {
	if r := []rune(s); len(r) > 90 {
		return string(r[:90])
	}
	return s
}

func TestPanelQueryCounts(t *testing.T) {
	e := setup(t)
	// 比照正式環境：遊戲目錄快取 10 秒（svctest 預設關閉，Cleanup 會還原）。自動簽到每天只查一次，不列入（維持 setup 的關閉）
	db.CatalogTTL = 10 * time.Second
	db.CatalogClear()
	p := e.makePlayer(alice, guildA, "Alice")
	dump := os.Getenv("FARMGM_SQL_DUMP") != ""
	counts := map[string]int{}
	step := func(name string, fn func()) {
		t.Helper()
		dbtest.SQL.Start(t)
		fn()
		stmts := dbtest.SQL.Stop()
		counts[name] = len(stmts)
		if os.Getenv("FARMGM_SQL_DUMP") == "seq" {
			fmt.Printf("===== %s：%d 條 SQL（依執行順序）\n", name, len(stmts))
			for _, s := range stmts {
				fmt.Printf("  %s\n", sqlShape(s))
			}
		} else if dump {
			byShape := map[string]int{}
			for _, s := range stmts {
				byShape[sqlShape(s)]++
			}
			keys := make([]string, 0, len(byShape))
			for k := range byShape {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				return byShape[keys[i]] > byShape[keys[j]] || byShape[keys[i]] == byShape[keys[j]] && keys[i] < keys[j]
			})
			fmt.Printf("===== %s：%d 條 SQL\n", name, len(stmts))
			for _, k := range keys {
				fmt.Printf("  %3d  %s\n", byShape[k], k)
			}
		}
	}
	step("切換到農田", func() { p.goPage("plots") })
	step("種滿農田", func() { p.choose("選擇作物", "wheat") })
	if !strings.Contains(p.Text, "種下") {
		t.Fatal(p.Text)
	}
	step("全部澆水", func() { p.click("全部澆水") })
	step("回總覽", func() { p.goPage("main") })
	step("商店", func() { p.goPage("shop") })
	step("選擇商品", func() { p.choose("選擇要買的商品", "seed:carrot") })
	step("買 5", func() { p.click("買 5") })
	step("排行榜", func() { p.goPage("board") })
	step("賭場", func() { p.goPage("casino") })
	total := 0
	for _, n := range counts {
		total += n
	}
	t.Logf("SQL 次數：%v，合計 %d", counts, total)
	// 上限（2026-11-12 優化後的次數＋少許餘裕；優化前合計 169、買 5 為 39）。增加查詢時先確認是否必要，再調整這裡
	if total > 145 {
		t.Errorf("SQL 合計 %d 條，超過上限 145", total)
	}
	if n := counts["買 5"]; n > 26 {
		t.Errorf("買 5：%d 條，超過上限 26（購買後的持有數量應與商店頁共用同一筆交易）", n)
	}
}
