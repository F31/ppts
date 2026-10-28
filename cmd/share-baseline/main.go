// share-baseline 生成一份可复现的成品包体积基线（tasks 6.7）。
//
// 用途：给 M6-4 的体积门禁确定"钱花在哪"。默认跑 5 分钟 / 30 页样本，
// 按底图、音频、播放器、字幕、其他分项统计，并输出 markdown 表格便于贴进文档。
//
// 两个数据来源：
//   - 默认：合成页图（渐变 + 色块），体积偏小，是**下限**，只用于看分项占比；
//   - -pages-dir：用真实导出的页图（PNG），得到可对标线上产物的绝对值。
//
// 工具不写任何临时文件（除非 -out 指定），也不访问网络。
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/share"
)

func main() {
	var (
		pages    = flag.Int("pages", 30, "页数")
		pageSecs = flag.Int("page-secs", 10, "每页时长（秒）")
		width    = flag.Int("width", 1920, "画布宽")
		height   = flag.Int("height", 1080, "画布高")
		pagesDir = flag.String("pages-dir", "", "可选：用该目录下的 PNG 作为页图（按文件名排序）")
		outDir   = flag.String("out", "", "可选：把包写成目录，便于人工核对")
		zipPath  = flag.String("zip", "", "可选：把包写成 zip，便于 share-verify / 分发")
		asJSON   = flag.Bool("json", false, "以 JSON 输出")
	)
	flag.Parse()

	if *pages <= 0 || *pageSecs <= 0 || *width <= 0 || *height <= 0 {
		log.Fatal("share-baseline: pages/page-secs/width/height must be positive")
	}

	source := "synthetic"
	var pagePNGs [][]byte
	if *pagesDir != "" {
		source = "pages-dir"
		var err error
		pagePNGs, err = loadPages(*pagesDir)
		if err != nil {
			log.Fatalf("share-baseline: %v", err)
		}
		if len(pagePNGs) < *pages {
			// 页数超过素材时循环复用，保证总时长仍由 -pages 控制。
			for len(pagePNGs) < *pages {
				pagePNGs = append(pagePNGs, pagePNGs[len(pagePNGs)%len(pagePNGs)])
			}
		}
	}

	timeline := buildTimeline(*pages, *pageSecs)
	reader := &memReader{pages: map[string][]byte{}, objects: map[string][]byte{}}
	keys := make([]string, 0, *pages)
	for i := range timeline.Slides {
		key := fmt.Sprintf("pages/%03d.png", i+1)
		if pagePNGs != nil {
			reader.pages[key] = pagePNGs[i]
		} else {
			reader.pages[key] = syntheticPage(*width, *height, i)
		}
		keys = append(keys, key)
		audioKey := timeline.Slides[i].Segments[0].AudioKey
		reader.objects[audioKey] = synthWAV(int(timeline.Slides[i].Segments[0].EndUS-timeline.Slides[i].Segments[0].StartUS)/1000, i)
	}
	reader.objects["subs/cues.vtt"] = []byte("WEBVTT\n\n00:00:00.000 --> 00:00:10.000\n体积基线样本\n")
	reader.objects["subs/cues.srt"] = []byte("1\r\n00:00:00,000 --> 00:00:10,000\r\n体积基线样本\r\n")

	var buf bytes.Buffer
	start := time.Now()
	manifest, err := share.NewBundleBuilder(reader, nil).Build(context.Background(), share.BundleInput{
		Timeline:     timeline,
		PagePNGKeys:  keys,
		VTTKey:       "subs/cues.vtt",
		SRTKey:       "subs/cues.srt",
		Width:        *width,
		Height:       *height,
		FPS:          30,
		Title:        fmt.Sprintf("体积基线样本（%d 页 / %ds）", *pages, *pages*(*pageSecs)),
		SnapshotHash: "sha256:baseline",
		CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, &buf)
	if err != nil {
		log.Fatalf("share-baseline: %v", err)
	}
	elapsed := time.Since(start)

	_, files, err := share.VerifyBundle(buf.Bytes())
	if err != nil {
		log.Fatalf("share-baseline: verify: %v", err)
	}
	report := summarize(source, manifest, files, buf.Len(), *pages, *pageSecs, elapsed)

	if *zipPath != "" {
		if err := os.WriteFile(*zipPath, buf.Bytes(), 0o644); err != nil {
			log.Fatalf("share-baseline: write zip: %v", err)
		}
		report.ZipPath = *zipPath
	}

	if *outDir != "" {
		if _, err := share.BuildLocalToDir(context.Background(), share.LocalBundle{Root: writeStagingDir(pagePNGs, timeline, *width, *height), Input: share.BundleInput{
			Timeline: timeline, PagePNGKeys: keys, VTTKey: "subs/cues.vtt", SRTKey: "subs/cues.srt",
			Width: *width, Height: *height, FPS: 30, Title: "体积基线样本",
		}}, *outDir); err != nil {
			log.Fatalf("share-baseline: write dir: %v", err)
		}
		report.OutputDir = *outDir
	}

	if *asJSON {
		raw, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(raw))
		return
	}
	printReport(report)
}

