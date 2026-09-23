package media

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
	"sync"
)

// ErrFFmpegUnavailable 表示 FFmpeg/ffprobe 未安装。
var ErrFFmpegUnavailable = errors.New("media: ffmpeg or ffprobe not found")

// ErrSubtitlesUnavailable 表示本机 ffmpeg 未编译 libass（缺 subtitles 滤镜），无法烧录字幕。
// 单独作为哨兵错误：调用方据此给出「明确不可用 + 原因」，而不是让用户拿到一段没有字幕的视频（A26）。
var ErrSubtitlesUnavailable = errors.New("media: ffmpeg lacks the subtitles filter (libass)")

// subtitleFileName 是烧录用字幕在工作目录内的固定文件名（SRT 回退路径）。
// 以 basename（而非绝对路径）引用：filtergraph 中的路径转义（Windows 盘符冒号、反斜杠）极易出错，
// 改用相对路径 + exec.Cmd.Dir 可完全规避。文件名由服务端生成，不含需转义字符。
const subtitleFileName = "subtitles.srt"

// subtitleASSFileName 是烧录用 ASS 字幕的固定文件名（逐行轮换 + 朗读位置高亮路径）。
const subtitleASSFileName = "subtitles.ass"

// MP4EncodeOptions 是 MP4 静态画面合成参数（G0-3 最小链路；G1 起由时间轴驱动）。
// 结果 must 通过 ffprobe + 抽帧验证（V4.0 §9.2）。
type MP4EncodeOptions struct {
	OutPath  string   // 输出 .mp4 路径
	FPS      int      // 画面帧率（0 = 1）
	Width    int      // 目标宽（0 = 1920）
	Height   int      // 目标高（0 = 1080）
	PagePNGs [][]byte // 页面图（一页一帧 → 静止画面序列）
	// PageDurationsMS 按页面指定显示时长；非空时长度必须等于 PagePNGs，且与 Totals 互斥。
	PageDurationsMS []int64
	AudioWAV        []byte  // 可选：音频（WAV）；nil 时用静音轨填充
	Totals          float64 // 总时长（秒）显式给定；0 = len(PagePNGs)/FPS
	// BurnSubtitles 为 true 时把 SubtitleASS（优先）或 SubtitleSRT 压进画面
	// （设计方案 V1_6 §338「字幕烧录选项」）。
	BurnSubtitles bool
	// SubtitleASS 为 UTF-8 编码的 ASS 内容（逐行轮换 + 朗读高亮）；优先于 SubtitleSRT。
	SubtitleASS []byte
	// SubtitleSRT 为 UTF-8 编码的 SRT 内容（回退路径）；BurnSubtitles 为 true 时二者必有一。
	SubtitleSRT []byte
	// SubtitleFontName 为烧录时锁定的字体族名（fontconfig family），经 subtitles 滤镜的
	// force_style=FontName=... 注入；非空时绕过 fontconfig 默认选择，保证中文渲染在各环境一致
	// （设计方案 V1_6 §338「字幕烧录字体锁定」）。空 = 不指定，由 ffmpeg/libass 走系统默认字体。
	SubtitleFontName string
}

