package project

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/F31/go-pptx"
	"github.com/F31/go-pptx/ir"
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

// GoPPTXReader 以 go-pptx v1.0.0 实现 DocumentReader。
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
	return &GoPPTXReader{limits: limits, parser: "go-pptx", version: "v1.0.0"}
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
	irdoc, err := ir.FromPresentation(p, ir.Options{
		IncludeNotes:      true,
		IncludeTimingNode: true,
		IncludeTimingIR:   false,
	})
	if err != nil {
		return nil, mapOpenError(err)
	}
	doc := &Document{
		SchemaVersion: SchemaVersion,
		ParserName:    g.parser,
		ParserVersion: g.version,
		Pages:         make([]*Page, 0, len(irdoc.Pages)),
		Features:      &Features{},
	}
	for i := range irdoc.Pages {
		src := &irdoc.Pages[i]
		pg := &Page{
			Index:     src.Index,
			SlideID:   slideIDString(src.SlideID),
			Part:      src.Part,
			Name:      src.Name,
			NotesText: src.NotesText,
			HasTiming: src.HasTiming,
		}
		for _, sh := range src.Shapes {
			pg.Shapes = append(pg.Shapes, mapShape(&sh))
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
	for _, d := range irdoc.Diagnostics {
		doc.Diagnostics = append(doc.Diagnostics, d.Code+": "+d.Message)
	}
	return doc, nil
}

// mapShape 把 ir.Shape 映射为领域 Shape。
func mapShape(s *ir.Shape) *Shape {
	out := &Shape{
		ID:        shapeIDString(s.ID),
		Name:      s.Name,
		Kind:      s.Kind,
		Opaque:    isOpaqueKind(s.Kind),
		NodePath:  s.NodePath,
		AltText:   s.AltText,
		Text:      s.Text,
		ChartType: s.ChartType,
		TableRows: s.TableRows,
		TableCols: s.TableCols,
	}
	if s.Bounds != nil {
		out.Bounds = &Box{X: s.Bounds.X, Y: s.Bounds.Y, Width: s.Bounds.Width, Height: s.Bounds.Height}
	}
	for i := range s.Children {
		out.Children = append(out.Children, mapShape(&s.Children[i]))
	}
	return out
}

func isOpaqueKind(kind string) bool {
	return strings.HasPrefix(strings.ToLower(kind), "shapeopaque") || kind == "ShapeUnknown" || kind == ""
}

// collectPageFeatures 汇总单页特性提示。
func collectPageFeatures(pg *Page) []string {
	var flags []string
	for _, sh := range pg.Shapes {
		if sh.Opaque {
			flags = append(flags, "opaque_shape:"+sh.Name)
		}
		switch strings.ToLower(sh.Kind) {
		case "shapepicture":
			flags = append(flags, "picture")
		case "shapeaudio":
			flags = append(flags, "audio")
		case "shapevideo":
			flags = append(flags, "video")
		case "shapechart":
			flags = append(flags, "chart")
		case "shapetable":
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
			case "shapepicture":
				f.Pictures++
			case "shapeaudio":
				f.AudioClips++
			case "shapevideo":
				f.VideoClips++
			case "shapechart":
				f.ChartShapes++
			case "shapetable":
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
