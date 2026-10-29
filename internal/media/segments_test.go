package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件分两层：
//   - 纯函数用例（compileConcatArgs / driftingSegments / escapeConcatPath）——任何机器都能跑，
//     这是 ADR-22 三条路径决策的主要防线；
//   - 真拼接用例——需要 ffmpeg，缺失时 Skip（与 mp4_test.go 同一惯例：CI 是它的真实执行场）。

func baseConcatOptions(segments ...VideoSegment) ConcatOptions {
	return ConcatOptions{
		Segments: segments,
		FPS:      30,
		Width:    320,
		Height:   240,
		OutPath:  "out.mp4",
	}
}

func matchingProbes(n int) []segmentProbe {
	out := make([]segmentProbe, n)
	for i := range out {
		out[i] = segmentProbe{Width: 320, Height: 240, FPS: 30, DurationMS: 1000}
	}
	return out
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func hasPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestCompileConcatArgsCopiesWhenUniform(t *testing.T) {
	opts := baseConcatOptions(VideoSegment{Path: "a.mp4", DurationMS: 1000}, VideoSegment{Path: "b.mp4", DurationMS: 2000})
	args, reencoded, err := compileConcatArgs(opts, matchingProbes(2), concatInputs{ListFile: "segments.txt"}, false)
	if err != nil {
		t.Fatalf("compileConcatArgs: %v", err)
	}
	if reencoded {
		t.Fatalf("参数一致时不该重编码，args=%v", args)
	}
	if !hasPair(args, "-c:v", "copy") {
		t.Fatalf("缺 -c:v copy，args=%v", args)
	}
	if !hasPair(args, "-map", "0:v:0") {
		t.Fatalf("缺 -map 0:v:0（会把降级段的静音轨拼进来），args=%v", args)
	}
	if !hasPair(args, "-map", "1:a:0") {
		t.Fatalf("缺 -map 1:a:0（音轨应来自时间轴），args=%v", args)
	}
	if !hasPair(args, "-t", "3.000") {
		t.Fatalf("缺 -t 3.000（成片时长应等于计划时长之和），args=%v", args)
	}
	if !hasPair(args, "-movflags", "+faststart") {
		t.Fatalf("缺 +faststart，args=%v", args)
	}
	if containsArg(args, "-vf") {
		t.Fatalf("copy 路径不该带 -vf（与 copy 互斥），args=%v", args)
	}
}

func TestCompileConcatArgsReencodesForSubtitles(t *testing.T) {
	opts := baseConcatOptions(VideoSegment{Path: "a.mp4", DurationMS: 1000})
	opts.BurnSubtitles = true
	opts.SubtitleASS = []byte("[Script Info]\n")
	opts.SubtitleFontName = "Noto Sans SC"
	args, reencoded, err := compileConcatArgs(opts, matchingProbes(1), concatInputs{ListFile: "segments.txt", SubtitleFile: subtitleASSFileName}, false)
	if err != nil {
		t.Fatalf("compileConcatArgs: %v", err)
	}
	if !reencoded {
		t.Fatal("烧字幕必须重编码")
	}
	if !hasPair(args, "-c:v", "libx264") {
		t.Fatalf("重编码路径应用 libx264，args=%v", args)
	}
	vf := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-vf" {
			vf = args[i+1]
		}
	}
	if !strings.Contains(vf, "subtitles=filename="+subtitleASSFileName) {
		t.Fatalf("滤镜链缺字幕阶段：%q", vf)
	}
	if !strings.Contains(vf, "FontName=Noto Sans SC") {
		t.Fatalf("字体名未注入：%q", vf)
	}
	if !strings.Contains(vf, "setsar=1") {
		t.Fatalf("缺 setsar=1（SAR 不一致会让后续 copy 拼接失败）：%q", vf)
	}
}

func TestCompileConcatArgsReencodesWhenGeometryDiffers(t *testing.T) {
	opts := baseConcatOptions(VideoSegment{Path: "a.mp4", DurationMS: 1000}, VideoSegment{Path: "b.mp4", DurationMS: 1000})
	probes := matchingProbes(2)
	probes[1].Width = 640
	_, reencoded, err := compileConcatArgs(opts, probes, concatInputs{ListFile: "segments.txt"}, false)
	if err != nil {
		t.Fatalf("compileConcatArgs: %v", err)
	}
	if !reencoded {
		t.Fatal("尺寸不一致时 copy 会产出坏片，必须转重编码")
	}
}

