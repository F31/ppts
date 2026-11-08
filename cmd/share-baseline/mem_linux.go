//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// peakMemory 返回进程的峰值常驻内存（VmHWM，字节）。false 表示读不到。
//
// Linux 上 VmHWM 就是"进程启动至今的最大 RSS"，与 Windows 的工作集峰值同义，
// 所以两个平台报出来的数字可以放在同一张表里比。
func peakMemory() (uint64, bool) {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, convErr := strconv.ParseUint(fields[1], 10, 64)
		if convErr != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
