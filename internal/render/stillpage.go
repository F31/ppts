package render

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/animation"
)

// pageNeedsCompositor 报告这一页必须进 Chromium 按页时长逐帧。
//
// 注视页（有 Focus.rect、fade/none、无 shots）走 look 链，不进这里。
// gaze+entrance 的入场层也留在 look 链：中点仍帧已经过完 fadeIn，xfade 承担显现。
// 纯 entrance、无矩形的图层焦点、切镜、slide/zoom、非入场 clip 仍要合成器。
func pageNeedsCompositor(p Page) bool {
	if isGazeLookPage(p) {
		return false
	}
	if len(p.Focus) > 0 || len(p.Shots) > 0 {
		return true
	}
	for _, layer := range p.Layers {
		if len(layer.Clips) > 0 {
			return true
		}
	}
	if tr := effectiveTransition(p.Transition); tr != nil && tr.Type != animation.TransitionFade {
		return true
	}
	return false
}

// isGazeLookPage 是教学注视页：用 look 中点仍帧而不是整页 Chrome。
// 教学页 catalog 的 lookCrop=false，所以 look 链仍是完整页+纱。
// 科技 punch-in 在 PageView 里做，不改这一页的分桶。
// 入场层（fadeIn/slideIn/pop/grow）可以留在链上：staging 挖洞，中点仍帧里已显现的层不透明。
func isGazeLookPage(p Page) bool {
	if len(p.Shots) > 0 {
		return false
	}
	if tr := effectiveTransition(p.Transition); tr != nil && tr.Type != animation.TransitionNone && tr.Type != animation.TransitionFade {
		return false
	}
	if !hasFocusRect(p) {
		return false
	}
	for _, layer := range p.Layers {
		if !lookChainLayerOK(layer) {
			return false
		}
	}
	return true
}

// lookChainLayerOK 允许无 clip 的层，以及只含入场 effect 的元素层。
// 整页层、bob/highlight 等非入场 clip 会把这一页踢回 Chrome。
func lookChainLayerOK(layer Layer) bool {
	if len(layer.Clips) == 0 {
		return true
	}
	if layer.ElementID == "" {
		return false
	}
	for _, clip := range layer.Clips {
		if !animation.IsEntranceEffect(clip.Effect) {
			return false
		}
	}
	return true
}

func hasFocusRect(p Page) bool {
	for _, f := range p.Focus {
		if f.Rect != nil && f.Rect.W > 0 && f.Rect.H > 0 {
			return true
		}
	}
	return false
}

// splitRenderPages 把本次要渲的页分成三桶，各组保持原顺序。
//
// gaze 永不并进 still：encodeStillPages 用单张 BaseImage 铺满，会丢掉纱。
func splitRenderPages(plan *Timeline, pages []int) (chrome, gaze, still []int) {
	for _, i := range pages {
		if i < 0 || i >= len(plan.Pages) {
			chrome = append(chrome, i)
			continue
		}
		p := plan.Pages[i]
		switch {
		case isGazeLookPage(p):
			gaze = append(gaze, i)
		case pageNeedsCompositor(p):
			chrome = append(chrome, i)
		default:
			still = append(still, i)
		}
	}
	return chrome, gaze, still
}

// framesForDuration 与 renderer/render.mjs 的 framesPerPage 同口径：
// max(1, round(durationMs * fps / 1000))。
func framesForDuration(durationMs int64, fps int) int {
	if fps <= 0 {
		fps = 30
	}
	n := int(float64(durationMs)*float64(fps)/1000 + 0.5)
	if n < 1 {
		return 1
	}
	return n
}

// stillEncodeSpec 是一页静止画面的编码输入。PrevImage 为空表示没有叠化。
type stillEncodeSpec struct {
	Width, Height, FPS, CRF, GOP int
	Frames                       int
	TransitionFrames             int
	Background                   string
	Image                        string
	PrevImage                    string
	Out                          string
}

