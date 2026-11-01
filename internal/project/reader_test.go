package project

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	pptx "github.com/F31/go-pptx/v2/pptx"
)

// buildTestDeck 用 go-pptx 生成一个最小可读样本：两页文本 + 备注，写回内存。
func buildTestDeck(t *testing.T) *bytes.Reader {
	t.Helper()
	p, err := pptx.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	layouts, err := p.Layouts()
	if err != nil || len(layouts) == 0 {
		t.Fatalf("Layouts: %v (n=%d)", err, len(layouts))
	}
	slide, err := p.AddSlide(layouts[0])
	if err != nil {
		t.Fatalf("AddSlide: %v", err)
	}
	if _, err := slide.AddTextBox(pptx.TextBoxSpec{
		X: 914400, Y: 914400, Width: 6000000, Height: 914400,
		Text: "PCIe 5.0 x16",
	}); err != nil {
		t.Fatalf("AddTextBox: %v", err)
	}
	if err := slide.SetSpeakerNotes("本页介绍 PCIe 5.0。"); err != nil {
		t.Fatalf("SetSpeakerNotes: %v", err)
	}

	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestGoPPTXReaderInspect(t *testing.T) {
	src := buildTestDeck(t)
	r := NewGoPPTXReader(Limits{})
	doc, err := r.Inspect(context.Background(), src, int64(src.Len()))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if doc.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion: got %q", doc.SchemaVersion)
	}
	if doc.ParserName != "go-pptx" || doc.ParserVersion != "v2.0.0" {
		t.Fatalf("parser info: %s %s", doc.ParserName, doc.ParserVersion)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("pages: got %d, want 1", len(doc.Pages))
	}
	pg := doc.Pages[0]
	if !strings.Contains(pg.NotesText, "PCIe 5.0") {
		t.Fatalf("notes: got %q", pg.NotesText)
	}
	if pg.SlideID == "" || pg.Part == "" {
		t.Fatalf("slide identity missing: id=%q part=%q", pg.SlideID, pg.Part)
	}
	var foundText bool
	for _, sh := range pg.Shapes {
		if sh.Opaque {
			t.Errorf("unexpected opaque shape: %s", sh.Name)
		}
		if strings.Contains(sh.Text, "PCIe 5.0") {
			foundText = true
		}
	}
	if !foundText {
		t.Fatalf("textbox text not extracted; shapes=%+v", pg.Shapes)
	}
	if doc.Features.PageCount != 1 || doc.Features.PagesWithNotes != 1 {
		t.Fatalf("features: %+v", doc.Features)
	}
}

func TestGoPPTXReaderRejectsNonPPTX(t *testing.T) {
	r := NewGoPPTXReader(Limits{})
	raw := bytes.NewReader([]byte("this is not a pptx zip"))
	if _, err := r.Inspect(context.Background(), raw, int64(raw.Len())); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("non-pptx should map to ErrUnsupportedFormat, got %v", err)
	}
}

func TestGoPPTXReaderTooLarge(t *testing.T) {
	r := NewGoPPTXReader(Limits{MaxBytes: 10, MaxPages: 100})
	src := bytes.NewReader(make([]byte, 64))
	if _, err := r.Inspect(context.Background(), src, 64); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize should map to ErrTooLarge, got %v", err)
	}
}

