package music

import (
	"syscall"
	"time"
)

// processCPU 本程序累計的 CPU 時間（使用者＋核心），壓力測試用。
func processCPU() time.Duration {
	var c, e, k, u syscall.Filetime
	h, err := syscall.GetCurrentProcess()
	if err != nil || syscall.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0
	}
	return time.Duration(k.Nanoseconds() + u.Nanoseconds())
}
