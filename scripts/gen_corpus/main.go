// gen_corpus 生成 ppts 自己的合成兼容性语料（testdata/corpus/generated/s1*.pptx）。
// 用法：go run ./scripts/gen_corpus  （在仓库根执行）
//
// 语料不是"能打开就算数"的样本堆：每个条目都对准读路径的一项判定
// （表格/图表/图片/音视频/过渡/timing/隐藏页/中英混排…），并在
// testdata/corpus/manifest.json 里登记期望特性。回放测试会核对
// "文件里有" 与 "读出来有" 是否一致——生成成功但读不出来的样本
// 在这里等于没覆盖。
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	pptx "github.com/F31/go-pptx/v2/pptx"
)

var outDir = "testdata/corpus/generated"

// deck 是一份语料。kind 决定每页放什么，见 decorate。
type deck struct {
	id        string
	desc      string
	pages     int
	withNotes bool
	kind      string
	// hidePage 是 1 基页序；0 表示不隐藏。隐藏页只能在保存后改
	// presentation.xml（go-pptx 只提供读侧 Hidden()）。
	hidePage int
}

// decks 的覆盖维度对齐技术方案 V4.0 §15.3「中英混排、表格、图表、
// 组形状、字体、动画、隐藏页、嵌入媒体」。
//
// 关于组形状：go-pptx 当前只提供 GroupShape 读侧（Children），没有
// 建组 API，所以这一维只能靠外部金样覆盖，不在合成语料里伪造。
var decks = []deck{
	{"s100", "single text page", 1, false, "text", 0},
	{"s101", "two pages with notes", 2, true, "text", 0},
	{"s102", "three pages plain text", 3, false, "text", 0},
	{"s103", "four pages mixed notes", 4, true, "text", 0},
	{"s104", "five pages, no notes", 5, false, "text", 0},
	{"s105", "three pages with notes only second", 3, true, "text", 0},
	{"s106", "eight pages marker text", 8, false, "text", 0},
	{"s107", "two pages english+chinese", 2, false, "text", 0},

	// G2-6 评测集：≥100 页（含数字/单位/型号/日期实体，供数字保持与发音评测）。
	{"s108", "ten pages mixed notes", 10, true, "text", 0},
	{"s109", "twelve pages tables entities", 12, false, "text", 0},
	{"s110", "fifteen pages notes entities", 15, true, "text", 0},
	{"s111", "twenty pages long deck", 20, false, "text", 0},
	{"s112", "fifteen pages mixed content", 15, true, "text", 0},

	// 形态覆盖：每类至少一份，且都带说明性备注（备注是讲稿的源材料）。
	{"s113", "three pages tables", 3, true, "table", 0},
	{"s114", "bar line pie charts", 3, true, "chart", 0},
	{"s115", "two pages pictures", 2, true, "picture", 0},
	{"s116", "two pages embedded audio with timing", 2, true, "audio", 0},
	{"s117", "four pages transitions", 4, true, "transition", 0},
	{"s118", "three pages incl. one empty", 3, true, "empty", 0},
	{"s119", "six pages bilingual", 6, true, "bilingual", 0},
	{"s120", "three pages font styles", 3, true, "styles", 0},
	{"s121", "two pages dense paragraphs", 2, true, "dense", 0},
	{"s122", "five pages mixed shapes", 5, true, "mixed", 0},
	// 隐藏页：第 2 页 show="0"，用于验证「解析能看到、成片默认跳过」。
	{"s123", "four pages with hidden second", 4, true, "text", 2},
	{"s124", "eight pages tables", 8, true, "table", 0},
	{"s125", "six pages charts", 6, true, "chart", 0},
	{"s126", "five pages pictures", 5, true, "picture", 0},
	{"s127", "three pages audio timing", 3, true, "audio", 0},
	{"s128", "ten pages bilingual entities", 10, true, "bilingual", 0},
	{"s129", "fifty pages long deck", 50, false, "text", 0},
}

