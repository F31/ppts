// Command render-perf 是 M5 的性能门禁：用**真实渲染服务 + 真实 ffmpeg**跑四个场景，
// 把「二次渲染 ≤10s / 命中率 ≥70% / P95 ≤120s」变成三个可复跑的数。
//
// 为什么不做成 Go 单测：它要 Chrome、要 ffmpeg、要几分钟。放进 `go test ./...`
// 会让每次提交都背上这些依赖，最后的结果一定是有人给它加 `-short` 跳过——
// 那和没写一样。做成命令并像 share-verify 一样给退出码，才是「发布前跑一次」的形态。
//
// 判据（任一不过则退出码 1）：
//
//	cold     P95 ≤ 120s：30 秒 1080p30 正片的冷渲染墙钟（含拼接）
//	second   全命中二次渲染 ≤ 10s
//	edit     改 1 页后命中率 ≥ 70%
//	parity   冷渲染 / 全命中 / 关闭缓存 三条路径的逐页片段逐字节相同
//	parallel 页级并行与串行的逐页片段逐字节相同（并行不改产物）
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/render"
)

type options struct {
	pages            int
	seconds          int
	runs             int
	width            int
	height           int
	fps              int
	pageConcurrency  int
	frameConcurrency int
	editPage         int
	scenario         string
	chrome           string
	script           string
	json             bool
	keep             bool
	verbose          bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "render-perf: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var opt options
	flag.IntVar(&opt.pages, "pages", 10, "夹具页数")
	flag.IntVar(&opt.seconds, "seconds", 3, "每页时长（秒）")
	flag.IntVar(&opt.runs, "runs", 5, "冷渲染样本数（P95 用）")
	flag.IntVar(&opt.width, "width", 1920, "画布宽")
	flag.IntVar(&opt.height, "height", 1080, "画布高")
	flag.IntVar(&opt.fps, "fps", 30, "帧率")
	flag.IntVar(&opt.pageConcurrency, "page-concurrency", 1, "页级并发（串行 = 1）")
	flag.IntVar(&opt.frameConcurrency, "frame-concurrency", 2, "帧并发（Remotion concurrency）")
	flag.IntVar(&opt.editPage, "edit-page", 5, "edit 场景改哪一页（0 基）")
	flag.StringVar(&opt.scenario, "scenario", "all", "all | cold | second | edit | parity | parallel")
	flag.StringVar(&opt.chrome, "chrome", os.Getenv("PPTS_CHROME"), "chrome-headless-shell 路径")
	flag.StringVar(&opt.script, "renderer-script", filepath.Join("renderer", "render.mjs"), "render.mjs 路径")
	flag.BoolVar(&opt.json, "json", false, "输出 JSON（默认人读表格）")
	flag.BoolVar(&opt.keep, "keep", false, "保留临时目录（排查用）")
	flag.BoolVar(&opt.verbose, "v", false, "打印渲染器日志")
	flag.Parse()

	if opt.pages < 2 {
		return errors.New("-pages 至少 2（需要一页作为转场的前置页）")
	}
	// 改一页会脏两页（自身 + 其后继的转场画它的末帧），所以命中率上限是 (n-2)/n：
	// 3 页片最多 66.7%，永远过不了 70%。判据只对「像真片」的页数成立，
	// 拿 3 页跑步数只会得到一条注定失败的红色——那不是判据，是算式。
	if opt.scenario == "all" || opt.scenario == "edit" {
		if opt.pages < 7 {
			return fmt.Errorf("命中率判据需要 ≥7 页（改 1 页脏 2 页：%d 页片的上限是 %.1f%%）",
				opt.pages, float64(opt.pages-2)/float64(opt.pages)*100)
		}
	}
	if opt.editPage <= 0 || opt.editPage >= opt.pages {
		return fmt.Errorf("-edit-page 必须在 1..%d（第 0 页没有转场）", opt.pages-1)
	}
	script, err := filepath.Abs(opt.script)
	if err != nil {
		return err
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("渲染脚本 %q 不存在：请在仓库根目录运行，或用 -renderer-script 指定", opt.script)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("找不到 ffmpeg（拼接需要）：%w", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return fmt.Errorf("找不到 ffprobe（校验需要）：%w", err)
	}
	if opt.chrome == "" {
		return errors.New("未指定 -chrome：设置 PPTS_CHROME 或显式给 -chrome")
	}

	root, err := os.MkdirTemp("", "ppts-perf-*")
	if err != nil {
		return err
	}
	if opt.keep {
		fmt.Fprintf(os.Stderr, "keep: %s\n", root)
	} else {
		defer os.RemoveAll(root)
	}

	assets := filepath.Join(root, "assets")
	plan, err := buildFixture(assets, opt)
	if err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("夹具计划不合法：%w", err)
	}

	res := &report{Options: opt, Gates: []gate{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	want := func(name string) bool { return opt.scenario == "all" || opt.scenario == name }

	// 基准冷渲染：所有其它场景都以它为准。
	var base *runResult
	if want("cold") || want("second") || want("edit") || want("parity") || want("parallel") {
		base, err = renderOnce(ctx, opt, root, assets, script, plan, "cold-0", filepath.Join(root, "cache-shared"), true)
		if err != nil {
			return fmt.Errorf("冷渲染失败：%w", err)
		}
		res.Cold = append(res.Cold, base)
	}

	if want("cold") {
		for i := 1; i < opt.runs; i++ {
			r, err := renderOnce(ctx, opt, root, assets, script, plan, fmt.Sprintf("cold-%d", i),
				filepath.Join(root, fmt.Sprintf("cache-cold-%d", i)), true)
			if err != nil {
				return fmt.Errorf("第 %d 次冷渲染失败：%w", i+1, err)
			}
			res.Cold = append(res.Cold, r)
		}
		p95 := percentile(durations(res.Cold), 95)
		res.P95Seconds = p95
		res.addGate("cold", fmt.Sprintf("P95 ≤ 120s（%d 次样本，P95=%.1fs）", len(res.Cold), p95), p95 <= 120)
	}

	if want("second") {
		// 同一个缓存目录、新的 Service 实例 = 进程重启后的第二次导出。
		r, err := renderOnce(ctx, opt, root, assets, script, plan, "second", filepath.Join(root, "cache-shared"), false)
		if err != nil {
			return fmt.Errorf("全命中渲染失败：%w", err)
		}
		res.Second = r
		res.addGate("second", fmt.Sprintf("全命中二次渲染 ≤ 10s（实测 %.2fs）", r.TotalSeconds()), r.TotalSeconds() <= 10)
		res.addGate("second-render", "二次渲染没有调用渲染器（命中页数 = 总页数）", r.Status.CachedPages == opt.pages && r.Status.RenderedPages == 0)
	}

	if want("parity") {
		// 关掉缓存再渲一次：证明「命中产物 == 冷渲染产物」，而不是「缓存抄了自己」。
		r, err := renderOnceNoCache(ctx, opt, root, assets, script, plan, "nocache")
		if err != nil {
			return fmt.Errorf("对照渲染失败：%w", err)
		}
		res.NoCache = r
		same, diffs := compareHashes(base.Hashes, r.Hashes)
		res.addGate("parity", fmt.Sprintf("冷渲染与关闭缓存逐页片段 SHA-256 相同（%d 页）", len(base.Hashes)), same)
		if !same {
			res.Notes = append(res.Notes, "差异页："+strings.Join(diffs, ", "))
		}
		if res.Second != nil {
			same2, diffs2 := compareHashes(base.Hashes, res.Second.Hashes)
			res.addGate("parity-hit", "命中产物与冷渲染逐页片段 SHA-256 相同", same2)
			if !same2 {
				res.Notes = append(res.Notes, "命中差异页："+strings.Join(diffs2, ", "))
			}
			res.addGate("parity-film", "成片时长与帧数一致（命中 vs 冷渲染）",
				almostEqual(base.FilmDuration, res.Second.FilmDuration, 0.05) && base.FilmFrames == res.Second.FilmFrames)
		}
	}

	if want("edit") {
		edited := clonePlan(plan)
		// 改一页的动画时长：这是「改文案」在 IR 层最接近的等价物（页面内容变 → 片段键变）。
		// 时长必须落在页内，否则计划校验会拒绝（那测的就不是缓存了）。
		clip := &edited.Pages[opt.editPage].Layers[0].Clips[0]
		clip.DurationMs = 1200
		if page := edited.Pages[opt.editPage].DurationMs; clip.StartMs+clip.DurationMs >= page {
			clip.DurationMs = page / 2
		}
		r, err := renderOnce(ctx, opt, root, assets, script, edited, "edit", filepath.Join(root, "cache-shared"), false)
		if err != nil {
			return fmt.Errorf("编辑后渲染失败：%w", err)
		}
		res.Edit = r
		// 增量渲的成片必须等于「同一份编辑后计划」的冷渲染：命中页来自缓存、
		// 脏页是现渲的，但成片里每一页（含转场那几帧）都该和全量重渲一模一样。
		// 这条比「缓存字节相同」更强——它能抓到「少 staging 了前一页」这类错。
		coldEdit, err := renderOnceNoCache(ctx, opt, root, assets, script, edited, "edit-nocache")
		if err != nil {
			return fmt.Errorf("编辑后冷渲染失败：%w", err)
		}
		res.EditNoCache = coldEdit
		sameEdit, diffsEdit := compareHashes(r.Hashes, coldEdit.Hashes)
		res.addGate("edit-parity", "增量渲的逐页片段与「同一份新计划」的冷渲染相同", sameEdit)
		if !sameEdit {
			res.Notes = append(res.Notes, "增量渲差异页："+strings.Join(diffsEdit, ", "))
		}
		rate := r.HitRate()
		res.EditHitRate = rate
		res.addGate("edit", fmt.Sprintf("改 1 页后命中率 ≥ 70%%（%d/%d = %.1f%%）",
			r.Status.CachedPages, opt.pages, rate*100), rate >= 0.70)
	}

	// all 场景不带页级并行（它需要显式指定并发度）；要测就单独跑 -scenario parallel。
	if want("parallel") && opt.pageConcurrency <= 1 && opt.scenario == "parallel" {
		return errors.New("parallel 场景需要 -page-concurrency > 1")
	}
	if want("parallel") && opt.pageConcurrency > 1 {
		// 对照组必须是同一次调用里的串行冷渲染：跨调用比较会把机器负载差异
		// 算进「并行的收益」里。
		serial := opt
		serial.pageConcurrency = 1
		sres, err := renderOnce(ctx, serial, root, assets, script, plan, "serial", filepath.Join(root, "cache-serial"), true)
		if err != nil {
			return fmt.Errorf("串行对照渲染失败：%w", err)
		}
		r, err := renderOnce(ctx, opt, root, assets, script, plan, "parallel", filepath.Join(root, "cache-parallel"), true)
		if err != nil {
			return fmt.Errorf("并行渲染失败：%w", err)
		}
		res.Serial = sres
		res.Parallel = r
		same, diffs := compareHashes(sres.Hashes, r.Hashes)
		res.addGate("parallel", fmt.Sprintf("页级并行（%d）与串行逐页片段相同", opt.pageConcurrency), same)
		if !same {
			res.Notes = append(res.Notes, "并行差异页："+strings.Join(diffs, ", "))
		}
		res.Notes = append(res.Notes, fmt.Sprintf("页级并行墙钟：串行 %.1fs → 并行 %.1fs（%.2fx）",
			sres.TotalSeconds(), r.TotalSeconds(), sres.TotalSeconds()/maxFloat(r.TotalSeconds(), 0.001)))
	}

	if opt.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		res.print(os.Stdout)
	}
	if !res.OK() {
		return fmt.Errorf("%d 条判据未通过", res.failedGates())
	}
	return nil
}

// ---------------------------------------------------------------- 夹具

// buildFixture 造一个「像真片」的计划：每页一张 1080p 底图 + 一条元素动画 + 页间转场。
//
// 为什么不用纯色页：纯色让 x264 几乎不干活，测出来的耗时是假的（ADR-16 §2 已经踩过）。
// 底图里放渐变与网格，让编码器有真实的残差要压。
func buildFixture(assets string, opt options) (*render.Timeline, error) {
	if err := os.MkdirAll(filepath.Join(assets, "pages"), 0o750); err != nil {
		return nil, err
	}
	pages := make([]render.Page, opt.pages)
	for i := 0; i < opt.pages; i++ {
		img := pageImage(opt.width, opt.height, i, opt.pages)
		path := filepath.Join(assets, "pages", fmt.Sprintf("p%03d.png", i))
		if err := writePNG(path, img); err != nil {
			return nil, err
		}
		pages[i] = render.Page{
			Index:      i,
			SlideID:    fmt.Sprintf("slide-%03d", i),
			DurationMs: int64(opt.seconds) * 1000,
			BaseImage:  fmt.Sprintf("pages/p%03d.png", i),
			Layers: []render.Layer{{
				ElementID: fmt.Sprintf("e%d", i),
				Kind:      animation.KindTitle,
				AssetMode: render.AssetBitmapRegion,
				Box: render.Box{
					X: opt.width / 16, Y: opt.height / 8,
					W: opt.width * 9 / 16, H: opt.height / 4,
				},
				Clips: []render.Motion{{
					ClipID:     fmt.Sprintf("c%d", i),
					Effect:     "fadeIn",
					StartMs:    200,
					DurationMs: 600,
					Easing:     animation.EasingCurves["easeOut"],
					Props: []animation.MotionProp{
						{Name: animation.PropOpacity, From: 0, To: 1},
						{Name: animation.PropTranslateY, From: 24, To: 0},
					},
				}},
			}},
		}
		pages[i].Transition = render.Transition{Type: animation.TransitionNone}
		if i > 0 {
			pages[i].Transition = render.Transition{Type: animation.TransitionFade, DurationMs: 300}
		}
	}
	return &render.Timeline{
		SchemaVersion: render.IRSchemaVersion,
		FPS:           opt.fps,
		Width:         opt.width,
		Height:        opt.height,
		Background:    "#101725",
		Pages:         pages,
	}, nil
}

// pageImage 画一张有纹理的页图：竖向渐变 + 网格线 + 一个高亮块。
func pageImage(w, h, index, total int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 每页基色不同：让「换页」在片段字节上真的可见（否则所有页渲出同一份文件，
	// 串页的缺陷会被「哈希相同」掩盖）。
	base := [3]uint8{uint8(30 + index*15), uint8(40 + (index*9)%90), uint8(90 + (index*7)%120)}
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h)
		for x := 0; x < w; x++ {
			// 网格线：每 64px 一条，给编码器制造高频残差。
			line := uint8(0)
			if x%64 == 0 || y%64 == 0 {
				line = 26
			}
			img.SetRGBA(x, y, color.RGBA{
				R: clamp(base[0] + uint8(30*t) + line),
				G: clamp(base[1] + uint8(20*t) + line),
				B: clamp(base[2] + uint8(10*t) + line),
				A: 255,
			})
		}
	}
	// 高亮块：让画面有明确的对比区域（纯渐变的压缩率过高）。
	block := color.RGBA{R: 240, G: 235, B: 220, A: 255}
	for y := 140; y < 400; y++ {
		for x := 120; x < 1020; x++ {
			img.SetRGBA(x, y, block)
		}
	}
	for y := 520; y < h-120; y += 1 {
		for x := 120; x < w-120; x++ {
			if (x/40+y/40)%2 == 0 {
				img.SetRGBA(x, y, color.RGBA{R: uint8(base[0] + 20), G: uint8(base[1] + 20), B: uint8(base[2] + 24), A: 255})
			}
		}
	}
	_ = total
	return img
}

