package render

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RemotionBackend 用 Remotion（Chromium 逐帧）产出逐页 H.264 片段。
//
// 它承担两件事：
//  1. staging：把「IR + 已就位底图」翻译成合成器能读的目录——生成去残影底板、
//     写 plan.json；
//  2. exec：调 `node render.mjs`，按 JSON 行解析进度，校验产物。
//
// staging 放在 backend 内是刻意的：它是渲染实现的一部分，不是编排契约。
// 编排层只知道「给我一个目录，里面是片段」。
type RemotionBackend struct {
	// Script 是 renderer/render.mjs 的路径。空则取 PPTS_RENDERER_SCRIPT。
	Script string
	// NodeBin 默认 node，可用 PPTS_NODE 覆盖。
	NodeBin string
	// Chrome 是 chrome-headless-shell 路径。空则取 PPTS_CHROME；再空则交给 Remotion 自寻。
	Chrome string
	// Concurrency 是 Remotion 的帧渲染并发（0 = 2）。
	Concurrency int
	// CRF 是 x264 质量（0 = 18）。
	CRF int
	// GOP 是固定关键帧间隔（0 = 30）。拼接依赖它，不能交给 x264 自适应。
	GOP int
	// Timeout 是整次渲染的上限（0 = 15min）。
	Timeout time.Duration
	// Logf 用于记录 staging 诊断（可为 nil）。
	Logf func(format string, args ...any)

	chromeOnce    sync.Once
	chromeVersion string
}

// Name 出现在 /healthz 与日志里。
func (b *RemotionBackend) Name() string { return "remotion" }

// ChromeVersion 返回浏览器版本（用于 /healthz 与漏洞台账）。
// 探测失败返回空串——版本缺失不该让整个服务起不来。
func (b *RemotionBackend) ChromeVersion() string {
	b.chromeOnce.Do(func() {
		chrome := b.resolveChrome()
		if chrome == "" {
			return
		}
		out, err := exec.Command(chrome, "--version").Output()
		if err != nil {
			return
		}
		b.chromeVersion = strings.TrimSpace(string(out))
	})
	return b.chromeVersion
}

// Render 实现 Backend。产物是 dir/segment_000.mp4 起的逐页片段。
func (b *RemotionBackend) Render(ctx context.Context, plan *Timeline, dir string, progress func(done, total int)) error {
	if plan == nil || len(plan.Pages) == 0 {
		return errors.New("render: remotion backend requires a plan with at least one page")
	}
	script := b.resolveScript()
	if script == "" {
		return errors.New("render: renderer script not configured (set PPTS_RENDERER_SCRIPT)")
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("render: renderer script %q: %w", script, err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("render: prepare workdir: %w", err)
	}
	// 后续路径全部用绝对路径：node 子进程的 cwd 是脚本目录，
	// 相对路径会按那个目录解析，最终报「找不到 plan.json」这种误导性错误。
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("render: resolve workdir: %w", err)
	}
	dir = absDir

	staged, err := b.stage(plan, dir)
	if err != nil {
		return err
	}

	planPath := filepath.Join(dir, "plan.json")
	payload, err := json.Marshal(staged)
	if err != nil {
		return fmt.Errorf("render: marshal staged plan: %w", err)
	}
	if err := os.WriteFile(planPath, payload, 0o640); err != nil {
		return fmt.Errorf("render: write staged plan: %w", err)
	}

	if err := b.exec(ctx, script, planPath, dir, len(plan.Pages), progress); err != nil {
		return err
	}

	// 产物校验：退出码 0 不等于有可播放的片段。缺文件或没有 ftyp box 都要报出来，
	// 否则上游会拿着空片段去拼接，最后表现成「成片黑屏」。
	for i := range plan.Pages {
		name := fmt.Sprintf("segment_%03d.mp4", i)
		path := filepath.Join(dir, name)
		if err := verifyMP4(path); err != nil {
			return fmt.Errorf("render: page %d: %w", i, err)
		}
	}
	return nil
}