// stillEncodeArgs 构造 ffmpeg 参数（不含二进制路径）。
//
// 产物要能和合成器的片段直接拼接：H.264、yuv420p、bt709 TV、无音轨、
// 关键帧间隔不超过 GOP、画布尺寸与帧数和计划一致。叠化只发生在本页开头，页长不变。
// 淡入按 easeOutCubic 加权，和 shared/motion.ts 的页间过渡同一条曲线。
func stillEncodeArgs(spec stillEncodeSpec) ([]string, error) {
	if spec.Image == "" || spec.Out == "" {
		return nil, fmt.Errorf("render: still page is missing image or output")
	}
	w, h := spec.Width, spec.Height
	if w <= 0 || h <= 0 || w%2 != 0 || h%2 != 0 {
		return nil, fmt.Errorf("render: still page canvas %dx%d must be positive and even", w, h)
	}
	fps := spec.FPS
	if fps <= 0 {
		fps = 30
	}
	frames := spec.Frames
	if frames < 1 {
		frames = 1
	}
	crf := spec.CRF
	if crf <= 0 {
		crf = 18
	}
	gop := spec.GOP
	if gop <= 0 {
		gop = 30
	}
	trans := spec.TransitionFrames
	if spec.PrevImage == "" || trans < 1 {
		trans = 0
	}
	if trans >= frames {
		if frames > 1 {
			trans = frames - 1
		} else {
			trans = 0
		}
	}

	fit := stillFitFilter(w, h, spec.Background, fps)
	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	var filter string
	if trans == 0 {
		args = append(args,
			"-loop", "1", "-framerate", strconv.Itoa(fps),
			"-i", spec.Image,
		)
		filter = "[0:v]" + fit + ",format=yuv420p[v]"
	} else {
		transSec := strconv.FormatFloat(float64(trans)/float64(fps), 'f', 3, 64)
		prevSec := strconv.FormatFloat(float64(trans+1)/float64(fps), 'f', 3, 64)
		pageSec := strconv.FormatFloat(float64(frames)/float64(fps), 'f', 3, 64)
		args = append(args,
			"-loop", "1", "-framerate", strconv.Itoa(fps), "-t", prevSec, "-i", spec.PrevImage,
			"-loop", "1", "-framerate", strconv.Itoa(fps), "-t", pageSec, "-i", spec.Image,
		)
		// easeOutCubic(P) = 1-(1-P)^3。逗号在 filtergraph 里是分隔符，必须转义。
		filter = "[0:v]" + fit + "[a];[1:v]" + fit + "[b];" +
			"[a][b]xfade=transition=custom:duration=" + transSec + ":offset=0:" +
			`expr='A*pow(1-P\,3)+B*(1-pow(1-P\,3))',format=yuv420p[v]`
	}
	args = append(args,
		"-filter_complex", filter,
		"-map", "[v]",
		"-frames:v", strconv.Itoa(frames),
		"-r", strconv.Itoa(fps),
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-crf", strconv.Itoa(crf), "-preset", "medium",
		"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0",
		"-an",
		"-movflags", "+faststart",
	)
	args = append(args, colorTagArgs()...)
	args = append(args, spec.Out)
	return args, nil
}

// colorTagArgs 是 Chrome 片段和 ffmpeg 片段共用的色彩标签。
// 缺了它们，两边的默认值一个是 TV、一个是 PC，拼接处渐变会发灰。
func colorTagArgs() []string {
	return []string{
		"-colorspace", "bt709",
		"-color_primaries", "bt709",
		"-color_trc", "bt709",
		"-color_range", "tv",
	}
}

// holdTailSpec 把 Chrome 画完的运动前缀铺成整页时长。
// 最后一帧被克隆，所以静止段的第一帧就是运动段的最后一帧，中间不插黑帧。
type holdTailSpec struct {
	MotionFrames int
	TotalFrames  int
	FPS          int
	CRF          int
	GOP          int
	In           string
	Out          string
}

