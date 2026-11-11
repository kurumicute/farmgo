// 產生管理員密碼雜湊，填入 .env 的 ADMIN_PASSWORD_HASH（對應 Python app/hash_password.py）。
//
// 用法：go run ./cmd/farmgm-hash-password   （密碼以隱藏方式輸入，不會出現在命令列歷史中）
//
// 產生的雜湊與 Python 版互通。提示文字輸出到 stderr，stdout 只有 ADMIN_PASSWORD_HASH=… 一行。
// 標準輸入不是終端機時（管線輸入），逐行讀取兩次密碼。
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"unicode/utf8"

	"farmgm/internal/auth"
)

// bom UTF-8 BOM：從 PowerShell 管線輸入時可能帶 BOM，若不去除，雜湊會與實際密碼不符。
const bom = "\uFEFF"

func main() {
	in := bufio.NewReader(os.Stdin)
	os.Exit(run(func(prompt string) (string, error) { return readSecret(in, prompt) }, os.Stdout, os.Stderr))
}

// run 對應 Python main()：回傳結束碼。read 讀取一次密碼（已顯示提示）。
func run(read func(prompt string) (string, error), stdout, stderr io.Writer) int {
	password, err := read("管理員密碼：")
	if err != nil {
		fmt.Fprintln(stderr, "無法讀取密碼。")
		return 1
	}
	if utf8.RuneCountInString(password) < 8 { // Python len() 以字元計算
		fmt.Fprintln(stderr, "密碼至少需要 8 個字元。")
		return 1
	}
	again, err := read("再輸入一次：")
	if err != nil || again != password {
		fmt.Fprintln(stderr, "兩次輸入的密碼不一致。")
		return 1
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintln(stderr, "產生雜湊失敗。")
		return 1
	}
	fmt.Fprintf(stdout, "ADMIN_PASSWORD_HASH=%s\n", hash)
	return 0
}

// readSecret 在 stderr 顯示提示，讀取一行（終端機時關閉回顯），去掉行尾換行與所有 BOM。
// 讀取中按 Ctrl+C 時先恢復回顯再結束，避免終端機停留在不顯示輸入的狀態。
func readSecret(in *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	restore, hidden := echoOff()
	if hidden {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		done := make(chan struct{})
		go func() {
			select {
			case <-sig:
				restore()
				fmt.Fprintln(os.Stderr)
				os.Exit(130)
			case <-done:
			}
		}()
		defer func() {
			signal.Stop(sig)
			close(done)
			restore()
			fmt.Fprintln(os.Stderr) // 回顯關閉時 Enter 不會換行
		}()
	}
	line, err := in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return cleanLine(line), nil
}

// cleanLine 去掉行尾的 \n、\r\n 與所有 BOM（Python getpass 不含換行；.replace("\uFEFF", "")）。
func cleanLine(line string) string {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return strings.ReplaceAll(line, bom, "")
}
