package project

import "strings"

// 导入报告的处置级别。只有两级：解析已经成功，能谈到的是"成片会有什么损失"（warn）
// 与"用户应当知道的事实"（info）。没有 error —— 解析失败根本走不到这里，
// 把失败塞进一份"报告"里会让前端既显示报告又显示失败，语义自相矛盾。
type Severity string

const (
	SeverityWarn Severity = "warn"
	SeverityInfo Severity = "info"
)

// 报告条目代码。前端按代码取文案；后端只负责判定"有没有这件事、影响多少页"。
const (
	// FindingOpaqueShapes：SmartArt / OLE / 公式等无法解析的形状。
	// 成片里这些内容不会以可讲解文本出现，用户需要知道"这页有一块我看不懂"。
	FindingOpaqueShapes = "opaque_shapes"
	// FindingEmbeddedMedia：源文件自带音视频。与配音音轨并存时可能互相抢声。
	FindingEmbeddedMedia = "embedded_media"
	// FindingAnimations：含 p:timing 的页。静态讲解（S 级）不承诺还原动画。
	FindingAnimations = "animations"
	// FindingHiddenPages：放映与 PDF 导出都会跳过的页。
	FindingHiddenPages = "hidden_pages"
	// FindingMissingNotes：没有演讲者备注、需要生成讲稿的页。
	FindingMissingNotes = "missing_notes"
	// FindingPagesWithoutText：既无形状文字也无备注的页，讲稿只能依赖图像理解。
	FindingPagesWithoutText = "pages_without_text"
	// FindingParseDiagnostics：解析器自身留下的诊断（某页形状读取失败等）。
	FindingParseDiagnostics = "parse_diagnostics"
)

// Finding 是一条导入报告结论。
// Pages 用 0 基页序而不是页名：页名可能重复，页序是唯一能指到具体位置的坐标。
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	// Count 是受影响对象的数量：形状数、页数或条目数，按 Code 语义不同。
	Count int   `json:"count"`
	Pages []int `json:"pages,omitempty"`
}

// ReportPage 是逐页报告的一行。它刻意只带摘要（字数、形状数、标记），
// 不带正文 —— 报告页不是阅读器，把全篇文字搬过来会让接口体积翻几十倍。
type ReportPage struct {
	Index       int      `json:"index"`
	SlideID     string   `json:"slideId"`
	Name        string   `json:"name,omitempty"`
	Hidden      bool     `json:"hidden"`
	NotesChars  int      `json:"notesChars"`
	ShapeCount  int      `json:"shapeCount"`
	OpaqueCount int      `json:"opaqueCount"`
	Flags       []string `json:"flags,omitempty"`
}

// ImportReport 是一次源文件解析的可用性报告（V4.0 §5.2 第 6 条"逐页报告"）。
// 它回答三个问题：这份文件被读成了什么、哪些内容成片时会丢、哪些页需要人工确认。
type ImportReport struct {
	SchemaVersion string `json:"schemaVersion"`
	ParserName    string `json:"parserName"`
	ParserVersion string `json:"parserVersion"`
	// PageCount 以解析结果为准（含隐藏页）；revision.page_count 是解析回写的副本，
	// 两者不一致时说明回写没落上，报告要能看出这件事。
	PageCount         int          `json:"pageCount"`
	HiddenPages       int          `json:"hiddenPages"`
	PagesWithNotes    int          `json:"pagesWithNotes"`
	PagesWithoutNotes int          `json:"pagesWithoutNotes"`
	Features          *Features    `json:"features,omitempty"`
	Findings          []Finding    `json:"findings"`
	Diagnostics       []string     `json:"diagnostics,omitempty"`
	Pages             []ReportPage `json:"pages"`
}

