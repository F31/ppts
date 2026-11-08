// 讲稿生成：页面事实抽取。
//
// 从 scriptdraft.go 按职责拆出（纯搬移，实现逐字未改）：把表格、图表、代码块与
// 结构化形状压成「可引用的事实」，供讲稿按 fact-id 引用并在生成后回查。

package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/narration"
)

// pgAnchors 收集页面形状文本作为来源锚点；无形状时回退到备注。
// source 指定时只收集该来源对应的形状（notes 时回退备注锚点）。
func pgAnchors(pg parsedPage, source string) []narration.SourceAnchor {
	anchors := make([]narration.SourceAnchor, 0, len(pg.Shapes))
	for _, sh := range pg.Shapes {
		if source == "title" && !strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		if source == "body" && strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		if source == "notes" {
			break
		}
		text := strings.TrimSpace(sh.Text)
		if text == "" {
			continue
		}
		// 与 PageSourceText 保持一致：版式编号既不该进讲稿，也不该成为来源锚点，
		// 否则对照面板会显示一个正文里永远不存在的"来源"。
		if IsLayoutOnlyNumber(sh.Kind, text) {
			continue
		}
		kind := "shape_text"
		if strings.TrimSpace(sh.Kind) != "" {
			kind = "shape_" + strings.TrimSpace(sh.Kind)
		}
		anchors = append(anchors, narration.SourceAnchor{
			SlideID: pg.SlideID, ShapeID: strings.TrimSpace(sh.ID), Kind: kind, Raw: text, Confidence: 1.0,
		})
	}
	if len(anchors) == 0 {
		if notes := strings.TrimSpace(pg.NotesText); notes != "" {
			anchors = append(anchors, narration.SourceAnchor{SlideID: pg.SlideID, Kind: "notes", Raw: notes, Confidence: 1.0})
		}
	}
	return anchors
}
func sourceRefsFromAnchors(slideID string, anchors []narration.SourceAnchor) []string {
	seen := map[string]bool{}
	refs := make([]string, 0, len(anchors)+1)
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	add(slideID)
	for _, anchor := range anchors {
		if anchor.ShapeID != "" {
			add(anchor.SlideID + "/" + anchor.ShapeID)
		}
	}
	return refs
}
func factsFromAnchors(slideID string, anchors []narration.SourceAnchor) []draftFact {
	out := make([]draftFact, 0, len(anchors))
	for _, anchor := range anchors {
		raw := strings.TrimSpace(anchor.Raw)
		if raw == "" {
			continue
		}
		kind := strings.TrimSpace(anchor.Kind)
		if kind == "" {
			kind = "shape_text"
		}
		out = append(out, draftFact{
			ID:          factID(len(out)),
			SlideID:     slideID,
			ShapeID:     strings.TrimSpace(anchor.ShapeID),
			ElementKind: factElementKind(kind),
			Kind:        kind,
			Value:       factValue(kind, raw),
			Confidence:  anchor.Confidence,
			Source:      factSource(kind),
			PromptOnly:  isSceneKind(kind),
		})
	}
	return out
}

// buildPageFacts 把一页的结构化 facts（表格/图表统计）与锚点 facts 合并，
// 统一分配 [fact-xx] 编号。结构化 facts 优先（编号靠前），锚点 facts 随后。
func buildPageFacts(pg parsedPage, source string, anchors []narration.SourceAnchor) []draftFact {
	var facts []draftFact
	facts = append(facts, factsFromStructuredShapes(pg, source)...)
	facts = append(facts, factsFromAnchors(pg.SlideID, anchors)...)
	for i := range facts {
		facts[i].ID = factID(i)
	}
	return facts
}

// factsFromStructuredShapes 针对携带结构化数据的形状（表格 TableCells / 图表
// ChartSeries / 代码块文本）产出**确定性的统计事实**，补充锚点文本里缺失的分析信号。
//
// 约束（M11）：不引入原素材没有的必需数字——表格/图表的极值取自单元格与
// 系列数值（这些数字本来就应被讲稿复现），而**行列计数这类元数据不进事实**
// （模型用中文数字「两列」「三行」表述时，数字逐字守门会误杀成 degraded）。
func factsFromStructuredShapes(pg parsedPage, source string) []draftFact {
	var out []draftFact
	for _, sh := range pg.Shapes {
		if containsSourceFilter(source, sh) {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(sh.Kind))
		switch {
		case (strings.Contains(lower, "table") || lower == "shape_table") && len(sh.TableCells) > 0:
			out = append(out, tableStatFacts(pg.SlideID, sh)...)
		case (strings.Contains(lower, "chart") || lower == "shape_chart") && len(sh.ChartSeries) > 0:
			out = append(out, chartStatFacts(pg.SlideID, sh)...)
		case isCodeShape(sh):
			out = append(out, codeBlockFacts(pg.SlideID, sh)...)
		}
	}
	return out
}

