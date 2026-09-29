package app

import (
	"context"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
)

// fakeVision 统计视觉提取调用次数，用于验证 insight 快照复用不重复调模型。
type fakeVision struct {
	calls int
	value llm.VisualAnchor
}

func (f *fakeVision) ExtractVisual(context.Context, llm.VisualExtractRequest) ([]llm.VisualAnchor, error) {
	f.calls++
	return []llm.VisualAnchor{f.value}, nil
}

// TestFactsFromAnchorsClassifiesKinds 锁定事实分类：同一份 anchors 投影成
// 带 ElementKind/Source 的原子事实，供 prompt 与后续按类型模板使用。
func TestFactsFromAnchorsClassifiesKinds(t *testing.T) {
	anchors := []narration.SourceAnchor{
		{SlideID: "slide-1", ShapeID: "shape-1", Kind: "shape_table", Raw: "A\t1\nB\t2", Confidence: 1.0},
		{SlideID: "slide-1", Kind: "visual_text", Raw: "图中标题", Confidence: 0.8},
		{SlideID: "slide-1", Kind: "notes", Raw: "演讲者备注", Confidence: 1.0},
	}
	facts := factsFromAnchors("slide-1", anchors)
	if len(facts) != 3 {
		t.Fatalf("facts len = %d, want 3", len(facts))
	}
	if facts[0].ElementKind != "table" || facts[0].Source != "structural" ||
		!strings.Contains(facts[0].Value, "表格内容") {
		t.Errorf("table fact = %+v", facts[0])
	}
	if facts[1].ElementKind != "image" || facts[1].Source != "visual" ||
		!strings.Contains(facts[1].Value, "图片/页面视觉观察") {
		t.Errorf("visual fact = %+v", facts[1])
	}
	if facts[2].ElementKind != "notes" || facts[2].Source != "structural" {
		t.Errorf("notes fact = %+v", facts[2])
	}
	seen := map[string]bool{}
	for _, f := range facts {
		if seen[f.ID] {
			t.Errorf("fact ID 重复：%q", f.ID)
		}
		seen[f.ID] = true
	}
}

// TestAugmentDraftSourceWithAndWithoutFacts 锁定 prompt 与校验基准的拆分：
// 有事实时 prompt 带 ID 编号、校验基准并入事实值；无事实时两者都保持原素材不变（原文/兼容路径）。
func TestAugmentDraftSourceWithAndWithoutFacts(t *testing.T) {
	// 无事实：双返回必须都等于原素材（缓存键、校验基准、model 输入全一致）。
	plainPrompt, plainValidate := augmentDraftSource("容量 80GB", nil)
	if plainPrompt != "容量 80GB" || plainValidate != "容量 80GB" {
		t.Fatalf("nil facts must passthrough: prompt=%q validate=%q", plainPrompt, plainValidate)
	}
	facts := []draftFact{
		{ID: "fact-a", Value: "表格列：型号A 128GB"},
		{ID: "fact-b", Value: "图中趋势上升"},
	}
	prompt, validate := augmentDraftSource("容量 80GB", facts)
	if !strings.Contains(prompt, "[fact-a]") || !strings.Contains(prompt, "结构化事实") {
		t.Errorf("prompt 缺少事实段落：%q", prompt)
	}
	if !strings.Contains(prompt, "容量 80GB") {
		t.Errorf("prompt 丢失原素材：%q", prompt)
	}
	for _, want := range []string{"容量 80GB", "表格列：型号A 128GB", "图中趋势上升"} {
		if !strings.Contains(validate, want) {
			t.Errorf("校验基准缺少 %q：%q", want, validate)
		}
	}
}