type bucket struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

type report struct {
	Source        string   `json:"source"`
	Pages         int      `json:"pages"`
	PageSeconds   int      `json:"pageSeconds"`
	DurationUS    int64    `json:"durationUs"`
	ZipBytes      int64    `json:"zipBytes"`
	Uncompressed  int64    `json:"uncompressedBytes"`
	BuildMillis   int64    `json:"buildMillis"`
	Buckets       []bucket `json:"buckets"`
	OutputDir     string   `json:"outputDir,omitempty"`
	ZipPath       string   `json:"zipPath,omitempty"`
	AudioMixBytes int64    `json:"audioMixBytes"`
}

func summarize(source string, m *share.Manifest, files map[string][]byte, zipBytes, pages, pageSecs int, elapsed time.Duration) report {
	buckets := map[string]*bucket{
		"底图（slides/*.jpg）":       {Name: "底图（slides/*.jpg）"},
		"逐段音频（audio/NNN.wav）":    {Name: "逐段音频（audio/NNN.wav）"},
		"混音轨（audio/mix.wav）":     {Name: "混音轨（audio/mix.wav）"},
		"播放器（app.js/app.css）":    {Name: "播放器（app.js/app.css）"},
		"字幕（subtitles.*）":        {Name: "字幕（subtitles.*）"},
		"其他（index/manifest/时间轴）": {Name: "其他（index/manifest/时间轴）"},
	}
	order := []string{
		"底图（slides/*.jpg）", "逐段音频（audio/NNN.wav）", "混音轨（audio/mix.wav）",
		"播放器（app.js/app.css）", "字幕（subtitles.*）", "其他（index/manifest/时间轴）",
	}
	var total int64
	for name, data := range files {
		key := "其他（index/manifest/时间轴）"
		switch {
		case strings.HasPrefix(name, "slides/"):
			key = "底图（slides/*.jpg）"
		case name == share.FileAudioMix:
			key = "混音轨（audio/mix.wav）"
		case strings.HasPrefix(name, "audio/"):
			key = "逐段音频（audio/NNN.wav）"
		case name == share.FileAppJS || name == share.FileAppCSS:
			key = "播放器（app.js/app.css）"
		case strings.HasPrefix(name, "subtitles."):
			key = "字幕（subtitles.*）"
		}
		buckets[key].Files++
		buckets[key].Bytes += int64(len(data))
		total += int64(len(data))
	}
	out := report{
		Source: source, Pages: pages, PageSeconds: pageSecs,
		DurationUS: m.Artifact.DurationUS, ZipBytes: int64(zipBytes), Uncompressed: total,
		BuildMillis:   elapsed.Milliseconds(),
		AudioMixBytes: int64(len(files[share.FileAudioMix])),
	}
	for _, name := range order {
		out.Buckets = append(out.Buckets, *buckets[name])
	}
	return out
}

func printReport(r report) {
	fmt.Printf("样本：%s，%d 页 × %ds = %ds（%.1f 分钟）\n", r.Source, r.Pages, r.PageSeconds, r.Pages*r.PageSeconds, float64(r.Pages*r.PageSeconds)/60)
	fmt.Printf("构建耗时：%dms，zip：%.1f MB，解压后：%.1f MB\n\n", r.BuildMillis, mb(r.ZipBytes), mb(r.Uncompressed))
	fmt.Println("| 分项 | 文件数 | 体积 | 占解压后 |")
	fmt.Println("| --- | ---: | ---: | ---: |")
	for _, b := range r.Buckets {
		pct := 0.0
		if r.Uncompressed > 0 {
			pct = float64(b.Bytes) / float64(r.Uncompressed) * 100
		}
		fmt.Printf("| %s | %d | %.2f MB | %.1f%% |\n", b.Name, b.Files, mb(b.Bytes), pct)
	}
	fmt.Printf("| **合计** | | **%.2f MB** | 100%% |\n", mb(r.Uncompressed))
	fmt.Println()
	fmt.Println("注：合成页图是纯色渐变，JPEG 体积远小于真实 PPT 截图（后者通常 150–400 KB/页）；")
	fmt.Println("要得到可对标的绝对值，用 -pages-dir 指向真实导出的页图目录重跑。")
	if r.OutputDir != "" {
		fmt.Printf("目录产物：%s\n", r.OutputDir)
	}
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }

