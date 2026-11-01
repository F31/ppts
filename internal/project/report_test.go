package project

import "testing"

func reportPage(index int, hidden bool, notes string, flags ...string) *Page {
	pg := &Page{Index: index, SlideID: "slide-" + string(rune('A'+index)), NotesText: notes, FeatureFlags: flags}
	if hidden {
		h := true
		pg.Hidden = &h
	}
	return pg
}

func textShape(text string) *Shape {
	return &Shape{ID: "shape-1", Kind: "textbox", Text: text}
}

func TestBuildImportReport_CountsAndFindings(t *testing.T) {
	doc := &Document{
		SchemaVersion: "1.0",
		ParserName:    "go-pptx",
		ParserVersion: "v2.0.0",
		Pages: []*Page{
			// 第 0 页：有文字有备注，干净。
			func() *Page {
				pg := reportPage(0, false, "本页讲 PCIe 5.0。")
				pg.Shapes = []*Shape{textShape("PCIe 5.0 x16")}
				return pg
			}(),
			// 第 1 页：隐藏页 + 有动画 + 两个媒体 + 一个不透明形状 + 无备注。
			func() *Page {
				pg := reportPage(1, true, "", "hidden", "timing", "audio", "video")
				pg.HasTiming = true
				pg.Shapes = []*Shape{
					textShape("架构总览"),
					{ID: "shape-2", Kind: "audio"},
					{ID: "shape-3", Kind: "video"},
					{ID: "shape-4", Kind: "smartart", Opaque: true},
				}
				return pg
			}(),
			// 第 2 页：既无文字也无备注 —— 讲稿只能靠图像理解。
			func() *Page {
				pg := reportPage(2, false, "  ")
				pg.Shapes = []*Shape{{ID: "shape-5", Kind: "picture"}}
				return pg
			}(),
		},
		Diagnostics: []string{"IR_SHAPES_READ: partial"},
	}
	doc.Features = &Features{PageCount: 3, SlidesWithTiming: 1, PagesWithNotes: 1}

	rep := BuildImportReport(doc)
	if rep == nil {
		t.Fatal("report = nil")
	}
	if rep.PageCount != 3 || rep.HiddenPages != 1 || rep.PagesWithNotes != 1 || rep.PagesWithoutNotes != 2 {
		t.Fatalf("汇总 = %d页/%d隐藏/%d有备注/%d无备注", rep.PageCount, rep.HiddenPages, rep.PagesWithNotes, rep.PagesWithoutNotes)
	}
	if rep.ParserName != "go-pptx" || rep.ParserVersion != "v2.0.0" || rep.SchemaVersion != "1.0" {
		t.Errorf("解析器信息 = %+v", rep)
	}

	byCode := map[string]Finding{}
	for _, f := range rep.Findings {
		byCode[f.Code] = f
	}
	// warn 全部在前，info 全部在后。
	seenInfo := false
	for _, f := range rep.Findings {
		if f.Severity == SeverityInfo {
			seenInfo = true
		}
		if f.Severity == SeverityWarn && seenInfo {
			t.Fatalf("warn 条目 %q 排在 info 之后：顺序会让用户先看到次要信息", f.Code)
		}
	}
	cases := []struct {
		code     string
		severity Severity
		count    int
		pages    []int
	}{
		{FindingOpaqueShapes, SeverityWarn, 1, []int{1}},
		{FindingEmbeddedMedia, SeverityWarn, 2, []int{1}},
		{FindingPagesWithoutText, SeverityWarn, 1, []int{2}},
		{FindingParseDiagnostics, SeverityWarn, 1, nil},
		{FindingAnimations, SeverityInfo, 1, []int{1}},
		{FindingHiddenPages, SeverityInfo, 1, []int{1}},
		{FindingMissingNotes, SeverityInfo, 2, []int{1, 2}},
	}
	if len(rep.Findings) != len(cases) {
		t.Fatalf("条目数 = %d，期望 %d：%+v", len(rep.Findings), len(cases), rep.Findings)
	}
	for _, c := range cases {
		got, ok := byCode[c.code]
		if !ok {
			t.Errorf("缺少条目 %q", c.code)
			continue
		}
		if got.Severity != c.severity {
			t.Errorf("%q 级别 = %q，期望 %q", c.code, got.Severity, c.severity)
		}
		if got.Count != c.count {
			t.Errorf("%q 计数 = %d，期望 %d", c.code, got.Count, c.count)
		}
		if len(c.pages) != len(got.Pages) {
			t.Errorf("%q 页列表 = %v，期望 %v", c.code, got.Pages, c.pages)
			continue
		}
		for i := range c.pages {
			if got.Pages[i] != c.pages[i] {
				t.Errorf("%q 页列表 = %v，期望 %v", c.code, got.Pages, c.pages)
				break
			}
		}
	}

	// 逐页摘要：备注字数按 rune 计（中文一个字算一个，不能按字节）。
	if rep.Pages[0].NotesChars != len([]rune("本页讲 PCIe 5.0。")) {
		t.Errorf("第 0 页备注字数 = %d", rep.Pages[0].NotesChars)
	}
	if rep.Pages[1].ShapeCount != 4 || rep.Pages[1].OpaqueCount != 1 || !rep.Pages[1].Hidden {
		t.Errorf("第 1 页摘要 = %+v", rep.Pages[1])
	}
	if rep.Pages[2].Hidden {
		t.Errorf("第 2 页应为可见页：%+v", rep.Pages[2])
	}
}