func holdTailArgs(spec holdTailSpec) ([]string, error) {
	if spec.In == "" || spec.Out == "" {
		return nil, fmt.Errorf("render: hold tail is missing input or output")
	}
	if spec.MotionFrames < 1 || spec.TotalFrames <= spec.MotionFrames {
		return nil, fmt.Errorf("render: hold tail frames %d/%d", spec.MotionFrames, spec.TotalFrames)
	}
	fps := spec.FPS
	if fps <= 0 {
		fps = 30
	}
	crf := spec.CRF
	if crf <= 0 {
		crf = 18
	}
	gop := spec.GOP
	if gop <= 0 {
		gop = 30
	}
	tail := spec.TotalFrames - spec.MotionFrames
	// 多留一帧再由 -frames:v 截断，避免时长取整少一帧。
	stop := float64(tail+1) / float64(fps)
	stopArg := strconv.FormatFloat(stop, 'f', 6, 64)
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", spec.In,
		"-vf", "tpad=stop_mode=clone:stop_duration=" + stopArg + ",fps=" + strconv.Itoa(fps) + ",format=yuv420p",
		"-frames:v", strconv.Itoa(spec.TotalFrames),
		"-r", strconv.Itoa(fps),
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-crf", strconv.Itoa(crf), "-preset", "medium",
		"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0",
		"-an",
		"-movflags", "+faststart",
	}
	args = append(args, colorTagArgs()...)
	args = append(args, spec.Out)
	return args, nil
}

// stillFitFilter 把任意静图铺进偶数画布，和合成器片段同尺寸、同帧率。
func stillFitFilter(w, h int, background string, fps int) string {
	return fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s,setsar=1,fps=%d",
		w, h, w, h, ffmpegPadColor(background), fps,
	)
}

func ffmpegPadColor(background string) string {
	s := strings.TrimSpace(background)
	if len(s) == 7 && s[0] == '#' {
		if _, err := strconv.ParseUint(s[1:], 16, 24); err == nil {
			return "0x" + s[1:]
		}
	}
	return "0xFFFFFF"
}

// encodeStillPages 按页序把静止页写成片段。升序是为了淡入能读到前一页已经落盘的末帧。
func encodeStillPages(ctx context.Context, ffmpeg string, plan *Timeline, dir string, pages []int, crf, gop int, logf func(string, ...any), progress func(done int)) error {
	ordered := append([]int(nil), pages...)
	sort.Ints(ordered)
	for n, i := range ordered {
		if err := encodeOneStill(ctx, ffmpeg, plan, dir, i, crf, gop, logf); err != nil {
			return err
		}
		if progress != nil {
			progress(n + 1)
		}
	}
	return nil
}

func encodeOneStill(ctx context.Context, ffmpeg string, plan *Timeline, dir string, i, crf, gop int, logf func(string, ...any)) error {
	page := plan.Pages[i]
	image, err := workPath(dir, page.BaseImage)
	if err != nil {
		return fmt.Errorf("render: still page %d image: %w", i, err)
	}
	prev := ""
	trans := 0
	if i > 0 {
		if tr := effectiveTransition(page.Transition); tr != nil && tr.Type == animation.TransitionFade {
			trans = framesForDuration(tr.DurationMs, plan.FPS)
			prev, err = stillPrevImage(ctx, ffmpeg, plan, dir, i, logf)
			if err != nil {
				return fmt.Errorf("render: still page %d previous frame: %w", i, err)
			}
		}
	}
	out := filepath.Join(dir, SegmentName(i))
	args, err := stillEncodeArgs(stillEncodeSpec{
		Width: plan.Width, Height: plan.Height, FPS: plan.FPS,
		CRF: crf, GOP: gop,
		Frames:           framesForDuration(page.DurationMs, plan.FPS),
		TransitionFrames: trans,
		Background:       plan.Background,
		Image:            image,
		PrevImage:        prev,
		Out:              out,
	})
	if err != nil {
		return fmt.Errorf("render: still page %d: %w", i, err)
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(outBytes))
		if len(tail) > 500 {
			tail = tail[len(tail)-500:]
		}
		return fmt.Errorf("render: still page %d: %w: %s", i, err, tail)
	}
	return nil
}

