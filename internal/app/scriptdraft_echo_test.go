package app

import (
	"context"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/validation"
)

// 本文件锁定 2026-11-02 复验时抓到的第二类假成功：模型把页面文字原样回显。
//
// slide-266 落库的"讲稿"就是源形状逐行拼接的结果（MULTI-PLATFORM / Web / 桌面端…），
// 实体校验必然通过（逐字相同不可能漏事实），于是"没改写"成了最安全的通过方式。
const echoSource = `MULTI-PLATFORM
多端统一体验，桌面端支持隐私模式
Web
浏览器直接使用，编辑、审核、播放随时随地
桌面端
支持「隐私模式」：PPT解析与配音可完全本地完成，文件不出设备
移动端
云端生成 + 本地缓存播放，专注审阅、试听与分享`

func TestDraftEchoesSource(t *testing.T) {
	cases := []struct {
		name   string
		source string
		out    string
		want   bool
	}{
		{
			name:   "逐行照抄",
			source: echoSource,
			out:    echoSource,
			want:   true,
		},
		{
			name:   "只改标点与换行",
			source: echoSource,
			out:    strings.ReplaceAll(echoSource, "\n", "，"),
			want:   true,
		},
		{
			name:   "改写成讲稿",
			source: echoSource,
			out: "这套系统在多端体验上是统一的：网页端打开浏览器就能用，" +
				"编辑、审核、播放不受地点限制；桌面端提供隐私模式，" +
				"PPT 解析和配音都可以完全在本地完成，文件不会离开设备；" +
				"移动端则是云端生成加本地缓存播放，专注审阅、试听与分享。",
			want: false,
		},
		{
			name:   "短标题页不误判",
			source: "封面",
			out:    "封面",
			want:   false,
		},
		{
			name:   "行数不足不判结构照搬",
			source: "为什么是现在\n三个结构性变化",
			out:    "为什么是现在\n三个结构性变化",
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := draftEchoesSource(c.source, c.out); got != c.want {
				t.Errorf("draftEchoesSource = %v, want %v\nsource=%q\nout=%q", got, c.want, c.source, c.out)
			}
		})
	}
}

// TestEchoOutputIsNotSuccess 两轮都照抄时不得判成功：回退原文并说明原因。
func TestEchoOutputIsNotSuccess(t *testing.T) {
	f := &fakeRewriter{replies: []string{echoSource, echoSource}}
	got, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, echoSource)
	if !degraded {
		t.Fatalf("照抄原文却被判成功，产出：%q", got)
	}
	if got != echoSource {
		t.Fatalf("降级应回退原文，得到 %q", got)
	}
	if len(f.calls) != 2 {
		t.Fatalf("应给第二轮机会，实际调用 %d 次", len(f.calls))
	}
	// 第二轮必须拿原始素材重写，而不是在"照抄版"上继续修补。
	if f.calls[1].SourceText != echoSource {
		t.Fatalf("第二轮素材不是原始素材：%q", f.calls[1].SourceText)
	}
	// 修复指令必须是"重新写成讲稿"，不能是"除实体外一字不动"。
	if !strings.Contains(f.calls[1].Instructions, "原样照抄") {
		t.Fatalf("第二轮指令未指出照抄问题：%q", f.calls[1].Instructions)
	}
	if strings.Contains(f.calls[1].Instructions, "不要改动其它措辞与结构") {
		t.Fatalf("第二轮用了定点修补指令，模型只会继续照抄：%q", f.calls[1].Instructions)
	}
}

// TestEchoThenRewriteSucceeds 第一版照抄、第二版真的改写 → 成功，且落库的是改写稿。
func TestEchoThenRewriteSucceeds(t *testing.T) {
	rewritten := "这套系统在多端体验上是统一的：网页端打开浏览器就能用，" +
		"编辑、审核、播放不受地点限制；桌面端提供隐私模式，" +
		"PPT 解析和配音都可以完全在本地完成，文件不会离开设备；" +
		"移动端则是云端生成加本地缓存播放，专注审阅、试听与分享。"
	f := &fakeRewriter{replies: []string{echoSource, rewritten}}
	got, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, echoSource)
	if degraded {
		t.Fatalf("第二轮已改写，不应降级：%q", got)
	}
	if got != rewritten {
		t.Fatalf("落库内容 = %q，应为第二版改写稿", got)
	}
}

// TestEchoReasonNamesTheProblem 降级原因必须写清"照抄"，不能落到"数字不一致"的兜底。
func TestEchoReasonNamesTheProblem(t *testing.T) {
	f := &fakeRewriter{replies: []string{echoSource, echoSource}}
	h := newDraftHandlerForTest(f)
	res, err := h.draftTextDetail(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, draftInput{Kind: inputPage, Text: echoSource}, nil, ScriptDraftSnapshot{}, h.polisher)
	if err != nil {
		t.Fatalf("draftTextDetail 报错：%v", err)
	}
	if !res.Degraded {
		t.Fatal("照抄应降级")
	}
	if !strings.Contains(res.Reason, "照抄") {
		t.Fatalf("降级原因未说明照抄：%q", res.Reason)
	}
}

// TestNumberContextHint 降级原因必须能自证："新增数字 4" 后面要跟着原文片段，
// 否则用户无法区分"模型在数条目"和"模型编了数字"。
func TestNumberContextHint(t *testing.T) {
	ins := []validation.Entity{{Kind: "number", Text: "4"}, {Kind: "model", Text: "A100"}}
	got := numberContextHint(ins, "整体覆盖 4 类典型场景，均已在客户侧验证。")
	if !strings.Contains(got, "4 类典型场景") {
		t.Fatalf("未截取到数字上下文：%q", got)
	}
	if got := numberContextHint([]validation.Entity{{Kind: "model", Text: "A100"}}, "A100 很强"); got != "" {
		t.Fatalf("非数字实体不该截取上下文：%q", got)
	}
}
