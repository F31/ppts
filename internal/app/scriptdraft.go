package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/usage"
	"github.com/F31/ppts/internal/validation"
)

// ScriptDraftSnapshot 是 script_draft 任务的输入快照。
// Mode 指定生成模式；original 直接提取备注原文，polish/ai_generated 经 LLM 润色并由确定性校验兜底。
type ScriptDraftSnapshot struct {
	ProjectID  string   `json:"projectId"`
	RevisionNo int      `json:"revisionNo"` // 0 = 当前最新已解析版本
	Language   string   `json:"language"`
	Mode       string   `json:"mode"`               // original / polish / ai_generated
	SlideIDs   []string `json:"slideIds,omitempty"` // 空 = 全部页面
	// SourceMode 是批量成稿的来源策略：notes_first=备注优先、page_content=页面内容。
	SourceMode    string `json:"sourceMode,omitempty"`
	Audience      string `json:"audience,omitempty"`
	Style         string `json:"style,omitempty"`
	TargetSeconds int    `json:"targetSeconds,omitempty"`
	// Sources 是"无备注页"显式指定的讲稿来源（slideID → kind：layout/title/body/notes/custom）。
	// 由 GenerateDraft handler 注入已存选择；script_draft worker 在 pgText/pgAnchors 中尊重。
	Sources map[string]string `json:"sources,omitempty"`
	// CustomSources 携带 kind=custom 时的自定义文本（slideID → 文本）。
	CustomSources map[string]string `json:"customSources,omitempty"`
	// Overwrite 为 true 时覆盖已有讲稿（用户显式"重新生成讲稿"）；false 时保持幂等（已有分段不覆盖）。
	Overwrite bool `json:"overwrite,omitempty"`
}

// ScriptDraftHandler 是 script_draft 任务的 worker handler：
// 读最新解析 document.json → 逐页生成原文朗读草稿（幂等：已有讲稿不覆盖）。
type ScriptDraftHandler struct {
	scripts  narration.Store
	objects  objectstore.ObjectStore
	polisher llm.TextRewriter
	vision   llm.VisionExtractor
	tokens   LLMTokenAccountant
	// 按租户解析的 LLM 供应商（模型网关）；优先于固定注入。
	polisherFor func(ctx context.Context, tenantID string) (llm.TextRewriter, error)
	visionFor   func(ctx context.Context, tenantID string) (llm.VisionExtractor, error)
	// steps 逐页登记处理结果（生成/跳过），供任务详情与成稿面板计数。
	// 未注入时只跑业务不记账（测试/私有化场景）。
	steps interface {
		MarkStep(context.Context, pipeline.JobStep) error
	}
}

// draftOutcome 是单页成稿的处理结果。跳过**必须可区分原因**：此前静默 return 让
// 「一键成稿」在全部页被跳过时仍显示成功，用户无从判断来源选择是否生效。
type draftOutcome string

const (
	// draftGenerated 该页写出了新讲稿。
	draftGenerated draftOutcome = "generated"
	// draftSkippedExisting 该页已有讲稿且未选择覆盖（默认「仅补空」策略）。
	draftSkippedExisting draftOutcome = "skipped_existing"
	// draftSkippedNoText 该页页面文字与备注均为空，没有可用的成稿素材。
	draftSkippedNoText draftOutcome = "skipped_no_text"
)

// stepTypePage 是逐页成稿步骤的 step_type（与 ingest 的 "pages"、narration 的
// "tts_segment" 同级；前端 enum.jobStepType.page 有对应文案）。
const stepTypePage = "page"

const maxScriptDraftRetryAttempts = 3

// LLMTokenAccountant 是 script_draft 任务预占/结算 LLM token 额度所需的窄能力（G2-5）。
type LLMTokenAccountant interface {
	Reserve(ctx context.Context, tenantID, logicalOperationID string, kind usage.Kind, units float64) (*usage.Reservation, error)
	Settle(ctx context.Context, tenantID, logicalOperationID string, kind usage.Kind, actualUnits float64, priceVersion string) error
}

// NewScriptDraftHandler 创建 handler。
func NewScriptDraftHandler(scripts narration.Store, objects objectstore.ObjectStore) *ScriptDraftHandler {
	return &ScriptDraftHandler{scripts: scripts, objects: objects}
}

