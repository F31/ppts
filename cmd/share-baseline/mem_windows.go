//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// processMemoryCounters 是 psapi 的 PROCESS_MEMORY_COUNTERS。
//
// 字段必须补全：Windows 按结构布局直接写内存，少一个字段就会把后面的值读偏，
// 而偏掉的数字看起来仍然"像个内存用量"。
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

var (
	psapiProc              = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
	kernel32CurProcessProc = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess")
)

// peakMemory 返回进程峰值工作集（字节）。false 表示这台机器取不到。
//
// 用工作集而不是 Go 堆：成品包组装的内存大头是 []byte 与磁盘缓冲，
// 真正决定"会不会被 OOM 杀掉"的是进程占用，不是 GC 统计。
func peakMemory() (uint64, bool) {
	h, _, _ := kernel32CurProcessProc.Call()
	var counters processMemoryCounters
	counters.cb = uint32(unsafe.Sizeof(counters))
	if r, _, _ := psapiProc.Call(h, uintptr(unsafe.Pointer(&counters)), uintptr(counters.cb)); r == 0 {
		return 0, false
	}
	return uint64(counters.peakWorkingSetSize), true
}