// TestSlideInsightSnapshotReusesCachedVisual 锁定快照复用：相同 (页, 来源) 第二次
// 取 insight 必须命中缓存，不再调用视觉模型（非确定性提取只做一次）。
func TestSlideInsightSnapshotReusesCachedVisual(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	oKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1",
		Revision: "src-01", AssetType: "render", AssetID: "page-0001", Ext: "png",
	}
	if err := objects.Put(context.Background(), oKey, strings.NewReader("png"), objectstore.ObjectMeta{ContentType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	vision := &fakeVision{value: llm.VisualAnchor{Kind: "text", Raw: "图中可见 42%", Confidence: 0.8}}
	renderKeys := map[string]string{"slide-1": oKey.String()}
	pg := parsedPage{SlideID: "slide-1", Shapes: []parsedShape{{ID: "shape-1", Kind: "text", Text: "可见文字"}}}
	h := NewScriptDraftHandler(nil, objects).WithVisualExtractor(vision)

	anchors1, facts1 := h.slideInsight(context.Background(), vision, "tenant-1", "project-1", 1, "zh-CN", pg, "", renderKeys)
	if vision.calls != 1 {
		t.Fatalf("第一次 visual calls = %d, want 1", vision.calls)
	}
	anchors2, facts2 := h.slideInsight(context.Background(), vision, "tenant-1", "project-1", 1, "zh-CN", pg, "", renderKeys)
	if vision.calls != 1 {
		t.Fatalf("命中快照后 visual calls = %d, want 1", vision.calls)
	}
	if len(anchors1) != len(anchors2) || len(facts1) != len(facts2) {
		t.Fatalf("快照复用内容不一致：anchors %d→%d, facts %d→%d",
			len(anchors1), len(anchors2), len(facts1), len(facts2))
	}
	hasVisualFact := false
	for _, f := range facts2 {
		if f.Source == "visual" {
			hasVisualFact = true
		}
	}
	if !hasVisualFact {
		t.Errorf("fastsnapshot facts 缺少视觉事实：%+v", facts2)
	}
}

// TestDraftTextWithFactsKeepsGuardSemantics 锁定「事实注入不改守门语义」：
// 事实里的数字仍参与校验（防幻觉），原素材缺数字时判定维持 result 语义。
func TestDraftTextWithFactsKeepsGuardSemantics(t *testing.T) {
	const source = "容量 80GB"
	facts := []draftFact{{ID: "fact-a", Value: "表格列：型号B 128GB"}}
	f := &fakeRewriter{replies: []string{"这款设备的存储能力足够充裕。"}}
	h := newDraftHandlerForTest(f)
	_, _, degraded, err := h.draftText(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, draftInput{Kind: inputPage, Text: source}, facts, ScriptDraftSnapshot{}, f)
	if err != nil {
		t.Fatalf("draftText: %v", err)
	}
	if !degraded {
		t.Fatalf("事实/原素材数字都被丢弃应 degraded（模型必须保留 80GB 与 128GB）")
	}
}

// TestTableStatFacts 锁定表格确定性统计：数值列产出「最小/最大」范围事实，
// 无表头列以列号兜底；非数值列不产出统计。
func TestTableStatFacts(t *testing.T) {
	sh := parsedShape{
		ID:   "shape-t1",
		Kind: "table",
		TableCells: [][]string{
			{"型号", "容量(GB)", "价格"},
			{"A100", "80", "10000"},
			{"H800", "128", "15000"},
			{"H900", "256", "12000"},
		},
		TableRows: 4,
		TableCols: 3,
	}
	facts := factsFromStructuredShapes(parsedPage{SlideID: "slide-1", Shapes: []parsedShape{sh}}, "")
	if len(facts) != 2 {
		t.Fatalf("facts = %+v，want 2（容量/价格两列含数字；型号列 A100 非纯数字被跳过）", facts)
	}
	byKind := map[string][]draftFact{}
	for _, f := range facts {
		if f.ElementKind != "table" || f.Source != "structural" || f.Confidence != 1.0 {
			t.Errorf("fact 元数据异常：%+v", f)
		}
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}
	// 第二列（容量）：最小 80 最大 256。
	if len(byKind["table_stat"]) != 2 {
		t.Fatalf("table_stat facts = %+v", byKind["table_stat"])
	}
	var capFact *draftFact
	for i := range byKind["table_stat"] {
		if strings.Contains(byKind["table_stat"][i].Value, "容量(GB)") {
			capFact = &byKind["table_stat"][i]
		}
	}
	if capFact == nil {
		t.Fatalf("缺容量列事实：%+v", byKind["table_stat"])
	}
	if !strings.Contains(capFact.Value, "最小 80") || !strings.Contains(capFact.Value, "最大 256") {
		t.Fatalf("容量列范围 = %q", capFact.Value)
	}
	// 型号列（首行是表头）也应为字符串列被跳过 → 实际只有 3 列里 3 列含数字？
	// 型号/价格两列含数字（A100 不是数字、10000/15000/12000 是数字），见下方断言。
}

// TestChartStatFacts 锁定图表确定性统计：每条系列产出「最小/最大」范围事实，
// 含对应类别名；NaN/Inf 不进入统计。
func TestChartStatFacts(t *testing.T) {
	sh := parsedShape{
		ID:              "shape-c1",
		Kind:            "chart",
		ChartType:       "bar",
		ChartCategories: []string{"Q1", "Q2", "Q3"},
		ChartSeries: []parsedChartSerie{
			{Name: "华东", Values: []float64{12.5, 20, 1676.0}},
			{Name: "华北", Values: []float64{30, 25, 8}},
		},
	}
	facts := factsFromStructuredShapes(parsedPage{SlideID: "slide-1", Shapes: []parsedShape{sh}}, "")
	if len(facts) != 2 {
		t.Fatalf("facts = %+v，want 2（每条系列一个范围事实）", facts)
	}
	var east, north *draftFact
	for i := range facts {
		if facts[i].Kind != "chart_stat" || facts[i].ElementKind != "chart" || facts[i].Confidence != 1.0 {
			t.Errorf("fact 元数据异常：%+v", facts[i])
		}
		if strings.Contains(facts[i].Value, "华东") {
			east = &facts[i]
		}
		if strings.Contains(facts[i].Value, "华北") {
			north = &facts[i]
		}
	}
	if east == nil || !strings.Contains(east.Value, "最小 12.5") || !strings.Contains(east.Value, "最大 1676") ||
		!strings.Contains(east.Value, "最小对应「Q1」") || !strings.Contains(east.Value, "最大对应「Q3」") {
		t.Fatalf("华东系列事实 = %+v", east)
	}
	if north == nil || !strings.Contains(north.Value, "最小 8") || !strings.Contains(north.Value, "最大 30") {
		t.Fatalf("华北系列事实 = %+v", north)
	}
}

// TestFmtFactNumber 锁定数值格式化：定点无科学计数、去尾零、-0 归一。
func TestFmtFactNumber(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1676.0, "1676"},
		{12.5, "12.5"},
		{0, "0"},
		{-0.0, "0"},
		{1e-7, "0.0000001"},
	}
	for _, c := range cases {
		if got := fmtFactNumber(c.in); got != c.want {
			t.Errorf("fmtFactNumber(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExtractFactCitations 锁定引用标记的提取与剥离：只摘出 [A-Za-z0-9-]
// 范围内的 fact ID、保序去重，正文标记被剥干净。
func TestExtractFactCitations(t *testing.T) {
	text := "模型容量是 128GB〔fact-a〕，支持 80GB〔fact-b〕 与 12.5 倍速〔fact-a〕。"
	clean, cited := extractFactCitations(text)
	if clean != "模型容量是 128GB，支持 80GB 与 12.5 倍速。" {
		t.Fatalf("clean = %q", clean)
	}
	if len(cited) != 2 || cited[0] != "fact-a" || cited[1] != "fact-b" {
		t.Fatalf("cited = %v, want [fact-a fact-b]（保序去重）", cited)
	}
	// 无标记原样返回。
	plain, none := extractFactCitations("纯正文没有引用")
	if plain != "纯正文没有引用" || len(none) != 0 {
		t.Fatalf("plain = %q cited=%v", plain, none)
	}
	// 非法/畸形标记不匹配白名单 → 原样保留、不产生引用（宁可漏判也不误剥）。
	weird, wCited := extractFactCitations("异常〔fact-©〕〔fact-〕〔fact-a b〕")
	if len(wCited) != 0 {
		t.Fatalf("weird cited = %v, want none", wCited)
	}
	if weird != "异常〔fact-©〕〔fact-〕〔fact-a b〕" {
		t.Fatalf("weird clean = %q，畸形标记应原样保留", weird)
	}
}

// TestUnknownFactCitations 锁定幻觉引用识别：available 外的 ID 被判为未知。
func TestUnknownFactCitations(t *testing.T) {
	facts := []draftFact{{ID: "fact-a"}, {ID: "fact-b"}}
	if got := unknownFactCitations([]string{"fact-a"}, facts); len(got) != 0 {
		t.Fatalf("known citation flagged: %v", got)
	}
	got := unknownFactCitations([]string{"fact-a", "fact-z"}, facts)
	if len(got) != 1 || got[0] != "fact-z" {
		t.Fatalf("unknown = %v, want [fact-z]", got)
	}
	if got := unknownFactCitations(nil, facts); len(got) != 0 {
		t.Fatalf("empty citations = %v", got)
	}
	// 无可用 facts 时出现的任何引用都无依据 → 判未知（触发二轮删标，防幻觉）。
	if got := unknownFactCitations([]string{"fact-a"}, nil); len(got) != 1 || got[0] != "fact-a" {
		t.Fatalf("no available facts: %v", got)
	}
}

// TestDraftTextRejectsUnknownFactCitation 锁定幻觉引用进第二轮修补：
// 首轮标注了不存在的 fact-z → 二轮指令必须点名该编号；二轮修正后落库纯文本。
func TestDraftTextRejectsUnknownFactCitation(t *testing.T) {
	const source = "容量 80GB"
	facts := []draftFact{{ID: "fact-a", Value: "容量 80GB"}}
	f := &fakeRewriter{replies: []string{
		"容量为 80GB，依据事实〔fact-z〕。",
		"容量为 80GB。", // 二轮删除幻觉引用
	}}
	h := newDraftHandlerForTest(f)
	text, _, degraded, err := h.draftText(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, draftInput{Kind: inputPage, Text: source}, facts, ScriptDraftSnapshot{}, f)
	if err != nil {
		t.Fatalf("draftText: %v", err)
	}
	if degraded {
		t.Fatalf("二轮修正后不应 degraded")
	}
	if text != "容量为 80GB。" {
		t.Fatalf("text = %q，应为二轮纯文本（剥离标记）", text)
	}
	if len(f.calls) != 2 {
		t.Fatalf("应调用两次（首轮 + 修补），实际 %d", len(f.calls))
	}
	if !strings.Contains(f.calls[1].Instructions, "fact-z") {
		t.Fatalf("二轮指令未点名幻觉引用：%q", f.calls[1].Instructions)
	}
}

// TestDraftTextCitationOKNoGuard 锁定合法引用不触发二轮且标记被剥离。
func TestDraftTextCitationOKNoGuard(t *testing.T) {
	const source = "容量 80GB"
	facts := []draftFact{{ID: "fact-a", Value: "容量 80GB"}}
	f := &fakeRewriter{replies: []string{"容量为 80GB〔fact-a〕，满足设计要求。"}}
	h := newDraftHandlerForTest(f)
	text, _, degraded, err := h.draftText(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, draftInput{Kind: inputPage, Text: source}, facts, ScriptDraftSnapshot{}, f)
	if err != nil {
		t.Fatalf("draftText: %v", err)
	}
	if degraded || len(f.calls) != 1 {
		t.Fatalf("合法引用应一次通过（calls=%d, degraded=%v）", len(f.calls), degraded)
	}
	if text != "容量为 80GB，满足设计要求。" {
		t.Fatalf("text = %q，应剥离标记", text)
	}
}
