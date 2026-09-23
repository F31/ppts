package media

import (
	"errors"
	"strings"
	"testing"
)

// 本文件只测 compileEncodeArgs（纯函数）：不依赖 ffmpeg，因此在任何机器上都会真正执行，
// 而不会像 mp4_test.go 的端到端用例那样在缺 ffmpeg 时被 Skip —— 字幕烧录的滤镜接线正是靠它守住。

func argsValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func argsHas(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

// inputsFor 按「页数 + 是否变时长」造出与 Encode 相同形态的输入落点。
func inputsFor(pages int, variable bool) encodeInputs {
	in := encodeInputs{WorkDir: "/work", PageFiles: make([]string, pages)}
	for i := 0; i < pages; i++ {
		if pages == 1 {
			in.PageFiles[i] = "/work/single.png"
			continue
		}
		in.PageFiles[i] = "/work/img-" + itoa(i+1) + ".png"
	}
	if pages > 1 && !variable {
		in.PagePattern = "/work/img-%d.png"
	}
	in.AudioFile = "/work/audio.wav"
	return in
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCompileEncodeArgsSinglePageLoopsImage(t *testing.T) {
	args, total, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 1, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}, {2}}[:1],
	}, inputsFor(1, false))
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	if !argsHas(args, "-loop") {
		t.Fatalf("single page must loop the still image: %v", args)
	}
	// 单页等时长路径用 -vf（不是 filter_complex）。
	if argsHas(args, "-filter_complex") {
		t.Fatalf("single page must not use filter_complex: %v", args)
	}
	vf, _ := argsValue(args, "-vf")
	if !strings.Contains(vf, "force_original_aspect_ratio=decrease") {
		t.Fatalf("vf must keep aspect ratio: %q", vf)
	}
	if total != 1 {
		t.Fatalf("total = %v, want 1", total)
	}
}

func TestCompileEncodeArgsMultiPageUsesImageSequence(t *testing.T) {
	args, total, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 2, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}, {2}},
	}, inputsFor(2, false))
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	seq, ok := argsValue(args, "-i")
	if !ok || seq != "/work/img-%d.png" {
		t.Fatalf("-i sequence = %q (ok=%v), want the %%d pattern", seq, ok)
	}
	if total != 1 {
		t.Fatalf("total = %v, want 2/2 = 1", total)
	}
}

func TestCompileEncodeArgsTimelineFilterChainPinsPageDurations(t *testing.T) {
	args, total, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 20, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}, {2}}, PageDurationsMS: []int64{700, 300},
	}, inputsFor(2, true))
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	fc, ok := argsValue(args, "-filter_complex")
	if !ok {
		t.Fatalf("timeline path must use filter_complex: %v", args)
	}
	want := "[0:v]scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2,setsar=1,trim=duration=0.700,setpts=PTS-STARTPTS[v0];" +
		"[1:v]scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2,setsar=1,trim=duration=0.300,setpts=PTS-STARTPTS[v1];" +
		"[v0][v1]concat=n=2:v=1:a=0[vout]"
	if fc != want {
		t.Fatalf("filter_complex mismatch\n got: %s\nwant: %s", fc, want)
	}
	if !argsHas(args, "-map") {
		t.Fatalf("missing -map: %v", args)
	}
	if v, _ := argsValue(args, "-map"); v != "[vout]" {
		t.Fatalf("first -map = %q, want [vout]", v)
	}
	// 音频输入索引 = 页数（每页各一个输入）。
	if !strings.Contains(strings.Join(args, " "), "-map 2:a:0") {
		t.Fatalf("audio map must target input #2: %v", args)
	}
	if total != 1 {
		t.Fatalf("total = %v, want (700+300)/1000 = 1", total)
	}
	// 注意：可变时长路径下每个输入各自带 -t（页时长），故输出总时长必须看**末尾**的 -t。
	// 其后固定跟 -movflags +faststart（moov 前置，浏览器可渐进播放/拖动）。
	tail := args[len(args)-5:]
	if tail[0] != "-t" || tail[1] != "1.000000" || tail[2] != "-movflags" || tail[3] != "+faststart" || tail[4] != "/out/v.mp4" {
		t.Fatalf("tail = %v, want [-t 1.000000 -movflags +faststart /out/v.mp4]", tail)
	}
}

func TestCompileEncodeArgsBurnsSubtitlesIntoVideoFilter(t *testing.T) {
	args, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 1, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}}, BurnSubtitles: true, SubtitleSRT: []byte("1\n"),
	}, inputsFor(1, false))
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	vf, _ := argsValue(args, "-vf")
	// 必须以 basename 引用（filtergraph 路径转义坑），且追加在 scale/pad 之后。
	if !strings.HasSuffix(vf, ",subtitles=filename="+subtitleFileName) {
		t.Fatalf("vf must end with the subtitles stage: %q", vf)
	}
	if strings.Contains(vf, "/work") {
		t.Fatalf("subtitle path must be a relative basename, got %q", vf)
	}
}

// TestCompileEncodeArgsUsesASSSubtitleFile 守护 ASS 烧录路径：in.SubtitleFile 指向 ASS 时，
// 滤镜必须引用 subtitles.ass（逐行轮换 + 朗读高亮），而不是默认的 subtitles.srt。
func TestCompileEncodeArgsUsesASSSubtitleFile(t *testing.T) {
	in := inputsFor(1, false)
	in.SubtitleFile = subtitleASSFileName
	args, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 1, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}}, BurnSubtitles: true, SubtitleASS: []byte("[Script Info]\n"),
	}, in)
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	vf, _ := argsValue(args, "-vf")
	if !strings.HasSuffix(vf, ",subtitles=filename="+subtitleASSFileName) {
		t.Fatalf("vf must reference the ASS file: %q", vf)
	}
}

