//go:build linux

package main

import (
	"syscall"
	"unsafe"
)

// echoOff 關閉終端機回顯；標準輸入不是終端機（管線、檔案）時回傳 ok=false。
func echoOff() (restore func(), ok bool) {
	fd := uintptr(syscall.Stdin)
	var old syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&old))); e != 0 {
		return func() {}, false
	}
	t := old
	t.Lflag &^= syscall.ECHO
	t.Lflag |= syscall.ICANON | syscall.ISIG
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		return func() {}, false
	}
	return func() { syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old))) }, true
}
