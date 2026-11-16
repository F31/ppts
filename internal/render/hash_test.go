package render

import (
	"strings"
	"testing"
)

func TestHashStable(t *testing.T) {
	a, err := validPlan().Hash(HashOptions{RendererVersion: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := validPlan().Hash(HashOptions{RendererVersion: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("同一计划两次 hash 不同: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("hash 长度 = %d, want 64", len(a))
	}
}

func TestHashChangesWithPixel(t *testing.T) {
	base, err := validPlan().Hash(HashOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := validPlan()
	p.Pages[0].Layers[0].Box.W++ // 只改 1px
	got, err := p.Hash(HashOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got == base {
		t.Fatal("改 1px 必须改变 hash")
	}
}

func TestHashChangesWithRendererVersion(t *testing.T) {
	p := validPlan()
	a, _ := p.Hash(HashOptions{RendererVersion: "r1"})
	b, _ := p.Hash(HashOptions{RendererVersion: "r2"})
	if a == b {
		t.Fatal("渲染器版本必须参与 hash：换版本同一份计划要重渲")
	}
}

// TestHashIgnoresPathPrefix 固定缓存语义：同一份规格在不同租户/项目前缀下
// 必须命中同一份产物，否则缓存按项目碎片化，命中率接近 0。
func TestHashIgnoresPathPrefix(t *testing.T) {
	a := validPlan()
	a.Pages[0].BaseImage = "tenants/t1/projects/p1/anim/p0.png"
	b := validPlan()
	b.Pages[0].BaseImage = "tenants/t2/projects/p9/anim/p0.png"
	ha, _ := a.Hash(HashOptions{})
	hb, _ := b.Hash(HashOptions{})
	if ha != hb {
		t.Fatal("路径前缀不应影响 hash")
	}
}

// 层素材是 baseImage + Boxes 的确定性派生物：文件名不该把缓存搅黄。
func TestHashIgnoresLayerImage(t *testing.T) {
	a := validPlan()
	b := validPlan()
	b.Pages[0].Layers[0].Image = "layers/layer_000_00.png"
	ha, _ := a.Hash(HashOptions{})
	hb, _ := b.Hash(HashOptions{})
	if ha != hb {
		t.Fatal("层素材路径不应影响 hash")
	}
}

func TestHashIgnoresShotPathPrefix(t *testing.T) {
	// 镜头图与 baseImage 同类：同一份规格换个租户前缀不该变成另一份计划。
	a := shotPlan(
		Shot{Index: 0, Image: "tenants/t1/p0.png", StartMs: 0, DurationMs: 6000},
	)
	b := shotPlan(
		Shot{Index: 0, Image: "tenants/t2/p0.png", StartMs: 0, DurationMs: 6000},
	)
	ha, _ := a.Hash(HashOptions{})
	hb, _ := b.Hash(HashOptions{})
	if ha != hb {
		t.Fatal("镜头图的路径前缀不应影响 hash")
	}
}

func TestHashChangesWithShotImage(t *testing.T) {
	// 与 Layer.Image 相反：镜头图是**内容**，换一张画面就变了，必须换 hash，
	// 否则缓存会拿旧片段顶替新画面。
	a := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 3000},
		Shot{Index: 1, Image: "pages/zoomA.png", StartMs: 3000, DurationMs: 3000},
	)
	b := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 3000},
		Shot{Index: 1, Image: "pages/zoomB.png", StartMs: 3000, DurationMs: 3000},
	)
	ha, _ := a.Hash(HashOptions{})
	hb, _ := b.Hash(HashOptions{})
	if ha == hb {
		t.Fatal("换镜头图必须改变 hash，否则会命中旧片段")
	}
}

func TestCanonicalJSONDoesNotMutateShots(t *testing.T) {
	// CanonicalJSON 会把 Image 归一为 basename。若 Shots 是浅拷贝，
	// 这一步会改到调用方的计划——下一次 PathPrefix 判断就失效了。
	plan := shotPlan(
		Shot{Index: 0, Image: "tenants/t1/p0.png", StartMs: 0, DurationMs: 6000},
	)
	if _, err := plan.CanonicalJSON(); err != nil {
		t.Fatal(err)
	}
	if got := plan.Pages[0].Shots[0].Image; got != "tenants/t1/p0.png" {
		t.Fatalf("CanonicalJSON 改到了原计划：%q", got)
	}
}

func TestHashIgnoresWarnings(t *testing.T) {
	a := validPlan()
	b := validPlan()
	b.Warnings = []Diagnostic{{Code: DiagGeometryInferred, Message: "x"}}
	ha, _ := a.Hash(HashOptions{})
	hb, _ := b.Hash(HashOptions{})
	if ha != hb {
		t.Fatal("诊断不影响像素，不应参与 hash")
	}
}

func TestHashIgnoresEmptyGazeFields(t *testing.T) {
	a := validPlan()
	ha, err := a.Hash(HashOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b := validPlan()
	b.Pages[0].Annotations = []Annotation{}
	b.Pages[0].LookCrop = false
	hb, err := b.Hash(HashOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatal("空 annotations / lookCrop=false 不得改变 hash")
	}
}

func TestHashChangesWithLookCrop(t *testing.T) {
	a := validPlan()
	a.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}}}
	ha, _ := a.Hash(HashOptions{})
	b := validPlan()
	b.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}}}
	b.Pages[0].LookCrop = true
	hb, _ := b.Hash(HashOptions{})
	if ha == hb {
		t.Fatal("lookCrop 必须参与 hash")
	}
	c := validPlan()
	c.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}}}
	c.Pages[0].LookCrop = true
	c.Pages[0].LookCropPadPx = 12
	hc, _ := c.Hash(HashOptions{})
	if hb == hc {
		t.Fatal("lookCropPadPx 必须参与 hash")
	}
}