func clamp(v uint8) uint8 {
	if v > 245 {
		return 245
	}
	return v
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	encErr := png.Encode(f, img)
	closeErr := f.Close()
	if encErr != nil {
		return encErr
	}
	return closeErr
}

func clonePlan(in *render.Timeline) *render.Timeline {
	out := *in
	out.Pages = make([]render.Page, len(in.Pages))
	copy(out.Pages, in.Pages)
	for i := range out.Pages {
		out.Pages[i].Layers = append([]render.Layer(nil), in.Pages[i].Layers...)
		for li := range out.Pages[i].Layers {
			out.Pages[i].Layers[li].Clips = append([]render.Motion(nil), in.Pages[i].Layers[li].Clips...)
		}
	}
	return &out
}

// ---------------------------------------------------------------- 单次渲染

type runResult struct {
	Name          string           `json:"name"`
	RenderSeconds float64          `json:"renderSeconds"`
	ConcatSeconds float64          `json:"concatSeconds"`
	CachedPages   int              `json:"cachedPages"`
	RenderedPages int              `json:"renderedPages"`
	FilmDuration  float64          `json:"filmDurationSeconds"`
	FilmFrames    int              `json:"filmFrames"`
	FilmSHA256    string           `json:"filmSha256"`
	Hashes        map[int]string   `json:"segmentSha256"`
	Status        render.JobStatus `json:"-"`
	cacheDir      string
	workDir       string
}