// WithPolisher 注入 G2 文案生成供应商；未注入时 polish/ai_generated 模式会显式失败。
func (h *ScriptDraftHandler) WithPolisher(p llm.TextRewriter) *ScriptDraftHandler {
	h.polisher = p
	return h
}

// WithVisualExtractor 注入可选视觉通道；失败时保留结构通道结果。
func (h *ScriptDraftHandler) WithVisualExtractor(v llm.VisionExtractor) *ScriptDraftHandler {
	h.vision = v
	return h
}

// WithSteps 注入逐页步骤记录器，登记每页是生成还是跳过（供任务详情计数）。
func (h *ScriptDraftHandler) WithSteps(s interface {
	MarkStep(context.Context, pipeline.JobStep) error
}) *ScriptDraftHandler {
	h.steps = s
	return h
}

// WithTokenAccounting 注入 LLM token 额度记账；未注入时不做预占/结算（测试/私有化）。
func (h *ScriptDraftHandler) WithTokenAccounting(a LLMTokenAccountant) *ScriptDraftHandler {
	h.tokens = a
	return h
}

// WithTenantPolisher 注入按租户解析的文本改写供应商（模型网关）；优先于 WithPolisher。
func (h *ScriptDraftHandler) WithTenantPolisher(f func(ctx context.Context, tenantID string) (llm.TextRewriter, error)) *ScriptDraftHandler {
	h.polisherFor = f
	return h
}

// WithTenantVision 注入按租户解析的视觉锚点提取器（模型网关）；优先于 WithVisualExtractor。
func (h *ScriptDraftHandler) WithTenantVision(f func(ctx context.Context, tenantID string) (llm.VisionExtractor, error)) *ScriptDraftHandler {
	h.visionFor = f
	return h
}

func (h *ScriptDraftHandler) polisherForTenant(ctx context.Context, tenantID string) (llm.TextRewriter, error) {
	if h.polisherFor != nil {
		return h.polisherFor(ctx, tenantID)
	}
	if h.polisher == nil {
		return nil, errors.New("script_draft: LLM draft mode requires configured text rewriter")
	}
	return h.polisher, nil
}

func (h *ScriptDraftHandler) visionForTenant(ctx context.Context, tenantID string) (llm.VisionExtractor, error) {
	if h.visionFor != nil {
		return h.visionFor(ctx, tenantID)
	}
	if h.vision == nil {
		return nil, nil
	}
	return h.vision, nil
}

// Handle 实现 pipeline.HandlerFunc。
func (h *ScriptDraftHandler) Handle(ctx context.Context, job *pipeline.Job) error {
	var snap ScriptDraftSnapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
		return fmt.Errorf("script_draft: invalid input snapshot: %w", err)
	}
	mode := narration.ScriptMode(snap.Mode)
	if mode == "" {
		mode = narration.ModeOriginal
	}
	switch mode {
	case narration.ModeOriginal:
	case narration.ModePolish, narration.ModeAIGenerated:
		if h.polisher == nil && h.polisherFor == nil {
			return errors.New("script_draft: LLM draft mode requires configured text rewriter")
		}
	default:
		return fmt.Errorf("script_draft: unsupported mode %q", snap.Mode)
	}
	if snap.Language == "" {
		snap.Language = "zh-CN"
	}
	pages, err := h.loadPages(ctx, job.TenantID, snap.ProjectID, snap.RevisionNo)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, id := range snap.SlideIDs {
		want[id] = true
	}
	targets := make([]parsedPage, 0, len(pages))
	for _, pg := range pages {
		if len(want) > 0 && !want[pg.SlideID] {
			continue
		}
		targets = append(targets, pg)
	}
	if len(targets) > 0 {
		_ = pipeline.ReportProgress(ctx, 0)
	}
	for i, pg := range targets {
		source := snap.Sources[pg.SlideID]
		custom := snap.CustomSources[pg.SlideID]
		outcome, err := h.ensureDraft(ctx, job.TenantID, snap.ProjectID, snap.RevisionNo, snap.Language, mode, pg, source, custom, snap)
		if err != nil {
			if retry := pipeline.AsRetry(err); retry != nil && job.Attempt >= maxScriptDraftRetryAttempts {
				return retry.Err
			}
			return err
		}
		// 逐页记账：任务成功但全部跳过时，用户能从任务详情看到「跳过」而不是
		// 一个没有任何解释的成功（A26：失败与未生效都必须可见）。
		h.markPageStep(ctx, job, pg, outcome)
		_ = pipeline.ReportProgress(ctx, ((i+1)*100)/len(targets))
	}
	return nil
}

