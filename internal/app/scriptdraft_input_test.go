package app

import (
	"testing"

	"github.com/F31/ppts/internal/narration"
)

// 本文件锁定「成稿来源优先级」——不依赖 PG/外部工具，任何机器可跑。
//
// 回归背景（2026-09-22）：一键成稿选「来源=页面内容 + 覆盖全部」时，产出的讲稿是
// 旧讲稿的润色版，页面文字从未进入提示词。原因是 pgTextForMode 的结果被
// `overwrite && rev != nil` 无条件覆盖。前三条用例即该缺陷的守门人。

type testShape struct{ kind, text string }

func testPage(notes string, shapes ...testShape) parsedPage {
	pg := parsedPage{SlideID: "slide-1", NotesText: notes}
	for _, sh := range shapes {
		var item struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		item.ID = "shape-" + sh.kind
		item.Kind = sh.kind
		item.Text = sh.text
		pg.Shapes = append(pg.Shapes, item)
	}
	return pg
}

func TestSelectDraftInputSourcePriority(t *testing.T) {
	page := testPage("备注文字",
		testShape{"title", "页面标题"},
		testShape{"autoshape", "页面正文一"},
		testShape{"autoshape", "页面正文二"},
	)
	const pageAll = "页面标题\n页面正文一\n页面正文二"
	const pageBody = "页面正文一\n页面正文二"
	pageNotesOnly := testPage("只有备注")
	pageNoText := testPage("")

	cases := []struct {
		name       string
		pg         parsedPage
		source     string
		custom     string
		sourceMode string
		mode       narration.ScriptMode
		overwrite  bool
		existing   string
		want       string
	}{
		{
			// ★ 回归：显式选页面内容 + 覆盖全部 + 已有旧讲稿 → 必须用页面文字
			name: "页面内容优先于沿用旧讲稿", pg: page, sourceMode: "page_content", mode: narration.ModePolish,
			overwrite: true, existing: "旧的讲稿文字", want: pageAll,
		},
		{
			// ★ 页面级来源（无备注页的「讲稿来源」）同样优先于旧讲稿
			name: "页面级来源优先于沿用旧讲稿", pg: page, source: "body", mode: narration.ModePolish,
			overwrite: true, existing: "旧的讲稿文字", want: pageBody,
		},
		{
			// ★ 自定义文本来源同样优先于旧讲稿
			name: "自定义文本来源优先于旧讲稿", pg: page, source: "custom", custom: "用户自定义输入",
			mode: narration.ModePolish, overwrite: true, existing: "旧的讲稿文字", want: "用户自定义输入",
		},
		{
			// 单页「重新生成讲稿」（未指定来源 + overwrite）保持沿用旧稿润色
			name: "未指定来源时沿用旧讲稿", pg: page, mode: narration.ModePolish,
			overwrite: true, existing: "旧的讲稿文字", want: "旧的讲稿文字",
		},
		{
			name: "未指定来源且无旧稿用页面文字", pg: page, mode: narration.ModePolish,
			overwrite: true, existing: "", want: pageAll,
		},
		{
			name: "原文模式未指定来源用备注", pg: page, mode: narration.ModeOriginal,
			overwrite: true, existing: "旧的讲稿文字", want: "备注文字",
		},
		{
			name: "原文模式显式仅备注", pg: page, sourceMode: "notes_only", mode: narration.ModeOriginal,
			overwrite: true, existing: "旧的讲稿文字", want: "备注文字",
		},
		{
			name: "备注优先且无备注时回退页面文字", pg: pageNotesOnly, sourceMode: "notes_first",
			mode: narration.ModePolish, overwrite: true, existing: "旧的讲稿文字", want: "只有备注",
		},
		{
			name: "页面内容来源在页面无文字但有备注时用备注", pg: pageNotesOnly,
			sourceMode: "page_content", mode: narration.ModePolish, overwrite: true, existing: "旧稿", want: "只有备注",
		},
		{
			name: "页面无文字无备注时无素材", pg: pageNoText, sourceMode: "page_content",
			mode: narration.ModePolish, overwrite: true, existing: "旧稿", want: "",
		},
		{
			name: "原文模式无备注时无素材", pg: pageNoText, mode: narration.ModeOriginal,
			overwrite: false, existing: "", want: "",
		},
		{
			name: "无文字页未指定来源时仍可沿用旧稿", pg: pageNoText, mode: narration.ModePolish,
			overwrite: true, existing: "旧稿", want: "旧稿",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectDraftInput(tc.pg, tc.source, tc.custom, tc.sourceMode, tc.mode, tc.overwrite, tc.existing)
			if got != tc.want {
				t.Fatalf("selectDraftInput = %q, want %q", got, tc.want)
			}
		})
	}
}
