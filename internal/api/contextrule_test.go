package api

import "testing"

// TestValidateContextRule 校验上下文规则合法性（R1：非法正则不可静默入库）。
func TestValidateContextRule(t *testing.T) {
	// 合法：含捕获组的 Go 正则 + 模板。
	if msg := validateContextRule(`([0-9]+)(%)`, `百分之$1`); msg != "" {
		t.Fatalf("valid rule rejected: %q", msg)
	}
	// 空 pattern / 空 replacement。
	if msg := validateContextRule("", `x`); msg == "" {
		t.Fatal("empty pattern should be rejected")
	}
	if msg := validateContextRule(`x`, ""); msg == "" {
		t.Fatal("empty replacement should be rejected")
	}
	// 非法正则（未闭合分组）→ 拒绝。
	if msg := validateContextRule(`([0-9]+`, `x`); msg == "" {
		t.Fatal("invalid regexp should be rejected")
	}
}