func TestHashIgnoresZeroVeilAlpha(t *testing.T) {
	a := validPlan()
	a.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1"}}
	b := validPlan()
	b.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1", VeilAlpha: 0}}
	ha, _ := a.Hash(HashOptions{})
	hb, _ := b.Hash(HashOptions{})
	if ha != hb {
		t.Fatal("veilAlpha=0 与缺省同义，不得改变 hash")
	}
}

func TestHashChangesWithVeilAlpha(t *testing.T) {
	a := validPlan()
	a.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1"}}
	b := validPlan()
	b.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1", VeilAlpha: 0.4}}
	ha, _ := a.Hash(HashOptions{})
	hb, _ := b.Hash(HashOptions{})
	if ha == hb {
		t.Fatal("非零 veilAlpha 必须参与 hash")
	}
}

func TestHashChangesWithAnnotations(t *testing.T) {
	a := validPlan()
	ha, _ := a.Hash(HashOptions{})
	b := validPlan()
	b.Pages[0].Annotations = []Annotation{{
		Kind: AnnotationCaption, StartMs: 0, Text: "要点",
	}}
	hb, _ := b.Hash(HashOptions{})
	if ha == hb {
		t.Fatal("非空 annotations 必须参与 hash")
	}
}

func TestHashChangesWithAnnotationEndMs(t *testing.T) {
	a := validPlan()
	a.Pages[0].Annotations = []Annotation{{
		Kind: AnnotationCallout, StartMs: 0, Rect: &Box{X: 1, Y: 1, W: 10, H: 10},
	}}
	ha, _ := a.Hash(HashOptions{})
	b := validPlan()
	b.Pages[0].Annotations = []Annotation{{
		Kind: AnnotationCallout, StartMs: 0, EndMs: 16000, Rect: &Box{X: 1, Y: 1, W: 10, H: 10},
	}}
	hb, _ := b.Hash(HashOptions{})
	if ha == hb {
		t.Fatal("callout EndMs 必须参与 hash，否则圈选放手不会重渲")
	}
}

func TestCanonicalJSONIsStable(t *testing.T) {
	data, err := validPlan().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "warnings") {
		t.Fatalf("规范化 JSON 不应包含 warnings: %s", data)
	}
	if strings.Contains(string(data), "tenants/") {
		t.Fatal("规范化 JSON 应把 baseImage 归一为 basename")
	}
}
