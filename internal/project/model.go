package project

// SchemaVersion 是提取结果的结构版本；解析器/消费方变更时递增，做兼容判断。
const SchemaVersion = "1.0"

// Document 是一次源文件提取的领域结果（页序、正文、备注、形状与特性清单，V4.0 §5.1/5.2）。
// 源文件本身不可变，由 SourceRevision 引用；本结构是可再生成的视图，不直接对应数据库表。
type Document struct {
	SchemaVersion string    `json:"schemaVersion"`
	ParserName    string    `json:"parserName"`    // 适配器实现名（go-pptx）
	ParserVersion string    `json:"parserVersion"` // 适配器版本（v1.0.0）
	Pages         []*Page   `json:"pages"`
	Features      *Features `json:"features,omitempty"`
	Diagnostics   []string  `json:"diagnostics,omitempty"`
}

// Page 是一页的提取结果。页序按 presentation 关系读取，不依赖文件名排序。
type Page struct {
	Index     int      `json:"index"`   // 0 基页序（真实放映顺序）
	SlideID   string   `json:"slideId"` // stable 内部 ID
	Part      string   `json:"part"`    // 源 part 名（ppt/slides/slide1.xml）
	Name      string   `json:"name,omitempty"`
	Hidden    *bool    `json:"hidden,omitempty"` // nil=无法确定（FEAT-003 缺口）
	NotesText string   `json:"notesText,omitempty"`
	Shapes    []*Shape `json:"shapes,omitempty"`
	HasTiming bool     `json:"hasTiming"` // 含 p:timing（动画/计时）
	// 注：p:transition advTm（自动切页）检测受 go-pptx TransitionSpec 限制，
	// 暂不可读，已登记 FEAT-003。
	FeatureFlags []string `json:"featureFlags,omitempty"` // 本页特性提示（未识别的渲染/播放风险）
}

// Shape 是页面形状的只读视图。Text 为形状内全部文本（按阅读顺序拼接的原始提取，
// 不做语义排序；阅读顺序增强属 FEAT-002）。
type Shape struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Kind      string   `json:"kind"`   // go-pptx ShapeKind 字符串
	Opaque    bool     `json:"opaque"` // 未知/未解析形状（SmartArt/OLE/公式/嵌入扩展）
	NodePath  string   `json:"nodePath,omitempty"`
	AltText   string   `json:"altText,omitempty"`
	Text      string   `json:"text,omitempty"`
	ChartType string   `json:"chartType,omitempty"`
	TableRows int      `json:"tableRows,omitempty"`
	TableCols int      `json:"tableCols,omitempty"`
	Bounds    *Box     `json:"bounds,omitempty"`
	Children  []*Shape `json:"children,omitempty"`
}

// Box 是轴对齐矩形（EMU），用于阅读顺序与命中判断。
type Box struct {
	X, Y, Width, Height int64
}

// Features 是全篇特性清单（V4.0 §5.2.6 的逐页报告聚合）。
// 用于渲染兼容提示与"继续导出已支持版本"判断。
type Features struct {
	// OpaqueShapes 未知/未解析形状（SmartArt、OLE、公式等）的数量。
	OpaqueShapes int `json:"opaqueShapes"`
	// Pictures 图片形状数。
	Pictures int `json:"pictures"`
	// AudioClips / VideoClips 嵌入音视频形状数。
	AudioClips int `json:"audioClips"`
	VideoClips int `json:"videoClips"`
	// ChartShapes 图表形状数（嵌入数据可读）。
	ChartShapes int `json:"chartShapes"`
	// TableShapes 表格形状数。
	TableShapes int `json:"tableShapes"`
	// SlidesWithTiming 含 p:timing 的页数（动画/计时）。
	SlidesWithTiming int `json:"slidesWithTiming"`
	// PagesWithNotes 有备注的页数。
	PagesWithNotes int `json:"pagesWithNotes"`
	// PageCount 总页数（含隐藏）。
	PageCount int `json:"pageCount"`
}
