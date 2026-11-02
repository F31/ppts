package app

import (
	"context"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
)

// TestScriptDraftCacheSkipsSecondLLMCall 锁定内容缓存：相同（模式+语言+指令+素材）
// 第二次生成必须命中缓存、不再调用模型（跨页/跨项目复用同一份产出）。
func TestScriptDraftCacheSkipsSecondLLMCall(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	f := &fakeRewriter{replies: []string{"本页介绍 100 型号A 的内容"}}
	h := NewScriptDraftHandler(nil, objects).WithPolisher(f)

	const source = "100 型号A"
	draft := func(projectID, slideID string) string {
		text, _, degraded, err := h.draftText(context.Background(), "tenant-1", projectID, slideID, "zh-CN",
			narration.ModePolish, draftInput{Kind: inputPage, Text: source}, nil, ScriptDraftSnapshot{}, f)
		if err != nil {
			t.Fatalf("draftText: %v", err)
		}
		if degraded {
			t.Fatalf("unexpected degraded result")
		}
		return text
	}

	first := draft("project-1", "slide-1")
	// 不同页、相同素材与参数：应从缓存复用，而不是再调用模型。
	second := draft("project-2", "slide-2")

	if first != second {
		t.Fatalf("cache mismatch: first=%q second=%q", first, second)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 LLM call (second should hit cache), got %d", len(f.calls))
	}
}

// TestScriptDraftCacheMissOnDifferentInstructions 锁定缓存按指令区分：
// 参数（如风格/受众）变化会改变指令 → 缓存未命中 → 必须重新调用模型。
func TestScriptDraftCacheMissOnDifferentInstructions(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	f := &fakeRewriter{replies: []string{"本页介绍 100 型号A 的内容"}}
	h := NewScriptDraftHandler(nil, objects).WithPolisher(f)

	const source = "100 型号A"
	call := func(style string) {
		if _, _, _, err := h.draftText(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
			narration.ModePolish, draftInput{Kind: inputPage, Text: source}, nil, ScriptDraftSnapshot{Style: style}, f); err != nil {
			t.Fatalf("draftText: %v", err)
		}
	}
	call("专业正式")
	call("口语自然")

	if len(f.calls) != 2 {
		t.Fatalf("expected 2 LLM calls (different instructions), got %d", len(f.calls))
	}
}

// TestScriptDraftCacheRevalidatesAgainstCurrentRules 锁定缓存命中也要过**现行**规则。
//
// 真实事故（2026-11-02，slide-258）：中文序数改成按序号判定后重新生成同一页，
// 任务 0.43 秒即成功——命中内容缓存，缓存路径只查了"文本没坏"（含内部标记/过短/
// 照抄原文），没有按当前规则复验，于是一份带「第一个变化」的旧稿被直接标成成功。
// 缓存 key 不含规则版本，规则一变，旧产出就可能不合格；这里必须重新判定。
func TestScriptDraftCacheRevalidatesAgainstCurrentRules(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	f := &fakeRewriter{replies: []string{"本页介绍 100 型号A 的内容。"}}
	h := NewScriptDraftHandler(nil, objects).WithPolisher(f)

	const source = "100 型号A"
	snap := ScriptDraftSnapshot{}
	in := draftInput{Kind: inputPage, Text: source}

	// 按生产同一套函数算出缓存 key，再种一份"旧规则下合格、现规则不合格"的稿子：
	// 素材没有序号，稿件自行编号——insertedOrdinals=block 该拦。
	instructions := draftInstructions(narration.ModePolish, in.Kind, snap) + factCitationInstruction(nil)
	promptSource, _ := augmentDraftSource(source, nil)
	key := h.scriptCacheKey("tenant-1", narration.ModePolish, "zh-CN", instructions, promptSource)
	h.saveScriptCache(context.Background(), key, "本页介绍 100 型号A 的内容。第一个变化很重要。")

	text, _, degraded, err := h.draftText(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, in, nil, snap, f)
	if err != nil {
		t.Fatalf("draftText: %v", err)
	}
	if degraded {
		t.Fatalf("重新生成后应成功，实际降级")
	}
	if len(f.calls) != 1 {
		t.Fatalf("缓存不合格时必须重新调用模型，实际调用 %d 次", len(f.calls))
	}
	if text != "本页介绍 100 型号A 的内容。" {
		t.Fatalf("应返回新生成的稿子，实际 %q", text)
	}
}
