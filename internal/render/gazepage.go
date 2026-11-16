package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/animation"
)

// lookWindow 是一页里一段注视的时间窗。MidMs 是仍帧取样点（稳态中点，避开交叉淡化）。
type lookWindow struct {
	StartMs int64
	EndMs   int64
	FadeMs  int64 // 与上一段交叉的时长；第一段为 0（页间 fade 另算）
	MidMs   int64
}

func lookWindows(page Page) []lookWindow {
	var spans []FocusSpan
	for _, f := range page.Focus {
		if f.Rect != nil && f.Rect.W > 0 && f.Rect.H > 0 {
			spans = append(spans, f)
		}
	}
	if len(spans) == 0 || page.DurationMs <= 0 {
		return nil
	}
	var out []lookWindow
	if spans[0].StartMs > 0 {
		end := spans[0].StartMs
		out = append(out, lookWindow{StartMs: 0, EndMs: end, FadeMs: 0, MidMs: lookHoldMid(0, end, 0)})
	}
	for i, f := range spans {
		start := f.StartMs
		end := page.DurationMs
		if f.EndMs > start {
			end = f.EndMs
		} else if i+1 < len(spans) && spans[i+1].StartMs > start {
			end = spans[i+1].StartMs
		}
		if end > page.DurationMs {
			end = page.DurationMs
		}
		if end <= start {
			continue
		}
		fade := int64(0)
		if len(out) > 0 {
			fade = f.FadeMs
			if fade == 0 {
				fade = focusFadeDefaultMS
			}
			if fade >= end-start {
				fade = 0
			}
		}
		out = append(out, lookWindow{StartMs: start, EndMs: end, FadeMs: fade, MidMs: lookHoldMid(start, end, fade)})
	}
	if n := len(out); n > 0 && out[n-1].EndMs < page.DurationMs {
		last := out[n-1]
		out[n-1].EndMs = page.DurationMs
		out[n-1].MidMs = lookHoldMid(last.StartMs, page.DurationMs, last.FadeMs)
	}
	return out
}

// lookHoldMid 是注视窗稳态中点。前缀窗（第一条 Focus 晚于 0）也走这里，
// 避免 MidMs=0 采到页边界上上一页的末帧。
func lookHoldMid(start, end, fade int64) int64 {
	if end <= start {
		return start
	}
	if fade < 0 {
		fade = 0
	}
	if fade >= end-start {
		fade = 0
	}
	hold := end - start - fade
	mid := start + fade + hold/2
	if mid <= start && end > start+1 {
		return start + 1
	}
	return mid
}

func pageStartFrameList(plan *Timeline) []int {
	starts := make([]int, len(plan.Pages))
	cursor := 0
	for i, p := range plan.Pages {
		starts[i] = cursor
		cursor += framesForDuration(p.DurationMs, plan.FPS)
	}
	return starts
}

func lookFrame(pageStart, pageFrames int, midMs int64, fps int) int {
	if pageFrames < 1 {
		pageFrames = 1
	}
	if midMs <= 0 {
		return pageStart
	}
	local := framesForDuration(midMs, fps)
	if local >= pageFrames {
		local = pageFrames - 1
	}
	if local < 0 {
		local = 0
	}
	return pageStart + local
}

type stillJob struct {
	Frame int    `json:"frame"`
	Out   string `json:"out"`
}

func lookStillPath(dir string, page, look int) string {
	return filepath.Join(dir, "looks", fmt.Sprintf("p%03d_l%02d.png", page, look))
}

