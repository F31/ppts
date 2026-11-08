// 讲稿生成：页面装载与页面洞察。
//
// 从 scriptdraft.go 按职责拆出（纯搬移，实现逐字未改）：负责把解析产物读成
// parsedPage、给每页算渲染图对象键、抽取视觉锚点与事实，并缓存这几步的结果。
// 拆分动机是原文件 2268 行里混着「编排 / 取数 / 质检 / 提示词 / 事实统计」五类关注点，
// 改任何一处都要在两千行里定位。

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
)

// parsedShape 是 parsedPage 内形状的最小读取视图。
type parsedShape struct {
	ID              string             `json:"id"`
	Kind            string             `json:"kind"`
	Text            string             `json:"text"`
	ChartType       string             `json:"chartType,omitempty"`
	TableCells      [][]string         `json:"tableCells,omitempty"`
	TableRows       int                `json:"tableRows,omitempty"`
	TableCols       int                `json:"tableCols,omitempty"`
	ChartSeries     []parsedChartSerie `json:"chartSeries,omitempty"`
	ChartCategories []string           `json:"chartCategories,omitempty"`
}
type parsedChartSerie struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

// parsedPage 是 document.json 页面的最小读取视图。
type parsedPage struct {
	Index     int           `json:"index"`
	SlideID   string        `json:"slideId"`
	NotesText string        `json:"notesText"`
	Shapes    []parsedShape `json:"shapes"`
}

// draftFact is the first-step common contract between heterogeneous slide elements
// and script generation. It deliberately mirrors SourceAnchor instead of replacing
// it: anchors preserve provenance, facts provide prompt-ready atomic statements.
type draftFact struct {
	ID          string  `json:"id"`
	SlideID     string  `json:"slideId"`
	ShapeID     string  `json:"shapeId,omitempty"`
	ElementKind string  `json:"elementKind"`
	Kind        string  `json:"kind"`
	Value       string  `json:"value"`
	Confidence  float64 `json:"confidence,omitempty"`
	Source      string  `json:"source"`
	// PromptOnly 为 true 时该事实仅供 prompt 组织讲解，不进入校验基准
	// （视觉场景描述等自由文本，低置信度、非确定——M11 教训）。
	PromptOnly bool `json:"promptOnly,omitempty"`
}
type slideInsightSnapshot struct {
	Version string                   `json:"version"`
	SlideID string                   `json:"slideId"`
	Source  string                   `json:"source"`
	Facts   []draftFact              `json:"facts"`
	Anchors []narration.SourceAnchor `json:"anchors"`
}

// slideInsightVersion 是页面洞察快照的格式版本。
//
// v2（2026-11-02）：锚点/facts 不再包含页脚页码与版式编号。v1 的快照把「13」这类
// 页码当成事实缓存了下来，命中旧缓存时过滤逻辑等于没生效——页面照旧"漏数字"降级。
// 改判定口径必须升版本，否则线上旧快照会继续污染校验基准。
const slideInsightVersion = "slide-insight-v2"

func (h *ScriptDraftHandler) loadPages(ctx context.Context, tenantID, projectID string, revisionNo int) ([]parsedPage, error) {
	if projectID == "" {
		return nil, errors.New("script_draft: project_id is required")
	}
	if revisionNo == 0 {
		revisionNo = 1 // G1：脚本生成跟随首个完成的解析版本；后续版本接入时改为查当前版本。
	}
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: srcRevString(revisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := h.objects.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("script_draft: read parsed document: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Pages []parsedPage `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("script_draft: invalid parsed document: %w", err)
	}
	return doc.Pages, nil
}

// renderKeysBySlide 读取页面渲染清单（render/pages.json），返回 slideId → PNG 对象键。
// 页图与源页的对应必须以清单为准：LibreOffice 渲染会跳过隐藏页，按页序号推算（page-%04d）
// 会从第一张隐藏页起整体错位，让视觉锚点读到别的页面。
func (h *ScriptDraftHandler) renderKeysBySlide(ctx context.Context, tenantID, projectID string, revisionNo int) map[string]string {
	if revisionNo == 0 {
		revisionNo = 1
	}
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID, Revision: srcRevString(revisionNo),
		AssetType: "render", AssetID: "pages", Ext: "json",
	}
	rc, _, err := h.objects.Get(ctx, key)
	if err != nil {
		return nil
	}
	defer rc.Close()
	var manifest PageManifest
	if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
		return nil
	}
	out := make(map[string]string, len(manifest.Pages))
	for _, pg := range manifest.Pages {
		if pg.SlideID != "" && pg.Key != "" {
			out[pg.SlideID] = pg.Key
		}
	}
	return out
}
func sourceForAnchors(kind draftInputKind, source string) string {
	switch kind {
	case inputNotes:
		return "notes"
	case inputCustom:
		return "custom"
	case inputExisting:
		return "existing"
	default:
		return source
	}
}

