// film-baseline 造一份「包与成片同源」的夹具：同一个 IR 派生成品包与真成片。
//
// 为什么需要它（V13）：dom-ssim.py 比的是「成品包画出来的那一帧」与「成片里的同一帧」。
// 在这之前，CI 里的成片是拿包自己的 DOM 逐帧截出来再编码的——那能验「截图落点 / 抽帧 /
// 解码 / 阈值」这条管线，却验不了「两端是否一致」，因为两端本来就是同一张图。本工具让
// 成片走产品真正的渲染路径（RemotionBackend → renderer/render.mjs → Chromium 逐帧 →
// H.264），包走产品真正的打包路径（app.BundleAnimations + share.BundleBuilder）。
// 两者同源于一份 IR，所以它们**应当**一致；不一致就是缺陷。
//
// 它不能证明的事：镜像里固定的 chrome-headless-shell 与本机 Chrome 不是同一个二进制
// （判据验的是「两端对同一份 IR 的解释一致」，不是「某个 Chrome 版本画得对」）；也不覆盖
// 页内切镜——成片侧的镜头图今天没有任何发布者（见 README「已知缺口」）。
//
// 用法：
//
//	go run ./cmd/film-baseline -out tmp/v13real -chrome /path/to/chrome
//	python3 scripts/review/dom-ssim.py --plan tmp/v13real/render/plan.json \
//	    --pkg tmp/v13real/pkg --segments tmp/v13real/render --out tmp/v13real/gate
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
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/render"
	"github.com/F31/ppts/internal/share"
)

// 与 PowerPoint 16:9 一致的页尺寸（EMU）。显式给，不让编译器去猜——
// 推断出来的页宽会带一条诊断，而夹具里出现诊断就等于「这一份的几何不是我们要的」。
const (
	slideWidthEMU  int64 = 12192000
	slideHeightEMU int64 = 6858000
	fixtureFPS           = 30
	pageTrimUS     int64 = 200_000 // 每页最后一个分段的音频尾部留白，与分享包的时间轴同构
)

