package app

import (
	"context"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/narration"
)

// 本文件锁定「概括计数放行后必须留提示」这条行为：放行是策略，不提示是失职。
// 依据见 internal/validation/counts.go 与 rules.json 的 counts 段。

func runDraftDetail(t *testing.T, f *fakeRewriter, source string) draftTextResult {
	t.Helper()
	h := newDraftHandlerForTest(f)
	res, err := h.draftTextDetail(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, draftInput{Kind: inputPage, Text: source}, nil, ScriptDraftSnapshot{}, h.polisher)
	if err != nil {
		t.Fatalf("draftTextDetail 报错：%v", err)
	}
	return res
}

// TestToleratedCountSurfacesNote：模型自己数了条目 → 成稿可用，但步骤上要留提示。
func TestToleratedCountSurfacesNote(t *testing.T) {
	const source = "企业培训\n在线课程\n销售路演\n无障碍"
	const out = "本页覆盖 4 类场景：内训、微课、路演素材与多语言讲解，分别对应不同受众与交付方式。"
	res := runDraftDetail(t, &fakeRewriter{replies: []string{out}}, source)

	if res.Degraded {
		t.Fatalf("概括计数不该导致降级（这正是本次优化的目标）：%q", res.Display)
	}
	if res.Display != out {
		t.Fatalf("落库内容 = %q，应为模型产出", res.Display)
	}
	if !strings.Contains(res.Note, "提示") {
		t.Fatalf("放行却没有提示，等于无声宽松：%q", res.Note)
	}
	if !strings.Contains(res.Note, "数字 4") {
		t.Fatalf("提示未点名数字：%q", res.Note)
	}
	if !strings.Contains(res.Note, "4 类场景") {
		t.Fatalf("提示未带原文片段，用户无法核对：%q", res.Note)
	}
}

// TestInsertedUnitFactStillDegrades：带单位的数字永远是事实，不享受计数豁免。
func TestInsertedUnitFactStillDegrades(t *testing.T) {
	const source = "本页讲增长与成本"
	out := "本页讲增长 40%，成本下降 20%，效率显著提升，值得在内部推广与复用。"
	res := runDraftDetail(t, &fakeRewriter{replies: []string{out, out}}, source)

	if !res.Degraded {
		t.Fatalf("编造带单位数值却判成功：%q", res.Display)
	}
	if !strings.Contains(res.Reason, "40%") && !strings.Contains(res.Reason, "数字 40") {
		t.Fatalf("降级原因未点名编造的数值：%q", res.Reason)
	}
	if res.Note != "" {
		t.Fatalf("降级页不该带「已放行」提示：%q", res.Note)
	}
}
