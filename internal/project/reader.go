package project

import (
	"context"
	"errors"
	"io"
	"strings"

	pptx "github.com/F31/go-pptx/v2/pptx"
)

// DocumentReader 读适配器端口（V4.0 §5.1）。页序、正文、备注、表格、图片与特性清单。
// 由 go-pptx 适配实现；渲染与播放能力不在此端口（分别由 integrations 的渲染/TTS 适配承担）。
type DocumentReader interface {
	// Inspect 只做结构与特性扫描，产出领域 Document（含逐页报告）。
	Inspect(ctx context.Context, r io.ReaderAt, size int64) (*Document, error)
}

var (
	// ErrUnsupportedFormat 表示文件不是受支持的 OOXML/PPTX（宏、加密、未知格式）。
	ErrUnsupportedFormat = errors.New("project: unsupported document format")
	// ErrTooLarge 表示超过保护阈值（页数/大小在任务创建前已检查，这里是二次防线）。
	ErrTooLarge = errors.New("project: document exceeds protection limits")
)

// DefaultLimits 是解析保护阈值（V4.0 §3.4 默认边界；压测后调整）。
var DefaultLimits = Limits{MaxPages: 100, MaxBytes: 100 << 20}

// Limits 解析保护阈值。
type Limits struct {
	MaxPages int
	MaxBytes int64
}

// GoPPTXReader 以 go-pptx v2.0.0 实现 DocumentReader。
type GoPPTXReader struct {
	limits  Limits
	parser  string
	version string
}

// NewGoPPTXReader 创建读适配器。limits 为零值时使用 DefaultLimits。
func NewGoPPTXReader(limits Limits) *GoPPTXReader {
	if limits.MaxPages == 0 && limits.MaxBytes == 0 {
		limits = DefaultLimits
	}
	return &GoPPTXReader{limits: limits, parser: "go-pptx", version: "v2.0.0"}
}

// Inspect 打开 OOXML 包并提取领域视图。
func (g *GoPPTXReader) Inspect(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	if size > g.limits.MaxBytes {
		return nil, ErrTooLarge
	}
	p, err := pptx.OpenReader(r, size)
	if err != nil {
		return nil, mapOpenError(err)
	}
	defer p.Close()

	doc, err := g.extract(p)
	if err != nil {
		return nil, err
	}
	if len(doc.Pages) > g.limits.MaxPages {
		return nil, ErrTooLarge
	}
	return doc, nil
}

func (g *GoPPTXReader) extract(p *pptx.Presentation) (*Document, error) {
	// go-pptx v2 起 ir 子包转为内部实现（门面收敛），这里直接用公共对象模型
	// （Presentation → Slide → Shape）投影领域视图，字段语义对齐 v1 ir：
	// 页序遍历、SpeakerNotesText、HasTiming、PartName，组合形状子项平铺到页级。
	slides, err := p.Slides()
	if err != nil {
		return nil, mapOpenError(err)
	}
	doc := &Document{
		SchemaVersion: SchemaVersion,
		ParserName:    g.parser,
		ParserVersion: g.version,
		Pages:         make([]*Page, 0, len(slides)),
		Features:      &Features{},
	}
	for i, s := range slides {
		pg := &Page{
			Index:     i,
			SlideID:   slideIDString(s.ID()),
			Part:      s.PartName(),
			Name:      s.Name(),
			HasTiming: s.HasTiming(),
		}
		// 隐藏页（p:sldId@show="0"）：放映与 PDF 导出都会跳过。读取失败则保持 nil（无法确定），
		// 消费方（如渲染页对齐）按"可见"保守处理。
		if hidden, err := s.Hidden(); err == nil {
			pg.Hidden = &hidden
		}
		if notes, err := s.SpeakerNotesText(); err == nil {
			pg.NotesText = notes
		}
		shapes, err := s.Shapes()
		if err != nil {
			doc.Diagnostics = append(doc.Diagnostics, "IR_SHAPES_READ: "+err.Error())
		}
		for _, sh := range shapes {
			// 组合（GroupShape）：子形状平铺到页级，组本身不入列（v1 ir 语义）。
			if gs, ok := sh.(*pptx.GroupShape); ok && gs != nil {
				if children, err := gs.Children(); err == nil {
					for _, c := range children {
						pg.Shapes = append(pg.Shapes, mapShape(c))
					}
					continue
				}
			}
			pg.Shapes = append(pg.Shapes, mapShape(sh))
		}
		pg.FeatureFlags = collectPageFeatures(pg)
		doc.Pages = append(doc.Pages, pg)
		doc.Features.PageCount++
		if pg.HasTiming {
			doc.Features.SlidesWithTiming++
		}
		if len(pg.NotesText) > 0 {
			doc.Features.PagesWithNotes++
		}
	}
	doc.Features = aggregateFeatures(doc.Pages, doc.Features)
	return doc, nil
}

