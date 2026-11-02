package validation

import (
	"strings"
	"testing"
)

// TestFormatEntitiesHuman 锁定「提示词里只说人话」这条契约。
//
// 事故背景（2026-11-02）：guard 指令把内部键 number:3 原样发给模型，模型把它当成
// 要写进正文的内容，产出「这就是number:1」「专业编辑8能力」「本系统为版本11。」，
// 而数字集合恰好匹配源素材，于是被记成 success。内部键只能出现在日志与诊断里。
func TestFormatEntitiesHuman(t *testing.T) {
	cases := []struct {
		in   Entity
		want string
	}{
		{Entity{Kind: "number", Text: "64gb"}, "64GB"},
		{Entity{Kind: "number", Text: "9"}, "数字 9"},
		{Entity{Kind: "number", Text: "12ms"}, "12毫秒"},
		{Entity{Kind: "number", Text: "5%"}, "5%"},
		{Entity{Kind: "number", Text: "3"}, "数字 3"},
		{Entity{Kind: "date", Text: "2026-09-13"}, "日期 2026-09-13"},
		{Entity{Kind: "model", Text: "a100"}, "型号 a100"},
	}
	for _, c := range cases {
		if got := humanEntity(c.in); got != c.want {
			t.Errorf("humanEntity(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatEntitiesHumanNeverLeaksInternalKeys(t *testing.T) {
	ents := ExtractEntities("容量 64GB，延迟 12ms，覆盖 5%，A100 于 2026-09-13 上线。")
	got := FormatEntitiesHuman(ents)
	if len(got) == 0 {
		t.Fatalf("前置条件不成立：未提取到实体")
	}
	joined := strings.Join(got, "、")
	for _, bad := range []string{"number:", "date:", "model:"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("人类可读格式泄漏内部键 %q：%q", bad, joined)
		}
	}
}

// TestFormatEntitiesHumanDedup 同一事实的不同写法归一后只出现一次，避免指令里
// 出现「数字 3、数字 3」这种让模型困惑的重复项。
func TestFormatEntitiesHumanDedup(t *testing.T) {
	ents := []Entity{
		{Kind: "number", Text: "3"},
		{Kind: "number", Text: "3"},
	}
	if got := FormatEntitiesHuman(ents); len(got) != 1 {
		t.Fatalf("去重失败：%v", got)
	}
}
