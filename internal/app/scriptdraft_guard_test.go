package app

import (
	"context"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/validation"
)

// 本文件锁定「两轮校验」的行为，全部用假供应商跑，不依赖 LLM / DB / 外部工具：
//  ① 第二轮必须以**第一版文案**为输入做定点修补（M12），不能重发原文重写一遍；
//  ② 两轮都不过仍必须回退原文并标 degraded（M9 的不变量，不得被 M12 改坏）；
//  ③ 归一化（M11）生效后，等价写法不再触发第二轮。

type fakeRewriter struct {
	replies []string
	calls   []llm.RewriteRequest
}

func (f *fakeRewriter) Rewrite(_ context.Context, req llm.RewriteRequest) (llm.RewriteResult, error) {
	f.calls = append(f.calls, req)
	i := len(f.calls) - 1
	if i >= len(f.replies) {
		i = len(f.replies) - 1
	}
	return llm.RewriteResult{Text: f.replies[i]}, nil
}

func newDraftHandlerForTest(f *fakeRewriter) *ScriptDraftHandler {
	return NewScriptDraftHandler(nil, nil).WithPolisher(f)
}

func runDraftText(t *testing.T, h *ScriptDraftHandler, kind draftInputKind, source string) (string, bool) {
	t.Helper()
	text, _, degraded, err := h.draftText(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, draftInput{Kind: kind, Text: source}, ScriptDraftSnapshot{})
	if err != nil {
		t.Fatalf("draftText 意外报错：%v", err)
	}
	return text, degraded
}

// TestGuardRoundRepairsPreviousDraft 是 M12 的守门用例：
// 第二轮必须拿到**第一版产出**作为待修补文本，且指令里带具体违规项。
// 若退回「重发原文」，calls[1].SourceText 会等于原始素材，本用例立即失败。
func TestGuardRoundRepairsPreviousDraft(t *testing.T) {
	const source = "容量 80GB"
	const firstDraft = "这台设备的存储容量相当充裕。"
	f := &fakeRewriter{replies: []string{firstDraft, "这台设备的存储容量为 80GB。"}}
	got, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, source)

	if degraded {
		t.Fatalf("第二轮已修好，不应标记 degraded")
	}
	if got != "这台设备的存储容量为 80GB。" {
		t.Fatalf("产出 = %q，应为第二轮修补结果", got)
	}
	if len(f.calls) != 2 {
		t.Fatalf("应恰好调用两次（第一版 + 一次定点修补），实际 %d 次", len(f.calls))
	}
	if f.calls[0].SourceText != source {
		t.Fatalf("第一轮输入 = %q，应为原始素材", f.calls[0].SourceText)
	}
	if f.calls[1].SourceText != firstDraft {
		t.Fatalf("第二轮输入 = %q，应为第一版文案 %q（重发原文会让模型重写一遍、丢掉已写好的部分）",
			f.calls[1].SourceText, firstDraft)
	}
	if !strings.Contains(f.calls[1].Instructions, "number:80gb") {
		t.Fatalf("第二轮指令未列出具体违规实体：%q", f.calls[1].Instructions)
	}
	if !strings.Contains(f.calls[1].Instructions, "最小必要修改") {
		t.Fatalf("第二轮指令未要求最小改动：%q", f.calls[1].Instructions)
	}
}

// TestBothRoundsFailFallsBackToSource 守住 M9 的不变量：两轮都不过 → 回退原文 + degraded。
// M12 改了第二轮的输入，这条路径必须依然成立（否则原始素材会被当成稿落库却报成功）。
func TestBothRoundsFailFallsBackToSource(t *testing.T) {
	const source = "容量 80GB"
	f := &fakeRewriter{replies: []string{"存储很大。", "存储依旧很大。"}}
	got, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, source)

	if !degraded {
		t.Fatalf("两轮都不过必须标记 degraded，得到 degraded=false")
	}
	if got != source {
		t.Fatalf("回退结果 = %q，应为原始素材 %q", got, source)
	}
	if len(f.calls) != 2 {
		t.Fatalf("应恰好尝试两次，实际 %d 次", len(f.calls))
	}
}

// TestFirstRoundOKSkipsGuard 保证不过度调用：第一轮就合规时不得再发第二轮。
func TestFirstRoundOKSkipsGuard(t *testing.T) {
	f := &fakeRewriter{replies: []string{"这台设备的存储容量为 80GB，相当充裕。"}}
	got, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, "容量 80GB")

	if degraded {
		t.Fatalf("产出合规，不应 degraded")
	}
	if got != "这台设备的存储容量为 80GB，相当充裕。" {
		t.Fatalf("产出 = %q", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("第一轮即通过，应只调用一次，实际 %d 次", len(f.calls))
	}
}

// TestEquivalentSpellingSkipsGuard 是 M11 × M12 的联动用例：
// 归一化之前，「12ms」被写成「12 毫秒」会判违规并触发第二轮（甚至最终回退原文）；
// 归一化之后必须一次通过。若这条变红，说明 M11 的等价表失效或 M12 又改回了重发原文。
func TestEquivalentSpellingSkipsGuard(t *testing.T) {
	cases := []struct {
		name   string
		source string
		draft  string
	}{
		{"单位改中文量词", "延迟 12ms", "延迟控制在 12 毫秒以内。"},
		{"日期加空格", "2026-09-13 上线", "计划在 2026 年 9 月 13 日上线。"},
		{"千分位与尾随零", "覆盖 1,000 家企业", "覆盖 1000 家企业。"},
		{"全角数字", "共 １２ 页", "共 12 页。"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRewriter{replies: []string{c.draft}}
			if _, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, c.source); degraded {
				t.Fatalf("等价写法不应降级：source=%q draft=%q", c.source, c.draft)
			}
			if len(f.calls) != 1 {
				t.Fatalf("等价写法不应触发第二轮，实际调用 %d 次", len(f.calls))
			}
		})
	}
}

// TestGuardInstructionsListViolations 锁定第二轮指令的内容（纯函数，无 LLM）。
func TestGuardInstructionsListViolations(t *testing.T) {
	report := validation.CheckPreserved("容量 80GB", "存储很大。")
	if report.OK() {
		t.Fatalf("前置条件不成立：应判为违规")
	}
	got := guardInstructions(report)
	if !strings.Contains(got, "number:80gb") {
		t.Errorf("指令未列出遗漏实体：%q", got)
	}
	if !strings.Contains(got, "最小必要修改") {
		t.Errorf("指令未要求最小改动：%q", got)
	}
	if !strings.Contains(got, "只输出修正后的正文") {
		t.Errorf("指令缺少输出约束：%q", got)
	}

	inserted := validation.CheckPreserved("容量 80GB", "存储 96GB，容量很大。")
	gotInserted := guardInstructions(inserted)
	if !strings.Contains(gotInserted, "number:96gb") {
		t.Errorf("指令未列出新增实体：%q", gotInserted)
	}

	// 兜底分支不得产出半句话（调用方只在 !OK() 时进入，但函数自身要无死角）。
	empty := guardInstructions(validation.Report{})
	if !strings.Contains(empty, "不一致") || !strings.Contains(empty, "只输出修正后的正文") {
		t.Errorf("空报告下的指令不完整：%q", empty)
	}
}
