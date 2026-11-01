package bind

import "testing"

func el(id string, x, y, w, h float64) Element {
	return Element{ID: id, Kind: "shape", Box: Box{X: x, Y: y, W: w, H: h}, Text: id}
}

func ids(els []Element) []string {
	out := make([]string, len(els))
	for i, e := range els {
		out[i] = e.ID
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 真实讲义里最常见的错序：作者先放正文块、后放标题，形状树顺序和阅读顺序整页反过来。
// 33 页 Token 方案的 p6 就是这种（y 从 676 一路递减到 30）。
func TestReadingOrderReversedInput(t *testing.T) {
	in := []Element{
		el("c", 100, 600, 400, 100),
		el("b", 100, 300, 400, 100),
		el("a", 100, 40, 800, 80),
	}
	got := ids(readingOrder(in))
	want := []string{"a", "b", "c"}
	if !equalIDs(got, want) {
		t.Fatalf("按阅读序应为 %v，得到 %v", want, got)
	}
}

// 同一行里左右颠倒：卡片/表格的一行常这样，形状树顺序是添加顺序，不是阅读顺序。
func TestReadingOrderRowKeepsLeftToRight(t *testing.T) {
	in := []Element{
		el("r1c3", 700, 300, 200, 100),
		el("r1c1", 100, 300, 200, 100),
		el("r1c2", 400, 302, 200, 100),
	}
	got := ids(readingOrder(in))
	want := []string{"r1c1", "r1c2", "r1c3"}
	if !equalIDs(got, want) {
		t.Fatalf("行内应从左到右 %v，得到 %v", want, got)
	}
}

// 两行交界：第二行顶边比第一行低得多，不能被吸进同一行。容差是中位高 0.6 倍，
// 这里行高 100、行距 150，必须断开。
func TestReadingOrderSplitsRows(t *testing.T) {
	in := []Element{
		el("r2c1", 100, 250, 200, 100),
		el("r1c1", 100, 100, 200, 100),
		el("r2c2", 400, 252, 200, 100),
		el("r1c2", 400, 100, 200, 100),
	}
	got := ids(readingOrder(in))
	want := []string{"r1c1", "r1c2", "r2c1", "r2c2"}
	if !equalIDs(got, want) {
		t.Fatalf("应为 %v，得到 %v", want, got)
	}
}

// 调用方的切片不能被改：PageBindElements 的结果还会被别的消费方按原顺序读。
func TestReadingOrderDoesNotMutateInput(t *testing.T) {
	in := []Element{
		el("c", 100, 600, 400, 100),
		el("a", 100, 40, 800, 80),
	}
	before := ids(in)
	_ = readingOrder(in)
	if after := ids(in); !equalIDs(before, after) {
		t.Fatalf("输入被就地改掉了：%v → %v", before, after)
	}
}

// 跨栏标签：右栏标签比正文行高一点点（y=546 vs 570），中间还夹着一块 302px 高的
// 无字底板。行高容差若把底板算进去（中位 169→101），两行会被并成一行，行内按 x
// 排序把右栏标签（x=1588）拉到正文（x=1183）之后——顺序反了，单调对齐只能把它
// 退回后一段。容差只看文本元素（中位 37→22）时两行正确断开。
// 对应 33 页 Token 方案 p16 的「Token增值」。
func TestReadingOrderIgnoresNonTextHeights(t *testing.T) {
	bg := Element{ID: "bg", Kind: "shape", Box: Box{X: 84, Y: 213, W: 1459, H: 302}} // 无字底板
	in := []Element{
		el("mid", 1183, 570, 319, 37),
		bg,
		el("right", 1588, 546, 276, 37),
		el("title", 59, 30, 1625, 88),
	}
	got := ids(readingOrder(in))
	want := []string{"title", "bg", "right", "mid"}
	if !equalIDs(got, want) {
		t.Fatalf("跨栏行应断开、右栏标签在前：want %v, got %v", want, got)
	}
}

// 空形状不能当行带的基准线。
//
// 一行字从上往下扫，第一个落进这一行的元素就是基准。底板常比它装的文字高、顶边也靠上
// 几像素（这里是 y=326 vs 331），基准线被抬高后，下一行字（y=382，只差 51）就掉出了
// 这一行，顺序反过来——单调对齐于是把本该属于后一段的元素错配。对应 33 页 Token 方案
// p7 的「核心逻辑闭环」：它整段让给了后面的段落，自己反倒变成「同块带出」。
func TestReadingOrderEmptyShapeIsNotAnchor(t *testing.T) {
	// 底板 302px 高，但文字元素有 3 个 37px：中位高度落在 37，容差 22.2，
	// 加不加底板都一样——所以这条用例只考「基准线是谁」，不掺容差变大。
	bg := Element{ID: "bg", Kind: "shape", Box: Box{X: 1001, Y: 326, W: 400, H: 302}} // 无字底板
	in := []Element{
		el("b", 1041, 382, 724, 37),
		el("a", 1112, 331, 360, 37),
		bg,
		el("title", 59, 30, 1625, 37),
	}
	got := ids(readingOrder(in))
	// b 的 y=382 与 a 的 331 差 51，超过容差，本来就该是两行；
	// 底板 y=326 若当基准，a 会被并进底板那一行、b 被切到下一行，顺序就成了 bg,a,b。
	want := []string{"title", "a", "bg", "b"}
	if !equalIDs(got, want) {
		t.Fatalf("底板抬高了基准线，把下一行切走了：want %v, got %v", want, got)
	}
}

// 全页没有任何文字元素时，容差退回全体高度，不能退化成 0 而逐元素成行。
func TestReadingOrderNoTextFallback(t *testing.T) {
	in := []Element{
		{ID: "a", Box: Box{X: 0, Y: 0, W: 100, H: 40}},
		{ID: "b", Box: Box{X: 200, Y: 20, W: 100, H: 40}},
	}
	got := ids(readingOrder(in))
	want := []string{"a", "b"}
	if !equalIDs(got, want) {
		t.Fatalf("同高元素应同行、从左到右：want %v, got %v", want, got)
	}
}

func TestReadingOrderSmallInputs(t *testing.T) {
	if got := readingOrder(nil); len(got) != 0 {
		t.Fatalf("nil 应原样返回，得到 %v", got)
	}
	single := []Element{el("a", 0, 0, 10, 10)}
	if got := readingOrder(single); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("单元素应原样返回，得到 %v", ids(got))
	}
}