// BuildImportReport 从解析结果派生导入报告。
// 纯函数：同一份 Document 永远得到同一份报告（条目顺序固定），便于测试与缓存。
func BuildImportReport(doc *Document) *ImportReport {
	if doc == nil {
		return nil
	}
	rep := &ImportReport{
		SchemaVersion: doc.SchemaVersion,
		ParserName:    doc.ParserName,
		ParserVersion: doc.ParserVersion,
		Features:      doc.Features,
		Findings:      []Finding{},
		Diagnostics:   doc.Diagnostics,
		Pages:         make([]ReportPage, 0, len(doc.Pages)),
	}
	if doc.Features != nil {
		rep.PageCount = doc.Features.PageCount
	}

	var (
		opaquePages  []int
		mediaPages   []int
		timingPages  []int
		hiddenPages  []int
		noNotesPages []int
		noTextPages  []int
		opaqueCount  int
		mediaCount   int
	)
	for _, pg := range doc.Pages {
		if pg == nil {
			continue
		}
		hidden := pg.Hidden != nil && *pg.Hidden
		// 备注先 trim 再判"有没有"：只有空白的备注不是备注。
		// 否则一份"备注栏里打了一个空格"的文件会被报成"每页都有备注"。
		notes := strings.TrimSpace(pg.NotesText)
		row := ReportPage{
			Index:      pg.Index,
			SlideID:    pg.SlideID,
			Name:       pg.Name,
			Hidden:     hidden,
			NotesChars: len([]rune(notes)),
			ShapeCount: len(pg.Shapes),
			Flags:      pg.FeatureFlags,
		}
		hasText := false
		hasMedia := false
		for _, sh := range pg.Shapes {
			if sh == nil {
				continue
			}
			if sh.Opaque {
				row.OpaqueCount++
			}
			if strings.TrimSpace(sh.Text) != "" {
				hasText = true
			}
			switch strings.ToLower(sh.Kind) {
			case "audio", "video":
				hasMedia = true
				mediaCount++
			}
		}
		opaqueCount += row.OpaqueCount
		if row.OpaqueCount > 0 {
			opaquePages = append(opaquePages, pg.Index)
		}
		if hasMedia {
			mediaPages = append(mediaPages, pg.Index)
		}
		if pg.HasTiming {
			timingPages = append(timingPages, pg.Index)
		}
		if hidden {
			hiddenPages = append(hiddenPages, pg.Index)
			rep.HiddenPages++
		}
		if notes != "" {
			rep.PagesWithNotes++
		} else {
			noNotesPages = append(noNotesPages, pg.Index)
		}
		if !hasText && notes == "" {
			noTextPages = append(noTextPages, pg.Index)
		}
		rep.Pages = append(rep.Pages, row)
	}
	rep.PagesWithoutNotes = len(noNotesPages)
	if rep.PageCount == 0 {
		// Features 缺失（旧产物/半截文件）时退回逐页计数，避免报告显示"0 页"。
		rep.PageCount = len(rep.Pages)
	}

	// 条目顺序固定：先 warn 后 info，同级按"越靠前越该先处理"。
	// 顺序稳定使前端不必排序，也让测试能逐条断言。
	if opaqueCount > 0 {
		rep.Findings = append(rep.Findings, Finding{Code: FindingOpaqueShapes, Severity: SeverityWarn, Count: opaqueCount, Pages: opaquePages})
	}
	if mediaCount > 0 {
		rep.Findings = append(rep.Findings, Finding{Code: FindingEmbeddedMedia, Severity: SeverityWarn, Count: mediaCount, Pages: mediaPages})
	}
	if len(noTextPages) > 0 {
		rep.Findings = append(rep.Findings, Finding{Code: FindingPagesWithoutText, Severity: SeverityWarn, Count: len(noTextPages), Pages: noTextPages})
	}
	if len(doc.Diagnostics) > 0 {
		rep.Findings = append(rep.Findings, Finding{Code: FindingParseDiagnostics, Severity: SeverityWarn, Count: len(doc.Diagnostics)})
	}
	if len(timingPages) > 0 {
		rep.Findings = append(rep.Findings, Finding{Code: FindingAnimations, Severity: SeverityInfo, Count: len(timingPages), Pages: timingPages})
	}
	if len(hiddenPages) > 0 {
		rep.Findings = append(rep.Findings, Finding{Code: FindingHiddenPages, Severity: SeverityInfo, Count: len(hiddenPages), Pages: hiddenPages})
	}
	if len(noNotesPages) > 0 {
		rep.Findings = append(rep.Findings, Finding{Code: FindingMissingNotes, Severity: SeverityInfo, Count: len(noNotesPages), Pages: noNotesPages})
	}
	return rep
}
