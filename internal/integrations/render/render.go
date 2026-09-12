// Package render 提供渲染适配器（V4.0 §5.1 SlideRenderer.Render）。
//
// 基线路径（计划 G0-2 决策）：PPTX → LibreOffice 无头转 PDF → 逐页栅格化 PNG。
// PDF→PNG 光栅化用 poppler-utils（pdftoppm）；可在部署时替换为 go-pdfium/Ghostscript。
// 渲染在 worker 子进程执行；本方只负责调用与参数，不持有文档对象模型。
package render

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrRendererUnavailable 表示渲染器（LibreOffice 或 poppler-utils）未安装。
var ErrRendererUnavailable = errors.New("render: required renderer executable not found")

// SlideRenderer 渲染适配器端口（V4.0 §5.1）：按指定尺寸输出页面图像与兼容性报告。
// 未实现/不可用时显式返回错误，不得输出伪成功。
type SlideRenderer interface {
	Render(ctx context.Context, src io.ReaderAt, size int64, opts RenderOptions) (*RenderResult, error)
}

// RenderOptions 渲染参数。
type RenderOptions struct {
	// DPI 栅格化分辨率。0 = 默认（150）。
	DPI int
	// WorkDir 可替换的临时工作目录；空表示使用系统临时目录。
	WorkDir string
	// Timeout 子进程总时限；0 = 默认（120s）。
	Timeout time.Duration
}

// PageImage 是一页的渲染结果（PNG 字节）。
type PageImage struct {
	Index  int
	Width  int
	Height int
	PNG    []byte
}

// RenderResult 是渲染输出：页面图 + 兼容性报告。
type RenderResult struct {
	Pages  []PageImage
	Report RenderReport
}

// RenderReport 记录渲染器身份、版本与逐页尺寸。
type RenderReport struct {
	Renderer    string   `json:"renderer"`
	Version     string   `json:"version"`
	PDFPageSize string   `json:"pdfPageSize,omitempty"` // "960 x 540 pts (16:9)"
	DPI         int      `json:"dpi"`
	PageCount   int      `json:"pageCount"`
	Warnings    []string `json:"warnings,omitempty"`
}

// SofficeRenderer 以 LibreOffice + poppler-utils 实现渲染。
type SofficeRenderer struct {
	soffice        string
	pdftoppm       string
	pdfinfo        string
	version        string
	defaultDPI     int
	defaultTimeout time.Duration
}

// NewSofficeRenderer 解析可执行文件路径；任一缺失返回 ErrRendererUnavailable。
func NewSofficeRenderer() (*SofficeRenderer, error) {
	soffice, err1 := exec.LookPath("soffice")
	pdfToPPM, err2 := exec.LookPath("pdftoppm")
	pdfInfo, err3 := exec.LookPath("pdfinfo")
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, fmt.Errorf("%w: soffice=%v pdftoppm=%v pdfinfo=%v",
			ErrRendererUnavailable, err1, err2, err3)
	}
	ver := "unknown"
	if v, err := exec.Command(soffice, "--version").Output(); err == nil {
		ver = strings.TrimSpace(firstLine(string(v)))
	}
	return &SofficeRenderer{
		soffice: soffice, pdftoppm: pdfToPPM, pdfinfo: pdfInfo,
		version: ver, defaultDPI: 150, defaultTimeout: 120 * time.Second,
	}, nil
}

var firstLineRe = regexp.MustCompile(`\r?\n`)

func firstLine(s string) string {
	parts := firstLineRe.Split(s, 2)
	return parts[0]
}

// Version 返回渲染器标识（LibreOffice 版本行）。
func (r *SofficeRenderer) Version() string { return r.version }