func main() {
	var (
		outDir      = flag.String("out", "", "输出目录（必填）：render/ 放成片与 plan.json，pkg/ 放成品包")
		pages       = flag.Int("pages", 2, "页数")
		pageSecs    = flag.Float64("page-secs", 2, "每页时长（秒）")
		width       = flag.Int("width", 1280, "画布宽")
		height      = flag.Int("height", 720, "画布高")
		preset      = flag.String("preset", animdefault.DefaultPresetID, "动效预设（决定转场与错落）")
		chrome      = flag.String("chrome", "", "Chromium 路径（空则取 PPTS_CHROME，再空交给 Remotion 自寻）")
		script      = flag.String("renderer-script", "renderer/render.mjs", "renderer/render.mjs 路径")
		node        = flag.String("node", "", "node 可执行文件（空则取 PPTS_NODE / node）")
		concurrency = flag.Int("concurrency", 1, "帧渲染并发")
		pageCC      = flag.Int("page-concurrency", 2, "页渲染并发")
		crf         = flag.Int("crf", 18, "x264 质量（与产品同档）")
		noRender    = flag.Bool("no-render", false, "只造包与计划，不跑成片（调试用）")
	)
	flag.Parse()

	if *outDir == "" {
		log.Fatal("film-baseline: -out 必填")
	}
	durationMs := int64(math.Round(*pageSecs * 1000))
	if *pages < 1 || durationMs < 500 || *width < 160 || *height < 90 {
		log.Fatal("film-baseline: pages>=1、page-secs>=0.5、画布不小于 160x90")
	}
	if *pageCC < 1 {
		*pageCC = 1
	}

	root, err := filepath.Abs(*outDir)
	if err != nil {
		log.Fatalf("film-baseline: %v", err)
	}
	renderDir := filepath.Join(root, "render")
	stagingDir := filepath.Join(root, "staging")
	pkgDir := filepath.Join(root, "pkg")
	for _, dir := range []string{
		renderDir,
		filepath.Join(renderDir, "pages"),
		filepath.Join(stagingDir, "pages"),
		filepath.Join(stagingDir, "audio"),
		filepath.Join(stagingDir, "subs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("film-baseline: %v", err)
		}
	}

	// 页图与元素框一次算好，三处共用（渲染工作目录、打包暂存目录、IR 的 BaseImage）：
	// 分两次生成的话，任何一个参数写歪都会变成「包与成片比的是两张不同的图」，
	// 而那种失败看起来像两端分歧，实际是夹具自己不一致。
	boxes := elementBoxes(*width, *height)
	pageBytes := make([][]byte, *pages)
	for i := 0; i < *pages; i++ {
		data, err := encodePNG(drawPage(*width, *height, i, boxes))
		if err != nil {
			log.Fatalf("film-baseline: page %d: %v", i, err)
		}
		pageBytes[i] = data
		rel := fmt.Sprintf("pages/p%03d.png", i+1)
		if err := os.WriteFile(filepath.Join(renderDir, filepath.FromSlash(rel)), data, 0o644); err != nil {
			log.Fatalf("film-baseline: write %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(stagingDir, filepath.FromSlash(rel)), data, 0o644); err != nil {
			log.Fatalf("film-baseline: write staging %s: %v", rel, err)
		}
	}

	plan, err := compilePlan(*width, *height, *pages, durationMs, *preset, boxes)
	if err != nil {
		log.Fatalf("film-baseline: %v", err)
	}
	if err := checkPlan(plan); err != nil {
		log.Fatalf("film-baseline: %v", err)
	}

	timeline, err := buildTimeline(*pages, durationMs)
	if err != nil {
		log.Fatalf("film-baseline: %v", err)
	}
	if err := writeStagingAudio(stagingDir, timeline); err != nil {
		log.Fatalf("film-baseline: %v", err)
	}

	// 包侧与成片侧共用同一份 IR：分层素材由 app.BundleAnimations 从 plan 派生，
	// 而渲染服务在 staging 时用同一组 BuildPlate/BuildLayerAssets 从同一张底图派生。
	animPages, err := app.BundleAnimations(plan, pageBytes, nil)
	if err != nil {
		log.Fatalf("film-baseline: bundle animations: %v", err)
	}
	keys := make([]string, *pages)
	for i := range keys {
		keys[i] = fmt.Sprintf("pages/p%03d.png", i+1)
	}
	if _, err := share.BuildLocalToDir(context.Background(), share.LocalBundle{
		Root: stagingDir,
		Input: share.BundleInput{
			Timeline:     timeline,
			PagePNGKeys:  keys,
			PagePNGBytes: pageBytes,
			VTTKey:       "subs/cues.vtt",
			SRTKey:       "subs/cues.srt",
			Pages:        animPages,
			Width:        *width,
			Height:       *height,
			FPS:          fixtureFPS,
			Title:        fmt.Sprintf("成片基线（%d 页 / %dms）", *pages, durationMs),
			SnapshotHash: "sha256:film-baseline",
			CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}, pkgDir); err != nil {
		log.Fatalf("film-baseline: build bundle: %v", err)
	}

	segments := 0
	if !*noRender {
		backend := &render.RemotionBackend{
			Script:          *script,
			NodeBin:         *node,
			Chrome:          *chrome,
			Concurrency:     *concurrency,
			PageConcurrency: *pageCC,
			CRF:             *crf,
			GOP:             30,
			Logf:            func(format string, args ...any) { log.Printf(format, args...) },
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		started := time.Now()
		report, err := backend.Render(ctx, plan, renderDir, func(done, total int) {
			log.Printf("film-baseline: 渲染 %d/%d 页", done, total)
		})
		if err != nil {
			log.Fatalf("film-baseline: render: %v", err)
		}
		// 降级页（渲染失败后换成静态画面）会让这一页的比对失去意义：两端比的是一张
		// 静止图，任何动效分歧都被抹平了。宁可让夹具失败，也不要静默地弱化判据。
		if len(report.Degraded) > 0 {
			log.Fatalf("film-baseline: 有 %d 页降级（首例：page %d %s），夹具不可用：%+v",
				len(report.Degraded), report.Degraded[0].Page, report.Degraded[0].Code, report.Degraded)
		}
		segments = countSegments(renderDir)
		log.Printf("film-baseline: 成片 %d 段，用时 %s", segments, time.Since(started).Round(time.Millisecond))
	}

	summary := map[string]any{
		"out":        root,
		"plan":       filepath.Join(renderDir, "plan.json"),
		"pkg":        pkgDir,
		"segments":   renderDir,
		"pages":      *pages,
		"durationMs": durationMs * int64(*pages),
		"segmentCnt": segments,
		"layers":     countLayers(plan),
	}
	raw, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(raw))
}

// box 是一页里一个动画元素的像素矩形。IR 用 EMU，页图用像素，这里保存像素并在
// 编译前换算——只保留一种单位，避免「同一个元素有两套坐标」这种会悄悄错位的状态。
type box struct{ x, y, w, h int }

// elementBoxes 返回每页的元素框（按比例，随画布缩放）。
//
// 两块矩形刻意不贴边、不互相包含：贴边会让底板填充取不到外环色，包含会让
// ADR-9 的「挖掉嵌套元素」这条路径在夹具里永远不触发——而它正是分层动画最容易出错的地方。
func elementBoxes(w, h int) []box {
	px := func(fx, fy, fw, fh float64) box {
		return box{
			x: int(math.Round(fx * float64(w))), y: int(math.Round(fy * float64(h))),
			w: int(math.Round(fw * float64(w))), h: int(math.Round(fh * float64(h))),
		}
	}
	return []box{
		px(0.08, 0.14, 0.34, 0.26),
		px(0.55, 0.56, 0.37, 0.28),
	}
}

// drawPage 画一页夹具：渐变底 + 斜纹 + 两个带边框的元素块。
//
// 底纹不是装饰：纯色底上「层位移算错 3px」在两端的 SSIM 里几乎不动，斜纹会把
// 亚像素级的错位放大成可见的相位差，判据才真的在量「位置对不对」。
func drawPage(w, h, index int, boxes []box) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	base := color.NRGBA{R: uint8(24 + index*10), G: uint8(34 + index*8), B: uint8(52), A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: base}, image.Point{}, draw.Src)
	for y := 0; y < h; y++ {
		shade := uint8(40 + 60*y/h)
		draw.Draw(img, image.Rect(0, y, w, y+1),
			&image.Uniform{C: color.NRGBA{R: base.R + shade/4, G: base.G + shade/3, B: base.B + shade/2, A: 255}},
			image.Point{}, draw.Src)
	}
	stripe := color.NRGBA{R: 235, G: 235, B: 240, A: 40}
	for x := -h; x < w; x += 48 {
		for y := 0; y < h; y++ {
			xx := x + y
			if xx >= 0 && xx < w {
				img.SetNRGBA(xx, y, blend(img.NRGBAAt(xx, y), stripe))
			}
		}
	}
	palette := []color.NRGBA{
		{R: 232, G: 92, B: 74, A: 255},
		{R: 66, G: 152, B: 214, A: 255},
	}
	for k, b := range boxes {
		rect := image.Rect(b.x, b.y, b.x+b.w, b.y+b.h)
		draw.Draw(img, rect, &image.Uniform{C: palette[k%len(palette)]}, image.Point{}, draw.Src)
		border := color.NRGBA{R: 250, G: 250, B: 250, A: 255}
		draw.Draw(img, image.Rect(b.x, b.y, b.x+b.w, b.y+4), &image.Uniform{C: border}, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(b.x, b.y+b.h-4, b.x+b.w, b.y+b.h), &image.Uniform{C: border}, image.Point{}, draw.Src)
		// 块内的同心小方块：缩放与位移在两端算得不一样时，这里会先露馅。
		inner := image.Rect(b.x+b.w/4, b.y+b.h/4, b.x+b.w*3/4, b.y+b.h*3/4)
		draw.Draw(img, inner, &image.Uniform{C: color.NRGBA{R: 250, G: 250, B: 250, A: 70}}, image.Point{}, draw.Src)
	}
	return img
}

