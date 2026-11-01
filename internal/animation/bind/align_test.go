package bind

import "testing"

// 同一页有多个字面相同的元素、讲稿也逐句提到时，必须各绑各的。
//
// 这是 p32 的真实形状：左右两张卡片各有一个「主要产品」，讲稿也是两句。
// `bind` 分支不推进 j，所以「两个都绑第一句」（0.8+0.8）与「各绑各的」（0.8+0.8）
// 总分完全相等，DP 选前者——第二句空着，第二个「主要产品」被算成错绑。
func TestAlignSpreadsDuplicateText(t *testing.T) {
	els := []Element{
		{ID: "lead", Kind: "bullet", Box: Box{X: 100, Y: 100, W: 300, H: 40}, Text: "方案一：轻量运营"},
		{ID: "left", Kind: "bullet", Box: Box{X: 100, Y: 200, W: 300, H: 40}, Text: "主要产品"},
		{ID: "right", Kind: "bullet", Box: Box{X: 1000, Y: 200, W: 300, H: 40}, Text: "主要产品"},
	}
	segs := []Segment{
		{ID: "s0", Text: "方案一：轻量运营"},
		{ID: "s1", Text: "主要产品"},
		{ID: "s2", Text: "主要产品"},
	}
	pairs, _ := align(els, segs, DefaultThreshold)
	want := map[string]string{"lead": "s0", "left": "s1", "right": "s2"}
	if len(pairs) != len(want) {
		t.Fatalf("应绑 %d 条，实际 %d（%v）", len(want), len(pairs), pairs)
	}
	for _, p := range pairs {
		if got := segs[p.seg].ID; got != want[els[p.elem].ID] {
			t.Errorf("%s 应绑 %s，实际绑了 %s", els[p.elem].ID, want[els[p.elem].ID], got)
		}
	}
}

// 讲稿只提了一次时不能摊开：第二个同名元素跟着这一句一起出场才是对的
// （真实讲解里「这一段讲三块：主要产品、主要产品…」是常态，页面不重复说就不该拆）。
func TestAlignKeepsPileupWhenSingleMention(t *testing.T) {
	els := []Element{
		{ID: "lead", Kind: "bullet", Box: Box{X: 100, Y: 100, W: 300, H: 40}, Text: "方案一：轻量运营"},
		{ID: "a", Kind: "bullet", Box: Box{X: 100, Y: 200, W: 300, H: 40}, Text: "主要产品"},
		{ID: "b", Kind: "bullet", Box: Box{X: 1000, Y: 200, W: 300, H: 40}, Text: "主要产品"},
	}
	segs := []Segment{
		{ID: "s0", Text: "方案一：轻量运营"},
		{ID: "s1", Text: "主要产品"},
	}
	pairs, _ := align(els, segs, DefaultThreshold)
	got := map[string]string{}
	for _, p := range pairs {
		got[els[p.elem].ID] = segs[p.seg].ID
	}
	if got["a"] != "s1" || got["b"] != "s1" {
		t.Fatalf("只有一句提到时应都跟着这一句：%v", got)
	}
}

// 摊开不能越过后面已被占用的段：那一段属于元素序更靠后的元素，插到它前面就打乱了讲解顺序。
func TestAlignSpreadStopsAtUsedSegment(t *testing.T) {
	els := []Element{
		{ID: "a", Kind: "bullet", Box: Box{X: 100, Y: 200, W: 300, H: 40}, Text: "主要产品"},
		{ID: "b", Kind: "bullet", Box: Box{X: 1000, Y: 200, W: 300, H: 40}, Text: "主要产品"},
		{ID: "tail", Kind: "bullet", Box: Box{X: 100, Y: 400, W: 300, H: 40}, Text: "适用场景"},
	}
	segs := []Segment{
		{ID: "s0", Text: "主要产品"},
		{ID: "s1", Text: "适用场景"},
	}
	pairs, _ := align(els, segs, DefaultThreshold)
	for _, p := range pairs {
		if els[p.elem].ID == "b" && segs[p.seg].ID == "s1" {
			t.Fatalf("b 不能越过 tail 的段：%v", pairs)
		}
	}
}