// isCodeShape 判定文本形状内容像代码：非空、含代码特征（关键字/符号/等宽多行）。
// 代码块在 PPT 里没有专用 ShapeKind（就是 autoshape/textbox），只能靠内容启发式。
func isCodeShape(sh parsedShape) bool {
	lowerKind := strings.ToLower(strings.TrimSpace(sh.Kind))
	if !(lowerKind == "autoshape" || lowerKind == "textbox" || lowerKind == "shape_autoshape" || lowerKind == "shape_textbox") {
		return false
	}
	text := strings.TrimSpace(sh.Text)
	if text == "" || len(text) > 4000 {
		return false
	}
	return looksLikeCode(text)
}

// looksLikeCode 用轻量启发式判断多行文本是否更像代码而非自然语言：
// 多个代码关键字、常见符号（{}=;()）出现、多行里含缩进或行尾分号。
func looksLikeCode(text string) bool {
	lines := 0
	symbolCount := 0
	for _, ch := range text {
		switch ch {
		case '=', '{', '}', ';', '(', ')', '<', '>':
			symbolCount++
		}
	}
	keywords := []string{"func ", "function ", "class ", "import ", "package ", "def ", "return ", "if ", "for ", "while ", "var ", "let ", "const ", "nil", "null", "true", "false", "TODO", "FIXME"}
	keywordHit := 0
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			keywordHit++
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines++
		}
	}
	// 代码特征：2 行以上 + 足够符号，或 1 行以上 + 关键字命中。
	if lines >= 2 && symbolCount >= 3 {
		return true
	}
	return lines >= 1 && keywordHit >= 2
}

// codeBlockFacts 为代码块产出确定性事实：行数、含注释/关键字提示，
// 引导模型讲"这段代码做什么/关键实现"，而非逐行解释语法。
func codeBlockFacts(slideID string, sh parsedShape) []draftFact {
	text := strings.TrimSpace(sh.Text)
	if text == "" {
		return nil
	}
	lines := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	hasComment := strings.Contains(text, "//") || strings.Contains(text, "#") || strings.Contains(text, "/*")
	// 只讲结构元数据；行数不进校验（模型用中文"十几行"表述会误杀）。
	detail := "代码块"
	if hasComment {
		detail += "，含注释（可参考意图）"
	}
	return []draftFact{{
		SlideID:     slideID,
		ShapeID:     strings.TrimSpace(sh.ID),
		ElementKind: "code",
		Kind:        "code_block",
		Value:       detail + "；请概括这段代码的意图与关键实现，而非逐行解释语法。",
		Confidence:  1.0,
		Source:      "structural",
	}}
}

// containsSourceFilter 复用 pgAnchors 的来源过滤规则：title 只取标题、body 排除标题。
func containsSourceFilter(source string, sh parsedShape) bool {
	if source == "title" && !strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
		return true
	}
	if source == "body" && strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
		return true
	}
	return false
}

// tableStatFacts 从表格单元格提取确定性极值统计。表头行（首行）作为列名；
// 其余行中可解析为数字的列，逐列输出「列名：最大/最小」。
func tableStatFacts(slideID string, sh parsedShape) []draftFact {
	cells := sh.TableCells
	rows := len(cells)
	if rows == 0 {
		return nil
	}
	cols := 0
	for _, r := range cells {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return nil
	}
	headers := make([]string, cols)
	for c := 0; c < cols; c++ {
		headers[c] = strings.TrimSpace(cellAt(cells, 0, c))
	}
	var out []draftFact
	for c := 0; c < cols; c++ {
		minV, maxV, ok := numericColumnRange(cells, rows, c)
		if !ok {
			continue // 该列无数字，不产出统计
		}
		label := headers[c]
		if label == "" {
			label = fmt.Sprintf("第 %d 列", c+1) // 无表头时以列号兜底
			// 列号只是说明性文本，不进入必需数字集合（见函数注释约束）。
		}
		out = append(out, draftFact{
			SlideID:     slideID,
			ShapeID:     strings.TrimSpace(sh.ID),
			ElementKind: "table",
			Kind:        "table_stat",
			Value:       fmt.Sprintf("表格列「%s」数值范围：最小 %s，最大 %s", label, fmtFactNumber(minV), fmtFactNumber(maxV)),
			Confidence:  1.0,
			Source:      "structural",
		})
	}
	// 排名事实：数值列（非首列）中数值最大/最小对应的行维度（通常为表格首列）。
	// 维度单元格是纯文本（无数字），不进校验基准；数值取自行内真实值。
	if rows >= 2 && cols >= 2 {
		if dim := strings.TrimSpace(cellAt(cells, 0, 0)); dim != "" {
			for c := 1; c < cols; c++ {
				minV, maxV, ok := numericColumnRange(cells, rows, c)
				if !ok {
					continue
				}
				minRow, maxRow, ok := rowExtremaRow(cells, rows, c)
				if !ok {
					continue
				}
				minDim := strings.TrimSpace(cellAt(cells, minRow, 0))
				maxDim := strings.TrimSpace(cellAt(cells, maxRow, 0))
				if minDim == "" && maxDim == "" {
					continue
				}
				label := headers[c]
				if label == "" {
					label = fmt.Sprintf("第 %d 列", c+1)
				}
				parts := []string{}
				if maxDim != "" {
					parts = append(parts, fmt.Sprintf("数值最大的维度是「%s」（%s）", maxDim, fmtFactNumber(maxV)))
				}
				if minDim != "" {
					parts = append(parts, fmt.Sprintf("数值最小的维度是「%s」（%s）", minDim, fmtFactNumber(minV)))
				}
				out = append(out, draftFact{
					SlideID:     slideID,
					ShapeID:     strings.TrimSpace(sh.ID),
					ElementKind: "table",
					Kind:        "table_rank",
					Value:       "表格列「" + label + "」： " + strings.Join(parts, "；"),
					Confidence:  1.0,
					Source:      "structural",
				})
			}
		}
	}
	return out
}

