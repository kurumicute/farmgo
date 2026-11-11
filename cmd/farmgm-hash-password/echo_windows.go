//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// enableEchoInput Windows 主控台的 ENABLE_ECHO_INPUT。
const enableEchoInput = 0x0004

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// echoOff 關閉主控台回顯；標準輸入不是主控台（管線、檔案）時回傳 ok=false。
func echoOff() (restore func(), ok bool) {
	h := syscall.Stdin
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return func() {}, false
	}
	if r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput)); r == 0 {
		return func() {}, false
	}
	return func() { procSetConsoleMode.Call(uintptr(h), uintptr(mode)) }, true
}