// blend 是 alpha 合成（NRGBA 直通 alpha，这里只用来叠一层很淡的斜纹）。
func blend(dst, src color.NRGBA) color.NRGBA {
	a := float64(src.A) / 255
	return color.NRGBA{
		R: uint8(float64(src.R)*a + float64(dst.R)*(1-a)),
		G: uint8(float64(src.G)*a + float64(dst.G)*(1-a)),
		B: uint8(float64(src.B)*a + float64(dst.B)*(1-a)),
		A: 255,
	}
}

// compilePlan 用产品同一个编译器把夹具编译成 IR。
//
// 走 Compiler 而不是手写 Timeline：手写的计划只能证明「渲染器能读我们以为的格式」，
// 而 Compile 会把 EMU→像素、触发锚点、clip 重叠截断、锚点推导这些规则一起走一遍。
// 夹具里出现诊断就说明输入写歪了，直接失败比「带着诊断往下跑」诚实。
func compilePlan(w, h, pages int, durationMs int64, preset string, boxes []box) (*render.Timeline, error) {
	compiler, err := render.NewCompiler(render.Options{
		Canvas:         design.Canvas{Width: w, Height: h},
		SlideWidthEMU:  slideWidthEMU,
		SlideHeightEMU: slideHeightEMU,
		FPS:            fixtureFPS,
		Background:     "#0B1020",
	})
	if err != nil {
		return nil, fmt.Errorf("compiler: %w", err)
	}
	if _, ok := animation.Catalog().Preset(preset); !ok {
		return nil, fmt.Errorf("预设 %q 不在目录里", preset)
	}
	emuPerPx := float64(slideWidthEMU) / float64(w)

	inputs := make([]render.PageInput, 0, pages)
	for i := 0; i < pages; i++ {
		elements := make([]render.Element, 0, len(boxes))
		tracks := make([]*animation.Track, 0, len(boxes))
		for k, b := range boxes {
			id := fmt.Sprintf("p%d-e%d", i, k)
			elements = append(elements, render.Element{
				ID: id, Kind: animation.KindShape,
				Bounds: &render.EMUBox{
					X: int64(math.Round(float64(b.x) * emuPerPx)), Y: int64(math.Round(float64(b.y) * emuPerPx)),
					Width: int64(math.Round(float64(b.w) * emuPerPx)), Height: int64(math.Round(float64(b.h) * emuPerPx)),
				},
			})
			// 两条 clip 叠在同一层上（淡入 + 上移）：这是产品里最常见的入场，
			// 也是两端最容易分叉的地方——透明度与位移分别求值，再合成一个 transform。
			tracks = append(tracks, &animation.Track{
				ElementID: id, Kind: animation.KindShape,
				Clips: []*animation.Clip{
					{ID: fmt.Sprintf("%s-slide", id), Type: "slideIn", StartMs: int64(k) * 150,
						DurationMs: 700, Easing: "easeOut", Trigger: "onEnter",
						Params: animation.Params{Direction: "up", Offset: 32}},
					{ID: fmt.Sprintf("%s-fade", id), Type: "fadeIn", StartMs: int64(k) * 150,
						DurationMs: 500, Easing: "easeOut", Trigger: "onEnter"},
				},
			})
		}
		inputs = append(inputs, render.PageInput{
			Index:      i,
			SlideID:    fmt.Sprintf("slide-%02d", i+1),
			DurationMs: durationMs,
			BaseImage:  fmt.Sprintf("pages/p%03d.png", i+1),
			Elements:   elements,
			Revision:   &animation.Revision{PresetID: preset, Intensity: 1, Tracks: tracks},
		})
	}
	return compiler.Compile(inputs)
}

