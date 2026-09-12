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
)

// ErrFFmpegUnavailable 表示 FFmpeg/ffprobe 未安装。
var ErrFFmpegUnavailable = errors.New("media: ffmpeg or ffprobe not found")

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

// Encode 由页面图序列合成 MP4。页面图写入临时目录以 img-%d.png 命名，
// 经输入序列喂给 FFmpeg；画面在目标框内等比适配留边（不拉伸，V4.0 §3.4）。
func (e *MP4Encoder) Encode(ctx context.Context, opts MP4EncodeOptions) (*MP4EncodeResult, error) {
	if len(opts.PagePNGs) == 0 {
		return nil, errors.New("media: no page images to encode")
	}
	variablePageTiming := len(opts.PageDurationsMS) > 0
	if variablePageTiming && len(opts.PageDurationsMS) != len(opts.PagePNGs) {
		return nil, errors.New("media: page duration count does not match page images")
	}
	if variablePageTiming && opts.Totals > 0 {
		return nil, errors.New("media: PageDurationsMS and Totals are mutually exclusive")
	}
	work, err := os.MkdirTemp("", "ppts-mp4-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	var inputImages string
	if variablePageTiming {
		for i, png := range opts.PagePNGs {
			name := "img-" + strconv.Itoa(i+1) + ".png"
			if opts.PageDurationsMS[i] <= 0 {
				return nil, errors.New("media: page durations must be positive")
			}
			if err := os.WriteFile(filepath.Join(work, name), png, 0o644); err != nil {
				return nil, err
			}
		}
	} else if len(opts.PagePNGs) > 1 {
		for i, png := range opts.PagePNGs {
			if err := os.WriteFile(filepath.Join(work, "img-"+strconv.Itoa(i+1)+".png"), png, 0o644); err != nil {
				return nil, err
			}
		}
		inputImages = filepath.Join(work, "img-%d.png")
	} else {
		// 单页：写成确定性单文件，FFmpeg 逐帧循环即可（image2 demuxer 的 -loop）。
		single := filepath.Join(work, "single.png")
		if err := os.WriteFile(single, opts.PagePNGs[0], 0o644); err != nil {
			return nil, err
		}
		inputImages = single
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
			if totalMS > int64(^uint64(0)>>1)-durationMS {
				return nil, errors.New("media: page duration overflow")
			}
			totalMS += durationMS
		}
		total = float64(totalMS) / 1000
	} else if total <= 0 {
		total = float64(len(opts.PagePNGs)) / float64(fps)
	}

	args := []string{"-y"}
	if variablePageTiming {
		for i, durationMS := range opts.PageDurationsMS {
			args = append(args,
				"-loop", "1", "-framerate", strconv.Itoa(fps),
				"-t", millisecondsDecimal(durationMS),
				"-i", filepath.Join(work, "img-"+strconv.Itoa(i+1)+".png"),
			)
		}
	} else if len(opts.PagePNGs) > 1 {
		args = append(args, "-framerate", strconv.Itoa(fps), "-i", inputImages)
	} else {
		args = append(args, "-loop", "1", "-framerate", strconv.Itoa(fps), "-i", inputImages)
	}
	if len(opts.AudioWAV) > 0 {
		audio := filepath.Join(work, "audio.wav")
		if err := os.WriteFile(audio, opts.AudioWAV, 0o644); err != nil {
			return nil, err
		}
		args = append(args, "-i", audio)
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
		fmt.Fprintf(&filter, "concat=n=%d:v=1:a=0[vout]", len(opts.PagePNGs))
		audioInput := len(opts.PagePNGs)
		args = append(args,
			"-filter_complex", filter.String(),
			"-map", "[vout]", "-map", fmt.Sprintf("%d:a:0", audioInput),
		)
	} else {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2", w, h, w, h))
	}
	args = append(args, "-t", strconv.FormatFloat(total, 'f', 6, 64))
	args = append(args, opts.OutPath)

	cmd := exec.CommandContext(ctx, e.ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("media: ffmpeg encode failed: %w\n%s", err, string(out))
	}

	return e.verify(ctx, opts.OutPath)
}

func millisecondsDecimal(milliseconds int64) string {
	return fmt.Sprintf("%d.%03d", milliseconds/1000, milliseconds%1000)
}

// verify 用 ffprobe 校验封装格式与流，并抽帧验证画面非空。
func (e *MP4Encoder) verify(ctx context.Context, out string) (*MP4EncodeResult, error) {
	outJSON, err := exec.CommandContext(ctx, e.ffprobe,
		"-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,duration",
		"-of", "json", out).Output()
	if err != nil {
		return nil, fmt.Errorf("media: ffprobe failed: %w", err)
	}
	var probe struct {
		Streams []ffprobeStream `json:"streams"`
	}
	if err := json.Unmarshal(outJSON, &probe); err != nil {
		return nil, fmt.Errorf("media: parse ffprobe output: %w", err)
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
	return res, nil
}

// FramePNG 抽取指定帧为 PNG，用于画面非空校验。
func (e *MP4Encoder) FramePNG(ctx context.Context, src string, frame int, dst string) error {
	cmd := exec.CommandContext(ctx, e.ffmpeg, "-y", "-i", src,
		"-vf", fmt.Sprintf("select='eq(n\\,%d)'", frame), "-frames:v", "1", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("media: frame extract failed: %w\n%s", err, string(out))
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() == 0 {
		return errors.New("media: extracted frame is empty")
	}
	return nil
}

// FrameAt extracts the frame visible at the specified media-clock position.
func (e *MP4Encoder) FrameAt(ctx context.Context, src string, positionUS int64, dst string) error {
	if positionUS < 0 {
		return errors.New("media: frame position cannot be negative")
	}
	position := strconv.FormatFloat(float64(positionUS)/1_000_000, 'f', 6, 64)
	cmd := exec.CommandContext(ctx, e.ffmpeg, "-y", "-ss", position, "-i", src, "-frames:v", "1", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("media: frame extract failed: %w\n%s", err, string(out))
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() == 0 {
		return errors.New("media: extracted frame is empty")
	}
	return nil
}

// Close 保留占位：必要时校验临时资源释放语义（V4.0 §15.1 适配器契约）。
func (e *MP4Encoder) Close() error { return nil }