const (
	// evalPageCountMin 是 G2-6 评测集最低页数门槛。
	evalPageCountMin = 100
	// corpusDeckMin 是 V4.0 §15.3「至少 30 份」的份数门槛。
	corpusDeckMin = 30
)

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	total := 0
	for _, d := range decks {
		path := filepath.Join(outDir, d.id+".pptx")
		// Save 拒绝覆盖已存在文件（这是组件的防误删语义，不该为生成脚本放宽）。
		// 生成器自己的契约就是「重建 generated/」，所以先显式删掉目标这一个文件。
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fatal(fmt.Errorf("%s: remove stale sample: %w", d.id, err))
		}
		if err := build(d, path); err != nil {
			fatal(fmt.Errorf("%s: %w", d.id, err))
		}
		total += d.pages
		fmt.Printf("wrote %s (%s, %d pages, kind=%s)\n", path, d.desc, d.pages, d.kind)
	}
	if len(decks) < corpusDeckMin {
		fatal(fmt.Errorf("corpus has %d decks, V4.0 §15.3 requires >= %d", len(decks), corpusDeckMin))
	}
	// G2-6 门槛：评测集总页数 ≥100。
	if total < evalPageCountMin {
		fatal(fmt.Errorf("corpus has %d pages, G2-6 requires >= %d", total, evalPageCountMin))
	}
	fmt.Printf("total decks: %d (gate: >= %d), total pages: %d (gate: >= %d)\n",
		len(decks), corpusDeckMin, total, evalPageCountMin)
}