// Render 执行：源 PPTX → 临时目录 → soffice 转 PDF → pdfinfo 尺寸/页数 → pdftoppm 逐页 PNG。
// 源文件字节不落地为可编辑路径；工作目录用完即清。
func (r *SofficeRenderer) Render(ctx context.Context, src io.ReaderAt, size int64, opts RenderOptions) (*RenderResult, error) {
	work, err := os.MkdirTemp(opts.WorkDir, "ppts-render-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	inPath := filepath.Join(work, "input.pptx")
	f, err := os.Create(inPath)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, io.NewSectionReader(src, 0, size)); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	timeout := r.defaultTimeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	dpi := r.defaultDPI
	if opts.DPI > 0 {
		dpi = opts.DPI
	}

	// 1) LibreOffice 无头转 PDF。
	convCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	outPDF, err := r.convertToPDF(convCtx, work, inPath)
	if err != nil {
		return nil, err
	}

	// 2) pdfinfo 取页数与页面尺寸；pdftoppm 逐页栅格化。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pageCount, sizeInfo, err := r.documentInfo(ctx, outPDF)
	if err != nil {
		return nil, err
	}

	prefix := filepath.Join(work, "page")
	rastCtx, cancel2 := context.WithTimeout(ctx, timeout)
	defer cancel2()
	if err := r.rasterize(rastCtx, outPDF, prefix, dpi, pageCount); err != nil {
		return nil, err
	}

	pages := make([]PageImage, 0, pageCount)
	for i := 1; i <= pageCount; i++ {
		png, err := os.ReadFile(fmt.Sprintf("%s-%d.png", prefix, i))
		if err != nil {
			return nil, fmt.Errorf("render: read page %d: %w", i, err)
		}
		w, h, err := pngSize(png)
		if err != nil {
			return nil, err
		}
		pages = append(pages, PageImage{Index: i - 1, Width: w, Height: h, PNG: png})
	}

	return &RenderResult{
		Pages: pages,
		Report: RenderReport{
			Renderer:    "LibreOffice",
			Version:     r.version,
			PDFPageSize: sizeInfo,
			DPI:         dpi,
			PageCount:   pageCount,
		},
	}, nil
}

// convertToPDF 运行 `soffice --headless --convert-to pdf --outdir <dir> <input>`。
func (r *SofficeRenderer) convertToPDF(ctx context.Context, workDir, input string) (string, error) {
	cmd := exec.CommandContext(ctx, r.soffice,
		"--headless", "--convert-to", "pdf", "--outdir", workDir, input)
	// LibreOffice 可能写 HOME 配置；隔离到工作目录，避免污染用户配置。
	cmd.Env = append(os.Environ(), "HOME="+workDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("render: soffice convert failed: %w\n%s", err, string(out))
	}
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	pdf := filepath.Join(workDir, base+".pdf")
	if _, err := os.Stat(pdf); err != nil {
		return "", fmt.Errorf("render: soffice output pdf not found: %s", pdf)
	}
	return pdf, nil
}

// documentInfo 用 pdfinfo 取页数与页面尺寸。
func (r *SofficeRenderer) documentInfo(ctx context.Context, pdf string) (int, string, error) {
	out, err := exec.CommandContext(ctx, r.pdfinfo, pdf).Output()
	if err != nil {
		return 0, "", fmt.Errorf("render: pdfinfo failed: %w", err)
	}
	pageCount := 0
	pageSize := ""
	rePages := regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)
	reSize := regexp.MustCompile(`(?m)^Page size:\s+(.+)`)
	if m := rePages.FindSubmatch(out); len(m) == 2 {
		fmt.Sscanf(string(m[1]), "%d", &pageCount)
	}
	if m := reSize.FindSubmatch(out); len(m) == 2 {
		pageSize = strings.TrimSpace(string(m[1]))
	}
	if pageCount == 0 {
		return 0, "", fmt.Errorf("render: pdfinfo reported zero pages")
	}
	return pageCount, pageSize, nil
}

// rasterize 用 pdftoppm 逐页栅格化：pdftoppm -png -r <dpi> -f 1 -l <n> <pdf> <prefix>。
func (r *SofficeRenderer) rasterize(ctx context.Context, pdf, prefix string, dpi, pageCount int) error {
	cmd := exec.CommandContext(ctx, r.pdftoppm,
		"-png", "-r", fmt.Sprintf("%d", dpi),
		"-f", "1", "-l", fmt.Sprintf("%d", pageCount),
		pdf, prefix)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("render: pdftoppm failed: %w\n%s", err, string(out))
	}
	return nil
}

// Available 报告渲染器是否可用的快捷判断（供平台能力清单使用）。
func (r *SofficeRenderer) Available() bool { return r != nil }