// MP4EncodeResult 是编码后的 ffprobe 验证结果。
type MP4EncodeResult struct {
	Duration   float64 `json:"duration"`
	VideoCodec string  `json:"videoCodec"`
	AudioCodec string  `json:"audioCodec"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	OutPath    string  `json:"outPath"`
}

// ffprobeStream 是 ffprobe -of json 的最小解析视图。
type ffprobeStream struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

// MP4Encoder 封装 FFmpeg 静态画面视频合成与 ffprobe 验证。
type MP4Encoder struct {
	ffmpeg  string
	ffprobe string

	// subtitles 滤镜（libass）能力探测结果，进程内只探一次（本类型只以指针使用，不可复制）。
	subOnce sync.Once
	subOK   bool
	subErr  error
}

// NewMP4Encoder 解析 ffmpeg/ffprobe 路径；缺失返回 ErrFFmpegUnavailable。
func NewMP4Encoder() (*MP4Encoder, error) {
	fm, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("%w: ffmpeg=%v ffprobe=%v", ErrFFmpegUnavailable, err1, err2)
	}
	return &MP4Encoder{ffmpeg: fm, ffprobe: fp}, nil
}

// SupportsSubtitles 探测本机 ffmpeg 是否带 subtitles 滤镜（libass），结果进程内缓存。
// 上报给调用方是为了让「烧录」在能力缺失时能被明确说明，而不是产出无字幕视频（A26）。
func (e *MP4Encoder) SupportsSubtitles(ctx context.Context) (bool, error) {
	e.subOnce.Do(func() {
		out, err := exec.CommandContext(ctx, e.ffmpeg, "-hide_banner", "-filters").CombinedOutput()
		if err != nil {
			e.subErr = fmt.Errorf("media: probe ffmpeg filters: %w", err)
			return
		}
		// 行格式： <flags> <name> <in->out> <description>
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "subtitles" {
				e.subOK = true
				return
			}
		}
	})
	return e.subOK, e.subErr
}

// encodeInputs 是输入文件在工作目录内的落点，供 compileEncodeArgs 构造命令行。
// 所有字段均为绝对路径（字幕除外 —— 它以固定 basename 相对工作目录引用）。
type encodeInputs struct {
	WorkDir string
	// PageFiles 长度 = len(PagePNGs)，为各页 PNG 的绝对路径（可变时长时即输入顺序）。
	PageFiles []string
	// PagePattern 非空 = image2 序列输入（多页且等时长），值为含 %d 的绝对路径。
	PagePattern string
	// AudioFile 非空 = 使用该 WAV；空 = 由 lavfi 生成静音轨。
	AudioFile string
	// SubtitleFile 为工作目录内字幕文件的 basename；空 = subtitleFileName（SRT）。
	SubtitleFile string
}

// compileEncodeArgs 由编码选项与输入落点构造完整的 ffmpeg 参数与目标总时长。
//
// 纯函数：不做任何 IO、不 exec —— 因此滤镜链（含字幕烧录）可以在**没有 ffmpeg 的机器上**被单测覆盖，
// 这正是本函数从 Encode 里抽出来的原因。
func compileEncodeArgs(opts MP4EncodeOptions, in encodeInputs) ([]string, float64, error) {
	if len(opts.PagePNGs) == 0 {
		return nil, 0, errors.New("media: no page images to encode")
	}
	variablePageTiming := len(opts.PageDurationsMS) > 0
	if variablePageTiming && len(opts.PageDurationsMS) != len(opts.PagePNGs) {
		return nil, 0, errors.New("media: page duration count does not match page images")
	}
	if variablePageTiming && opts.Totals > 0 {
		return nil, 0, errors.New("media: PageDurationsMS and Totals are mutually exclusive")
	}
	if opts.BurnSubtitles && len(opts.SubtitleASS) == 0 && len(opts.SubtitleSRT) == 0 {
		return nil, 0, errors.New("media: burn subtitles requested without subtitle data")
	}
	if len(in.PageFiles) != len(opts.PagePNGs) {
		return nil, 0, errors.New("media: page file count does not match page images")
	}
	if len(opts.PagePNGs) > 1 && !variablePageTiming && in.PagePattern == "" {
		return nil, 0, errors.New("media: missing image sequence pattern")
	}

	fps := opts.FPS
	if fps <= 0 {
		if variablePageTiming {
			fps = 30
		} else {
			fps = 1
		}
	}
	w := opts.Width
	if w <= 0 {
		w = 1920
	}
	h := opts.Height
	if h <= 0 {
		h = 1080
	}
	total := opts.Totals
	if variablePageTiming {
		var totalMS int64
		for _, durationMS := range opts.PageDurationsMS {
			if durationMS <= 0 {
				return nil, 0, errors.New("media: page durations must be positive")
			}
			if totalMS > int64(^uint64(0)>>1)-durationMS {
				return nil, 0, errors.New("media: page duration overflow")
			}
			totalMS += durationMS
		}
		total = float64(totalMS) / 1000
	} else if total <= 0 {
		total = float64(len(opts.PagePNGs)) / float64(fps)
	}

	args := []string{"-y"}
	switch {
	case variablePageTiming:
		for i, durationMS := range opts.PageDurationsMS {
			args = append(args,
				"-loop", "1", "-framerate", strconv.Itoa(fps),
				"-t", millisecondsDecimal(durationMS),
				"-i", in.PageFiles[i],
			)
		}
	case len(opts.PagePNGs) > 1:
		args = append(args, "-framerate", strconv.Itoa(fps), "-i", in.PagePattern)
	default:
		args = append(args, "-loop", "1", "-framerate", strconv.Itoa(fps), "-i", in.PageFiles[0])
	}
	if in.AudioFile != "" {
		args = append(args, "-i", in.AudioFile)
	} else {
		args = append(args,
			"-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo",
		)
	}
	// 等比适配 + 补边；H.264/AAC/yuv420p 为项目默认预设（V4.0 §9.2）。
	args = append(args,
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
	)
	subtitle := ""
	if opts.BurnSubtitles {
		// 相对 basename（cwd = in.WorkDir），规避 filtergraph 路径转义；字体名锁定见 subtitleStage。
		name := in.SubtitleFile
		if name == "" {
			name = subtitleFileName
		}
		subtitle = subtitleStage(opts.SubtitleFontName, name)
	}
	if variablePageTiming {
		var filter strings.Builder
		for i, durationMS := range opts.PageDurationsMS {
			fmt.Fprintf(&filter,
				"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,trim=duration=%s,setpts=PTS-STARTPTS[v%d];",
				i, w, h, w, h, millisecondsDecimal(durationMS), i)
		}
		for i := range opts.PagePNGs {
			fmt.Fprintf(&filter, "[v%d]", i)
		}
		fmt.Fprintf(&filter, "concat=n=%d:v=1:a=0", len(opts.PagePNGs))
		if subtitle != "" {
			// 字幕挂为最后一级：concat 先出 [vbase]，再烧录为 [vout]。
			filter.WriteString("[vbase];[vbase]" + subtitle + "[vout]")
		} else {
			filter.WriteString("[vout]")
		}
		audioInput := len(opts.PagePNGs)
		args = append(args,
			"-filter_complex", filter.String(),
			"-map", "[vout]", "-map", fmt.Sprintf("%d:a:0", audioInput),
		)
	} else {
		vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2", w, h, w, h)
		if subtitle != "" {
			vf += "," + subtitle
		}
		args = append(args, "-vf", vf)
	}
	args = append(args, "-t", strconv.FormatFloat(total, 'f', 6, 64))
	// moov 前置：浏览器 <video> 无需下载完整文件即可起播/拖动（配合对象端点的 Range 支持）。
	args = append(args, "-movflags", "+faststart")
	args = append(args, opts.OutPath)
	return args, total, nil
}

// Encode 由页面图序列合成 MP4。页面图写入临时目录以 img-%d.png 命名，
// 经输入序列喂给 FFmpeg；画面在目标框内等比适配留边（不拉伸，V4.0 §3.4）。
// BurnSubtitles 为 true 时把 SubtitleASS（优先）或 SubtitleSRT 压进画面（ffmpeg 需带 libass）。
func (e *MP4Encoder) Encode(ctx context.Context, opts MP4EncodeOptions) (*MP4EncodeResult, error) {
	if len(opts.PagePNGs) == 0 {
		return nil, errors.New("media: no page images to encode")
	}
	if opts.BurnSubtitles {
		if len(opts.SubtitleASS) == 0 && len(opts.SubtitleSRT) == 0 {
			return nil, errors.New("media: burn subtitles requested without subtitle data")
		}
		// 能力前置校验：缺 libass 时明确失败，不产出无字幕的视频（A26）。
		ok, err := e.SupportsSubtitles(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrSubtitlesUnavailable
		}
	}
	work, err := os.MkdirTemp("", "ppts-mp4-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	variablePageTiming := len(opts.PageDurationsMS) > 0
	in := encodeInputs{WorkDir: work}
	switch {
	case variablePageTiming:
		in.PageFiles = make([]string, len(opts.PagePNGs))
		for i, png := range opts.PagePNGs {
			name := "img-" + strconv.Itoa(i+1) + ".png"
			if err := os.WriteFile(filepath.Join(work, name), png, 0o644); err != nil {
				return nil, err
			}
			in.PageFiles[i] = filepath.Join(work, name)
		}
	case len(opts.PagePNGs) > 1:
		in.PageFiles = make([]string, len(opts.PagePNGs))
		for i, png := range opts.PagePNGs {
			name := "img-" + strconv.Itoa(i+1) + ".png"
			if err := os.WriteFile(filepath.Join(work, name), png, 0o644); err != nil {
				return nil, err
			}
			in.PageFiles[i] = filepath.Join(work, name)
		}
		in.PagePattern = filepath.Join(work, "img-%d.png")
	default:
		// 单页：写成确定性单文件，FFmpeg 逐帧循环即可（image2 demuxer 的 -loop）。
		single := filepath.Join(work, "single.png")
		if err := os.WriteFile(single, opts.PagePNGs[0], 0o644); err != nil {
			return nil, err
		}
		in.PageFiles = []string{single}
	}
	if len(opts.AudioWAV) > 0 {
		audio := filepath.Join(work, "audio.wav")
		if err := os.WriteFile(audio, opts.AudioWAV, 0o644); err != nil {
			return nil, err
		}
		in.AudioFile = audio
	}
	if opts.BurnSubtitles {
		// ASS 优先（逐行轮换 + 朗读高亮）；否则回退 SRT。
		if len(opts.SubtitleASS) > 0 {
			in.SubtitleFile = subtitleASSFileName
			if err := os.WriteFile(filepath.Join(work, subtitleASSFileName), opts.SubtitleASS, 0o644); err != nil {
				return nil, err
			}
		} else {
			in.SubtitleFile = subtitleFileName
			if err := os.WriteFile(filepath.Join(work, subtitleFileName), opts.SubtitleSRT, 0o644); err != nil {
				return nil, err
			}
		}
	}

	args, _, err := compileEncodeArgs(opts, in)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, e.ffmpeg, args...)
	// 工作目录 = work：字幕滤镜以相对 basename 引用（见 subtitleFileName 注释）。
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("media: ffmpeg encode failed: %w\n%s", err, string(out))
	}

	return e.verify(ctx, opts.OutPath)
}

func millisecondsDecimal(milliseconds int64) string {
	return fmt.Sprintf("%d.%03d", milliseconds/1000, milliseconds%1000)
}

// subtitleStage 构造字幕烧录滤镜段。fontName 非空时追加 force_style=FontName=... 锁定字体，
// 避免依赖 fontconfig 默认选择导致中文渲染结果在各部署环境不一致（V1_6 §338）。
// 字幕以固定 basename 相对引用（file 为 work 目录内的文件名），规避 filtergraph 路径转义。
func subtitleStage(fontName, file string) string {
	s := "subtitles=filename=" + file
	if fontName == "" {
		return s
	}
	// force_style 值内含空格（如 "WenQuanYi Zen Hei"），用单引号包裹以规避 filtergraph 的逗号/空格分隔歧义。
	return s + ":force_style='FontName=" + fontName + "'"
}

// VerifyError 表示编码完成后的 ffprobe/抽帧校验失败。与 ffmpeg 编码失败区分：
// 校验失败可能是瞬时性的（例如 ffprobe 在特定环境下偶发段错误），调用方据此按
// 可重试错误处理，而不是把一段已成功编码的视频判为永久失败。
type VerifyError struct {
	Stage  string // "ffprobe" | "frame"
	Detail string // 命令输出（stderr），便于诊断
	Err    error
}

func (e *VerifyError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("media: verify %s failed: %v\n%s", e.Stage, e.Err, e.Detail)
	}
	return fmt.Sprintf("media: verify %s failed: %v", e.Stage, e.Err)
}

func (e *VerifyError) Unwrap() error { return e.Err }

// verify 用 ffprobe 校验封装格式与流，并抽帧验证画面非空。
func (e *MP4Encoder) verify(ctx context.Context, out string) (*MP4EncodeResult, error) {
	cmd := exec.CommandContext(ctx, e.ffprobe,
		"-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,duration",
		"-of", "json", out)
	outJSON, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &VerifyError{Stage: "ffprobe", Detail: string(outJSON), Err: err}
	}
	var probe struct {
		Streams []ffprobeStream `json:"streams"`
	}
	if err := json.Unmarshal(outJSON, &probe); err != nil {
		return nil, &VerifyError{Stage: "ffprobe", Detail: string(outJSON), Err: err}
	}
	res := &MP4EncodeResult{OutPath: out}
	for _, s := range probe.Streams {
		switch {
		case s.CodecType == "video":
			res.VideoCodec = s.CodecName
			res.Width, res.Height = s.Width, s.Height
			if s.Duration != "" {
				res.Duration, _ = strconv.ParseFloat(s.Duration, 64)
			}
		case s.CodecType == "audio":
			res.AudioCodec = s.CodecName
		}
	}
	if res.VideoCodec != "h264" || res.AudioCodec != "aac" || res.Width <= 0 || res.Height <= 0 {
		return nil, fmt.Errorf("media: verify failed: %+v", res)
	}
	// 抽帧验证画面非空（V4.0 §9.2）：取首帧，确认可解码且输出非空。
	if res.Duration > 0 {
		dst := filepath.Join(filepath.Dir(out), ".ppts-verify-frame.png")
		if err := extractFrame(ctx, e.ffmpeg, out, "0", dst); err != nil {
			return nil, &VerifyError{Stage: "frame", Err: err}
		}
	}
	return res, nil
}

// extractFrame 抽取指定时间位置的单帧到 dst，校验输出非空（verify 与测试共用）。
// -ss 置于 -i 之后（output seek），低帧率视频也能取到最近一帧。
func extractFrame(ctx context.Context, ffmpeg, src, position, dst string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-i", src, "-ss", position, "-frames:v", "1", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("media: frame extract failed: %w\n%s", err, string(out))
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() == 0 {
		return errors.New("media: extracted frame is empty")
	}
	return nil
}