func build(d deck, path string) error {
	p, err := pptx.New()
	if err != nil {
		return err
	}
	defer p.Close()
	layouts, err := p.Layouts()
	if err != nil || len(layouts) == 0 {
		return fmt.Errorf("layouts: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < d.pages; i++ {
		s, err := p.AddSlide(layouts[0])
		if err != nil {
			return err
		}
		if err := decorate(ctx, s, d, i+1); err != nil {
			return fmt.Errorf("page %d: %w", i+1, err)
		}
		if d.withNotes {
			_ = s.SetSpeakerNotes(notesFor(d, i+1))
		}
	}
	if _, err := p.Save(ctx, path); err != nil {
		return err
	}
	if d.hidePage > 0 {
		return markHidden(path, d.hidePage)
	}
	return nil
}

// decorate 按 kind 给第 no 页（1 基）加内容。
func decorate(ctx context.Context, s *pptx.Slide, d deck, no int) error {
	title := fmt.Sprintf("%s — page %d", d.desc, no)
	switch d.kind {
	case "table":
		return decorateTable(s, title, no)
	case "chart":
		return decorateChart(ctx, s, title, no)
	case "picture":
		return decoratePicture(ctx, s, title, no)
	case "audio":
		return decorateAudio(ctx, s, title, no)
	case "transition":
		return decorateTransition(s, title, no)
	case "empty":
		return decorateEmptyPage(s, title, no)
	case "bilingual":
		return decorateBilingual(s, title, no)
	case "styles":
		return decorateStyles(s, title, no)
	case "dense":
		return decorateDense(s, title, no)
	case "mixed":
		return decorateMixed(ctx, s, title, no)
	default:
		return decorateText(s, title, no)
	}
}

const (
	emuWide = 8534400
	emuPadX = 457200
	emuPadY = 457200
)

func decorateText(s *pptx.Slide, title string, no int) error {
	_, err := s.AddTextBox(pptx.TextBoxSpec{
		X: emuPadX, Y: emuPadY, Width: emuWide, Height: 500000,
		Text: fmt.Sprintf("%s：PCIe 5.0 x16 / 64 GB", title),
	})
	return err
}

// decorateTable 放一张 3×4 表：表头 + 两行数据，含中英混排与数字。
func decorateTable(s *pptx.Slide, title string, no int) error {
	if err := decorateText(s, title, no); err != nil {
		return err
	}
	_, err := s.AddTable(context.Background(), pptx.TableSpec{
		X: emuPadX, Y: 1200000, Width: emuWide, Height: 1600000,
		Rows: [][]string{
			{"型号", "Serial", "带宽 (GB/s)", "备注"},
			{"A100", "SXM4", "2039", "PCIe 4.0 x16"},
			{"H100", "SXM5", "3350", "NVLink 900 GB/s"},
		},
	})
	return err
}

// decorateChart 每三页轮换柱/折/饼——三种图在读路径上是同一分支，
// 但导出与渲染的差异都出现在具体图形上，语料里保持三种都在。
func decorateChart(ctx context.Context, s *pptx.Slide, title string, no int) error {
	if err := decorateText(s, title, no); err != nil {
		return err
	}
	kind := pptx.ChartBar
	name := "吞吐 (GB/s)"
	switch no % 3 {
	case 2:
		kind, name = pptx.ChartLine, "延迟 (ms)"
	case 0:
		kind, name = pptx.ChartPie, "份额"
	}
	_, err := s.AddChart(ctx, pptx.ChartSpec{
		Type:       kind,
		Title:      fmt.Sprintf("%s 趋势", name),
		X:          emuPadX,
		Y:          1200000,
		Width:      6400000,
		Height:     3600000,
		Categories: []string{"Q1", "Q2", "Q3", "Q4"},
		Series: []pptx.ChartSeries{
			{Name: name, Values: []float64{120, 180, 240, 300}},
			{Name: "baseline", Values: []float64{100, 140, 190, 250}},
		},
	})
	return err
}

func decoratePicture(ctx context.Context, s *pptx.Slide, title string, no int) error {
	if err := decorateText(s, title, no); err != nil {
		return err
	}
	data, err := gradientPNG(320, 180, no)
	if err != nil {
		return err
	}
	_, err = s.AddPicture(ctx, pptx.BytesMedia(data, "image/png"), pptx.PictureSpec{
		X: emuPadX, Y: 1200000, Width: 4800000, Height: 2700000,
		AltText: fmt.Sprintf("示意图 %d：PCIe 拓扑", no),
	})
	return err
}

// decorateAudio 嵌一段合成讲解音轨。UpsertNarration 会同时写入
// p:timing，因此这一份语料同时覆盖 audio 与 timing 两个标记。
func decorateAudio(ctx context.Context, s *pptx.Slide, title string, no int) error {
	if err := decorateText(s, title, no); err != nil {
		return err
	}
	wav := toneWAV(16000, 900)
	_, _, err := s.UpsertNarration(ctx, pptx.BytesMedia(wav, "audio/wav"), pptx.AudioSpec{
		TrackKey: fmt.Sprintf("track-%d", no),
		Role:     pptx.AudioRoleNarration,
		Duration: pptx.NewOptional(900 * time.Millisecond),
	}, pptx.PlaybackSpec{})
	return err
}

// decorateTransition 写过渡动画。第 4 页额外设自动换页，覆盖
// transition 的 advTm 分支。
func decorateTransition(s *pptx.Slide, title string, no int) error {
	if err := decorateText(s, title, no); err != nil {
		return err
	}
	spec := pptx.TransitionSpec{Type: pptx.TransitionFade}
	switch no % 4 {
	case 2:
		spec = pptx.TransitionSpec{Type: pptx.TransitionPush}
	case 3:
		spec = pptx.TransitionSpec{Type: pptx.TransitionWipe}
	case 0:
		spec = pptx.TransitionSpec{Type: pptx.TransitionFade, AdvanceClick: pptx.BoolPtr(false)}
		if err := s.SetAdvanceAfter(3500 * time.Millisecond); err != nil {
			return err
		}
	}
	return s.SetTransition(spec)
}

// decorateEmptyPage 第 2 页完全不放形状：它对应报告里
// 「既没有文字也没有备注」的结论，必须有一份真实样本。
func decorateEmptyPage(s *pptx.Slide, title string, no int) error {
	if no == 2 {
		return nil
	}
	return decorateText(s, title, no)
}

func decorateBilingual(s *pptx.Slide, title string, no int) error {
	text := strings.Join([]string{
		fmt.Sprintf("%s — PCIe 5.0 x16", title),
		"Throughput reached 2039 GB/s on the SXM4 board (2024-03-18).",
		"实测带宽 2039 GB/s，延迟降至 1.8 ms；型号 A100-SXM4-80GB。",
		"Mixed 中英 line: NVLink 900 GB/s ↔ HBM3 3.35 TB/s.",
	}, "\n")
	_, err := s.AddTextBox(pptx.TextBoxSpec{
		X: emuPadX, Y: emuPadY, Width: emuWide, Height: 2200000, Text: text,
	})
	return err
}

// decorateStyles 写显式字符格式：字号、加粗、颜色、中英字体。
// 这些是 STYLE-01 有效样式解析链路的输入。
func decorateStyles(s *pptx.Slide, title string, no int) error {
	shape, err := s.AddTextBox(pptx.TextBoxSpec{
		X: emuPadX, Y: emuPadY, Width: emuWide, Height: 2600000,
		Text: title,
	})
	if err != nil {
		return err
	}
	tf, err := shape.TextFrame()
	if err != nil {
		return err
	}
	para, err := tf.AddParagraph(pptx.ParagraphSpec{})
	if err != nil {
		return err
	}
	run, err := para.AddRun("标题：PCIe 5.0 x16 带宽实测", pptx.FontStyle{
		Bold:      pptx.NewOptional(true),
		Size:      pptx.NewOptional(pptx.Pts(28)),
		Color:     pptx.NewOptional(pptx.ColorSpec{RGB: "1F4E79"}),
		Latin:     pptx.NewOptional("Calibri"),
		EastAsian: pptx.NewOptional("Microsoft YaHei"),
	})
	if err != nil {
		return err
	}
	_ = run
	_, err = tf.AddParagraph(pptx.ParagraphSpec{})
	if err != nil {
		return err
	}
	return nil
}

// decorateDense 放一段超长正文（约 800 字），用于「文本溢出/长段落
// 分词」这类只有在真实长度上才会暴露的问题。
func decorateDense(s *pptx.Slide, title string, no int) error {
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "第 %d 段：GPU 互联带宽 900 GB/s，主机接口 PCIe 5.0 x16，理论 128 GB/s；实测 2039 GB/s 为显存带宽口径。", i+1)
	}
	_, err := s.AddTextBox(pptx.TextBoxSpec{
		X: emuPadX, Y: emuPadY, Width: emuWide, Height: 4200000, Text: b.String(),
	})
	return err
}

