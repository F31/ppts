package animation

import "testing"

func revWithClip(elemID, clipID, trigger, source string) *Revision {
	return &Revision{Tracks: []*Track{{
		ElementID: elemID, Kind: KindBullet,
		Clips: []*Clip{{ID: clipID, Type: "fadeIn", DurationMs: 400, Easing: "easeOut",
			Trigger: trigger, TriggerSource: source}},
	}}}
}

func TestReconcileDowngradesChangedTrigger(t *testing.T) {
	prev := revWithClip("shape-1", "c1", "withSegment:seg-1", TriggerSourceText)
	next := revWithClip("shape-1", "c1", "withSegment:seg-2", TriggerSourceText)

	if n := ReconcileTriggerSources(prev, next); n != 1 {
		t.Fatalf("降级 %d 条，期望 1", n)
	}
	if got := next.Tracks[0].Clips[0].TriggerSource; got != TriggerSourceManual {
		t.Fatalf("来源 = %q，期望 manual（不能保留原规则名）", got)
	}
}

func TestReconcileKeepsSourceWhenTriggerUnchanged(t *testing.T) {
	prev := revWithClip("shape-1", "c1", "withSegment:seg-1", TriggerSourceSibling)
	next := revWithClip("shape-1", "c1", "withSegment:seg-1", TriggerSourceSibling)
	// 只调时长：来源描述的是绑定结论，不该因为改了动效时长就作废。
	next.Tracks[0].Clips[0].DurationMs = 800

	if n := ReconcileTriggerSources(prev, next); n != 0 {
		t.Fatalf("降级 %d 条，期望 0", n)
	}
	if got := next.Tracks[0].Clips[0].TriggerSource; got != TriggerSourceSibling {
		t.Fatalf("来源被清成了 %q", got)
	}
}

func TestReconcileIgnoresUnannotatedPrev(t *testing.T) {
	// 手写规格改 trigger：本来就没有规则名可清，不动。
	prev := revWithClip("shape-1", "c1", "onEnter", "")
	next := revWithClip("shape-1", "c1", "withSegment:seg-3", "")
	ReconcileTriggerSources(prev, next)
	if got := next.Tracks[0].Clips[0].TriggerSource; got != "" {
		t.Fatalf("无标注的历史规格被改成了 %q", got)
	}
}

func TestReconcileKeepsManual(t *testing.T) {
	prev := revWithClip("shape-1", "c1", "withSegment:seg-1", TriggerSourceManual)
	next := revWithClip("shape-1", "c1", "withSegment:seg-2", TriggerSourceManual)
	ReconcileTriggerSources(prev, next)
	if got := next.Tracks[0].Clips[0].TriggerSource; got != TriggerSourceManual {
		t.Fatalf("manual 被改成了 %q", got)
	}
}

func TestReconcileDoesNotGuessByPositionWhenIDMismatched(t *testing.T) {
	// 前端换了新 id：匹配不上就不动。按位置猜会在用户插入一条 clip 时把结论安错元素。
	prev := revWithClip("shape-1", "c1", "withSegment:seg-1", TriggerSourceOrdinal)
	next := revWithClip("shape-1", "c9", "withSegment:seg-2", TriggerSourceOrdinal)
	if n := ReconcileTriggerSources(prev, next); n != 0 {
		t.Fatalf("按位置猜中了 %d 条", n)
	}
}

func TestReconcileMatchesByPositionWithoutID(t *testing.T) {
	// 没有 id 的 spec（旧手写规格）只能按位置：同一元素下第 i 条对第 i 条。
	prev := revWithClip("shape-1", "", "withSegment:seg-1", TriggerSourceText)
	next := revWithClip("shape-1", "", "withSegment:seg-2", TriggerSourceText)
	if n := ReconcileTriggerSources(prev, next); n != 1 {
		t.Fatalf("降级 %d 条，期望 1", n)
	}
}

func TestReconcileNilSafe(t *testing.T) {
	if n := ReconcileTriggerSources(nil, revWithClip("a", "c", "onEnter", "")); n != 0 {
		t.Fatal("prev 为 nil 时不该动")
	}
	if n := ReconcileTriggerSources(revWithClip("a", "c", "onEnter", ""), nil); n != 0 {
		t.Fatal("next 为 nil 时不该动")
	}
}
