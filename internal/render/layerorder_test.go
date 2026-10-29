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

func TestPartialOverlaps(t *testing.T) {
	lay := func(id string, x, y, w, h int) Layer {
		return Layer{ElementID: id, Kind: "bullet", AssetMode: "bitmap-region",
			Box: Box{X: x, Y: y, W: w, H: h}}
	}

	cases := []struct {
		name   string
		layers []Layer
		want   int
	}{
		{"完全重叠但互不包含的两块（同尺寸）", []Layer{lay("a", 100, 100, 400, 200), lay("b", 100, 100, 400, 200)}, 0},
		{"包含关系交给抹除算法", []Layer{lay("card", 0, 0, 600, 600), lay("text", 100, 100, 300, 100)}, 0},
		{"完全不相交", []Layer{lay("a", 0, 0, 100, 100), lay("b", 500, 500, 100, 100)}, 0},
		{"部分重叠（非包含）", []Layer{lay("a", 0, 0, 400, 300), lay("b", 300, 150, 400, 300)}, 1},
		{"边距级交叠不算", []Layer{lay("a", 0, 0, 1000, 400), lay("b", 999, 120, 800, 200)}, 0},
		{"0×0 不参与", []Layer{lay("zero", 10, 10, 0, 0), lay("a", 0, 0, 400, 300)}, 0},
		{"三块两两部分重叠", []Layer{lay("a", 0, 0, 400, 300), lay("b", 300, 0, 400, 300), lay("c", 600, 0, 400, 300)}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := partialOverlaps(tc.layers)
			if len(got) != tc.want {
				t.Fatalf("overlaps = %v, want %d 对", got, tc.want)
			}
		})
	}
}
