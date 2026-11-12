//go:build !windows

package bot

import (
	"syscall"
	"time"
)

// processCPU 本程序累計的 CPU 時間（使用者＋核心），壓力測試用。
func processCPU() time.Duration {
	var r syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &r) != nil {
		return 0
	}
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}