func TestCompileConcatArgsReencodesWhenFPSDiffers(t *testing.T) {
	opts := baseConcatOptions(VideoSegment{Path: "a.mp4", DurationMS: 1000})
	probes := matchingProbes(1)
	probes[0].FPS = 25
	_, reencoded, err := compileConcatArgs(opts, probes, concatInputs{ListFile: "segments.txt"}, false)
	if err != nil {
		t.Fatalf("compileConcatArgs: %v", err)
	}
	if !reencoded {
		t.Fatal("帧率不一致时必须重编码")
	}
}

func TestCompileConcatArgsForceReencode(t *testing.T) {
	opts := baseConcatOptions(VideoSegment{Path: "a.mp4", DurationMS: 1000})
	_, reencoded, err := compileConcatArgs(opts, matchingProbes(1), concatInputs{ListFile: "segments.txt"}, true)
	if err != nil {
		t.Fatalf("compileConcatArgs: %v", err)
	}
	if !reencoded {
		t.Fatal("forceReencode 未被采纳")
	}
}

func TestCompileConcatArgsRejects(t *testing.T) {
	t.Run("空分段", func(t *testing.T) {
		if _, _, err := compileConcatArgs(ConcatOptions{OutPath: "o.mp4"}, nil, concatInputs{ListFile: "l"}, false); err == nil {
			t.Fatal("应报错")
		}
	})
	t.Run("探测数不匹配", func(t *testing.T) {
		opts := baseConcatOptions(VideoSegment{Path: "a.mp4"})
		if _, _, err := compileConcatArgs(opts, nil, concatInputs{ListFile: "l"}, false); err == nil {
			t.Fatal("应报错")
		}
	})
	t.Run("缺输出路径", func(t *testing.T) {
		opts := baseConcatOptions(VideoSegment{Path: "a.mp4"})
		opts.OutPath = ""
		if _, _, err := compileConcatArgs(opts, matchingProbes(1), concatInputs{ListFile: "l"}, false); err == nil {
			t.Fatal("应报错")
		}
	})
	t.Run("字幕无数据", func(t *testing.T) {
		opts := baseConcatOptions(VideoSegment{Path: "a.mp4"})
		opts.BurnSubtitles = true
		if _, _, err := compileConcatArgs(opts, matchingProbes(1), concatInputs{ListFile: "l"}, false); err == nil {
			t.Fatal("应报错")
		}
	})
}

