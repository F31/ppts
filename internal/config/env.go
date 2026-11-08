// Package config 是进程配置的唯一读取入口。
//
// 为什么不让各处直接调 os.Getenv：同一件事——「布尔环境变量哪些取值算真」——在
// api 与 cmd 里各写过一份，合法取值集合靠人肉保持同步；更麻烦的是被抄出来的分叉
// 版本只认 "1"/"true"，于是 PPTS_PPROF=yes 与 PPTS_HSTS=yes 一个开一个不开，
// 而没有任何地方说得出这是有意的。这里把「未设置 / 空串 / 非法值」三种情况的语义
// 定死一次，调用方只关心键名与缺省值。
//
// 约定：
//   - 未设置、空串、纯空白一律视为「没配」，返回 fallback；
//   - 非法值同样返回 fallback，不 panic、不静默取零值——零值常常是危险默认；
//   - 布尔只认 1/true/yes/on 与 0/false/no/off（大小写不敏感），其余按非法处理。
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// String 返回去掉首尾空白后的取值；未设置或仅空白时返回 fallback。
func String(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// Set 报告该键是否被显式设置为非空值。
//
// 用于「配了空串」与「压根没配」需要区别对待的场合：前者通常是运维显式清空，
// 后者才该落到缺省值。
func Set(key string) bool {
	return strings.TrimSpace(os.Getenv(key)) != ""
}

// Bool 读取布尔开关。合法取值为 1/true/yes/on 与 0/false/no/off（大小写不敏感），
// 其余取值（含未设置）返回 fallback。
func Bool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// Int 读取整数；未设置或非法时返回 fallback。
func Int(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// Int64 读取 64 位整数；未设置或非法时返回 fallback。
func Int64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

// Float 读取浮点数；未设置或非法时返回 fallback。
func Float(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return f
}

// Duration 按 Go 时长语法读取（如 90s、5m）；未设置、非法或非正值时返回 fallback。
//
// 不额外支持「纯数字按秒」：同一份配置里混着两种量纲的写法，读的人必须去翻实现
// 才知道 30 是秒还是纳秒。
//
// 非正值一律回退：这一族配置几乎全是轮询间隔与超时，0 会让 worker 忙转、负数
// 让 time.After 立即触发。真要「关掉」某一项，用对应的布尔开关而不是把间隔设 0。
func Duration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// List 读取逗号分隔的字符串列表，逐项去空白并丢弃空项。
//
// 未设置时返回 nil 而不是空切片：调用方用 len()==0 判断「没配」是常态，
// 但需要区分「配了但全是逗号」的场合应当自行比较 nil。
func List(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
