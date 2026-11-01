package bind

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// 金标复现：`sample-customer-deck.pptx` 的 1/2/4/8/10 页 + `q8-script.json`。
//
// 这两个数（内容元素召回 0.972 / 精确 0.962）是 Q7 实验的结论，也是本变更的验收线。
// 移植「看起来能跑」不算数：任何一处正则、权重、转移顺序的偏差都会把它们打下来。
// 所以下面既断言比率，也断言**五类计数逐项相等**——比率相同而分类漂移（错绑变成漏绑）
// 说明规则虽然总数对上了，但错的是另一批，那同样是移植走样。

type fixturePage struct {
	Index    int            `json:"index"`
	Elements []fixtureElem  `json:"elements"`
	Segments []fixtureSeg   `json:"segments"`
	Golden   []fixtureBinds `json:"golden"`
}

type fixtureElem struct {
	ID   string     `json:"id"`
	Kind string     `json:"kind"`
	Box  [4]float64 `json:"box"`
	Text string     `json:"text"`
}

type fixtureSeg struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type fixtureBinds struct {
	ElementID string `json:"elementId"`
	SegmentID string `json:"segmentId"`
	Role      string `json:"role"`
}

type fixtureDoc struct {
	Pages []fixturePage `json:"pages"`
}

func loadFixture(t *testing.T) fixtureDoc {
	t.Helper()
	raw, err := os.ReadFile("testdata/q8-golden.json")
	if err != nil {
		t.Fatalf("读夹具: %v", err)
	}
	var doc fixtureDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析夹具: %v", err)
	}
	if len(doc.Pages) != 5 {
		t.Fatalf("夹具页数 = %d，期望 5", len(doc.Pages))
	}
	return doc
}

type tally struct {
	exact, wrongSeg, missed, extra, bothSkip int
	need, needContent, hitContent            int
}

func (t tally) precision() float64 {
	d := t.exact + t.wrongSeg + t.extra
	if d == 0 {
		return 0
	}
	return float64(t.exact) / float64(d)
}

func (t tally) recallContent() float64 {
	if t.needContent == 0 {
		return 0
	}
	return float64(t.hitContent) / float64(t.needContent)
}

func TestGoldenQ8(t *testing.T) {
	doc := loadFixture(t)
	var total tally
	bySource := map[string]int{}

	for _, page := range doc.Pages {
		elements := make([]Element, len(page.Elements))
		for i, e := range page.Elements {
			elements[i] = Element{
				ID: e.ID, Kind: e.Kind, Text: e.Text,
				Box: Box{X: e.Box[0], Y: e.Box[1], W: e.Box[2], H: e.Box[3]},
			}
		}
		segments := make([]Segment, len(page.Segments))
		for i, s := range page.Segments {
			segments[i] = Segment{ID: s.ID, Text: s.Text}
		}

		res := Run(elements, segments, Options{})
		for src, ids := range res.BySource {
			bySource[src] += len(ids)
		}

		pred := make(map[string]string, len(res.Bindings))
		for _, b := range res.Bindings {
			if _, dup := pred[b.ElementID]; dup {
				t.Fatalf("page %d: 元素 %s 被绑了两次（同元素两条 clip）", page.Index, b.ElementID)
			}
			pred[b.ElementID] = b.SegmentID
		}
		gold := make(map[string]fixtureBinds, len(page.Golden))
		for _, g := range page.Golden {
			gold[g.ElementID] = g
		}

		for _, e := range page.Elements {
			g, has := gold[e.ID]
			p, ok := pred[e.ID]
			switch {
			case has && ok && g.SegmentID == p:
				total.exact++
			case has && ok:
				total.wrongSeg++
				t.Logf("page %d: %s 绑错段 golden=%s pred=%s", page.Index, e.ID, g.SegmentID, p)
			case has:
				total.missed++
				t.Logf("page %d: %s 漏绑 golden=%s", page.Index, e.ID, g.SegmentID)
			case ok:
				total.extra++
				t.Logf("page %d: %s 误绑 pred=%s", page.Index, e.ID, p)
			default:
				total.bothSkip++
			}
			if has {
				total.need++
				if g.Role != "decor" {
					total.needContent++
					if ok && g.SegmentID == p {
						total.hitContent++
					}
				}
			}
		}
	}

	got := fmt.Sprintf("exact=%d wrong_seg=%d missed=%d extra=%d both_skip=%d recall_content=%.3f precision=%.3f",
		total.exact, total.wrongSeg, total.missed, total.extra, total.bothSkip,
		total.recallContent(), total.precision())
	t.Logf("合计 %s", got)
	t.Logf("来源分布 text=%d count=%d ordinal=%d spatial=%d sibling=%d prior=%d structural=%d",
		bySource["text"], bySource["count"], bySource["ordinal"], bySource["spatial"],
		bySource["sibling"], bySource["prior"], bySource["structural"])

	// 五类计数逐项钉死：Python 版 v2 规则在同一份夹具上就是这组数。
	want := tally{exact: 51, wrongSeg: 1, missed: 0, extra: 1, bothSkip: 18,
		need: 52, needContent: 36, hitContent: 35}
	if total != want {
		t.Fatalf("分类计数与金标不一致\n got: %+v\nwant: %+v", total, want)
	}
	if got := total.recallContent(); got < 0.972 {
		t.Fatalf("内容元素召回 %.3f < 0.972（移植走样）", got)
	}
	if got := total.precision(); got < 0.962 {
		t.Fatalf("精确率 %.3f < 0.962（移植走样）", got)
	}
}