// holdChromeTails 把切开的 Chrome 前缀铺成整页帧数。跳过的页没有片段，不在这里补。
func holdChromeTails(ctx context.Context, ffmpeg string, plan *Timeline, dir string, motion map[int]int, skipped map[int]struct{}, crf, gop int, logf func(string, ...any)) error {
	if len(motion) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(motion))
	for i := range motion {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		if _, skip := skipped[i]; skip {
			continue
		}
		if i < 0 || i >= len(plan.Pages) {
			continue
		}
		total := framesForDuration(plan.Pages[i].DurationMs, plan.FPS)
		prefix := motion[i]
		if prefix >= total {
			continue
		}
		in := filepath.Join(dir, SegmentName(i))
		tmp := in + ".hold.mp4"
		args, err := holdTailArgs(holdTailSpec{
			MotionFrames: prefix, TotalFrames: total,
			FPS: plan.FPS, CRF: crf, GOP: gop,
			In: in, Out: tmp,
		})
		if err != nil {
			return fmt.Errorf("render: page %d hold: %w", i, err)
		}
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		outBytes, err := cmd.CombinedOutput()
		if err != nil {
			tail := strings.TrimSpace(string(outBytes))
			if len(tail) > 500 {
				tail = tail[len(tail)-500:]
			}
			return fmt.Errorf("render: page %d hold: %w: %s", i, err, tail)
		}
		// Windows 上 Rename 不能覆盖已存在的文件。先删再换，失败时临时文件留在目录里。
		if err := os.Remove(in); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("render: page %d hold: %w", i, err)
		}
		if err := os.Rename(tmp, in); err != nil {
			return fmt.Errorf("render: page %d hold: %w", i, err)
		}
		if logf != nil {
			logf("render: page %d chrome %d/%d frames, hold %d", i, prefix, total, total-prefix)
		}
	}
	return nil
}

// lastLookStill 返回一页最后一段注视的中点 PNG。注视页末帧就是这张图铺满到页尾。
func lastLookStill(dir string, pageIndex int, page Page) string {
	windows := lookWindows(page)
	if len(windows) == 0 {
		return ""
	}
	img := lookStillPath(dir, pageIndex, len(windows)-1)
	if st, err := os.Stat(img); err == nil && st.Size() > 0 {
		return img
	}
	return ""
}

// stillPrevImage 取前一页的末帧。片段已经在目录里（合成器刚渲完、缓存命中，
// 或前一张静止页刚写完）时用末帧，这样前一页的出场动画不会被底图盖掉。
// 前一页是注视页时用最后一张 look PNG，避免对 concat 片段抽帧。
// 抽帧失败就退回底图，并记一条日志：淡入差一帧好过整页导出失败。
func stillPrevImage(ctx context.Context, ffmpeg string, plan *Timeline, dir string, i int, logf func(string, ...any)) (string, error) {
	if i <= 0 || i > len(plan.Pages) {
		return "", fmt.Errorf("render: no previous page for %d", i)
	}
	prev := plan.Pages[i-1]
	if img := lastLookStill(dir, i-1, prev); img != "" {
		return img, nil
	}
	segment := filepath.Join(dir, SegmentName(i-1))
	if st, err := os.Stat(segment); err == nil && st.Size() > 0 {
		dst := filepath.Join(dir, fmt.Sprintf("still_prev_%03d.png", i))
		frames := framesForDuration(prev.DurationMs, plan.FPS)
		if err := extractFrame(ctx, ffmpeg, segment, frames-1, dst); err == nil {
			return dst, nil
		} else if logf != nil {
			logf("render: still page %d: previous frame extract failed (%v), using the base image", i, err)
		}
	}
	return workPath(dir, prev.BaseImage)
}

func extractFrame(ctx context.Context, ffmpeg, segment string, frame int, dst string) error {
	if frame < 0 {
		frame = 0
	}
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", segment,
		"-vf", fmt.Sprintf("select=eq(n\\,%d)", frame),
		"-frames:v", "1",
		"-update", "1",
		dst,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		return fmt.Errorf("extract frame %d: %w: %s", frame, err, tail)
	}
	if err := extractedFrameOK(dst); err != nil {
		return fmt.Errorf("extract frame %d: %w", frame, err)
	}
	return nil
}

func extractedFrameOK(dst string) error {
	st, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("empty output %s: %w", dst, err)
	}
	if st.Size() == 0 {
		return fmt.Errorf("empty output %s", dst)
	}
	return nil
}
