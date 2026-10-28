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