// slideInsight 是「结构化 + 视觉」事实的统一入口（第一步）：把 anchors 投影成 facts，
// 并按 (页, 来源) 缓存到对象存储（insight 快照）。命中快照时既不重算结构事实，
// 也不重调视觉模型——视觉提取是非确定性的，每次重调都可能得到不同描述，
// 会让后续的引用溯源校验不稳定（同一份讲稿今天校验通过、明天校验失败）。
func (h *ScriptDraftHandler) slideInsight(ctx context.Context, vision llm.VisionExtractor, tenantID, projectID string, revisionNo int, language string, pg parsedPage, source string, renderKeys map[string]string) ([]narration.SourceAnchor, []draftFact) {
	if snap, ok := h.loadSlideInsight(ctx, tenantID, projectID, revisionNo, pg.SlideID, source); ok {
		return snap.Anchors, snap.Facts
	}
	anchors := pgAnchors(pg, source)
	anchors = append(anchors, h.visualAnchors(ctx, vision, tenantID, projectID, language, pg, renderKeys)...)
	facts := buildPageFacts(pg, source, anchors)
	if len(anchors) > 0 || len(facts) > 0 {
		h.saveSlideInsight(ctx, tenantID, projectID, revisionNo, pg.SlideID, source, anchors, facts)
	}
	return anchors, facts
}
func (h *ScriptDraftHandler) loadSlideInsight(ctx context.Context, tenantID, projectID string, revisionNo int, slideID, source string) (*slideInsightSnapshot, bool) {
	if h.objects == nil || slideID == "" {
		return nil, false
	}
	rc, _, err := h.objects.Get(ctx, slideInsightKey(tenantID, projectID, revisionNo, slideID, source))
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	var snap slideInsightSnapshot
	if err := json.NewDecoder(rc).Decode(&snap); err != nil {
		return nil, false
	}
	if snap.Version != slideInsightVersion || snap.SlideID != slideID || snap.Source != normalizedInsightSource(source) {
		return nil, false
	}
	return &snap, true
}
func (h *ScriptDraftHandler) saveSlideInsight(ctx context.Context, tenantID, projectID string, revisionNo int, slideID, source string, anchors []narration.SourceAnchor, facts []draftFact) {
	if h.objects == nil || slideID == "" {
		return
	}
	snap := slideInsightSnapshot{
		Version: slideInsightVersion,
		SlideID: slideID,
		Source:  normalizedInsightSource(source),
		Facts:   facts,
		Anchors: anchors,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_ = h.objects.Put(ctx, slideInsightKey(tenantID, projectID, revisionNo, slideID, source), bytes.NewReader(data), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(data), Size: int64(len(data)),
	})
}
func slideInsightKey(tenantID, projectID string, revisionNo int, slideID, source string) objectstore.ObjectKey {
	if revisionNo == 0 {
		revisionNo = 1
	}
	assetID := "slide." + hashBytes([]byte(slideID + "\x00" + normalizedInsightSource(source)))[:16]
	return objectstore.ObjectKey{TenantID: tenantID, ProjectID: projectID, Revision: srcRevString(revisionNo), AssetType: "insight", AssetID: assetID, Ext: "json"}
}
func normalizedInsightSource(source string) string {
	s := strings.TrimSpace(source)
	if s == "" {
		return "layout"
	}
	return s
}
func (h *ScriptDraftHandler) visualAnchors(ctx context.Context, vision llm.VisionExtractor, tenantID, projectID, language string, pg parsedPage, renderKeys map[string]string) []narration.SourceAnchor {
	if vision == nil || pg.SlideID == "" {
		return nil
	}
	rawKey, ok := renderKeys[pg.SlideID]
	if !ok {
		return nil
	}
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return nil
	}
	r, _, err := h.objects.Get(ctx, key)
	if err != nil {
		return nil
	}
	defer r.Close()
	img, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	visual, err := vision.ExtractVisual(ctx, llm.VisualExtractRequest{
		LogicalOpID: tenantID + ":" + projectID + ":" + pg.SlideID + ":visual",
		Language:    language, SlideID: pg.SlideID, ImagePNG: img,
	})
	if err != nil {
		return nil
	}
	out := make([]narration.SourceAnchor, 0, len(visual))
	for _, v := range visual {
		raw := strings.TrimSpace(v.Raw)
		if raw == "" {
			continue
		}
		kind := strings.TrimSpace(v.Kind)
		if kind == "" {
			kind = "visual_text"
		}
		if !strings.HasPrefix(kind, "visual_") {
			kind = "visual_" + kind
		}
		confidence := v.Confidence
		if confidence <= 0 || confidence > 0.85 {
			confidence = 0.7
		}
		out = append(out, narration.SourceAnchor{SlideID: pg.SlideID, Kind: kind, Raw: raw, Confidence: confidence})
	}
	return out
}