func (r *runResult) TotalSeconds() float64 { return r.RenderSeconds + r.ConcatSeconds }

func (r *runResult) HitRate() float64 {
	total := r.CachedPages + r.RenderedPages
	if total == 0 {
		return 0
	}
	return float64(r.CachedPages) / float64(total)
}

func renderOnce(ctx context.Context, opt options, root, assets, script string, plan *render.Timeline,
	name, cacheDir string, freshCache bool) (*runResult, error) {
	return renderWith(ctx, opt, root, assets, script, plan, name, cacheDir, freshCache)
}

func renderOnceNoCache(ctx context.Context, opt options, root, assets, script string, plan *render.Timeline, name string) (*runResult, error) {
	return renderWith(ctx, opt, root, assets, script, plan, name, "", false)
}

// renderWith 起一个新的 Service（= 新进程），渲染一次并拼接成片。
// freshCache 为 true 时清空缓存目录，模拟「第一次导出」。
func renderWith(ctx context.Context, opt options, root, assets, script string, plan *render.Timeline,
	name, cacheDir string, freshCache bool) (*runResult, error) {
	workDir := filepath.Join(root, "work-"+name)
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return nil, err
	}
	var cache render.SegmentCache
	if cacheDir != "" {
		if freshCache {
			if err := os.RemoveAll(cacheDir); err != nil {
				return nil, err
			}
		}
		c, err := render.NewDirSegmentCache(cacheDir, 0)
		if err != nil {
			return nil, err
		}
		cache = c
	}

	logf := func(string, ...any) {}
	if opt.verbose {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, "[renderer] "+format+"\n", args...) }
	}
	backend := &render.RemotionBackend{
		Script:          script,
		Chrome:          opt.chrome,
		Concurrency:     opt.frameConcurrency,
		PageConcurrency: opt.pageConcurrency,
		Timeout:         30 * time.Minute,
		Logf:            logf,
	}
	svc, err := render.NewService(render.ServiceConfig{
		Backend:       backend,
		WorkDir:       workDir,
		AssetDir:      assets,
		Version:       "perf-1",
		SegmentCache:  cache,
		MaxConcurrent: 1,
		Logf:          logf,
	})
	if err != nil {
		return nil, err
	}

	start := time.Now()
	id, err := svc.Submit(plan)
	if err != nil {
		return nil, fmt.Errorf("submit: %w", err)
	}
	st, err := waitJob(ctx, svc, id)
	if err != nil {
		return nil, err
	}
	if st.State != render.StateSucceeded {
		return nil, fmt.Errorf("渲染未成功：%s (%s)", st.State, st.Error)
	}
	renderElapsed := time.Since(start)

	out := filepath.Join(root, name+".mp4")
	segments := make([]media.VideoSegment, len(plan.Pages))
	hashes := make(map[int]string, len(plan.Pages))
	for i := range plan.Pages {
		p := filepath.Join(workDir, st.ID, render.SegmentName(i))
		segments[i] = media.VideoSegment{Path: p, DurationMS: plan.Pages[i].DurationMs}
		h, err := fileSHA(p)
		if err != nil {
			return nil, fmt.Errorf("片段 %d 缺失：%w", i, err)
		}
		hashes[i] = h
	}
	enc, err := media.NewMP4Encoder()
	if err != nil {
		return nil, err
	}
	concatStart := time.Now()
	cr, err := enc.ConcatSegments(ctx, media.ConcatOptions{
		Segments: segments,
		FPS:      plan.FPS,
		Width:    plan.Width,
		Height:   plan.Height,
		OutPath:  out,
	})
	if err != nil {
		return nil, fmt.Errorf("拼接：%w", err)
	}
	concatElapsed := time.Since(concatStart)
	filmSHA, _ := fileSHA(out)

	return &runResult{
		Name:          name,
		RenderSeconds: renderElapsed.Seconds(),
		ConcatSeconds: concatElapsed.Seconds(),
		CachedPages:   st.CachedPages,
		RenderedPages: st.RenderedPages,
		FilmDuration:  cr.Duration,
		FilmFrames:    int(cr.Duration*float64(plan.FPS) + 0.5),
		FilmSHA256:    filmSHA,
		Hashes:        hashes,
		Status:        st,
		cacheDir:      cacheDir,
		workDir:       workDir,
	}, nil
}