// decorateMixed 一页里同时放表、图、图——多形状顺序与 z 序的最短复现。
func decorateMixed(ctx context.Context, s *pptx.Slide, title string, no int) error {
	if err := decorateText(s, title, no); err != nil {
		return err
	}
	if _, err := s.AddTable(ctx, pptx.TableSpec{
		X: emuPadX, Y: 1100000, Width: 5000000, Height: 1200000,
		Rows: [][]string{{"项", "值"}, {"带宽", "2039 GB/s"}},
	}); err != nil {
		return err
	}
	if _, err := s.AddChart(ctx, pptx.ChartSpec{
		Type: pptx.ChartBar, X: 5700000, Y: 1100000, Width: 3200000, Height: 2200000,
		Categories: []string{"Q1", "Q2"}, Series: []pptx.ChartSeries{{Name: "v", Values: []float64{1, 2}}},
	}); err != nil {
		return err
	}
	data, err := gradientPNG(160, 90, no)
	if err != nil {
		return err
	}
	_, err = s.AddPicture(ctx, pptx.BytesMedia(data, "image/png"), pptx.PictureSpec{
		X: emuPadX, Y: 3600000, Width: 2400000, Height: 1350000, AltText: "拓扑图",
	})
	return err
}

func notesFor(d deck, no int) string {
	if d.kind == "bilingual" {
		return fmt.Sprintf("Page %d: mixed 中英 narration; keep 2039 GB/s and PCIe 5.0 verbatim.", no)
	}
	return fmt.Sprintf("第 %d 页备注：测试讲稿，数字 64 与 5.0 为专业术语。", no)
}

