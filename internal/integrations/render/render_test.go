package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/F31/go-pptx"
)

// minimalPDF 生成一份含 xref 的最小一页 PDF（200x200pt，蓝色方块），
// 用于在不安装 LibreOffice 的机器上验证 poppler（pdfinfo/pdftoppm）链路。
func minimalPDF() []byte {
	var b bytes.Buffer
	off := []int{}
	writeObj := func(body string) {
		off = append(off, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(off), body)
	}
	writeObj("<< /Type /Catalog /Pages 2 0 R >>")
	writeObj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << >> >>")
	stream := "q 0.4 0.6 0.8 rg 20 20 160 160 re f Q"
	writeObj("<< /Length " + strconv.Itoa(len(stream)) + " >>\nstream\n" + stream + "\nendstream")

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(off)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(off)+1, xref)
	return b.Bytes()
}

func TestPNGSize(t *testing.T) {
	data := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, // signature
		0, 0, 0, 13, 'I', 'H', 'D', 'R',
		0, 0, 3, 232, // width = 1000
		0, 0, 1, 144, // height = 400
	}
	w, h, err := pngSize(data)
	if err != nil {
		t.Fatalf("pngSize: %v", err)
	}
	if w != 1000 || h != 400 {
		t.Fatalf("pngSize: got %dx%d, want 1000x400", w, h)
	}
	if _, _, err := pngSize([]byte("nope")); err == nil {
		t.Fatalf("pngSize should reject non-png")
	}
}

// TestPopplerRasterize 在不依赖 LibreOffice 的情况下验证 poppler 链路
// （pdfinfo 页数/尺寸 + pdftoppm 栅格化 + pngSize 校验）。依赖 pdftoppm/pdfinfo。
func TestPopplerRasterize(t *testing.T) {
	r := &SofficeRenderer{pdftoppm: "pdftoppm", pdfinfo: "pdfinfo"}
	// 通过文档信息与栅格化方法直接驱动最小 PDF。
	pdf := writePDF(t)
	ctx := context.Background()
	pageCount, sizeInfo, err := r.documentInfo(ctx, pdf)
	if err != nil {
		t.Fatalf("documentInfo: %v", err)
	}
	if pageCount != 1 || sizeInfo == "" {
		t.Fatalf("documentInfo: pages=%d size=%q", pageCount, sizeInfo)
	}
	dir := t.TempDir()
	prefix := dir + "/page"
	if err := r.rasterize(ctx, pdf, prefix, 150, pageCount); err != nil {
		t.Fatalf("rasterize: %v", err)
	}
	png, err := os.ReadFile(prefix + "-1.png")
	if err != nil {
		t.Fatalf("read page png: %v", err)
	}
	w, h, err := pngSize(png)
	if err != nil || w <= 0 || h <= 0 {
		t.Fatalf("page png size: %dx%d err=%v", w, h, err)
	}
}

// TestSofficeRenderFullPipeline 只在安装 LibreOffice 的机器上执行真渲染；
// 否则 Skip（对齐 go-pptx corpus 缺席即 Skip 的惯例）。
func TestSofficeRenderFullPipeline(t *testing.T) {
	r, err := NewSofficeRenderer()
	if err != nil {
		t.Skipf("libreoffice unavailable: %v", err)
	}
	src := buildDeckBytes(t)
	res, err := r.Render(context.Background(), bytes.NewReader(src), int64(len(src)), RenderOptions{DPI: 96})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.Report.PageCount != 1 {
		t.Fatalf("page count: %d", res.Report.PageCount)
	}
	if len(res.Pages) != 1 || len(res.Pages[0].PNG) == 0 || res.Pages[0].Width <= 0 {
		t.Fatalf("pages: %+v", res.Pages)
	}
	if res.Report.Renderer != "LibreOffice" || res.Report.Version == "" {
		t.Fatalf("report: %+v", res.Report)
	}
}

// TestSofficeRenderWindowsInterop 验证 WSL→宿主机 Windows LibreOffice interop 全链路：
// 仅在设置了 PPTS_SOFFICE_BIN（Windows .exe）与 PPTS_RENDER_WORK_ROOT（/mnt 下）时执行。
func TestSofficeRenderWindowsInterop(t *testing.T) {
	if os.Getenv("PPTS_SOFFICE_BIN") == "" || os.Getenv("PPTS_RENDER_WORK_ROOT") == "" {
		t.Skip("PPTS_SOFFICE_BIN + PPTS_RENDER_WORK_ROOT not set")
	}
	r, err := NewSofficeRenderer()
	if err != nil {
		t.Fatalf("NewSofficeRenderer: %v", err)
	}
	if !r.windows {
		t.Fatalf("expected windows soffice, got %q", r.soffice)
	}
	src := buildDeckBytes(t)
	res, err := r.Render(context.Background(), bytes.NewReader(src), int64(len(src)), RenderOptions{DPI: 96})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.Report.PageCount != 1 || len(res.Pages) != 1 || res.Pages[0].Width <= 0 {
		t.Fatalf("pages: %+v", res.Pages)
	}
}

func TestToWinPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/mnt/c/Users/a/b", "C:\\Users\\a\\b"},
		{"/mnt/d/LibreOffice/x.pptx", "D:\\LibreOffice\\x.pptx"},
		{"/tmp/not-windows", "/tmp/not-windows"},
	}
	for _, c := range cases {
		if got := toWinPath(c.in); got != c.want {
			t.Fatalf("toWinPath(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func writePDF(t *testing.T) string {
	t.Helper()
	p := t.TempDir() + "/min.pdf"
	if err := os.WriteFile(p, minimalPDF(), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	return p
}

func buildDeckBytes(t *testing.T) []byte {
	t.Helper()
	p, err := pptx.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()
	layouts, err := p.Layouts()
	if err != nil || len(layouts) == 0 {
		t.Fatalf("Layouts: %v", err)
	}
	slide, err := p.AddSlide(layouts[0])
	if err != nil {
		t.Fatalf("AddSlide: %v", err)
	}
	if _, err := slide.AddTextBox(pptx.TextBoxSpec{
		X: 914400, Y: 914400, Width: 6000000, Height: 914400, Text: "渲染测试",
	}); err != nil {
		t.Fatalf("AddTextBox: %v", err)
	}
	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}
