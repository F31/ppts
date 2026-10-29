package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// 本文件把「逐页渲染出的单页片段」拼成整片（M3 / animation-export-e2e 任务 1.1–1.5）。
//
// 与 mp4.go 的分工：mp4.go 负责「静态页 → 视频」，本文件负责「视频段 → 视频」。
// 两者共用同一套编码预设（libx264 / yuv420p / aac 128k / +faststart），这不是巧合：
// 拼接默认走 -c:v copy，只有各段参数一致时 copy 才成立（ADR-22）。

// VideoSegment 是一段单页视频：渲染服务产出的动画段，或单页降级时的静态段。
// DurationMS 是**计划**时长（来自时间轴），用于拼接前的漂移校验；<= 0 表示不校验该段。
type VideoSegment struct {
	Path       string
	DurationMS int64
}

// ConcatOptions 是「分段 → 整片」的合成参数。
type ConcatOptions struct {
	Segments []VideoSegment
	// AudioPath 是时间轴音频（WAV）。空表示用静音轨填充——与静态版一致：
	// 既有 MP4 编码永远带一条音轨，缺音轨的成片会让下游按「无音频」处理。
	AudioPath string
	// BurnSubtitles 为 true 时烧录字幕；此时无法 -c:v copy，必须重编码（ADR-22 路径 2）。
	BurnSubtitles    bool
	SubtitleASS      []byte // 优先
	SubtitleSRT      []byte // 回退
	SubtitleFontName string
	FPS              int
	Width            int
	Height           int
	OutPath          string
}

// ConcatResult 是拼接结果与决策留痕。
type ConcatResult struct {
	Duration float64
	Width    int
	Height   int
	// Reencoded 为 true 表示走了重编码路径（有字幕、参数不一致，或 copy 失败回退）。
	Reencoded bool
	// Drifted 是时长漂移超限的段索引。调用方应把这些页替换为静态段后重拼，
	// 而不是接受一个音画整体错位的成片（ADR-22「已知边界」）。
	Drifted []int
}

// segmentProbe 是单段的探测结果（只关心决定能否 copy 的字段）。
type segmentProbe struct {
	Width      int
	Height     int
	FPS        float64
	DurationMS int64
}

// concatInputs 是拼接调用的输入落点。字幕以工作目录内的 basename 引用
// （同 mp4.go：规避 filtergraph 的路径转义），其余为绝对路径。
type concatInputs struct {
	WorkDir      string
	ListFile     string
	AudioFile    string
	SubtitleFile string
}

// segmentDriftToleranceMS 是单段时长与计划时长的容差。
// 与 §11.4「成片时长误差 ≤ 100ms」同量级：单段就超 100ms 时，整片必然超。
const segmentDriftToleranceMS = 100

// totalPlannedMS 返回各段计划时长之和；任一段未给出计划时长时返回 ok=false。
func totalPlannedMS(segments []VideoSegment) (int64, bool) {
	var total int64
	for _, s := range segments {
		if s.DurationMS <= 0 {
			return 0, false
		}
		total += s.DurationMS
	}
	return total, true
}

// driftingSegments 返回偏差超容差的段索引（纯函数）。
// 无计划时长的段不参与判定——那说明调用方也没有可比对的基准。
func driftingSegments(segments []VideoSegment, probes []segmentProbe) []int {
	var out []int
	for i, s := range segments {
		if i >= len(probes) || s.DurationMS <= 0 || probes[i].DurationMS <= 0 {
			continue
		}
		if diff := probes[i].DurationMS - s.DurationMS; diff > segmentDriftToleranceMS || diff < -segmentDriftToleranceMS {
			out = append(out, i)
		}
	}
	return out
}

