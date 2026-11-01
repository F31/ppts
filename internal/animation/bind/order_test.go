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

func TestReadingOrderSmallInputs(t *testing.T) {
	if got := readingOrder(nil); len(got) != 0 {
		t.Fatalf("nil 应原样返回，得到 %v", got)
	}
	single := []Element{el("a", 0, 0, 10, 10)}
	if got := readingOrder(single); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("单元素应原样返回，得到 %v", ids(got))
	}
}
