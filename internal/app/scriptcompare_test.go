package app

import (
	"testing"

	"github.com/F31/ppts/internal/project"
)

// 并排对照的"原文"必须与成稿输入同源：它决定了用户拿什么跟 AI 稿比对。
// 这里守两件事：
//  1. 来源优先级（显式来源 > 形状文字 > 备注回退）只有一份实现；
//  2. 成稿读 document.json（parsedPage）、对照读 project.Document 时结果一致。

func TestPageOriginalTextSourcePriority(t *testing.T) {
	shapes := []SourceShapeText{
		{Kind: "title", Text: "第三季度回顾"},
		{Kind: "body", Text: "营收同比增长 23%"},
		{Kind: "body", Text: " "}, // 纯空白不得产生空行
	}
	cases := []struct {
		name    string
		shapes  []SourceShapeText
		notes   string
		source  string
		custom  string
		wantKnd string
		wantTxt string
	}{
		{"默认取全部形状文字", shapes, "备注", "", "", "page", "第三季度回顾\n营收同比增长 23%"},
		{"只取标题", shapes, "", "title", "", "page", "第三季度回顾"},
		{"只取正文", shapes, "", "body", "", "page", "营收同比增长 23%"},
		{"显式备注优先于形状", shapes, "  备注原文  ", "notes", "", "notes", "备注原文"},
		{"自定义文本", shapes, "备注", "custom", "手写素材", "custom", "手写素材"},
		{"自定义为空回退备注", shapes, "备注", "custom", "   ", "notes", "备注"},
		{"无形状回退备注", nil, "备注", "", "", "notes", "备注"},
		{"形状全空白回退备注", []SourceShapeText{{Kind: "body", Text: "  "}}, "备注", "", "", "notes", "备注"},
		{"全都为空", nil, "", "", "", "notes", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, text := PageSourceText(tc.shapes, tc.notes, tc.source, tc.custom)
			if kind != tc.wantKnd || text != tc.wantTxt {
				t.Fatalf("PageSourceText = (%q, %q), want (%q, %q)", kind, text, tc.wantKnd, tc.wantTxt)
			}
		})
	}
}

// 对照面板读 project.Page、成稿读 parsedPage：两条读路径必须给出同一段原文。
// 若哪天有人只改了一边（例如给 project 侧加"包含图片 altText"），这条会红。
func TestPageOriginalTextMatchesDraftInput(t *testing.T) {
	pg := project.Page{
		SlideID:   "slide-3",
		NotesText: "这是备注",
		Shapes: []*project.Shape{
			{ID: "s1", Kind: "title", Text: "标题"},
			{ID: "s2", Kind: "body", Text: " 要点一 "},
			{ID: "s3", Kind: "body", Text: "要点二"},
			{ID: "s4", Kind: "chart", Text: ""},
		},
	}
	for _, source := range []string{"", "layout", "title", "body", "notes", "custom"} {
		kind, text := PageOriginalText(&pg, source, "自定义")
		in := pgInput(parsedPage{
			SlideID:   pg.SlideID,
			NotesText: pg.NotesText,
			Shapes: []parsedShape{
				{ID: "s1", Kind: "title", Text: "标题"},
				{ID: "s2", Kind: "body", Text: " 要点一 "},
				{ID: "s3", Kind: "body", Text: "要点二"},
				{ID: "s4", Kind: "chart", Text: ""},
			},
		}, source, "自定义")
		if kind != string(in.Kind) || text != in.Text {
			t.Fatalf("source=%q: 对照 (%q,%q) != 成稿 (%q,%q)", source, kind, text, in.Kind, in.Text)
		}
	}
}

func TestPageOriginalTextNilPage(t *testing.T) {
	kind, text := PageOriginalText(nil, "", "")
	if kind != "notes" || text != "" {
		t.Fatalf("nil page = (%q,%q), want (notes,\"\")", kind, text)
	}
}

// 空的 *project.Shape（解析文档里可能出现 null 元素）不得 panic。
func TestPageOriginalTextSkipsNilShape(t *testing.T) {
	pg := &project.Page{Shapes: []*project.Shape{nil, {Kind: "body", Text: "正文"}}}
	kind, text := PageOriginalText(pg, "", "")
	if kind != "page" || text != "正文" {
		t.Fatalf("= (%q,%q), want (page,正文)", kind, text)
	}
}
