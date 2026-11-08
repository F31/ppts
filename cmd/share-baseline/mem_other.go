//go:build !windows && !linux

package main

// peakMemory 在其它平台取不到峰值内存；返回 false 让 -rss 退化成"不报这一项"，
// 而不是编一个数字——这条基线只在这些平台上有定义。
func peakMemory() (uint64, bool) { return 0, false }