func encodeGazePages(ctx context.Context, b *RemotionBackend, staged *Timeline, dir string, pages []int, planPath string, progress func(done int)) error {
	if len(pages) == 0 {
		return nil
	}
	jobs, err := collectLookJobs(staged, dir, pages)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return fmt.Errorf("render: gaze pages have no look windows")
	}
	if err := os.MkdirAll(filepath.Join(dir, "looks"), 0o750); err != nil {
		return fmt.Errorf("render: prepare looks dir: %w", err)
	}
	stillsPath := filepath.Join(dir, "stills.json")
	raw, err := json.Marshal(jobs)
	if err != nil {
		return fmt.Errorf("render: marshal stills: %w", err)
	}
	if err := os.WriteFile(stillsPath, raw, 0o640); err != nil {
		return fmt.Errorf("render: write stills: %w", err)
	}
	script := b.resolveScript()
	if script == "" {
		return errors.New("render: renderer script not configured (set PPTS_RENDERER_SCRIPT)")
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("render: renderer script %q: %w", script, err)
	}
	if err := b.execStills(ctx, script, planPath, stillsPath, dir, contentDuration(staged)); err != nil {
		return err
	}
	ffmpeg := b.resolveFFmpeg()
	for n, i := range pages {
		if err := encodeGazePage(ctx, ffmpeg, staged, dir, i, b.crf(), b.gop(), b.Logf); err != nil {
			return err
		}
		if progress != nil {
			progress(n + 1)
		}
	}
	return nil
}

func collectLookJobs(plan *Timeline, dir string, pages []int) ([]stillJob, error) {
	starts := pageStartFrameList(plan)
	var jobs []stillJob
	for _, i := range pages {
		if i < 0 || i >= len(plan.Pages) {
			return nil, fmt.Errorf("render: gaze page index %d out of range", i)
		}
		page := plan.Pages[i]
		windows := lookWindows(page)
		if len(windows) == 0 {
			return nil, fmt.Errorf("render: gaze page %d has no look windows", i)
		}
		pageFrames := framesForDuration(page.DurationMs, plan.FPS)
		for li, w := range windows {
			out := lookStillPath(dir, i, li)
			jobs = append(jobs, stillJob{
				Frame: lookFrame(starts[i], pageFrames, w.MidMs, plan.FPS),
				Out:   out,
			})
		}
	}
	return jobs, nil
}

func encodeGazePage(ctx context.Context, ffmpeg string, plan *Timeline, dir string, i, crf, gop int, logf func(string, ...any)) error {
	page := plan.Pages[i]
	windows := lookWindows(page)
	if len(windows) == 0 {
		return fmt.Errorf("render: gaze page %d has no look windows", i)
	}
	pageFrames := framesForDuration(page.DurationMs, plan.FPS)
	counts := lookFrameCounts(windows, pageFrames, plan.FPS)
	looks := make([]gazeLookEncode, 0, len(windows))
	for li, w := range windows {
		img := lookStillPath(dir, i, li)
		if st, err := os.Stat(img); err != nil || st.Size() == 0 {
			return fmt.Errorf("render: gaze page %d look %d missing still %s", i, li, img)
		}
		prev := ""
		trans := 0
		if li == 0 && i > 0 {
			if tr := effectiveTransition(page.Transition); tr != nil && tr.Type == animation.TransitionFade {
				trans = framesForDuration(tr.DurationMs, plan.FPS)
				p, err := stillPrevImage(ctx, ffmpeg, plan, dir, i, logf)
				if err != nil {
					return fmt.Errorf("render: gaze page %d previous frame: %w", i, err)
				}
				prev = p
			}
		} else if li > 0 && w.FadeMs > 0 {
			trans = framesForDuration(w.FadeMs, plan.FPS)
			prev = lookStillPath(dir, i, li-1)
		}
		looks = append(looks, gazeLookEncode{
			Image: img, PrevImage: prev,
			Frames: counts[li], TransitionFrames: trans,
		})
	}
	out := filepath.Join(dir, SegmentName(i))
	args, err := encodeGazePageArgs(gazePageEncodeSpec{
		Width: plan.Width, Height: plan.Height, FPS: plan.FPS,
		CRF: crf, GOP: gop,
		Background: plan.Background,
		Out:        out,
		PageFrames: pageFrames,
		Looks:      looks,
	})
	if err != nil {
		return fmt.Errorf("render: gaze page %d: %w", i, err)
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("render: gaze page %d: %w: %s", i, err, trimTail(outBytes))
	}
	return nil
}