// checkPlan 把「夹具不合格」与「判据不通过」分开：这里失败意味着输入本身不成立，
// 后面的 SSIM 跑不跑都没有意义。
func checkPlan(plan *render.Timeline) error {
	if plan == nil || len(plan.Pages) == 0 {
		return fmt.Errorf("计划为空")
	}
	layers, clips := 0, 0
	for _, page := range plan.Pages {
		layers += len(page.Layers)
		for _, layer := range page.Layers {
			clips += len(layer.Clips)
		}
	}
	if layers == 0 || clips == 0 {
		return fmt.Errorf("计划里没有动画层（layers=%d clips=%d）：V13 会变成比两张静态图", layers, clips)
	}
	for _, d := range plan.Warnings {
		// 这些诊断的共同点：编译器**丢掉了**本该存在的东西。带着它们往下跑，
		// 判据覆盖的就不是夹具声明的那份动画了。
		switch d.Code {
		case "plan_motion_failed", "anchor_missing", render.DiagClipTruncated, render.DiagCameraInvalid:
			return fmt.Errorf("编译器诊断 page %d %s: %s", d.Page, d.Code, d.Message)
		}
	}
	return nil
}

// buildTimeline 造时间轴。页首尾相接，每页一条分段、一条字幕。
func buildTimeline(pages int, durationMs int64) (*media.Timeline, error) {
	pageUS := durationMs * 1000
	timeline := &media.Timeline{SchemaVersion: media.TimelineSchemaVersion, DurationUS: int64(pages) * pageUS}
	for i := 0; i < pages; i++ {
		slideID := fmt.Sprintf("slide-%02d", i+1)
		segID := fmt.Sprintf("seg-%02d", i+1)
		start := int64(i) * pageUS
		end := start + pageUS
		timeline.Slides = append(timeline.Slides, media.SlideCue{
			SlideID: slideID, StartUS: start, EndUS: end,
			Segments: []media.SegmentCue{{
				SegmentID: segID, AudioKey: fmt.Sprintf("audio/%03d.wav", i+1),
				StartUS: start, EndUS: end - pageTrimUS,
			}},
		})
		timeline.Subtitles = append(timeline.Subtitles, media.SubtitleCue{
			SlideID: slideID, SegmentID: segID,
			StartUS: start, EndUS: end - pageTrimUS,
			Text: fmt.Sprintf("第 %d 页：两端同源夹具", i+1),
		})
	}
	return timeline, nil
}

