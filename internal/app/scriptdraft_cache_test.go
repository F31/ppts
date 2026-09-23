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
			narration.ModePolish, draftInput{Kind: inputPage, Text: source}, ScriptDraftSnapshot{}, f)
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
			narration.ModePolish, draftInput{Kind: inputPage, Text: source}, ScriptDraftSnapshot{Style: style}, f); err != nil {
			t.Fatalf("draftText: %v", err)
		}
	}
	call("专业正式")
	call("口语自然")

	if len(f.calls) != 2 {
		t.Fatalf("expected 2 LLM calls (different instructions), got %d", len(f.calls))
	}
}