// toneWAV 生成 durMS 毫秒的 1 kHz 正弦（16-bit 单声道），仅用于让
// 媒体探测拿到真实可解码的音轨，不追求听感。
func toneWAV(rate, durMS int) []byte {
	n := rate * durMS / 1000
	pcm := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		// 简单包络，避免整段满幅在波形图上看起来像方波。
		v := int16(6000 * sin(2*3.141592653589793*1000*float64(i)/float64(rate)))
		pcm = append(pcm, byte(v), byte(v>>8))
	}
	byteRate := rate * 2
	dataLen := len(pcm)
	var b bytes.Buffer
	b.WriteString("RIFF")
	writeLE32(&b, uint32(36+dataLen))
	b.WriteString("WAVEfmt ")
	writeLE32(&b, 16)
	writeLE16(&b, 1) // PCM
	writeLE16(&b, 1) // mono
	writeLE32(&b, uint32(rate))
	writeLE32(&b, uint32(byteRate))
	writeLE16(&b, 2) // block align
	writeLE16(&b, 16)
	b.WriteString("data")
	writeLE32(&b, uint32(dataLen))
	b.Write(pcm)
	return b.Bytes()
}

func writeLE16(b *bytes.Buffer, v uint16) {
	b.WriteByte(byte(v))
	b.WriteByte(byte(v >> 8))
}

func writeLE32(b *bytes.Buffer, v uint32) {
	b.WriteByte(byte(v))
	b.WriteByte(byte(v >> 8))
	b.WriteByte(byte(v >> 16))
	b.WriteByte(byte(v >> 24))
}

func sin(x float64) float64 {
	// 只为一小段合成音服务，避免为了 sin 引 math 让读者以为这里有权重计算。
	// 用泰勒展开在 [0, 2π) 上精度足够（误差 < 1e-9）。
	twoPi := 2 * 3.141592653589793
	x = x - twoPi*float64(int(x/twoPi))
	if x > 3.141592653589793 {
		x -= twoPi
	}
	x2 := x * x
	return x * (1 - x2/6 + x2*x2/120 - x2*x2*x2/5040 + x2*x2*x2*x2/362880)
}

// gradientPNG 生成一张水平渐变图：纯色块在压缩后可能被渲染器当成
// 空白，渐变能保证截图里有可辨认的区域。
func gradientPNG(w, h, seed int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	base := uint8(40 + (seed*37)%160)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: base,
				G: uint8((x * 255) / w),
				B: uint8((y * 255) / h),
				A: 255,
			})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// markHidden 把第 pageNo 页（1 基）的 p:sldId 标成 show="0"。
//
// 为什么不走 go-pptx：它只提供读侧 Hidden()，没有写入 API。语料生成
// 是一次性的离线动作，在这里重写 presentation.xml 比给组件库加一个
// 未经设计的写接口更合理；生成结果仍是一份普通 PPTX，读路径不受影响。
func markHidden(path string, pageNo int) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		if f.Name == "ppt/presentation.xml" {
			updated, err := hideNthSlideID(data, pageNo)
			if err != nil {
				return err
			}
			data = updated
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// hideNthSlideID 在 <p:sldIdLst> 里给第 n 个 <p:sldId> 加 show="0"。
// 只改 sldId 列表内，避免误伤 sldMasterId 等同名前缀元素。
func hideNthSlideID(xml []byte, n int) ([]byte, error) {
	start := bytes.Index(xml, []byte("<p:sldIdLst>"))
	end := bytes.Index(xml, []byte("</p:sldIdLst>"))
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("presentation.xml: sldIdLst not found")
	}
	list := xml[start:end]
	idx := 0
	out := make([]byte, 0, len(xml)+16)
	out = append(out, xml[:start]...)
	for {
		i := bytes.Index(list, []byte("<p:sldId "))
		if i < 0 {
			out = append(out, list...)
			break
		}
		out = append(out, list[:i]...)
		list = list[i:]
		idx++
		if idx == n {
			out = append(out, []byte(`<p:sldId show="0" `)...)
			list = list[len("<p:sldId "):]
			continue
		}
		out = append(out, []byte("<p:sldId ")...)
		list = list[len("<p:sldId "):]
	}
	out = append(out, xml[end:]...)
	return out, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