// gazeLookEncode 是一页里一段注视在单次 ffmpeg 图里的输入。
// PrevImage 非空时，本段开头做 TransitionFrames 的 easeOutCubic xfade。
type gazeLookEncode struct {
	Image            string
	PrevImage        string
	Frames           int
	TransitionFrames int
}

type gazePageEncodeSpec struct {
	Width, Height, FPS, CRF, GOP int
	Background                   string
	Out                          string
	PageFrames                   int
	Looks                        []gazeLookEncode
}

const gazeXfadeExpr = `expr='A*pow(1-P\,3)+B*(1-pow(1-P\,3))'`

// encodeGazePageArgs 一页一张 filtergraph：静图 xfade + concat + 一次 ultrafast 编码。
// 替代「每 look 一次 medium + concat 再 medium」。输入是仍 PNG，不是 N 路运动流。
func encodeGazePageArgs(spec gazePageEncodeSpec) ([]string, error) {
	if spec.Out == "" || len(spec.Looks) == 0 {
		return nil, fmt.Errorf("render: gaze page is missing looks or output")
	}
	w, h := spec.Width, spec.Height
	if w <= 0 || h <= 0 || w%2 != 0 || h%2 != 0 {
		return nil, fmt.Errorf("render: gaze page canvas %dx%d must be positive and even", w, h)
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
	pageFrames := spec.PageFrames
	if pageFrames < 1 {
		pageFrames = 1
	}

	type prepared struct {
		prevIdx, imgIdx int
		frames, trans   int
	}
	type loopIn struct {
		path string
		sec  string
	}
	var inputs []loopIn
	addInput := func(path string, frames int) int {
		if frames < 1 {
			frames = 1
		}
		sec := strconv.FormatFloat(float64(frames+1)/float64(fps), 'f', 3, 64)
		inputs = append(inputs, loopIn{path: path, sec: sec})
		return len(inputs) - 1
	}

	preparedLooks := make([]prepared, 0, len(spec.Looks))
	for _, look := range spec.Looks {
		if look.Image == "" {
			return nil, fmt.Errorf("render: gaze look is missing image")
		}
		frames := look.Frames
		if frames < 1 {
			frames = 1
		}
		trans := look.TransitionFrames
		if look.PrevImage == "" || trans < 1 {
			trans = 0
		}
		if trans >= frames {
			if frames > 1 {
				trans = frames - 1
			} else {
				trans = 0
			}
		}
		prevIdx := -1
		if trans > 0 {
			prevIdx = addInput(look.PrevImage, trans+1)
		}
		imgIdx := addInput(look.Image, frames)
		preparedLooks = append(preparedLooks, prepared{prevIdx: prevIdx, imgIdx: imgIdx, frames: frames, trans: trans})
	}

	fit := stillFitFilter(w, h, spec.Background, fps)
	fc := make([]string, 0, len(inputs)+len(preparedLooks)+1)
	for i := range inputs {
		fc = append(fc, fmt.Sprintf("[%d:v]%s,format=yuv420p[s%d]", i, fit, i))
	}
	labels := make([]string, len(preparedLooks))
	for i, p := range preparedLooks {
		label := fmt.Sprintf("l%d", i)
		if p.trans > 0 {
			transSec := strconv.FormatFloat(float64(p.trans)/float64(fps), 'f', 3, 64)
			fc = append(fc, fmt.Sprintf("[s%d][s%d]xfade=transition=custom:duration=%s:offset=0:%s,trim=end_frame=%d,setpts=PTS-STARTPTS[%s]",
				p.prevIdx, p.imgIdx, transSec, gazeXfadeExpr, p.frames, label))
		} else {
			fc = append(fc, fmt.Sprintf("[s%d]trim=end_frame=%d,setpts=PTS-STARTPTS[%s]", p.imgIdx, p.frames, label))
		}
		labels[i] = "[" + label + "]"
	}
	if len(preparedLooks) == 1 {
		fc = append(fc, "[l0]format=yuv420p,tpad=stop_mode=clone:stop_duration=1[v]")
	} else {
		fc = append(fc, strings.Join(labels, "")+fmt.Sprintf("concat=n=%d:v=1:a=0,format=yuv420p,tpad=stop_mode=clone:stop_duration=1[v]", len(preparedLooks)))
	}

	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	for _, in := range inputs {
		args = append(args,
			"-loop", "1", "-framerate", strconv.Itoa(fps), "-t", in.sec, "-i", in.path,
		)
	}
	args = append(args,
		"-filter_complex", strings.Join(fc, ";"),
		"-map", "[v]",
		"-frames:v", strconv.Itoa(pageFrames),
		"-r", strconv.Itoa(fps),
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-crf", strconv.Itoa(crf), "-preset", "ultrafast",
		"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0",
		"-an", "-movflags", "+faststart",
	)
	args = append(args, colorTagArgs()...)
	args = append(args, spec.Out)
	return args, nil
}

// lookFrameCounts 把各段注视的帧数调成恰好等于页长。
// 每段单独 round(ms×fps) 时，8–12 段会累计超过 100ms，整片拼接会把这些页换成静段。
func lookFrameCounts(windows []lookWindow, pageFrames, fps int) []int {
	n := len(windows)
	if n == 0 {
		return nil
	}
	if pageFrames < n {
		pageFrames = n
	}
	raw := make([]int, n)
	sum := 0
	for i, w := range windows {
		f := framesForDuration(w.EndMs-w.StartMs, fps)
		if f < 1 {
			f = 1
		}
		raw[i] = f
		sum += f
	}
	diff := pageFrames - sum
	if diff == 0 {
		return raw
	}
	raw[n-1] += diff
	if raw[n-1] >= 1 {
		return raw
	}
	need := 1 - raw[n-1]
	raw[n-1] = 1
	for i := n - 2; i >= 0 && need > 0; i-- {
		take := raw[i] - 1
		if take > need {
			take = need
		}
		if take > 0 {
			raw[i] -= take
			need -= take
		}
	}
	return raw
}

// concatListBody 写出 ffmpeg concat demuxer 清单：每行 file 'rel'。
// strconv.Quote 会写成 \"rel\"，demuxer 会去打开带反斜杠引号的路径。
func concatListBody(base string, clips []string) string {
	var b strings.Builder
	for _, c := range clips {
		rel, err := filepath.Rel(base, c)
		if err != nil {
			rel = c
		}
		path := strings.ReplaceAll(filepath.ToSlash(rel), "'", `'\''`)
		b.WriteString("file '")
		b.WriteString(path)
		b.WriteString("'\n")
	}
	return b.String()
}

func concatLooksArgs(list, out string, frames, fps, crf, gop int) []string {
	if frames < 1 {
		frames = 1
	}
	if fps < 1 {
		fps = 30
	}
	if crf <= 0 {
		crf = 18
	}
	if gop <= 0 {
		gop = 30
	}
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", list,
		"-vf", "tpad=stop_mode=clone:stop_duration=1,fps=" + strconv.Itoa(fps) + ",format=yuv420p",
		"-frames:v", strconv.Itoa(frames),
		"-r", strconv.Itoa(fps),
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-crf", strconv.Itoa(crf), "-preset", "medium",
		"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0",
		"-an", "-movflags", "+faststart",
	}
	args = append(args, colorTagArgs()...)
	args = append(args, out)
	return args
}

func concatLooks(ctx context.Context, ffmpeg string, clips []string, out string, frames, fps, crf, gop int) error {
	list := out + ".concat.txt"
	if err := os.WriteFile(list, []byte(concatListBody(filepath.Dir(list), clips)), 0o640); err != nil {
		return fmt.Errorf("render: concat list: %w", err)
	}
	cmd := exec.CommandContext(ctx, ffmpeg, concatLooksArgs(list, out, frames, fps, crf, gop)...)
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("render: concat looks: %w: %s", err, trimTail(outBytes))
	}
	return nil
}

func trimTail(out []byte) string {
	tail := strings.TrimSpace(string(out))
	if len(tail) > 500 {
		return tail[len(tail)-500:]
	}
	return tail
}