func TestDriftingSegments(t *testing.T) {
	cases := []struct {
		name     string
		segments []VideoSegment
		probes   []segmentProbe
		want     []int
	}{
		{
			name:     "容差内不报",
			segments: []VideoSegment{{Path: "a", DurationMS: 1000}, {Path: "b", DurationMS: 1000}},
			probes:   []segmentProbe{{DurationMS: 1080}, {DurationMS: 950}},
			want:     nil,
		},
		{
			name:     "超容差报该页",
			segments: []VideoSegment{{Path: "a", DurationMS: 1000}, {Path: "b", DurationMS: 1000}},
			probes:   []segmentProbe{{DurationMS: 1000}, {DurationMS: 1151}},
			want:     []int{1},
		},
		{
			name:     "负向超容差也报",
			segments: []VideoSegment{{Path: "a", DurationMS: 1000}},
			probes:   []segmentProbe{{DurationMS: 849}},
			want:     []int{0},
		},
		{
			name:     "无计划时长不判定",
			segments: []VideoSegment{{Path: "a"}},
			probes:   []segmentProbe{{DurationMS: 5000}},
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := driftingSegments(tc.segments, tc.probes)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
}

func TestEscapeConcatPath(t *testing.T) {
	// 斜杠风格由 filepath.ToSlash 决定，是平台相关的（Windows 转、Linux 不转），
	// 所以这里不断言跨平台转换结果——只断言清单语法真正需要的两件事：
	// 路径能被单引号包住、路径里的单引号被闭合再续。
	if got, want := escapeConcatPath("/tmp/it's.mp4"), `/tmp/it'\''s.mp4`; got != want {
		t.Fatalf("单引号未转义：got %q want %q", got, want)
	}
	if got := escapeConcatPath("/tmp/plain.mp4"); got != "/tmp/plain.mp4" {
		t.Fatalf("普通路径不该被改写：%q", got)
	}
}

func TestParseFrameRate(t *testing.T) {
	if got := parseFrameRate("30000/1001"); got < 29.97 || got > 29.98 {
		t.Fatalf("got %v", got)
	}
	if got := parseFrameRate("30"); got != 30 {
		t.Fatalf("got %v", got)
	}
	if got := parseFrameRate("0/0"); got != 0 {
		t.Fatalf("got %v", got)
	}
}

// requireFFmpeg 返回可用的编码器；缺失时 Skip（本机 Windows 无 ffmpeg，CI 有）。
func requireFFmpeg(t *testing.T) *MP4Encoder {
	t.Helper()
	enc, err := NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	return enc
}

// makeVideoSegment 用 lavfi 造一段无声测试片段。
func makeVideoSegment(t *testing.T, dir, name, color string, w, h, seconds int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "color=c="+color+":s="+strconv.Itoa(w)+"x"+strconv.Itoa(h),
		"-t", strconv.Itoa(seconds), "-r", "30", "-pix_fmt", "yuv420p", "-an", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make segment %s: %v\n%s", name, err, out)
	}
	return path
}

func TestConcatSegmentsEndToEnd(t *testing.T) {
	enc := requireFFmpeg(t)
	dir := t.TempDir()
	a := makeVideoSegment(t, dir, "a.mp4", "red", 320, 240, 1)
	b := makeVideoSegment(t, dir, "b.mp4", "blue", 320, 240, 1)
	out := filepath.Join(dir, "out.mp4")

	res, err := enc.ConcatSegments(context.Background(), ConcatOptions{
		Segments: []VideoSegment{{Path: a, DurationMS: 1000}, {Path: b, DurationMS: 1000}},
		FPS:      30, Width: 320, Height: 240, OutPath: out,
	})
	if err != nil {
		t.Fatalf("ConcatSegments: %v", err)
	}
	if res.Reencoded {
		t.Fatalf("同参数分段应走 copy，实际重编码了")
	}
	if len(res.Drifted) != 0 {
		t.Fatalf("分段时长应在容差内，drifted=%v", res.Drifted)
	}
	if res.Duration < 1.9 || res.Duration > 2.2 {
		t.Fatalf("成片时长应约 2s，got %v", res.Duration)
	}
	if res.Width != 320 || res.Height != 240 {
		t.Fatalf("成片尺寸变了：%dx%d", res.Width, res.Height)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("成片不存在：%v", err)
	}
}

func TestConcatSegmentsDriftReported(t *testing.T) {
	enc := requireFFmpeg(t)
	dir := t.TempDir()
	a := makeVideoSegment(t, dir, "a.mp4", "green", 320, 240, 1)
	out := filepath.Join(dir, "out.mp4")

	// 计划 2s，实际 1s → 偏差 1000ms，应报出来而不是静默拼出短片。
	res, err := enc.ConcatSegments(context.Background(), ConcatOptions{
		Segments: []VideoSegment{{Path: a, DurationMS: 2000}},
		FPS:      30, Width: 320, Height: 240, OutPath: out,
	})
	if err != nil {
		t.Fatalf("ConcatSegments: %v", err)
	}
	if len(res.Drifted) != 1 || res.Drifted[0] != 0 {
		t.Fatalf("应报出第 0 段漂移，got %v", res.Drifted)
	}
}

func TestEncodeStillSegment(t *testing.T) {
	enc := requireFFmpeg(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "still.mp4")

	// 一张纯色 PNG（用 ffmpeg 生成，避免在测试里手写 PNG 编码）。
	pngPath := filepath.Join(dir, "page.png")
	if outBytes, err := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "color=c=gray:s=320x240", "-frames:v", "1", pngPath).CombinedOutput(); err != nil {
		t.Fatalf("make png: %v\n%s", err, outBytes)
	}
	png, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := enc.EncodeStillSegment(context.Background(), png, 1200, out, 30, 320, 240)
	if err != nil {
		t.Fatalf("EncodeStillSegment: %v", err)
	}
	if got.DurationMS != 1200 {
		t.Fatalf("段时长标记错了：%d", got.DurationMS)
	}
	probe, err := enc.probeVideoStream(context.Background(), got.Path)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.DurationMS < 1100 || probe.DurationMS > 1300 {
		t.Fatalf("静态段时长应约 1.2s，got %dms", probe.DurationMS)
	}
	if probe.Width != 320 || probe.Height != 240 {
		t.Fatalf("静态段尺寸不符：%dx%d", probe.Width, probe.Height)
	}
}