// compileConcatArgs 构造拼接命令并返回「是否必须重编码」。
//
// 纯函数：不 exec、不读盘，因此三条路径（copy / 字幕重编码 / 参数不符重编码）都能在
// 没有 ffmpeg 的机器上单测——与 compileEncodeArgs 同一理由。
//
// forceReencode 用于 copy 执行失败后的回退（ADR-22 路径 3）：此时不再相信参数一致性。
func compileConcatArgs(opts ConcatOptions, probes []segmentProbe, in concatInputs, forceReencode bool) ([]string, bool, error) {
	if len(opts.Segments) == 0 {
		return nil, false, errors.New("media: no segments to concatenate")
	}
	if len(probes) != len(opts.Segments) {
		return nil, false, errors.New("media: segment probe count does not match segments")
	}
	if opts.OutPath == "" {
		return nil, false, errors.New("media: missing concat output path")
	}
	if opts.BurnSubtitles && len(opts.SubtitleASS) == 0 && len(opts.SubtitleSRT) == 0 {
		return nil, false, errors.New("media: burn subtitles requested without subtitle data")
	}
	if in.ListFile == "" {
		return nil, false, errors.New("media: missing concat list file")
	}

	fps := opts.FPS
	if fps <= 0 {
		fps = 30
	}
	w, h := opts.Width, opts.Height
	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}

	// 决定能否 copy：只有「逐段尺寸与帧率都等于目标」且「不烧字幕」时才可以。
	// 尺寸不一致时 -c:v copy 不会报错，而是产出一段下游解不出的坏片——必须在决策层拦住。
	reencode := forceReencode || opts.BurnSubtitles
	if !reencode {
		for _, p := range probes {
			if p.Width != w || p.Height != h || (p.FPS > 0 && math.Abs(p.FPS-float64(fps)) > 0.01) {
				reencode = true
				break
			}
		}
	}

	args := []string{"-y", "-f", "concat", "-safe", "0", "-i", in.ListFile}
	if in.AudioFile != "" {
		args = append(args, "-i", in.AudioFile)
	} else {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo")
	}

	// 只取第一路输入的视频流：降级段由 Encode 生成，自带一条 anullsrc 静音轨，
	// 不显式 map 会把它拼进成片，表现为「有音轨但全程静音」（ADR-22）。
	args = append(args, "-map", "0:v:0")
	if reencode {
		vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1", w, h, w, h)
		if opts.BurnSubtitles {
			vf += "," + subtitleStage(opts.SubtitleFontName, in.SubtitleFile)
		}
		args = append(args,
			"-vf", vf,
			"-c:v", "libx264", "-pix_fmt", "yuv420p",
			"-r", strconv.Itoa(fps),
		)
	} else {
		args = append(args, "-c:v", "copy")
	}

	// 音轨始终来自第二路输入（时间轴 WAV 或静音填充），不来自分段。
	args = append(args, "-map", "1:a:0", "-c:a", "aac", "-b:a", "128k")

	if totalMS, ok := totalPlannedMS(opts.Segments); ok {
		// 显式 -t：既保证成片时长等于时间轴，也避免 anullsrc 无限长把命令挂住。
		args = append(args, "-t", millisecondsDecimal(totalMS))
	} else {
		args = append(args, "-shortest")
	}
	// moov 前置：浏览器 <video> 无需下载完整文件即可起播/拖动。
	args = append(args, "-movflags", "+faststart", opts.OutPath)
	return args, reencode, nil
}

// probeVideoStream 取单段的视频流信息（宽高、帧率、时长）。
func (e *MP4Encoder) probeVideoStream(ctx context.Context, path string) (segmentProbe, error) {
	cmd := exec.CommandContext(ctx, e.ffprobe,
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,duration",
		"-of", "json", path)
	outJSON, err := cmd.CombinedOutput()
	if err != nil {
		return segmentProbe{}, &VerifyError{Stage: "ffprobe", Detail: string(outJSON), Err: err}
	}
	var probe struct {
		Streams []struct {
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			FrameRate  string `json:"r_frame_rate"`
			DurationMS string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(outJSON, &probe); err != nil {
		return segmentProbe{}, &VerifyError{Stage: "ffprobe", Detail: string(outJSON), Err: err}
	}
	if len(probe.Streams) == 0 {
		return segmentProbe{}, fmt.Errorf("media: segment %s has no video stream", path)
	}
	s := probe.Streams[0]
	p := segmentProbe{Width: s.Width, Height: s.Height, FPS: parseFrameRate(s.FrameRate)}
	if d, err := strconv.ParseFloat(s.DurationMS, 64); err == nil && d > 0 {
		p.DurationMS = int64(math.Round(d * 1000))
	}
	return p, nil
}

// parseFrameRate 解析 ffprobe 的 "30000/1001" 形式。
func parseFrameRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// escapeConcatPath 转义 concat demuxer 清单里的路径。
// 清单语法是 file '<path>'，路径中的单引号需写成 '\” 才能闭合再续。
func escapeConcatPath(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), "'", `'\''`)
}