func waitJob(ctx context.Context, svc *render.Service, id string) (render.JobStatus, error) {
	for {
		st, ok := svc.Status(id)
		if !ok {
			return render.JobStatus{}, fmt.Errorf("job %s 消失", id)
		}
		switch st.State {
		case render.StateSucceeded, render.StateFailed, render.StateCanceled:
			return st, nil
		}
		select {
		case <-ctx.Done():
			return render.JobStatus{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func fileSHA(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ---------------------------------------------------------------- 报告

type gate struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Pass   bool   `json:"pass"`
}

type report struct {
	Options     options      `json:"options"`
	Cold        []*runResult `json:"cold"`
	Second      *runResult   `json:"second,omitempty"`
	NoCache     *runResult   `json:"noCache,omitempty"`
	Edit        *runResult   `json:"edit,omitempty"`
	EditNoCache *runResult   `json:"editNoCache,omitempty"`
	Parallel    *runResult   `json:"parallel,omitempty"`
	Serial      *runResult   `json:"serial,omitempty"`
	P95Seconds  float64      `json:"p95Seconds"`
	EditHitRate float64      `json:"editHitRate"`
	Gates       []gate       `json:"gates"`
	Notes       []string     `json:"notes,omitempty"`
}

func (r *report) addGate(name, detail string, pass bool) {
	r.Gates = append(r.Gates, gate{Name: name, Detail: detail, Pass: pass})
}

func (r *report) OK() bool {
	for _, g := range r.Gates {
		if !g.Pass {
			return false
		}
	}
	return true
}

func (r *report) failedGates() int {
	n := 0
	for _, g := range r.Gates {
		if !g.Pass {
			n++
		}
	}
	return n
}

func (r *report) print(w *os.File) {
	fmt.Fprintf(w, "夹具：%d 页 × %ds @ %dx%d/%dfps，页并发 %d，帧并发 %d\n\n",
		r.Options.pages, r.Options.seconds, r.Options.width, r.Options.height, r.Options.fps,
		r.Options.pageConcurrency, r.Options.frameConcurrency)
	rows := [][5]string{{"场景", "渲染", "拼接", "命中/实渲", "成片"}}
	add := func(name string, res *runResult) {
		if res == nil {
			return
		}
		rows = append(rows, [5]string{
			name,
			fmt.Sprintf("%.1fs", res.RenderSeconds),
			fmt.Sprintf("%.2fs", res.ConcatSeconds),
			fmt.Sprintf("%d/%d", res.CachedPages, res.RenderedPages),
			fmt.Sprintf("%.2fs", res.FilmDuration),
		})
	}
	for i, c := range r.Cold {
		add(fmt.Sprintf("冷渲染 #%d", i+1), c)
	}
	add("全命中", r.Second)
	add("关闭缓存", r.NoCache)
	add("改 1 页（增量）", r.Edit)
	add("改 1 页（冷渲）", r.EditNoCache)
	add("串行对照", r.Serial)
	add("页级并行", r.Parallel)
	printTable(w, rows)
	if len(r.Notes) > 0 {
		fmt.Fprintf(w, "\n备注：\n")
		for _, n := range r.Notes {
			fmt.Fprintf(w, "  - %s\n", n)
		}
	}
	fmt.Fprintf(w, "\n判据：\n")
	failed := 0
	for _, g := range r.Gates {
		mark := "PASS"
		if !g.Pass {
			mark = "FAIL"
			failed++
		}
		fmt.Fprintf(w, "  [%s] %-14s %s\n", mark, g.Name, g.Detail)
	}
	if failed == 0 {
		fmt.Fprintf(w, "\n全部 %d 条判据通过。\n", len(r.Gates))
	}
}

func printTable(w *os.File, rows [][5]string) {
	widths := [5]int{}
	for _, row := range rows {
		for i, cell := range row {
			if n := displayWidth(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// displayWidth 按「东亚宽字符占两列」估算，够用于本工具的输出对齐。
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		if r > 0x2E80 {
			n += 2
			continue
		}
		n++
	}
	return n
}

func durations(rs []*runResult) []float64 {
	out := make([]float64, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.TotalSeconds())
	}
	return out
}

// percentile 用最近秩法（nearest-rank）：样本少时不做插值，
// 「5 次里最慢的一次」比一个插出来的小数更能说明问题。
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	rank := int(float64(len(sorted))*p/100 + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func compareHashes(a, b map[int]string) (bool, []string) {
	var diffs []string
	if len(a) != len(b) {
		return false, []string{fmt.Sprintf("页数 %d vs %d", len(a), len(b))}
	}
	for k, v := range a {
		if b[k] != v {
			diffs = append(diffs, fmt.Sprintf("%d", k))
		}
	}
	sort.Strings(diffs)
	return len(diffs) == 0, diffs
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func almostEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}