// hideSlide 把 pptx 里第 index 页标记为隐藏（p:sldIdLst 里加 show="0"）。
// 直接改包而不是找一个隐藏页样本：样本一旦换掉，这条用例就不再验"隐藏"了，
// 而 show="0" 是 PowerPoint 表达隐藏页的唯一方式，改一处就够。
func hideSlide(t *testing.T, deck []byte, index int) *bytes.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(deck), int64(len(deck)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		if f.Name == "ppt/presentation.xml" {
			marked, err := markHiddenSlide(body, index)
			if err != nil {
				t.Fatalf("mark hidden: %v", err)
			}
			body = marked
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatalf("create %s: %v", f.Name, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("write %s: %v", f.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return bytes.NewReader(out.Bytes())
}

// markHiddenSlide 在 sldIdLst 的第 index 个 sldId 上补 show="0"。
func markHiddenSlide(xmlBody []byte, index int) ([]byte, error) {
	s := string(xmlBody)
	const open = "<p:sldId "
	pos := 0
	for i := 0; i <= index; i++ {
		next := strings.Index(s[pos:], open)
		if next < 0 {
			return nil, fmt.Errorf("sldId #%d not found", index)
		}
		pos += next
		if i < index {
			pos += len(open)
		}
	}
	end := strings.Index(s[pos:], "/>")
	if end < 0 {
		return nil, fmt.Errorf("sldId #%d is not self-closing", index)
	}
	end += pos
	return []byte(s[:end] + ` show="0"` + s[end:]), nil
}

func TestGoPPTXReaderMarksHiddenSlide(t *testing.T) {
	src := buildTestDeck(t)
	deck := make([]byte, src.Len())
	if _, err := src.ReadAt(deck, 0); err != nil && err != io.EOF {
		t.Fatalf("read deck: %v", err)
	}
	hidden := hideSlide(t, deck, 0)

	r := NewGoPPTXReader(Limits{})
	doc, err := r.Inspect(context.Background(), hidden, int64(hidden.Len()))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("pages: %d", len(doc.Pages))
	}
	pg := doc.Pages[0]
	if pg.Hidden == nil || !*pg.Hidden {
		t.Fatalf("page should be hidden, got %v", pg.Hidden)
	}
	// 关键：隐藏这个事实必须出现在 featureFlags 里——接口只回传这一份，
	// 不进 flags 等于界面上看不出这页是隐藏页。
	var found bool
	for _, f := range pg.FeatureFlags {
		if f == "hidden" {
			found = true
		}
	}
	if !found {
		t.Fatalf("featureFlags should contain %q, got %v", "hidden", pg.FeatureFlags)
	}
}

func TestGoPPTXReaderVisibleSlideHasNoHiddenFlag(t *testing.T) {
	src := buildTestDeck(t)
	r := NewGoPPTXReader(Limits{})
	doc, err := r.Inspect(context.Background(), src, int64(src.Len()))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, f := range doc.Pages[0].FeatureFlags {
		if f == "hidden" {
			t.Fatalf("visible page must not carry hidden flag: %v", doc.Pages[0].FeatureFlags)
		}
	}
}

// buildTransitionDeck 生成两页样本：第 1 页带 fade 过渡 + 3 秒自动换页，
// 第 2 页什么都没有。两份页面放一起才能证明"标记是按页判定的"，
// 而不是整份文档只要有一页有过渡就全标上。
func buildTransitionDeck(t *testing.T) *bytes.Reader {
	t.Helper()
	p, err := pptx.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()
	layouts, err := p.Layouts()
	if err != nil || len(layouts) == 0 {
		t.Fatalf("Layouts: %v (n=%d)", err, len(layouts))
	}
	for i := 0; i < 2; i++ {
		s, err := p.AddSlide(layouts[0])
		if err != nil {
			t.Fatalf("AddSlide: %v", err)
		}
		if _, err := s.AddTextBox(pptx.TextBoxSpec{
			X: 914400, Y: 914400, Width: 6000000, Height: 914400,
			Text: fmt.Sprintf("page %d", i+1),
		}); err != nil {
			t.Fatalf("AddTextBox: %v", err)
		}
		if i == 0 {
			if err := s.SetTransition(pptx.TransitionSpec{Type: pptx.TransitionFade}); err != nil {
				t.Fatalf("SetTransition: %v", err)
			}
			if err := s.SetAdvanceAfter(3 * time.Second); err != nil {
				t.Fatalf("SetAdvanceAfter: %v", err)
			}
		}
	}
	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestGoPPTXReaderReadsTransitionAndAutoAdvance(t *testing.T) {
	src := buildTransitionDeck(t)
	doc, err := NewGoPPTXReader(Limits{}).Inspect(context.Background(), src, int64(src.Len()))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(doc.Pages) != 2 {
		t.Fatalf("pages: %d", len(doc.Pages))
	}
	first, second := doc.Pages[0], doc.Pages[1]
	if first.Transition != "fade" {
		t.Errorf("第 1 页过渡 = %q，期望 fade", first.Transition)
	}
	if first.AutoAdvanceMS == nil || *first.AutoAdvanceMS != 3000 {
		t.Errorf("第 1 页自动换页 = %v，期望 3000ms", first.AutoAdvanceMS)
	}
	if second.Transition != "" {
		t.Errorf("第 2 页不该有过渡，得到 %q", second.Transition)
	}
	if second.AutoAdvanceMS != nil {
		t.Errorf("第 2 页不该有自动换页，得到 %v", *second.AutoAdvanceMS)
	}
	// 汇总计数必须按页累加，否则报告里的"多少页有过渡"会变成布尔值。
	if doc.Features.SlidesWithTransition != 1 || doc.Features.SlidesAutoAdvance != 1 {
		t.Errorf("features 过渡=%d 自动换页=%d，期望都是 1",
			doc.Features.SlidesWithTransition, doc.Features.SlidesAutoAdvance)
	}
	hasFlag := func(pg *Page, want string) bool {
		for _, f := range pg.FeatureFlags {
			if f == want {
				return true
			}
		}
		return false
	}
	// 过渡与自动换页必须分开标记：合并成一个 flag 就看不出"这页只是有过渡"
	// 还是"这页会自己翻走"——后者直接影响成片节奏。
	if !hasFlag(first, "transition") || !hasFlag(first, "auto_advance") {
		t.Errorf("第 1 页标记 = %v，期望含 transition 与 auto_advance", first.FeatureFlags)
	}
	if hasFlag(second, "transition") || hasFlag(second, "auto_advance") {
		t.Errorf("第 2 页标记 = %v，不该含过渡相关项", second.FeatureFlags)
	}
}
