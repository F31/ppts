package render

import "testing"

func TestOrderLayersParentsFirst(t *testing.T) {
	page := Layer{ElementID: "page", Box: Box{X: 0, Y: 0, W: 1920, H: 1080}}
	card := Layer{ElementID: "card", Box: Box{X: 96, Y: 320, W: 1728, H: 560}}
	bullet := Layer{ElementID: "bullet", Box: Box{X: 180, Y: 380, W: 900, H: 88}}

	t.Run("子层在前时父层被提前", func(t *testing.T) {
		got := orderLayersParentsFirst([]Layer{bullet, card})
		if got[0].ElementID != "card" || got[1].ElementID != "bullet" {
			t.Fatalf("got %v, want [card bullet]", ids(got))
		}
	})

	t.Run("三层嵌套逐层展开", func(t *testing.T) {
		got := orderLayersParentsFirst([]Layer{bullet, card, page})
		want := []string{"page", "card", "bullet"}
		for i := range want {
			if got[i].ElementID != want[i] {
				t.Fatalf("got %v, want %v", ids(got), want)
			}
		}
	})

	t.Run("并列层保持原序", func(t *testing.T) {
		a := Layer{ElementID: "a", Box: Box{X: 0, Y: 0, W: 100, H: 100}}
		b := Layer{ElementID: "b", Box: Box{X: 50, Y: 50, W: 100, H: 100}}
		got := orderLayersParentsFirst([]Layer{a, b})
		if got[0].ElementID != "a" || got[1].ElementID != "b" {
			t.Fatalf("部分重叠不是包含关系，不该换序，got %v", ids(got))
		}
	})

	t.Run("同尺寸两层不互换", func(t *testing.T) {
		// 同 rect 时 BuildLayerAssets 互不抹除，换序只会让结果不稳定（甚至死循环）。
		a := Layer{ElementID: "a", Box: Box{X: 0, Y: 0, W: 100, H: 100}}
		b := Layer{ElementID: "b", Box: Box{X: 0, Y: 0, W: 100, H: 100}}
		got := orderLayersParentsFirst([]Layer{a, b})
		if got[0].ElementID != "a" || got[1].ElementID != "b" {
			t.Fatalf("同尺寸不该换序，got %v", ids(got))
		}
	})

	t.Run("无效几何不参与包含判断", func(t *testing.T) {
		zero := Layer{ElementID: "zero", Box: Box{X: 500, Y: 500, W: 0, H: 0}}
		got := orderLayersParentsFirst([]Layer{zero, card})
		if got[0].ElementID != "zero" {
			t.Fatalf("0×0 的层不该被判定为「被包住」，got %v", ids(got))
		}
	})

	t.Run("已正确顺序不被改动", func(t *testing.T) {
		got := orderLayersParentsFirst([]Layer{page, card, bullet})
		want := []string{"page", "card", "bullet"}
		for i := range want {
			if got[i].ElementID != want[i] {
				t.Fatalf("got %v, want %v", ids(got), want)
			}
		}
	})
}

func ids(layers []Layer) []string {
	out := make([]string, len(layers))
	for i, l := range layers {
		out[i] = l.ElementID
	}
	return out
}