// stage 生成底板并返回一份副本（不修改调用方的 plan：服务端可能还要用它算别的）。
func (b *RemotionBackend) stage(plan *Timeline, dir string) (*Timeline, error) {
	staged := *plan
	staged.Pages = append([]Page(nil), plan.Pages...)

	plateDir := filepath.Join(dir, "plates")
	needPlates := false
	for _, page := range staged.Pages {
		for _, layer := range page.Layers {
			if layer.ElementID != "" {
				needPlates = true
				break
			}
		}
		if needPlates {
			break
		}
	}
	if !needPlates {
		return &staged, nil
	}
	if err := os.MkdirAll(plateDir, 0o750); err != nil {
		return nil, fmt.Errorf("render: prepare plate dir: %w", err)
	}

	for i := range staged.Pages {
		page := &staged.Pages[i]
		hasElementLayer := false
		for _, layer := range page.Layers {
			if layer.ElementID != "" {
				hasElementLayer = true
				break
			}
		}
		if !hasElementLayer {
			continue
		}
		basePath, err := workPath(dir, page.BaseImage)
		if err != nil {
			return nil, fmt.Errorf("render: page %d base image: %w", i, err)
		}
		base, err := readImage(basePath)
		if err != nil {
			return nil, fmt.Errorf("render: page %d base image: %w", i, err)
		}
		plate, patches := BuildPlate(base, page.Layers, plan.Width, plan.Height)

		name := fmt.Sprintf("plates/plate_%03d.png", i)
		out, err := os.Create(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("render: page %d plate: %w", i, err)
		}
		encodeErr := png.Encode(out, plate)
		closeErr := out.Close()
		if encodeErr != nil {
			return nil, fmt.Errorf("render: page %d plate encode: %w", i, encodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("render: page %d plate close: %w", i, closeErr)
		}
		page.PlateImage = name

		for _, patch := range patches {
			if patch.Noisy && b.Logf != nil {
				b.Logf("render: page %d element %s: plate patch may be visible (spread=%d)",
					i, patch.ElementID, patch.Spread)
			}
		}
	}
	return &staged, nil
}

func (b *RemotionBackend) exec(ctx context.Context, script, planPath, outDir string, pages int, progress func(done, total int)) error {
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	concurrency := b.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	crf := b.CRF
	if crf <= 0 {
		crf = 18
	}
	gop := b.GOP
	if gop <= 0 {
		gop = 30
	}

	// 脚本路径转成绝对路径：cmd.Dir 会被设成脚本所在目录（node_modules 在那），
	// 若脚本本身还是相对路径，Node 会按 cmd.Dir 再拼一次，变成 renderer/renderer/render.mjs。
	absScript, err := filepath.Abs(script)
	if err != nil {
		return fmt.Errorf("render: resolve renderer script: %w", err)
	}

	args := []string{
		absScript,
		"--plan", planPath,
		"--out-dir", outDir,
		"--concurrency", strconv.Itoa(concurrency),
		"--crf", strconv.Itoa(crf),
		"--gop", strconv.Itoa(gop),
	}
	cmd := exec.CommandContext(ctx, b.resolveNode(), args...)
	cmd.Dir = filepath.Dir(absScript)
	cmd.Env = os.Environ()
	if chrome := b.resolveChrome(); chrome != "" {
		cmd.Env = append(cmd.Env, "PPTS_CHROME="+chrome)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("render: pipe stdout: %w", err)
	}
	stderr := &limitedBuffer{limit: 8 << 10}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("render: start renderer (%s): %w", b.resolveNode(), err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var reported error
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Page    int    `json:"page"`
			File    string `json:"file"`
			Frames  int    `json:"frames"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// 非 JSON 行（例如 Node 的弃用警告）忽略，不让它打断渲染。
			continue
		}
		switch event.Type {
		case "page":
			if progress != nil {
				progress(event.Page+1, pages)
			}
		case "error":
			reported = errors.New(event.Message)
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()

	if waitErr != nil {
		if reported != nil {
			return fmt.Errorf("render: %w", reported)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("render: timed out after %s: %w", timeout, waitErr)
		}
		return fmt.Errorf("render: renderer exited: %w: %s", waitErr, stderr.String())
	}
	if scanErr != nil {
		return fmt.Errorf("render: read renderer output: %w", scanErr)
	}
	if reported != nil {
		return fmt.Errorf("render: %w", reported)
	}
	return nil
}

func (b *RemotionBackend) resolveScript() string {
	if b.Script != "" {
		return b.Script
	}
	return os.Getenv("PPTS_RENDERER_SCRIPT")
}

func (b *RemotionBackend) resolveNode() string {
	if b.NodeBin != "" {
		return b.NodeBin
	}
	if env := os.Getenv("PPTS_NODE"); env != "" {
		return env
	}
	return "node"
}

func (b *RemotionBackend) resolveChrome() string {
	if b.Chrome != "" {
		return b.Chrome
	}
	return os.Getenv("PPTS_CHROME")
}

// workPath 把「相对工作目录的路径」解析成绝对路径，并拒绝逃逸。
//
// IR 校验已经拦过一次（SafeAssetPath）；这里再拦一次是因为 dir 由调用方给，
// 而 backend 是最后一道能接触到文件系统的关口。
func workPath(dir, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("absolute path not allowed: %q", rel)
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workdir: %q", rel)
	}
	full := filepath.Join(dir, cleaned)
	if !strings.HasPrefix(full, filepath.Clean(dir)+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workdir: %q", rel)
	}
	return full, nil
}

func readImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %q: %w", filepath.Base(path), err)
	}
	return img, nil
}

// verifyMP4 只做最基本的容器校验：文件存在、非空、第 4–8 字节是 ftyp。
// 不做时长校验——那需要解析 moov，而时长契约由 render.mjs 的帧数保证。
func verifyMP4(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("segment missing: %w", err)
	}
	defer f.Close()
	header := make([]byte, 12)
	if _, err := io.ReadFull(f, header); err != nil {
		return fmt.Errorf("segment truncated: %w", err)
	}
	if !bytes.Equal(header[4:8], []byte("ftyp")) {
		return errors.New("segment is not an mp4 container")
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < 1024 {
		return fmt.Errorf("segment suspiciously small (%d bytes)", info.Size())
	}
	return nil
}

// limitedBuffer 只保留末尾 N 字节：Node 的报错通常在最末几行，
// 而一个失控的日志不该把内存吃穿。
type limitedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.buf.Write(p)
	if l.buf.Len() > l.limit {
		trimmed := l.buf.Bytes()[l.buf.Len()-l.limit:]
		l.buf.Reset()
		l.buf.Write(trimmed)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return strings.TrimSpace(l.buf.String()) }