func TestBuildImportReport_CleanDeckHasNoFindings(t *testing.T) {
	doc := &Document{
		Pages: []*Page{
			func() *Page {
				pg := reportPage(0, false, "备注")
				pg.Shapes = []*Shape{textShape("标题")}
				return pg
			}(),
		},
		Features: &Features{PageCount: 1, PagesWithNotes: 1},
	}
	rep := BuildImportReport(doc)
	if len(rep.Findings) != 0 {
		t.Fatalf("干净文件不该有条目：%+v", rep.Findings)
	}
	if rep.PagesWithoutNotes != 0 || rep.HiddenPages != 0 {
		t.Errorf("汇总 = %+v", rep)
	}
}

func TestBuildImportReport_NilFeaturesFallsBackToPageCount(t *testing.T) {
	doc := &Document{
		Pages: []*Page{
			reportPage(0, false, "备注"),
			reportPage(1, false, "备注"),
		},
	}
	rep := BuildImportReport(doc)
	if rep.PageCount != 2 {
		t.Fatalf("页数 = %d，期望退回逐页计数 2", rep.PageCount)
	}
}

func TestBuildImportReport_NilDocument(t *testing.T) {
	if rep := BuildImportReport(nil); rep != nil {
		t.Fatalf("nil 文档应返回 nil，得到 %+v", rep)
	}
}

func TestBuildImportReport_NilPageSkipped(t *testing.T) {
	doc := &Document{
		Pages:    []*Page{nil, reportPage(0, false, "备注")},
		Features: &Features{PageCount: 1},
	}
	rep := BuildImportReport(doc)
	if len(rep.Pages) != 1 || rep.Pages[0].Index != 0 {
		t.Fatalf("nil 页应被跳过：%+v", rep.Pages)
	}
}

// 过渡与自动换页是两份独立事实：前者影响观感，后者影响节奏。
// 合成一个结论会让"这页只是切换带过渡"与"这页会自己翻走"看起来一样。
func TestBuildImportReport_TransitionAndAutoAdvance(t *testing.T) {
	doc := &Document{
		SchemaVersion: "1.0",
		ParserName:    "go-pptx",
		ParserVersion: "v2.0.0",
		Features:      &Features{PageCount: 3},
		Pages: []*Page{
			// 第 0 页：只有过渡，不自动换页。
			reportPage(0, false, "有过渡。"),
			// 第 1 页：过渡 + 自动换页。
			reportPage(1, false, "有过渡且自动换页。"),
			// 第 2 页：两者都没有。
			reportPage(2, false, "普通页。"),
		},
	}
	doc.Pages[0].Transition = "fade"
	doc.Pages[1].Transition = "push"
	ms := int64(3000)
	doc.Pages[1].AutoAdvanceMS = &ms
	doc.Pages[0].Shapes = []*Shape{textShape("a")}
	doc.Pages[1].Shapes = []*Shape{textShape("b")}
	doc.Pages[2].Shapes = []*Shape{textShape("c")}

	rep := BuildImportReport(doc)
	var trans, advance *Finding
	for i := range rep.Findings {
		switch rep.Findings[i].Code {
		case FindingTransitions:
			trans = &rep.Findings[i]
		case FindingAutoAdvance:
			advance = &rep.Findings[i]
		}
	}
	if trans == nil {
		t.Fatalf("缺少过渡结论：%+v", rep.Findings)
	}
	if trans.Count != 2 || len(trans.Pages) != 2 || trans.Pages[0] != 0 || trans.Pages[1] != 1 {
		t.Errorf("过渡结论 = %+v，期望 2 页（0,1）", trans)
	}
	if trans.Severity != SeverityInfo {
		t.Errorf("过渡的级别 = %s，期望 info（只影响观感）", trans.Severity)
	}
	if advance == nil {
		t.Fatalf("缺少自动换页结论：%+v", rep.Findings)
	}
	if advance.Count != 1 || len(advance.Pages) != 1 || advance.Pages[0] != 1 {
		t.Errorf("自动换页结论 = %+v，期望 1 页（1）", advance)
	}
}