// mapShape 把 v2 公共对象模型的形状句柄投影为领域 Shape（等价 v1 ir 的
// shapeSummary+projectShape：文本框/自选图形取段落文本，表格取行列与单元格
// 文本，图表取类型与标题/分类摘要，图片等仅元信息）。
func mapShape(sh pptx.Shape) *Shape {
	kind := sh.Kind().String()
	out := &Shape{
		ID:       shapeIDString(sh.ID()),
		Name:     sh.Name(),
		Kind:     kind,
		Opaque:   isOpaqueKind(kind),
		NodePath: sh.NodePath(),
		AltText:  sh.AltText(),
	}
	if b, err := sh.Bounds(); err == nil {
		out.Bounds = &Box{X: int64(b.X), Y: int64(b.Y), Width: int64(b.W), Height: int64(b.H)}
	}
	switch s := sh.(type) {
	case *pptx.TableShape:
		rows, _ := s.RowCount()
		cols, _ := s.ColumnCount()
		out.TableRows = rows
		out.TableCols = cols
		out.Text = tableText(s, rows, cols)
	case *pptx.ChartShape:
		if data, err := s.Data(); err == nil {
			out.ChartType = data.Type.String()
			var sb strings.Builder
			if data.Title != "" {
				sb.WriteString(data.Title)
				sb.WriteString(": ")
			}
			for i, c := range data.Categories {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(c)
			}
			out.Text = sb.String()
		}
	case *pptx.AutoShape:
		out.Text = shapeText(s)
	}
	return out
}

// shapeText 提取文本框/自选图形正文（段落以 '\n' 连接）；无正文返回空串。
func shapeText(s *pptx.AutoShape) string {
	tf, err := s.TextFrame()
	if err != nil {
		return ""
	}
	return textFrameText(tf)
}

// textFrameText 拼接 TextFrame 全部段落文本（'\n' 分隔）。
func textFrameText(tf *pptx.TextFrame) string {
	paragraphs, err := tf.Paragraphs()
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for i, par := range paragraphs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if txt, err := par.Text(); err == nil {
			sb.WriteString(txt)
		}
	}
	return sb.String()
}

// tableText 把表格内容序列化为多行 tab 分隔串。
func tableText(ts *pptx.TableShape, rows, cols int) string {
	if rows <= 0 || cols <= 0 {
		return ""
	}
	var sb strings.Builder
	for r := 0; r < rows; r++ {
		if r > 0 {
			sb.WriteByte('\n')
		}
		for c := 0; c < cols; c++ {
			cell, err := ts.Cell(r, c)
			if err != nil {
				continue
			}
			if c > 0 {
				sb.WriteByte('\t')
			}
			if tf, err := cell.TextFrame(); err == nil {
				sb.WriteString(strings.TrimRight(textFrameText(tf), "\n"))
			}
		}
	}
	return sb.String()
}

// isOpaqueKind 判定未知/未解析形状。go-pptx ShapeKind 字符串为短名
// （"opaque"/"picture"/…），未知枚举值形如 "ShapeKind(n)"。
func isOpaqueKind(kind string) bool {
	return kind == "opaque" || kind == "" || strings.HasPrefix(kind, "ShapeKind(")
}

// collectPageFeatures 汇总单页特性提示。
func collectPageFeatures(pg *Page) []string {
	var flags []string
	for _, sh := range pg.Shapes {
		if sh.Opaque {
			flags = append(flags, "opaque_shape:"+sh.Name)
		}
		switch strings.ToLower(sh.Kind) {
		case "picture":
			flags = append(flags, "picture")
		case "audio":
			flags = append(flags, "audio")
		case "video":
			flags = append(flags, "video")
		case "chart":
			flags = append(flags, "chart")
		case "table":
			flags = append(flags, "table")
		}
	}
	if pg.HasTiming {
		flags = append(flags, "timing")
	}
	return dedupe(flags)
}

func aggregateFeatures(pages []*Page, f *Features) *Features {
	for _, pg := range pages {
		for _, sh := range pg.Shapes {
			switch strings.ToLower(sh.Kind) {
			case "picture":
				f.Pictures++
			case "audio":
				f.AudioClips++
			case "video":
				f.VideoClips++
			case "chart":
				f.ChartShapes++
			case "table":
				f.TableShapes++
			}
			if sh.Opaque {
				f.OpaqueShapes++
			}
		}
	}
	return f
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func slideIDString(id pptx.SlideID) string {
	if id == 0 {
		return ""
	}
	return "slide-" + uint32String(uint32(id))
}

func shapeIDString(id pptx.ShapeID) string {
	if id == 0 {
		return ""
	}
	return "shape-" + uint32String(uint32(id))
}

func uint32String(v uint32) string {
	// 小整数用十进制；避免 fmt 引入额外开销，直接手工转换。
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func mapOpenError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not a zip"), strings.Contains(msg, "malformed"),
		strings.Contains(msg, "office document"), strings.Contains(msg, "content types"):
		return ErrUnsupportedFormat
	default:
		return err
	}
}
