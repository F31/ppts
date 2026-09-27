// Package contextrule 提供上下文替换规则的持久化（M5，V3.0 §3.3 数据驱动）。
//
// 与发音词典的区别：规则是「正则 → 产出模板」（如 `([0-9]+)(%)` → `百分之$1`），
// 由 textnorm 引擎按 ReplacementPattern 语义执行（逐段匹配 + 精确坐标上报），
// 而非整 span 字面量 ReplaceAll。
package contextrule

// Record 是一条上下文替换规则（单行=单规则模型，contextual_rules 表）。
// Pattern 为 Go regexp（支持捕获组）；Replacement 为产出模板（$1 / ${name}）。
// Priority 决定执行顺序（小值先，同 textnorm 规则语义）。Enabled=false 时跳过。
// TenantID 为空（PG NULL）表示平台默认行（对标发音词典 is_platform_default）。
type Record struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenantId,omitempty"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   int64  `json:"createdAt,omitempty"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}