// buildTimeline 构造 pages 页、每页 pageSecs 秒的时间轴；段音频略短于页时长，
// 留出 200ms 页间停顿（与线上 narration 的 tailHold 语义一致）。
func buildTimeline(pages, pageSecs int) *media.Timeline {
	pageUS := int64(pageSecs) * 1_000_000
	timeline := &media.Timeline{SchemaVersion: media.TimelineSchemaVersion, DurationUS: int64(pages) * pageUS}
	for i := 0; i < pages; i++ {
		slideID := fmt.Sprintf("slide-%02d", i+1)
		segID := fmt.Sprintf("seg-%02d", i+1)
		start := int64(i) * pageUS
		end := start + pageUS
		segEnd := end - 200_000
		text := fmt.Sprintf("第 %d 页：体积基线样本用于确定音频与底图在成品包中的占比，%d 秒。", i+1, pageSecs)
		timeline.Slides = append(timeline.Slides, media.SlideCue{
			SlideID: slideID, StartUS: start, EndUS: end,
			Segments: []media.SegmentCue{{
				SegmentID: segID, AudioKey: fmt.Sprintf("audio/%03d.wav", i+1), StartUS: start, EndUS: segEnd,
			}},
		})
		timeline.Subtitles = append(timeline.Subtitles, media.SubtitleCue{
			SlideID: slideID, SegmentID: segID, StartUS: start, EndUS: segEnd, Text: text,
		})
	}
	return timeline
}

// syntheticPage 画一页"看起来像 PPT"的图：深色底 + 标题条 + 若干正文块。
// 刻意用大色块而非噪点——噪点会把 JPEG 体积推到不真实的高度。
func syntheticPage(w, h, index int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	base := []color.RGBA{
		{R: 18, G: 28, B: 48, A: 255},
		{R: 30, G: 22, B: 44, A: 255},
		{R: 16, G: 40, B: 34, A: 255},
	}[index%3]
	draw.Draw(img, img.Bounds(), &image.Uniform{C: base}, image.Point{}, draw.Src)
	accent := []color.RGBA{
		{R: 92, G: 148, B: 255, A: 255},
		{R: 190, G: 120, B: 255, A: 255},
		{R: 90, G: 220, B: 170, A: 255},
	}[index%3]

	// 标题条
	fillRect(img, w/12, h/8, w/3, h/16, accent)
	// 正文行：长短交错，模拟真实文字块
	for row := 0; row < 5; row++ {
		y := h/8 + h/10 + row*h/12
		lineW := w/2 + (row%3)*w/12
		fillRect(img, w/12, y, lineW, h/40, color.RGBA{R: 210, G: 216, B: 228, A: 255})
	}
	// 右下角图形，避免整页过于均匀
	fillRect(img, w*3/4, h*2/3, w/8, h/6, color.RGBA{R: accent.R / 2, G: accent.G / 2, B: accent.B / 2, A: 255})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

func fillRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), &image.Uniform{C: c}, image.Point{}, draw.Src)
}

// synthWAV 生成 24kHz 单声道 PCM16 语音状波形（带包络的正弦，避免纯静音让压缩失真）。
func synthWAV(durationMS, index int) []byte {
	const sampleRate = 24000
	frames := durationMS * sampleRate / 1000
	dataSize := frames * 2
	buf := make([]byte, 44+dataSize)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataSize))
	freq := 180.0 + float64(index%5)*20
	for i := 0; i < frames; i++ {
		t := float64(i) / sampleRate
		// 4Hz 包络模拟音节起伏
		env := 0.35 + 0.35*math.Sin(2*math.Pi*4*t)
		v := int16(9000 * env * math.Sin(2*math.Pi*freq*t))
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(v))
	}
	return buf
}

func loadPages(dir string) ([][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("no .png files in %s", dir)
	}
	pages := make([][]byte, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		pages = append(pages, data)
	}
	return pages, nil
}

// writeStagingDir 只在 -out 时使用：把素材落到临时目录，走与隐私模式完全相同的
// BuildLocalToDir 路径，顺便验证"目录产物可用"。
func writeStagingDir(pages [][]byte, timeline *media.Timeline, width, height int) string {
	root, err := os.MkdirTemp("", "share-baseline-*")
	if err != nil {
		log.Fatalf("share-baseline: temp dir: %v", err)
	}
	write := func(name string, data []byte) {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			log.Fatalf("share-baseline: %v", err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			log.Fatalf("share-baseline: %v", err)
		}
	}
	for i, slide := range timeline.Slides {
		if pages != nil {
			write(fmt.Sprintf("pages/%03d.png", i+1), pages[i])
		} else {
			write(fmt.Sprintf("pages/%03d.png", i+1), syntheticPage(width, height, i))
		}
		write(slide.Segments[0].AudioKey, synthWAV(int(slide.Segments[0].EndUS-slide.Segments[0].StartUS)/1000, i))
	}
	write("subs/cues.vtt", []byte("WEBVTT\n\n00:00:00.000 --> 00:00:10.000\n体积基线样本\n"))
	write("subs/cues.srt", []byte("1\r\n00:00:00,000 --> 00:00:10,000\r\n体积基线样本\r\n"))
	raw, err := json.Marshal(timeline)
	if err != nil {
		log.Fatalf("share-baseline: %v", err)
	}
	write("timeline.json", raw)
	return root
}

type memReader struct {
	pages   map[string][]byte
	objects map[string][]byte
}

func (m *memReader) OpenPagePNG(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.pages[key]
	if !ok {
		return nil, fmt.Errorf("page %s not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memReader) OpenObject(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %s not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
