package app

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
)

// SuggestFocus 是只读建议，判据不能手写「某段该指谁」——那是把绑定器的实现又抄一遍。
// 这里拿两侧权威当裁判：建议必须来自 bind.Run 的真实绑定，且必须落在
// FocusTargetsForPage 的候选里（成片里指得到的那份集合）。

func realSuggestPage(t *testing.T, index int) *project.Page {
	t.Helper()
	f, err := os.Open("../animation/animdefault/testdata/sample-customer-deck.pptx")
	if err != nil {
		t.Fatalf("打开样本: %v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := project.NewGoPPTXReader(project.Limits{}).Inspect(context.Background(), f, st.Size())
	if err != nil {
		t.Fatalf("解析样本: %v", err)
	}
	if index >= len(doc.Pages) {
		t.Fatalf("样本只有 %d 页", len(doc.Pages))
	}
	return doc.Pages[index]
}

// q8ScriptSegments 取项目已登记的真实讲稿（scripts/fidelity/q8-script.json）。
// 它含「第一步 / 第一条」这类结构词，是 structured 档的语料；从页面文字临造的
// 讲稿没有这些词，会把整页判成低置信、建议恒为空——那样三条闸门的断言全在空转。
func q8ScriptSegments(t *testing.T, pageIndex int) []*narration.Segment {
	t.Helper()
	raw, err := os.ReadFile("../../scripts/fidelity/q8-script.json")
	if err != nil {
		t.Fatalf("读 q8 讲稿: %v", err)
	}
	var doc struct {
		Pages []struct {
			Segments []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"segments"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 q8 讲稿: %v", err)
	}
	if pageIndex >= len(doc.Pages) {
		t.Fatalf("q8 只有 %d 页", len(doc.Pages))
	}
	out := make([]*narration.Segment, 0, len(doc.Pages[pageIndex].Segments))
	for _, s := range doc.Pages[pageIndex].Segments {
		out = append(out, &narration.Segment{
			SegmentID: s.ID, DisplayText: s.Text, SpokenText: s.Text,
		})
	}
	return out
}

func suggestionText(segs []*narration.Segment) []bind.Segment {
	out := make([]bind.Segment, 0, len(segs))
	for _, s := range segs {
		text := s.DisplayText
		if strings.TrimSpace(text) == "" {
			text = s.SpokenText
		}
		out = append(out, bind.Segment{ID: s.SegmentID, Text: text})
	}
	return out
}

func TestSuggestFocusUsesBinderAndCandidatesAsJudge(t *testing.T) {
	// pptx index 9 = q8 语料的第 10 页（q8SlideIndexes 最后一项），编号卡片 + 五段序数讲稿。
	page := realSuggestPage(t, 9)
	segs := q8ScriptSegments(t, 4)
	if len(segs) < 3 {
		t.Fatalf("讲稿段数太少，夹具失效：%d 段", len(segs))
	}

	res := SuggestFocus(page, segs)
	t.Logf("band=%s note=%q，有候选的段 %d 个", res.Band, res.Note, countSuggested(res))

	// 沉默守卫：一条建议都没有时，下面关于「候选清单 / 来源标注 / 单值」的断言
	// 全部空转，测试会绿得毫无意义。先证明这条语料真的走到了给建议的分支。
	if res.Band != string(bind.BandStructured) {
		t.Fatalf("夹具应是结构化页，实得 %s（分档一变，整组用例失去检验力）", res.Band)
	}
	if countSuggested(res) == 0 {
		diag := bind.Run(PageBindElements(page, 0, 0), suggestionText(segs), bind.Options{})
		bySeg := map[string][]string{}
		for _, b := range diag.Bindings {
			bySeg[b.SegmentID] = append(bySeg[b.SegmentID], b.ElementID)
		}
		sel := map[string]bool{}
		for _, c := range FocusTargetsForPage(page).Targets {
			if c.BarID == "" {
				sel[c.ShapeID] = true
			}
		}
		for _, sug := range res.Suggestions {
			t.Logf("  %s -> %s | bindings=%v", sug.SegmentID, sug.Reason, bySeg[sug.SegmentID])
		}
		t.Logf("  可选整形状=%d, 绑定总数=%d", len(sel), len(diag.Bindings))
		t.Fatal("结构化页一段候选都没产出，判据在主场沉默——先修夹具再看闸门")
	}

	// 裁判一：每条建议都来自绑定器这次真的产出的绑定。
	bound := map[string]bool{}
	for _, b := range bind.Run(PageBindElements(page, 0, 0), suggestionText(segs), bind.Options{}).Bindings {
		bound[b.ElementID] = true
	}
	// 裁判二：每条建议都必须在候选清单里，且是整形状（绑定器不产 BarID）。
	selectable := map[string]bool{}
	for _, c := range FocusTargetsForPage(page).Targets {
		if c.BarID == "" {
			selectable[c.ShapeID] = true
		}
	}

	if len(res.Suggestions) != len(segs) {
		t.Fatalf("每段都应有一条记录（含「无建议」的原因），实得 %d / %d", len(res.Suggestions), len(segs))
	}
	for i, sug := range res.Suggestions {
		if sug.SegmentID != segs[i].SegmentID {
			t.Fatalf("第 %d 条顺序错：%s != %s", i, sug.SegmentID, segs[i].SegmentID)
		}
		if sug.Reason == "" {
			t.Fatalf("第 %d 段没有说明原因（给没给建议都要有说法）", i)
		}
		lastOrder := -1
		for _, c := range sug.Candidates {
			if c.Focus.BarID != "" {
				t.Fatalf("第 %d 段建议了一根柱子，但绑定器只产 ShapeID，不能硬凑 BarID", i)
			}
			if !bound[c.Focus.ShapeID] {
				t.Fatalf("第 %d 段建议的 %s 不在绑定器这次的绑定里", i, c.Focus.ShapeID)
			}
			if !selectable[c.Focus.ShapeID] {
				t.Fatalf("第 %d 段建议的 %s 不在可选焦点清单里（成片指不到）", i, c.Focus.ShapeID)
			}
			if c.Source == "" {
				t.Fatalf("第 %d 段有建议却没标规则来源", i)
			}
			// 输出顺序必须跟随候选清单（界面阅读顺序），不能随绑定器内部顺序漂。
			order := indexOfShape(candidateOrderForTest(page), c.Focus.ShapeID)
			if order <= lastOrder {
				t.Fatalf("第 %d 段候选顺序不是候选清单顺序：%s", i, c.Focus.ShapeID)
			}
			lastOrder = order
		}
	}
}

func candidateOrderForTest(page *project.Page) []string {
	var out []string
	for _, t := range FocusTargetsForPage(page).Targets {
		if t.BarID == "" {
			out = append(out, t.ShapeID)
		}
	}
	return out
}

func indexOfShape(list []string, id string) int {
	for i, s := range list {
		if s == id {
			return i
		}
	}
	return -1
}

func TestSuggestFocusIsDeterministicAndSideEffectFree(t *testing.T) {
	page := realSuggestPage(t, 9)
	segs := q8ScriptSegments(t, 4)
	a := SuggestFocus(page, segs)
	b := SuggestFocus(page, segs)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("同一输入两次结果不同：建议必须是纯函数，不能偷偷落库或依赖外部状态")
	}
	// 输入不被改写：只读端点不能把 "建议" 写回 Segment.Focus。
	for _, s := range segs {
		if s.Focus != nil {
			t.Fatalf("SuggestFocus 改写了输入段落 %s 的 Focus", s.SegmentID)
		}
	}
}

func TestSuggestFocusLowConfidencePageDoesNotSuggest(t *testing.T) {
	// 纯文字、无编号/卡片/方位信号的一页：classifyBand 应判低置信。
	const emu = 914400
	page := &project.Page{Shapes: []*project.Shape{
		{ID: "shape-1", Kind: "text", Text: "关于本次汇报", Bounds: &project.Box{X: emu, Y: emu, Width: 8 * emu, Height: emu}},
		{ID: "shape-2", Kind: "text", Text: "这里是一段很长的说明文字，讲稿里也会原样念出来，用来让字面匹配成立。",
			Bounds: &project.Box{X: emu, Y: 3 * emu, Width: 8 * emu, Height: 2 * emu}},
	}}
	segs := []*narration.Segment{
		{SegmentID: "s1", DisplayText: "关于本次汇报"},
		{SegmentID: "s2", DisplayText: "这里是一段很长的说明文字，讲稿里也会原样念出来，用来让字面匹配成立。"},
	}
	res := SuggestFocus(page, segs)
	t.Logf("band=%s reason 见 note=%q", res.Band, res.Note)
	if res.Band != string(bind.BandLowConfidence) {
		t.Fatalf("这页应是低置信（否则夹具失效），实得 %s", res.Band)
	}
	if got := countSuggested(res); got != 0 {
		t.Fatalf("低置信页不应自动建议，实得 %d 段有候选", got)
	}
	if res.Note == "" {
		t.Fatal("低置信页必须给出整体说明，不能静默返回空建议")
	}
}

func TestSuggestFocusDoesNotGuessABarForWholeChart(t *testing.T) {
	const emu = 914400
	page := &project.Page{Shapes: []*project.Shape{
		{ID: "chart-1", Kind: "chart", ChartType: "bar", ChartOrientation: "vertical",
			ChartBars: []project.ChartBar{
				{ID: "chart-1-b0", Category: "华东", Value: 174, Box: [4]int{100, 300, 80, 400}, LabelDir: "up", LabelPad: 42},
				{ID: "chart-1-b1", Category: "华北", Value: 131, Box: [4]int{220, 380, 80, 320}, LabelDir: "up", LabelPad: 42},
			},
			Bounds: &project.Box{X: emu, Y: emu, Width: 6 * emu, Height: 5 * emu}},
	}}
	segs := []*narration.Segment{{SegmentID: "s1", DisplayText: "华东这一季是 174"}}
	res := SuggestFocus(page, segs)
	for _, sug := range res.Suggestions {
		for _, c := range sug.Candidates {
			if c.Focus.BarID != "" {
				t.Fatalf("绑定器只认整块图表，不应猜出某一根柱子：%+v", c.Focus)
			}
		}
	}
}

func TestSuggestFocusEmptyInputsExplainThemselves(t *testing.T) {
	if got := SuggestFocus(nil, []*narration.Segment{{SegmentID: "s1"}}); got.Note == "" {
		t.Fatal("没有解析产物时要说明原因")
	}
	if got := SuggestFocus(&project.Page{}, nil); got.Note == "" {
		t.Fatal("没有讲稿时要说明原因")
	}
}

func countSuggested(res PageFocusSuggestions) int {
	n := 0
	for _, s := range res.Suggestions {
		if len(s.Candidates) > 0 {
			n++
		}
	}
	return n
}
