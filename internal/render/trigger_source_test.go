package render

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
)

// 1.2（后半）：来源标注是**编辑期元信息**，不能影响渲染计划。
//
// 这条测试的意义不在「加了字段没坏」，而在于把「Clip 的编辑字段不得泄漏进 IR」
// 钉成契约：一旦有人图省事把整个 Clip 塞进 Motion，或者把触发来源写进 planHash，
// 同一份画面会因为「这条是自动绑的还是人改的」而算出两个不同的哈希，
// 缓存命中率下降，且排查「同输入两份产物」时会得到一个假线索。
func TestTriggerSourceDoesNotAffectPlan(t *testing.T) {
	c := newTestCompiler(t)

	build := func(source string) Timeline {
		clip := clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")
		clip.TriggerSource = source
		rev := revisionOf("business-calm", 1, &animation.Track{
			ElementID: "shape-1", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clip},
		})
		page, _, err := c.CompilePage(PageInput{
			Index: 0, SlideID: "slide-1", DurationMs: 6000, BaseImage: "pages/p0.png",
			Elements:       []Element{{ID: "shape-1", Kind: animation.KindBullet, Bounds: emuBox(96, 54, 960, 540)}},
			Revision:       rev,
			SegmentOffsets: map[string]int64{"seg-1": 1200},
		})
		if err != nil {
			t.Fatalf("编译失败: %v", err)
		}
		return Timeline{SchemaVersion: "1", FPS: 30, Width: 1920, Height: 1080, Pages: []Page{page}}
	}

	plain := build("")
	annotated := build(animation.TriggerSourceSibling)

	a, err := plain.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := annotated.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("来源标注改变了计划字节\n无标注: %s\n有标注: %s", a, b)
	}

	ha, err := plain.Hash(HashOptions{RendererVersion: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	hb, err := annotated.Hash(HashOptions{RendererVersion: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("来源标注改变了 planHash: %s vs %s", ha, hb)
	}
}
