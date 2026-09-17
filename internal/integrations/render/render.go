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
	windows        bool // soffice 为 Windows .exe（WSL interop 场景，需 Windows 路径与工作目录）
	workRoot       string
	version        string
	defaultDPI     int
	defaultTimeout time.Duration
}

// Config 允许显式指定可执行文件路径与工作目录根（用于非标准安装/WSL→Windows interop）。
type Config struct {
	SofficeBin  string // 缺省 exec.LookPath("soffice")
	PdfToPPMBin string // 缺省 exec.LookPath("pdftoppm")
	PdfInfoBin  string // 缺省 exec.LookPath("pdfinfo")
	WorkRoot    string // 渲染临时目录根；Windows soffice 场景必须位于 /mnt/<drive> 下
}

// NewSofficeRenderer 从环境变量解析可执行文件路径并构建渲染器。
// 支持 PPTS_SOFFICE_BIN / PPTS_PDFTOPPPM_BIN / PPTS_PDFINFO_BIN / PPTS_RENDER_WORK_ROOT 覆盖。
func NewSofficeRenderer() (*SofficeRenderer, error) {
	return NewSofficeRendererWithConfig(Config{
		SofficeBin:  os.Getenv("PPTS_SOFFICE_BIN"),
		PdfToPPMBin: os.Getenv("PPTS_PDFTOPPPM_BIN"),
		PdfInfoBin:  os.Getenv("PPTS_PDFINFO_BIN"),
		WorkRoot:    os.Getenv("PPTS_RENDER_WORK_ROOT"),
	})
}

// NewSofficeRendererWithConfig 解析可执行文件路径；任一缺失返回 ErrRendererUnavailable。
func NewSofficeRendererWithConfig(cfg Config) (*SofficeRenderer, error) {
	soffice := cfg.SofficeBin
	var err1 error
	if soffice == "" {
		soffice, err1 = exec.LookPath("soffice")
	}
	pdfToPPM := cfg.PdfToPPMBin
	var err2 error
	if pdfToPPM == "" {
		pdfToPPM, err2 = exec.LookPath("pdftoppm")
	}
	pdfInfo := cfg.PdfInfoBin
	var err3 error
	if pdfInfo == "" {
		pdfInfo, err3 = exec.LookPath("pdfinfo")
	}
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, fmt.Errorf("%w: soffice=%v pdftoppm=%v pdfinfo=%v",
			ErrRendererUnavailable, err1, err2, err3)
	}
	windows := strings.HasSuffix(strings.ToLower(soffice), ".exe")
	workRoot := cfg.WorkRoot
	if workRoot == "" {
		workRoot = os.TempDir()
	}
	ver := "unknown"
	if v, err := exec.Command(soffice, "--version").Output(); err == nil && strings.TrimSpace(string(v)) != "" {
		ver = strings.TrimSpace(firstLine(string(v)))
	}
	if windows && ver == "unknown" {
		ver = "unknown (Windows)"
	}
	return &SofficeRenderer{
		soffice: soffice, pdftoppm: pdfToPPM, pdfinfo: pdfInfo,
		windows: windows, workRoot: workRoot, version: ver,
		defaultDPI: 150, defaultTimeout: 120 * time.Second,
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
	workRoot := r.workRoot
	if opts.WorkDir != "" {
		workRoot = opts.WorkDir
	}
	if r.windows && !strings.HasPrefix(workRoot, "/mnt/") {
		return nil, fmt.Errorf("render: Windows soffice requires a work dir under /mnt/<drive> (set PPTS_RENDER_WORK_ROOT), got %q", workRoot)
	}
	work, err := os.MkdirTemp(workRoot, "ppts-render-*")
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

	pageFiles, err := rasterizedPageFiles(prefix, pageCount)
	if err != nil {
		return nil, err
	}

	pages := make([]PageImage, 0, pageCount)
	for i, file := range pageFiles {
		png, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("render: read page %d: %w", i+1, err)
		}
		w, h, err := pngSize(png)
		if err != nil {
			return nil, err
		}
		pages = append(pages, PageImage{Index: i, Width: w, Height: h, PNG: png})
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

// rasterizedPageFiles 返回 pdftoppm 实际产出的页图路径，按页码顺序。
//
// 页码宽度取决于文档总页数（14 页 → page-01.png…page-14.png，2 页 → page-1.png…page-2.png），
// 所以不能按固定的 "%s-%d.png" 拼路径 —— 那会让 10 页及以上的文档一个页图都读不到。
// 同一次运行的宽度一致，filepath.Glob 的词法序即页序；数量不符时报错而不是漏页。
func rasterizedPageFiles(prefix string, pageCount int) ([]string, error) {
	files, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return nil, fmt.Errorf("render: list rasterized pages: %w", err)
	}
	if len(files) != pageCount {
		return nil, fmt.Errorf("render: pdftoppm produced %d page images, pdfinfo reported %d pages",
			len(files), pageCount)
	}
	return files, nil
}

// convertToPDF 运行 `soffice --headless --convert-to pdf --outdir <dir> <input>`。
// Windows .exe（WSL interop）需要 Windows 风格路径与独立 UserInstallation，避免锁冲突。
func (r *SofficeRenderer) convertToPDF(ctx context.Context, workDir, input string) (string, error) {
	outDir := workDir
	args := []string{"--headless", "--convert-to", "pdf", "--outdir", outDir, input}
	env := append(os.Environ(), "HOME="+workDir)
	if r.windows {
		outDir = toWinPath(workDir)
		winInput := toWinPath(input)
		profile := "file:///" + strings.ReplaceAll(toWinPath(filepath.Join(workDir, "lo-profile")), "\\", "/")
		args = []string{"--headless", "--convert-to", "pdf", "--outdir", outDir, winInput,
			"-env:UserInstallation=" + profile}
		env = append(os.Environ(), "HOME="+toWinPath(workDir))
	}
	cmd := exec.CommandContext(ctx, r.soffice, args...)
	// LibreOffice 可能写 HOME 配置；隔离到工作目录，避免污染用户配置。
	cmd.Env = env
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

// toWinPath 把 /mnt/<drive>/<rest> 转换为 <drive>:\<rest>，供 Windows 可执行文件使用。
func toWinPath(p string) string {
	if len(p) < 6 || p[:5] != "/mnt/" {
		return p
	}
	return strings.ToUpper(p[5:6]) + ":\\" + strings.ReplaceAll(p[7:], "/", "\\")
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
