package app

import (
	"context"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/validation"
)

// 本文件锁定 2026-11-02 那次「一键成稿 14 页里 5 页降级、3 页假成功」的回归：
//  ① 页脚页码/版式编号不得进入校验基准（否则每页都"漏一个数字"，两轮后回退原文）；
//  ② 内部键与思考过程不得落库（否则出现「这就是number:1」「专业编辑8能力」）；
//  ③ 供应商 429 要能被识别出来，供任务层收敛为单页失败而非整任务判死。

func TestIsLayoutOnlyNumber(t *testing.T) {
	cases := []struct {
		kind, text string
		want       bool
	}{
		{"textbox", "03", true},
		{"textbox", "12", true},
		{"autoshape", " 7 ", true},
		{"textbox", "2026", false},  // 年份是内容
		{"textbox", "10086", false}, // 电话/编号是内容
		{"textbox", "3a", false},
		{"title", "03", false}, // 标题里的数字是内容
		{"textbox", "", false},
		{"textbox", "第3页", false},
	}
	for _, c := range cases {
		if got := IsLayoutOnlyNumber(c.kind, c.text); got != c.want {
			t.Errorf("IsLayoutOnlyNumber(%q,%q) = %v, want %v", c.kind, c.text, got, c.want)
		}
	}
}

// TestPageSourceTextDropsFooterPageNumber 是这次事故最直接的复现：
// 页面文字末尾带页脚 "03"。过滤前，它会被 ExtractEntities 当成必须保留的事实。
func TestPageSourceTextDropsFooterPageNumber(t *testing.T) {
	shapes := []SourceShapeText{
		{Kind: "title", Text: "MARKET TREND"},
		{Kind: "textbox", Text: "为什么是现在"},
		{Kind: "textbox", Text: "三个正在同时发生的结构性变化"},
		{Kind: "textbox", Text: "03"},
	}
	kind, text := PageSourceText(shapes, "", "layout", "")
	if kind != "page" {
		t.Fatalf("来源种类 = %q，应为 page", kind)
	}
	if strings.Contains(text, "03") {
		t.Fatalf("页脚页码进入了页面文本：%q", text)
	}
	if !strings.Contains(text, "为什么是现在") {
		t.Fatalf("正文被误删：%q", text)
	}
	// 正文里的「三个」现在会被解析成计数实体 number:3（chinese.go，2026-11-02）——这是预期：
	// 成稿写「三个变化」要与「3 个变化」可比。页脚 "03" 若漏进来会与它同键，所以"页码有没有
	// 被过滤掉"由上面的字符串断言守住；这里只确认实体没有被页码额外污染。
	if got := validation.FormatEntities(validation.ExtractEntities(text)); len(got) != 1 || got[0] != "number:3" {
		t.Fatalf("实体 = %v，期望仅 [number:3]（正文的「三个」；页脚 03 不得额外贡献）", got)
	}
}

// TestPageSourceTextKeepsContentNumbers 防止过滤过度：正文里的年份/规格必须保留。
func TestPageSourceTextKeepsContentNumbers(t *testing.T) {
	shapes := []SourceShapeText{
		{Kind: "textbox", Text: "2026 年发布"},
		{Kind: "textbox", Text: "64GB"},
		{Kind: "textbox", Text: "01"},
	}
	_, text := PageSourceText(shapes, "", "layout", "")
	if !strings.Contains(text, "2026") || !strings.Contains(text, "64GB") {
		t.Fatalf("正文数字被误删：%q", text)
	}
	if strings.Contains(text, "01") {
		t.Fatalf("目录序号未被过滤：%q", text)
	}
}

func TestPgAnchorsSkipPageNumber(t *testing.T) {
	pg := parsedPage{
		SlideID: "slide-x",
		Shapes: []parsedShape{
			{ID: "shape-1", Kind: "textbox", Text: "为什么是现在"},
			{ID: "shape-2", Kind: "textbox", Text: "03"},
		},
	}
	anchors := pgAnchors(pg, "layout")
	for _, a := range anchors {
		if a.Raw == "03" {
			t.Fatalf("页脚页码成了来源锚点：%+v", anchors)
		}
	}
	if len(anchors) != 1 {
		t.Fatalf("锚点数 = %d，应为 1（只保留正文）", len(anchors))
	}
}

// TestDraftIgnoresFooterPageNumber 端到端：源素材含页脚页码，而模型只讲正文时，
// 必须一次通过；否则会触发第二轮并最终回退原文（degraded）。
func TestDraftIgnoresFooterPageNumber(t *testing.T) {
	shapes := []SourceShapeText{
		{Kind: "title", Text: "为什么是现在"},
		{Kind: "textbox", Text: "三个正在同时发生的结构性变化"},
		{Kind: "textbox", Text: "03"},
	}
	_, source := PageSourceText(shapes, "", "layout", "")
	f := &fakeRewriter{replies: []string{"为什么是现在？因为三个结构性变化正在同时发生。"}}
	got, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, source)
	if degraded {
		t.Fatalf("页脚页码导致误判降级：%q", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("页码不该触发第二轮修补，实际调用 %d 次", len(f.calls))
	}
}