// ConcatSegments 把分段拼成整片：探测 → 决策 → 执行 → 校验。
//
// 失败语义与 mp4.go 一致：返回错误即「没有可用成片」，调用方据此降级到静态整片；
// 单页问题不在这里处理，而是通过 Drifted 交回调用方替换成静态段后重拼。
func (e *MP4Encoder) ConcatSegments(ctx context.Context, opts ConcatOptions) (*ConcatResult, error) {
	if len(opts.Segments) == 0 {
		return nil, errors.New("media: no segments to concatenate")
	}
	if opts.BurnSubtitles {
		ok, err := e.SupportsSubtitles(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrSubtitlesUnavailable
		}
	}
	probes := make([]segmentProbe, len(opts.Segments))
	for i, seg := range opts.Segments {
		if seg.Path == "" {
			return nil, fmt.Errorf("media: segment %d has no path", i)
		}
		p, err := e.probeVideoStream(ctx, seg.Path)
		if err != nil {
			return nil, fmt.Errorf("media: segment %d: %w", i, err)
		}
		probes[i] = p
	}
	drifted := driftingSegments(opts.Segments, probes)

	work, err := os.MkdirTemp("", "ppts-concat-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	var list strings.Builder
	for _, seg := range opts.Segments {
		abs, err := filepath.Abs(seg.Path)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&list, "file '%s'\n", escapeConcatPath(abs))
	}
	listName := "segments.txt"
	if err := os.WriteFile(filepath.Join(work, listName), []byte(list.String()), 0o644); err != nil {
		return nil, err
	}

	in := concatInputs{WorkDir: work, ListFile: listName, AudioFile: opts.AudioPath}
	if opts.BurnSubtitles {
		if len(opts.SubtitleASS) > 0 {
			in.SubtitleFile = subtitleASSFileName
			if err := os.WriteFile(filepath.Join(work, in.SubtitleFile), opts.SubtitleASS, 0o644); err != nil {
				return nil, err
			}
		} else {
			in.SubtitleFile = subtitleFileName
			if err := os.WriteFile(filepath.Join(work, in.SubtitleFile), opts.SubtitleSRT, 0o644); err != nil {
				return nil, err
			}
		}
	}

	args, reencoded, err := compileConcatArgs(opts, probes, in, false)
	if err != nil {
		return nil, err
	}
	if err := e.runConcat(ctx, work, args); err != nil {
		if !reencoded {
			// copy 失败（参数/时间基不一致）→ 降级重编码重试一次（ADR-22 路径 3）。
			retryArgs, _, cerr := compileConcatArgs(opts, probes, in, true)
			if cerr != nil {
				return nil, errors.Join(err, cerr)
			}
			if rerr := e.runConcat(ctx, work, retryArgs); rerr != nil {
				return nil, errors.Join(err, rerr)
			}
			reencoded = true
		} else {
			return nil, err
		}
	}

	res, err := e.verify(ctx, opts.OutPath)
	if err != nil {
		return nil, err
	}
	return &ConcatResult{
		Duration:  res.Duration,
		Width:     res.Width,
		Height:    res.Height,
		Reencoded: reencoded,
		Drifted:   drifted,
	}, nil
}

// runConcat 在工作目录内执行一次拼接（字幕滤镜以相对 basename 引用）。
func (e *MP4Encoder) runConcat(ctx context.Context, work string, args []string) error {
	cmd := exec.CommandContext(ctx, e.ffmpeg, args...)
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("media: ffmpeg concat failed: %w\n%s", err, string(out))
	}
	return nil
}

// EncodeStillSegment 把单页静态图编码成一段与动画段同参数的 mp4，供单页降级替换（ADR-23）。
//
// 复用 Encode 而不是另写一条编码路径，是为了让「降级段」与「动画段」在分辨率、帧率、
// 像素格式上天然一致——否则拼接只能退化为整片重编码。
func (e *MP4Encoder) EncodeStillSegment(ctx context.Context, png []byte, durationMS int64, outPath string, fps, width, height int) (*VideoSegment, error) {
	if len(png) == 0 {
		return nil, errors.New("media: no still image for segment")
	}
	if durationMS <= 0 {
		return nil, errors.New("media: still segment duration must be positive")
	}
	if outPath == "" {
		return nil, errors.New("media: missing still segment output path")
	}
	_, err := e.Encode(ctx, MP4EncodeOptions{
		OutPath:         outPath,
		FPS:             fps,
		Width:           width,
		Height:          height,
		PagePNGs:        [][]byte{png},
		PageDurationsMS: []int64{durationMS},
	})
	if err != nil {
		return nil, err
	}
	return &VideoSegment{Path: outPath, DurationMS: durationMS}, nil
}
