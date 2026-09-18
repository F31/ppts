package project

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