// parsedPage 是 document.json 页面的最小读取视图。
type parsedPage struct {
	Index     int    `json:"index"`
	SlideID   string `json:"slideId"`
	NotesText string `json:"notesText"`
	Shapes    []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"shapes"`
}

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

// ensureDraft 为单个页面生成草稿，返回该页的处理结果（生成/跳过及原因）。
// source/custom 为该页显式选择的讲稿来源；为空时回退默认行为（见 selectDraftInput）。
func (h *ScriptDraftHandler) ensureDraft(ctx context.Context, tenantID, projectID string, revisionNo int, language string, mode narration.ScriptMode, pg parsedPage, source, custom string, snap ScriptDraftSnapshot) (draftOutcome, error) {
	slideID := pg.SlideID
	if slideID == "" {
		return draftSkippedNoText, nil
	}
	rev, err := h.scripts.Get(ctx, tenantID, projectID, slideID, language)
	if err == nil && len(rev.Segments) > 0 && !snap.Overwrite {
		return draftSkippedExisting, nil // 已存在实际分段，不覆盖用户稿（仅显式"重新生成讲稿"时 overwrite=true）。
	} else if err != nil && !errors.Is(err, narration.ErrNotFound) {
		return "", err
	} else if err != nil {
		rev = nil
	}

	text := selectDraftInput(pg, source, custom, snap.SourceMode, mode, snap.Overwrite, revisionText(rev))
	if text == "" {
		return draftSkippedNoText, nil // 无正文/备注，不生成空讲稿。
	}
	var vision llm.VisionExtractor
	if v, err := h.visionForTenant(ctx, tenantID); err == nil {
		vision = v
	}
	anchors := pgAnchors(pg, source)
	anchors = append(anchors, h.visualAnchors(ctx, vision, tenantID, projectID, revisionNo, language, pg)...)
	refs := sourceRefsFromAnchors(slideID, anchors)
	if len(refs) == 0 {
		refs = []string{slideID}
	}
	displayText, spokenText, err := h.draftText(ctx, tenantID, projectID, slideID, language, mode, text, snap)
	if err != nil {
		return "", err
	}
	if rev == nil {
		rev, err = h.scripts.EnsureExists(ctx, tenantID, projectID, slideID, language, mode)
		if err != nil {
			return "", fmt.Errorf("script_draft: ensure script: %w", err)
		}
	}
	segment := &narration.Segment{
		SegmentID:     fmt.Sprintf("seg-%02d", pg.Index+1),
		DisplayText:   displayText,
		SpokenText:    spokenText,
		SourceRefs:    refs,
		SourceAnchors: anchors,
		Status:        narration.StatusDraft,
	}
	if _, err := h.scripts.Update(ctx, tenantID, projectID, slideID, language, rev.Revision, []*narration.Segment{segment}); err != nil {
		return "", fmt.Errorf("script_draft: write draft: %w", err)
	}
	return draftGenerated, nil
}

// markPageStep 登记单页处理结果。步骤写失败不影响已完成的成稿（计数缺一条好过整任务失败）。
//
// step_key 以 slideId 为幂等键：同一任务的同一页重复执行只留一条步骤（job_steps 有
// UNIQUE(job_id, step_key)），重试不会把计数翻倍。
func (h *ScriptDraftHandler) markPageStep(ctx context.Context, job *pipeline.Job, pg parsedPage, outcome draftOutcome) {
	if h.steps == nil || pg.SlideID == "" {
		return
	}
	state := pipeline.StepSkipped
	if outcome == draftGenerated {
		state = pipeline.StepSuccess
	}
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID,
		StepType: stepTypePage,
		StepKey:  "page:v1:" + pg.SlideID,
		State:    state,
	}
	_ = h.steps.MarkStep(ctx, step)
}

func (h *ScriptDraftHandler) visualAnchors(ctx context.Context, vision llm.VisionExtractor, tenantID, projectID string, revisionNo int, language string, pg parsedPage) []narration.SourceAnchor {
	if vision == nil || pg.SlideID == "" {
		return nil
	}
	if revisionNo == 0 {
		revisionNo = 1
	}
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID, Revision: srcRevString(revisionNo),
		AssetType: "render", AssetID: fmt.Sprintf("page-%04d", pg.Index+1), Ext: "png",
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

func (h *ScriptDraftHandler) draftText(ctx context.Context, tenantID, projectID, slideID, language string, mode narration.ScriptMode, source string, snap ScriptDraftSnapshot) (string, string, error) {
	if mode == narration.ModeOriginal {
		return source, source, nil
	}
	polisher, err := h.polisherForTenant(ctx, tenantID)
	if err != nil {
		return "", "", err
	}
	opID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode)
	if err := h.reserveLLMTokens(ctx, tenantID, opID, source); err != nil {
		return "", "", err
	}
	result, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID:  opID,
		Mode:         string(mode),
		Language:     language,
		SourceText:   source,
		Instructions: draftInstructions(mode, snap),
	})
	if err != nil {
		return "", "", classifyLLMError(fmt.Errorf("script_draft: generate text: %w", err))
	}
	h.settleLLMTokens(ctx, tenantID, opID, result)
	text := strings.TrimSpace(result.Text)
	report := validation.CheckPreserved(source, text)
	if report.OK() {
		return text, text, nil
	}
	// 一次定向修正；仍失败则回退原文，保证零未经批准数字变更。
	guardOpID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode) + ":guard"
	if err := h.reserveLLMTokens(ctx, tenantID, guardOpID, source); err != nil {
		return "", "", err
	}
	fix, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID: guardOpID,
		Mode:        string(mode), Language: language, SourceText: source,
		Instructions: "上一版文案违反数字/单位/型号保持规则。必须保留这些实体：" + strings.Join(validation.FormatEntities(report.Source), ", ") + "。不得新增其它数字。只输出修正后的正文。",
	})
	if err == nil {
		h.settleLLMTokens(ctx, tenantID, guardOpID, fix)
		fixed := strings.TrimSpace(fix.Text)
		if validation.CheckPreserved(source, fixed).OK() {
			return fixed, fixed, nil
		}
	}
	return source, source, nil
}

// reserveLLMTokens 预占 LLM token 额度（G2-5）；超出上限时任务失败。
func (h *ScriptDraftHandler) reserveLLMTokens(ctx context.Context, tenantID, opID, source string) error {
	if h.tokens == nil {
		return nil
	}
	if _, err := h.tokens.Reserve(ctx, tenantID, opID, usage.KindLLMTokens, usage.EstimateLLMTokens(utf8.RuneCountInString(source))); err != nil {
		if errors.Is(err, usage.ErrInsufficientQuota) {
			return fmt.Errorf("script_draft: LLM token quota exceeded: %w", err)
		}
		return fmt.Errorf("script_draft: reserve llm tokens: %w", err)
	}
	return nil
}

// settleLLMTokens 以供应商返回的真实 token 数结算（prompt+completion）。
func (h *ScriptDraftHandler) settleLLMTokens(ctx context.Context, tenantID, opID string, result llm.RewriteResult) {
	if h.tokens == nil {
		return
	}
	actual := float64(result.PromptTokens + result.CompletionTokens)
	if err := h.tokens.Settle(ctx, tenantID, opID, usage.KindLLMTokens, actual, ""); err != nil {
		// 记账失败不阻塞已成功的文案生成（额度账本最终一致性由重试/TTL 兜底）。
		_ = err
	}
}

func draftInstructions(mode narration.ScriptMode, snap ScriptDraftSnapshot) string {
	parts := []string{}
	if strings.TrimSpace(snap.Audience) != "" {
		parts = append(parts, "目标受众："+strings.TrimSpace(snap.Audience)+"。")
	}
	if strings.TrimSpace(snap.Style) != "" {
		parts = append(parts, "讲解风格："+strings.TrimSpace(snap.Style)+"。")
	}
	if snap.TargetSeconds > 0 {
		parts = append(parts, fmt.Sprintf("控制成适合约 %d 秒口播的长度。", snap.TargetSeconds))
	}
	suffix := strings.Join(parts, "") + "必须逐字保留所有数字、单位、日期、型号，不新增未经原文支持的数字。只输出正文。"
	if mode == narration.ModeAIGenerated {
		return "基于当前讲稿生成一段更完整、自然、适合客户演示的讲解稿；可以补足衔接和解释，但不得引入当前讲稿没有支持的事实。" + suffix
	}
	return "把当前讲稿润色为更适合 PPT 演示讲解的自然口播稿；" + suffix
}

func revisionText(rev *narration.Revision) string {
	if rev == nil {
		return ""
	}
	parts := make([]string, 0, len(rev.Segments))
	for _, seg := range rev.Segments {
		if seg == nil {
			continue
		}
		text := strings.TrimSpace(seg.DisplayText)
		if text == "" {
			text = strings.TrimSpace(seg.SpokenText)
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func classifyLLMError(err error) error {
	var retryable *llm.RetryableError
	if !errors.As(err, &retryable) {
		return err
	}
	// 429 速率限制（含免费用户配额耗尽）不可重试：重试只会继续 429，浪费 worker 资源。
	var httpErr interface{ HTTPStatus() int }
	if errors.As(retryable.Err, &httpErr) && httpErr.HTTPStatus() == 429 {
		return retryable.Err // 不包装为 RetryError，pipeline 视为非可重试
	}
	retry := &pipeline.RetryError{Err: err}
	if retryable.RetryAfter > 0 {
		retry.At = time.Now().Add(retryable.RetryAfter)
	}
	return retry
}

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

// pgText 拼接页面形状文本；为空时回退到备注。
// source 指定时只取该来源对应文本；source=custom 时使用 custom 文本；source=notes 时仅用备注。
func pgText(pg parsedPage, source, custom string) string {
	switch source {
	case "notes":
		return strings.TrimSpace(pg.NotesText)
	case "custom":
		if t := strings.TrimSpace(custom); t != "" {
			return t
		}
		return strings.TrimSpace(pg.NotesText)
	}
	var parts []string
	for _, sh := range pg.Shapes {
		if source == "title" && !strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		if source == "body" && strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		if t := strings.TrimSpace(sh.Text); t != "" {
			parts = append(parts, t)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		text = strings.TrimSpace(pg.NotesText)
	}
	return text
}

func pgTextForMode(pg parsedPage, source, custom, sourceMode string) string {
	switch strings.TrimSpace(sourceMode) {
	case "notes_first":
		if notes := strings.TrimSpace(pg.NotesText); notes != "" {
			return notes
		}
		return pgText(pg, "layout", "")
	case "notes_only":
		return strings.TrimSpace(pg.NotesText)
	case "page_content":
		return pgText(pg, "layout", "")
	default:
		return pgText(pg, source, custom)
	}
}

// selectDraftInput 决定单页成稿的来源文本（空串 = 该页没有可用素材，应跳过）。
//
// **来源优先级是本函数的唯一职责**，也是曾经的缺陷所在：
//
//	① 用户显式指定的来源（批量 sourceMode，或页面级 source）永远优先——即使 overwrite=true，
//	   也不得用已有讲稿顶替用户选择的来源；
//	② 只有在「未显式指定来源」时才沿用历史行为：
//	   - 原文模式：取备注（无备注则不生成）；
//	   - overwrite=true：以当前讲稿为输入（单页「重新生成讲稿」= 润色现有稿）。
//
// 缺陷史（2026-09-22）：原实现先按来源算好页面文字，再用 `overwrite && rev != nil` 无条件
// 覆盖成旧讲稿。于是「一键成稿 · 来源=页面内容 · 覆盖全部」实际是把旧讲稿润色一遍，
// 页面文字从未进入提示词；产出看起来"没按页面内容生成"。
func selectDraftInput(pg parsedPage, source, custom, sourceMode string, mode narration.ScriptMode, overwrite bool, existing string) string {
	explicit := strings.TrimSpace(sourceMode) != "" || strings.TrimSpace(source) != ""
	if !explicit {
		if mode == narration.ModeOriginal {
			return strings.TrimSpace(pg.NotesText)
		}
		// 显式重新生成时，润色/AI 生成以用户当前讲稿为输入，而不是重新从 PPT 版面抽取。
		if overwrite {
			if current := strings.TrimSpace(existing); current != "" {
				return current
			}
		}
	}
	return pgTextForMode(pg, source, custom, sourceMode)
}