func TestDraftOutputSuspect(t *testing.T) {
	cases := []struct {
		name     string
		language string
		text     string
		want     bool
	}{
		{"内部键泄漏", "zh-CN", "这就是number:1，接下来是第二部分。", true},
		{"内部键大小写", "zh-CN", "Here is Date: 2026.", true},
		{"思维链中文", "zh-CN", "用户要求我修正一段PPT讲解文案，需要补回原文中遗漏的数字。", true},
		{"思维链英文", "zh-CN", "The user wants me to polish this text while preserving 13.", true},
		{"中文正常稿", "zh-CN", "本页讲三件事：市场趋势、产品定位与技术架构。", false},
		{"中文稿含英文术语", "zh-CN", "我们提供 Go API 服务，兼容 PostgreSQL 与 RLS 行级安全。", false},
		{"英文讲稿不误判", "en-US", strings.Repeat("This slide explains the product roadmap and next steps. ", 6), false},
		{"中文目标但整段英文", "zh-CN", strings.Repeat("This slide explains the product roadmap and next steps. ", 6), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := draftOutputSuspect(c.language, c.text); got != c.want {
				t.Errorf("draftOutputSuspect(%q,%q) = %v, want %v", c.language, c.text, got, c.want)
			}
		})
	}
}

// TestSuspectOutputFallsBackInsteadOfLanding 保证脏产出不会落库：
// 两轮都返回内部键时，只能回退原文并标 degraded。
func TestSuspectOutputFallsBackInsteadOfLanding(t *testing.T) {
	const source = "容量 80GB"
	f := &fakeRewriter{replies: []string{
		"这就是number:80gb，容量很大。",
		"我确认一下：number:80gb 表示容量。",
	}}
	got, degraded := runDraftText(t, newDraftHandlerForTest(f), inputPage, source)
	if !degraded {
		t.Fatalf("脏产出必须标 degraded，实际 degraded=false，产出 %q", got)
	}
	if got != source {
		t.Fatalf("回退结果 = %q，应为原文 %q", got, source)
	}
}

type fakeStatusError struct{ code int }

func (e fakeStatusError) Error() string   { return "provider status" }
func (e fakeStatusError) HTTPStatus() int { return e.code }

func TestIsRateLimitError(t *testing.T) {
	if !isRateLimitError(fakeStatusError{code: 429}) {
		t.Fatal("429 未被识别为限流")
	}
	if isRateLimitError(fakeStatusError{code: 500}) {
		t.Fatal("500 被误判为限流")
	}
	if isRateLimitError(nil) {
		t.Fatal("nil 被误判为限流")
	}
}

// TestDegradedReasonExplainsWhy 保证降级不是"黑盒"：result_ref 里必须有
// 具体缺了什么/多了什么，用户才能判断该改页面还是换模型。
func TestDegradedReasonExplainsWhy(t *testing.T) {
	const source = "容量 80GB"
	f := &fakeRewriter{replies: []string{"存储很大。", "存储依旧很大。"}}
	h := newDraftHandlerForTest(f)
	res, err := h.draftTextDetail(context.Background(), "tenant-1", "project-1", "slide-1", "zh-CN",
		narration.ModePolish, draftInput{Kind: inputPage, Text: source}, nil, ScriptDraftSnapshot{}, h.polisher)
	if err != nil {
		t.Fatalf("draftTextDetail 报错：%v", err)
	}
	if !res.Degraded {
		t.Fatalf("两轮都缺 80GB，应降级")
	}
	if !strings.Contains(res.Reason, "80GB") {
		t.Fatalf("降级原因未指出缺失事实：%q", res.Reason)
	}
	if res.Display != source {
		t.Fatalf("降级应回退原文，得到 %q", res.Display)
	}
	if strings.Contains(res.Reason, "number:") {
		t.Fatalf("降级原因泄漏内部键：%q", res.Reason)
	}
}

func TestDraftTooShort(t *testing.T) {
	cases := []struct {
		text string
		sec  int
		want bool
	}{
		{"各位，我们来看一下市场趋势。答案在于3个变化。", 60, true},
		{strings.Repeat("这是一句讲解。", 30), 60, false},
		{"很短", 0, false},
	}
	for _, c := range cases {
		if got := draftTooShort(c.text, c.sec); got != c.want {
			t.Errorf("draftTooShort(len=%d,sec=%d) = %v, want %v", len([]rune(c.text)), c.sec, got, c.want)
		}
	}
}

// TestExtractFactCitationsDropsTruncatedMarker 守住 69 字残稿那种结尾：
// 未闭合的「〔fact-c」必须被剥离，且不能被当成"引用了不存在的出处"。
func TestExtractFactCitationsDropsTruncatedMarker(t *testing.T) {
	clean, cited := extractFactCitations("这是正文。〔fact-c")
	if strings.Contains(clean, "〔fact") {
		t.Fatalf("残片未被剥离：%q", clean)
	}
	if len(cited) != 0 {
		t.Fatalf("残缺标记被记成引用：%v", cited)
	}

	clean2, cited2 := extractFactCitations("正文〔fact-a1〕继续〔fact-b2〕。")
	if clean2 != "正文继续。" {
		t.Fatalf("完整标记剥离结果 = %q", clean2)
	}
	if len(cited2) != 2 {
		t.Fatalf("完整标记应提取 2 条，得到 %v", cited2)
	}
}
