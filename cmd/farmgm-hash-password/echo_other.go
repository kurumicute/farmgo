//go:build !windows && !linux

package main

import (
	"fmt"
	"os"
)

// echoOff 其他平台不支援關閉回顯（與 Python getpass 的 GetPassWarning 相同，提醒輸入會顯示在畫面上）。
func echoOff() (restore func(), ok bool) {
	fmt.Fprint(os.Stderr, "（警告：此平台無法隱藏輸入，密碼會顯示在畫面上）")
	return func() {}, false
}
