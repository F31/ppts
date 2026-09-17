package render

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/F31/go-pptx"
)

// minimalPDFPages 生成含 xref 的 n 页最小 PDF（每页 200x200pt 蓝色方块），
// 用于在不安装 LibreOffice 的机器上验证 poppler（pdfinfo/pdftoppm）链路。
// 页数可配是因为 pdftoppm 的产物命名宽度随文档总页数变化（≥10 页零填充）。
func minimalPDFPages(n int) []byte {
	var b bytes.Buffer
	off := []int{}
	writeObj := func(body string) {
		off = append(off, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(off), body)
	}
	// 对象 1 = Catalog，对象 2 = Pages，其后每页占 2 个对象（Page + Contents）。
	kids := ""
	for i := 0; i < n; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+2*i)
	}
	writeObj("<< /Type /Catalog /Pages 2 0 R >>")
	writeObj("<< /Type /Pages /Kids [" + kids + "] /Count " + strconv.Itoa(n) + " >>")
	stream := "q 0.4 0.6 0.8 rg 20 20 160 160 re f Q"
	for i := 0; i < n; i++ {
		writeObj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents %d 0 R /Resources << >> >>", 4+2*i))
		writeObj("<< /Length " + strconv.Itoa(len(stream)) + " >>\nstream\n" + stream + "\nendstream")
	}

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(off)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(off)+1, xref)
	return b.Bytes()
}

// minimalPDF 生成单页最小 PDF。
func minimalPDF() []byte { return minimalPDFPages(1) }

// TestPopplerRasterizePaddedPageNames 锁定 pdftoppm 的产物命名：≥10 页时页码会零填充
// （page-01.png…page-14.png），按固定的 "%s-%d.png" 拼路径会一个页图都读不到。
// 该用例不需要 LibreOffice，可在 CI 上直接拦住这类回归。
func TestPopplerRasterizePaddedPageNames(t *testing.T) {
	for _, bin := range []string{"pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("poppler unavailable (%s): %v", bin, err)
		}
	}
	r := &SofficeRenderer{pdftoppm: "pdftoppm", pdfinfo: "pdfinfo"}
	ctx := context.Background()
	const pages = 14
	pdf := t.TempDir() + "/many.pdf"
	if err := os.WriteFile(pdf, minimalPDFPages(pages), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	pageCount, _, err := r.documentInfo(ctx, pdf)
	if err != nil {
		t.Fatalf("documentInfo: %v", err)
	}
	if pageCount != pages {
		t.Fatalf("pageCount = %d, want %d", pageCount, pages)
	}
	prefix := t.TempDir() + "/page"
	if err := r.rasterize(ctx, pdf, prefix, 96, pageCount); err != nil {
		t.Fatalf("rasterize: %v", err)
	}
	// 与 Render 相同的取图方式：按实际产物枚举，而不是拼 page-1.png。
	files, err := rasterizedPageFiles(prefix, pages)
	if err != nil {
		t.Fatalf("rasterizedPageFiles: %v", err)
	}
	if _, err := os.Stat(prefix + "-1.png"); err == nil {
		t.Fatalf("expected zero-padded names, but %s-1.png exists", prefix)
	}
	for i, f := range files {
		png, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if w, h, err := pngSize(png); err != nil || w <= 0 || h <= 0 {
			t.Fatalf("%s size: %dx%d err=%v", f, w, h, err)
		}
		want := fmt.Sprintf("%s-%02d.png", prefix, i+1)
		if f != want {
			t.Fatalf("file %d = %s, want %s (词法序必须即页序)", i, f, want)
		}
	}
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
// （pdfinfo 页数/尺寸 + pdftoppm 栅格化 + pngSize 校验）。依赖 pdftoppm/pdfinfo，二者缺席时 Skip。
func TestPopplerRasterize(t *testing.T) {
	// 与同文件 LibreOffice 用例一致：外部工具缺席即 Skip，避免在未装 poppler 的机器上假失败。
	for _, bin := range []string{"pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("poppler unavailable (%s): %v", bin, err)
		}
	}
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
// 用 12 页而不是 1 页：pdftoppm 在 ≥10 页时会给页码补零（page-01.png），
// 单页 deck 恰好掩盖了取图路径的命名问题（历史 bug，见 rasterizedPageFiles）。
func TestSofficeRenderFullPipeline(t *testing.T) {
	r, err := NewSofficeRenderer()
	if err != nil {
		t.Skipf("libreoffice unavailable: %v", err)
	}
	const slides = 12
	src := buildDeckBytesN(t, slides)
	res, err := r.Render(context.Background(), bytes.NewReader(src), int64(len(src)), RenderOptions{DPI: 96})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.Report.PageCount != slides {
		t.Fatalf("page count: %d, want %d", res.Report.PageCount, slides)
	}
	if len(res.Pages) != slides {
		t.Fatalf("pages: got %d, want %d", len(res.Pages), slides)
	}
	for i, page := range res.Pages {
		if page.Index != i || len(page.PNG) == 0 || page.Width <= 0 || page.Height <= 0 {
			t.Fatalf("page %d: index=%d w=%d h=%d bytes=%d", i, page.Index, page.Width, page.Height, len(page.PNG))
		}
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

// buildDeckBytesN 生成含 slides 页的合成 deck。页数直接影响 pdftoppm 的产物命名宽度，
// 所以验证取图路径的用例必须真的产出足够多的页数。
func buildDeckBytesN(t *testing.T, slides int) []byte {
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
	for i := 0; i < slides; i++ {
		slide, err := p.AddSlide(layouts[0])
		if err != nil {
			t.Fatalf("AddSlide %d: %v", i, err)
		}
		if _, err := slide.AddTextBox(pptx.TextBoxSpec{
			X: 914400, Y: 914400, Width: 6000000, Height: 914400,
			Text: "渲染测试 " + strconv.Itoa(i+1),
		}); err != nil {
			t.Fatalf("AddTextBox %d: %v", i, err)
		}
	}
	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

func buildDeckBytes(t *testing.T) []byte {
	t.Helper()
	return buildDeckBytesN(t, 1)
}