func TestCompileEncodeArgsBurnsSubtitlesOntoConcatOutput(t *testing.T) {
	args, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 20, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}, {2}}, PageDurationsMS: []int64{700, 300},
		BurnSubtitles: true, SubtitleSRT: []byte("1\n"),
	}, inputsFor(2, true))
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	fc, _ := argsValue(args, "-filter_complex")
	// concat 先出 [vbase]，字幕作为最后一级产出 [vout]：杜绝"烧录后仍映射旧标签"的静默失效。
	if !strings.Contains(fc, "concat=n=2:v=1:a=0[vbase];[vbase]subtitles=filename="+subtitleFileName+"[vout]") {
		t.Fatalf("concat output must feed the subtitles stage: %q", fc)
	}
	if v, _ := argsValue(args, "-map"); v != "[vout]" {
		t.Fatalf("first -map = %q, want [vout] (the burned label)", v)
	}
}

func TestCompileEncodeArgsLocksSubtitleFont(t *testing.T) {
	args, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 1, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}}, BurnSubtitles: true, SubtitleSRT: []byte("1\n"),
		SubtitleFontName: "WenQuanYi Zen Hei",
	}, inputsFor(1, false))
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	vf, _ := argsValue(args, "-vf")
	want := ",subtitles=filename=" + subtitleFileName + ":force_style='FontName=WenQuanYi Zen Hei'"
	if !strings.HasSuffix(vf, want) {
		t.Fatalf("vf must lock the subtitle font: %q (want suffix %q)", vf, want)
	}
	if strings.Contains(vf, "/work") {
		t.Fatalf("subtitle path must be a relative basename, got %q", vf)
	}
}

func TestCompileEncodeArgsLocksSubtitleFontOnConcat(t *testing.T) {
	args, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", FPS: 20, Width: 320, Height: 180,
		PagePNGs: [][]byte{{1}, {2}}, PageDurationsMS: []int64{700, 300},
		BurnSubtitles: true, SubtitleSRT: []byte("1\n"), SubtitleFontName: "WenQuanYi Zen Hei",
	}, inputsFor(2, true))
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	fc, _ := argsValue(args, "-filter_complex")
	want := "[vbase]subtitles=filename=" + subtitleFileName + ":force_style='FontName=WenQuanYi Zen Hei'[vout]"
	if !strings.Contains(fc, want) {
		t.Fatalf("concat output must lock the subtitle font: %q (want contains %q)", fc, want)
	}
}

func TestCompileEncodeArgsUsesLavfiSilenceWhenNoAudio(t *testing.T) {
	in := inputsFor(1, false)
	in.AudioFile = ""
	args, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", PagePNGs: [][]byte{{1}},
	}, in)
	if err != nil {
		t.Fatalf("compileEncodeArgs: %v", err)
	}
	if !strings.Contains(strings.Join(args, " "), "-f lavfi -i anullsrc=r=44100:cl=stereo") {
		t.Fatalf("must synthesise a silent track: %v", args)
	}
}

func TestCompileEncodeArgsRejectsBurnWithoutSubtitleData(t *testing.T) {
	_, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", PagePNGs: [][]byte{{1}}, BurnSubtitles: true,
	}, inputsFor(1, false))
	if err == nil {
		t.Fatal("burning without SRT data must fail rather than silently produce a video without subtitles")
	}
}

func TestCompileEncodeArgsRejectsBadTimelineInputs(t *testing.T) {
	cases := []struct {
		name string
		opts MP4EncodeOptions
	}{
		{"no pages", MP4EncodeOptions{OutPath: "/out/v.mp4"}},
		{"duration count mismatch", MP4EncodeOptions{OutPath: "/out/v.mp4", PagePNGs: [][]byte{{1}, {2}}, PageDurationsMS: []int64{1}}},
		{"durations and totals both set", MP4EncodeOptions{OutPath: "/out/v.mp4", PagePNGs: [][]byte{{1}}, PageDurationsMS: []int64{1}, Totals: 2}},
		{"non-positive duration", MP4EncodeOptions{OutPath: "/out/v.mp4", PagePNGs: [][]byte{{1}}, PageDurationsMS: []int64{0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := len(tc.opts.PagePNGs)
			in := inputsFor(maxInt(pages, 1), len(tc.opts.PageDurationsMS) > 0)
			if pages == 0 {
				in = encodeInputs{WorkDir: "/work"}
			}
			if _, _, err := compileEncodeArgs(tc.opts, in); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestCompileEncodeArgsRejectsMismatchedInputFiles(t *testing.T) {
	_, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", PagePNGs: [][]byte{{1}, {2}},
	}, encodeInputs{WorkDir: "/work", PageFiles: []string{"/work/img-1.png"}, PagePattern: "/work/img-%d.png"})
	if err == nil {
		t.Fatal("page file count mismatch must be rejected")
	}
}

func TestCompileEncodeArgsRequiresPagePatternForSequence(t *testing.T) {
	_, _, err := compileEncodeArgs(MP4EncodeOptions{
		OutPath: "/out/v.mp4", PagePNGs: [][]byte{{1}, {2}},
	}, encodeInputs{WorkDir: "/work", PageFiles: []string{"/work/img-1.png", "/work/img-2.png"}})
	if err == nil {
		t.Fatal("a multi-page still sequence must carry its image2 pattern")
	}
}

func TestEncodeWithoutFFmpegReportsUnavailable(t *testing.T) {
	// 本机通常没有 ffmpeg；此处只断言「构造器把缺失如实报出来」，不依赖具体环境。
	if _, err := NewMP4Encoder(); err != nil && !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