// writeStagingAudio 写出静音轨与字幕：成品包要求每段都有音轨与 VTT。
//
// 声音在这里只是打包器的必填项——V13 比的是画面，不读音频。
func writeStagingAudio(staging string, timeline *media.Timeline) error {
	for i, slide := range timeline.Slides {
		seg := slide.Segments[0]
		durationMS := int((seg.EndUS - seg.StartUS) / 1000)
		if err := os.WriteFile(filepath.Join(staging, "audio", fmt.Sprintf("%03d.wav", i+1)),
			silentWAV(durationMS), 0o644); err != nil {
			return err
		}
	}
	vtt := "WEBVTT\n\n"
	for _, sub := range timeline.Subtitles {
		vtt += fmt.Sprintf("%s --> %s\n%s\n\n", vttTime(sub.StartUS), vttTime(sub.EndUS), sub.Text)
	}
	if err := os.WriteFile(filepath.Join(staging, "subs", "cues.vtt"), []byte(vtt), 0o644); err != nil {
		return err
	}
	srt := ""
	for i, sub := range timeline.Subtitles {
		srt += fmt.Sprintf("%d\r\n%s --> %s\r\n%s\r\n\r\n", i+1, srtTime(sub.StartUS), srtTime(sub.EndUS), sub.Text)
	}
	return os.WriteFile(filepath.Join(staging, "subs", "cues.srt"), []byte(srt), 0o644)
}

func vttTime(us int64) string {
	ms := us / 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

func srtTime(us int64) string {
	ms := us / 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

// silentWAV 写一段静音 PCM（24kHz 单声道 16bit），与打包器的音频格式一致。
func silentWAV(durationMS int) []byte {
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
	return buf
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// countSegments 数落盘的分段。渲染器返回成功但少一段是可能的（例如页边界的空页），
// 而「少一段」在 SSIM 里表现为 skip，不会红——所以夹具自己先数一遍。
func countSegments(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "segment_") && strings.HasSuffix(e.Name(), ".mp4") {
			n++
		}
	}
	return n
}

func countLayers(plan *render.Timeline) int {
	n := 0
	for _, page := range plan.Pages {
		n += len(page.Layers)
	}
	return n
}