// rowExtremaRow 返回数值列第 col 列中数值最大/最小对应的数据行下标
// （首行表头之后）。返回 ok=false 表示该列无有效数字。
func rowExtremaRow(cells [][]string, rows, col int) (minRow, maxRow int, ok bool) {
	minV, maxV := 0.0, 0.0
	for r := 1; r < rows; r++ {
		raw := strings.TrimSpace(cellAt(cells, r, col))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
		if err != nil {
			continue
		}
		if !ok {
			minV, maxV, minRow, maxRow, ok = v, v, r, r, true
			continue
		}
		if v < minV {
			minV, minRow = v, r
		}
		if v > maxV {
			maxV, maxRow = v, r
		}
	}
	return
}

// numericColumnRange 计算第 col 列数据区（首行表头之后）的数值最小/最大。
// 返回 ok=false 表示该列没有任何可解析数字。
func numericColumnRange(cells [][]string, rows, col int) (minV, maxV float64, ok bool) {
	for r := 1; r < rows; r++ {
		raw := strings.TrimSpace(cellAt(cells, r, col))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
		if err != nil {
			continue
		}
		if !ok {
			minV, maxV, ok = v, v, true
			continue
		}
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	return
}

// chartStatFacts 从图表底层数据系列提取确定性极值。每条系列输出
// 「最大值 / 最小值」（含对应类别名），数值即工作簿里的真实值；
// 额外输出趋势事实（上升/下降/波动/持平 + 首末值）。
func chartStatFacts(slideID string, sh parsedShape) []draftFact {
	cats := sh.ChartCategories
	var out []draftFact
	for _, s := range sh.ChartSeries {
		minV, maxV, minIdx, maxIdx, ok := seriesRange(s.Values)
		if !ok {
			continue
		}
		detail := ""
		if len(cats) > 0 {
			minCat, maxCat := categoryAt(cats, minIdx), categoryAt(cats, maxIdx)
			detail = fmt.Sprintf("；最小对应「%s」，最大对应「%s」", minCat, maxCat)
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = "未命名系列"
		}
		out = append(out, draftFact{
			SlideID:     slideID,
			ShapeID:     strings.TrimSpace(sh.ID),
			ElementKind: "chart",
			Kind:        "chart_stat",
			Value:       fmt.Sprintf("图表系列「%s」数值范围：最小 %s，最大 %s%s", name, fmtFactNumber(minV), fmtFactNumber(maxV), detail),
			Confidence:  1.0,
			Source:      "structural",
		})
		if trend, firstV, lastV, trendOK := seriesTrend(s.Values); trendOK && trend != "数据不足" {
			out = append(out, draftFact{
				SlideID:     slideID,
				ShapeID:     strings.TrimSpace(sh.ID),
				ElementKind: "chart",
				Kind:        "chart_trend",
				Value:       fmt.Sprintf("图表系列「%s」整体趋势：%s，起始 %s，结束 %s", name, trend, fmtFactNumber(firstV), fmtFactNumber(lastV)),
				Confidence:  1.0,
				Source:      "structural",
			})
		}
	}
	return out
}

// seriesRange 计算系列数值的最小/最大及其对应索引（接近真实「最小/最大在哪一类」）。
func seriesRange(values []float64) (minV, maxV float64, minIdx, maxIdx int, ok bool) {
	for i, v := range values {
		if !(v >= -1e308 && v <= 1e308) {
			continue // NaN/Inf 不作为统计对象（go-pptx 写入侧本来就拒绝，读侧防御）
		}
		if !ok {
			minV, maxV, minIdx, maxIdx, ok = v, v, i, i, true
			continue
		}
		if v < minV {
			minV, minIdx = v, i
		}
		if v > maxV {
			maxV, maxIdx = v, i
		}
	}
	return
}

// seriesTrend 判定一条系列的趋势。只依赖真实数值，返回：
//   - trend：上升 / 下降 / 波动 / 持平 / 数据不足（长度 < 2 或全 NaN）；
//   - first/last：首/末有效值（供讲解引用，进校验基准）。
func seriesTrend(values []float64) (trend string, first, last float64, ok bool) {
	valid := make([]float64, 0, len(values))
	for _, v := range values {
		if v >= -1e308 && v <= 1e308 {
			valid = append(valid, v)
		}
	}
	if len(valid) < 2 {
		return "数据不足", 0, 0, false
	}
	first, last = valid[0], valid[len(valid)-1]
	// 相对首值的整体变化幅度（避免绝对差受数量级干扰）。
	const relEps = 1e-9
	direction := 0 // +1 上升 / -1 下降 / 0 持平
	switch {
	case last-first > relEps*absF(first):
		direction = 1
	case first-last > relEps*absF(first):
		direction = -1
	}
	// 波动：相邻方向变化次数。单调（0 次翻转）才算明确的上升/下降。
	flips := 0
	lastSign := 0 // 上一次非零变化的方向（+1/-1）；首次非零只初始化不计翻转
	for i := 1; i < len(valid); i++ {
		d := valid[i] - valid[i-1]
		sign := 0
		if d > relEps*absF(valid[i-1]) {
			sign = 1
		} else if d < -relEps*absF(valid[i-1]) {
			sign = -1
		}
		if sign != 0 {
			if lastSign != 0 && sign != lastSign {
				flips++
			}
			lastSign = sign
		}
	}
	switch {
	case direction == 1 && flips == 0:
		return "上升", first, last, true
	case direction == -1 && flips == 0:
		return "下降", first, last, true
	case direction == 0:
		return "持平", first, last, true
	default:
		return "波动", first, last, true
	}
}
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// fmtFactNumber 用定点（非科学计数）格式化事实数值：校验器的数量正则只认十进制，
// 科学计数（1.676e+03）会把它拆成噪声实体导致误判。
func fmtFactNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "-0" {
		return "0"
	}
	return s
}
func cellAt(cells [][]string, r, c int) string {
	if r < len(cells) && c < len(cells[r]) {
		return cells[r][c]
	}
	return ""
}
func categoryAt(cats []string, i int) string {
	if i >= 0 && i < len(cats) {
		return strings.TrimSpace(cats[i])
	}
	return ""
}
func factID(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	if i < len(letters) {
		return "fact-" + string(letters[i])
	}
	return "fact-" + string(letters[i%len(letters)]) + string(letters[(i/len(letters))%len(letters)])
}
func factElementKind(kind string) string {
	lower := strings.ToLower(kind)
	switch {
	case strings.Contains(lower, "table"):
		return "table"
	case strings.Contains(lower, "chart"):
		return "chart"
	case strings.Contains(lower, "code"):
		return "code"
	case strings.HasPrefix(lower, "visual_"):
		return "image"
	case strings.Contains(lower, "notes"):
		return "notes"
	case strings.Contains(lower, "opaque") || strings.Contains(lower, "graphicframe") || strings.Contains(lower, "ole"):
		// SmartArt/OLE/未解析对象框：无法确定性解析，归为 image 走视觉退化讲解。
		return "image"
	default:
		return "text"
	}
}
func factSource(kind string) string {
	if strings.HasPrefix(strings.ToLower(kind), "visual_") {
		return "visual"
	}
	return "structural"
}

// isSceneKind 判断该 kind 是否视觉场景描述（自由文本、低置信度）——
// 这类事实只进 prompt、不进校验基准（M11）。
func isSceneKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "visual_scene")
}
func factValue(kind, raw string) string {
	switch factElementKind(kind) {
	case "table":
		return "表格内容：" + raw
	case "chart":
		return "图表内容：" + raw
	case "image":
		if isSceneKind(kind) {
			return "整页视觉场景：" + raw
		}
		return "图片/页面视觉观察：" + raw
	case "notes":
		return "备注内容：" + raw
	default:
		return raw
	}
}
