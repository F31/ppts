package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateStaysValidUTF8 防止按字节截断切断多字节字符（回归：中文备注预览曾导致
// proto marshal 报 "contains invalid UTF-8"）。
func TestTruncateStaysValidUTF8(t *testing.T) {
	long := strings.Repeat("汉", 200) // 每字 3 字节，远超 120
	for _, n := range []int{1, 2, 40, 120, 121} {
		got := truncate(long, n)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(len=%d) produced invalid UTF-8: %q", n, got)
		}
		if utf8.RuneCountInString(got) != n+1 { // n 个字符 + 省略号
			t.Fatalf("truncate(len=%d) rune count = %d, want %d", n, utf8.RuneCountInString(got), n+1)
		}
	}
}

func TestTruncateShortStringUnchanged(t *testing.T) {
	s := "短备注"
	if got := truncate(s, 120); got != s {
		t.Fatalf("truncate short = %q, want %q", got, s)
	}
}
