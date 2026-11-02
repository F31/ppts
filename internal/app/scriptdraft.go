package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
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
	// 由 GenerateDraft handler 注入已存选择；script_draft worker 在 pgInput/pgAnchors 中尊重。
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
	// notes 是用户在控制台编辑过的单页备注（对象存储侧载文件）；非 nil 时覆盖解析所得备注。
	notes project.SlideNotesStore
	// concurrency 是逐页成稿的并发度（>1 时并行调用 LLM，显著缩短整任务墙钟时间）。
	// <=0 视为 1（串行）。LLM 供应商限流时应调小。
	concurrency int
	// disableCache 关闭内容缓存。默认开启：相同（模式+语言+指令+素材）复用上次产出。
	// 关闭后每次都会调用模型（"重新生成"想要不同措辞时可关）。
	disableCache bool
}

// WithCacheDisabled 关闭讲稿内容缓存（默认开启）。
func (h *ScriptDraftHandler) WithCacheDisabled() *ScriptDraftHandler {
	h.disableCache = true
	return h
}

// stepLister 是 steps 的可选扩展：读取任务已登记的步骤（供重试跳过已完成页）。
// 未实现时重试会从头跑（语义正确但有重复模型调用）。
type stepLister interface {
	ListSteps(ctx context.Context, tenantID, jobID string) ([]pipeline.JobStep, error)
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
	// draftDegraded 该页写出了讲稿，但内容是**未经 AI 加工的原始素材**：数字/单位/型号校验
	// 两轮仍不过，模型的两版产出都被判为不可信，遂保守回退为原文。必须与 draftGenerated
	// 分开——否则界面会把"页面要点片段"报告成"成稿完成"（A26：不得假成功）。
	draftDegraded draftOutcome = "degraded"
)

// draftInputKind 是成稿输入文本的**实际来源**。由 selectDraftInput 单点决定，
// 供 draftInstructions 生成与输入一致的指令（M3）。
type draftInputKind string

const (
	// inputPage 输入来自 PPT 页面形状文字（layout/title/body，以及 page_content 来源）。
	inputPage draftInputKind = "page"
	// inputNotes 输入来自演讲者备注。
	inputNotes draftInputKind = "notes"
	// inputCustom 输入来自用户为该页填写的自定义文本。
	inputCustom draftInputKind = "custom"
	// inputExisting 输入来自该页已有讲稿（未显式指定来源且 overwrite 时沿用）。
	inputExisting draftInputKind = "existing"
)

// draftInput 是单页成稿的输入：文本 + 它的实际来源。
// 两者必须**同源产生**——否则会出现"按页面文字生成、却告诉模型这是在润色现有讲稿"。
type draftInput struct {
	Kind draftInputKind
	Text string
}

// stepTypePage 是逐页成稿步骤的 step_type（与 ingest 的 "pages"、narration 的
// "tts_segment" 同级；前端 enum.jobStepType.page 有对应文案）。
const stepTypePage = "page"

// pageStepKey 是逐页步骤的幂等键（重试时据此判断该页是否已在本任务中成功产出）。
func pageStepKey(slideID string) string { return "page:v1:" + slideID }

const maxScriptDraftRetryAttempts = 3

// llm429Backoff 是无 Retry-After 头时 429 限流的默认重试退避（通常覆盖一分钟窗口）。
const llm429Backoff = 15 * time.Second

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

// WithSlideNotes 注入用户备注存储：成稿时用户编辑过的备注覆盖解析所得备注（空串=显式清空）。
func (h *ScriptDraftHandler) WithSlideNotes(n project.SlideNotesStore) *ScriptDraftHandler {
	h.notes = n
	return h
}

// WithConcurrency 设置逐页成稿的并发度（默认 1=串行）。>1 时并行调用 LLM，
// 让整任务的墙钟时间随并发度下降；需注意供应商限流（可配小一些）。
func (h *ScriptDraftHandler) WithConcurrency(n int) *ScriptDraftHandler {
	h.concurrency = n
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
	// 合并用户在控制台编辑过的备注（与读路径 internal/api/project.go 语义一致）：
	// 用户备注优先于解析所得备注，空串表示显式清空。此前只有读路径合并，
	// 成稿 worker 读的是解析原件 → 讲稿基于旧备注，且"原 PPT 无备注、用户手写备注"的页
	// 会被判为 skipped_no_text（有备注却不生成）。
	if h.notes != nil {
		revisionNo := snap.RevisionNo
		if revisionNo == 0 {
			revisionNo = 1
		}
		if stored, err := h.notes.Get(ctx, job.TenantID, snap.ProjectID, revisionNo); err == nil && stored != nil {
			for i := range pages {
				if n, ok := stored[pages[i].SlideID]; ok {
					pages[i].NotesText = n
				}
			}
		}
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
	if len(targets) == 0 {
		return nil
	}
	// 重试优化：上一轮已成功/降级产出的页直接跳过，避免整任务重跑时重复调用模型（双倍耗时与费用）。
	if job.Attempt > 0 && h.steps != nil {
		if lister, ok := h.steps.(stepLister); ok {
			if steps, err := lister.ListSteps(ctx, job.TenantID, job.ID); err == nil {
				done := make(map[string]struct{}, len(steps))
				for _, st := range steps {
					if st.StepType == stepTypePage && (st.State == pipeline.StepSuccess || st.State == pipeline.StepDegraded) {
						done[st.StepKey] = struct{}{}
					}
				}
				if len(done) > 0 {
					kept := targets[:0]
					for _, pg := range targets {
						if _, ok := done[pageStepKey(pg.SlideID)]; ok {
							continue
						}
						kept = append(kept, pg)
					}
					targets = kept
				}
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	_ = pipeline.ReportProgress(ctx, 0)
	// 页图映射以渲染清单为准（渲染跳过隐藏页，按页序号推算会整体错位），逐页复用同一份清单。
	renderKeys := h.renderKeysBySlide(ctx, job.TenantID, snap.ProjectID, snap.RevisionNo)
	// 按租户解析 LLM 供应商**一次**（此前每页都查一次网关库），供所有页面复用。
	var polisher llm.TextRewriter
	if mode != narration.ModeOriginal {
		p, perr := h.polisherForTenant(ctx, job.TenantID)
		if perr != nil {
			return perr
		}
		polisher = p
	}
	vision, _ := h.visionForTenant(ctx, job.TenantID)

	// 逐页成稿**有界并发**：页面之间无依赖，串行会让整任务墙钟时间 = Σ单页 LLM 延迟。
	// 并发度可配（h.concurrency，默认 1）；低于供应商限流时调小即可。
	concurrency := h.concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(targets) {
		concurrency = len(targets)
	}

	runCtx := ctx
	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, concurrency)
		errMu    sync.Mutex
		dbMu     sync.Mutex // 串行化 SQLite 写：逐页步骤记录与进度上报
		firstErr error
		// firstNonRateLimitErr 只记「非限流」的失败：限流用尽后不该让整任务判死。
		firstNonRateLimitErr error
		done                 atomic.Int64
		total                = int64(len(targets))
	)
	reportDone := func() {
		n := done.Add(1)
		dbMu.Lock()
		_ = pipeline.ReportProgress(runCtx, int(n*100/total))
		dbMu.Unlock()
	}
	for _, pg := range targets {
		pg := pg
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if runCtx.Err() != nil {
				return
			}
			source := snap.Sources[pg.SlideID]
			custom := snap.CustomSources[pg.SlideID]
			outcome, reason, note, err := h.ensureDraft(runCtx, job.TenantID, snap.ProjectID, snap.RevisionNo, snap.Language, mode, pg, source, custom, snap, renderKeys, polisher, vision)
			if err != nil {
				// 单页失败**不牵连其它页**：记该页失败步骤后继续。整任务最终仍返回错误，
				// 借助「重试跳过已完成页」补跑，避免一页超时就让已完成的页白跑一遍。
				if runCtx.Err() != nil {
					return // worker 停机/取消：不写终态，靠租约重领
				}
				dbMu.Lock()
				h.markPageStepState(runCtx, job, pg, pipeline.StepFailed, err.Error())
				dbMu.Unlock()
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				// 限流（429）是外部配额问题，不是这页的内容问题：单独记下来，
				// 重试窗口内照常退避重跑；用尽后只保留该页 failed，不连坐整任务。
				if !isRateLimitError(err) && firstNonRateLimitErr == nil {
					firstNonRateLimitErr = err
				}
				errMu.Unlock()
				reportDone()
				return
			}
			dbMu.Lock()
			h.markPageStep(runCtx, job, pg, outcome, reason, note)
			dbMu.Unlock()
			reportDone()
		}()
	}
	wg.Wait()
	if firstNonRateLimitErr != nil {
		if retry := pipeline.AsRetry(firstNonRateLimitErr); retry != nil && job.Attempt >= maxScriptDraftRetryAttempts {
			return retry.Err
		}
		return firstNonRateLimitErr
	}
	if firstErr != nil {
		// 走到这里说明失败页**全部**是限流（429）：重试窗口内继续退避重跑（跳过已完成页），
		// 窗口用尽后保留单页 failed 步骤并让任务收敛为成功——否则一页配额失败会让
		// 另外十几页的成果整体呈现为"任务失败"，用户无法区分"没生成"和"跑挂了"。
		if retry := pipeline.AsRetry(firstErr); retry != nil && job.Attempt >= maxScriptDraftRetryAttempts {
			return nil
		}
		return firstErr
	}
	return nil
}

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

// ensureDraft 为单个页面生成草稿，返回该页的处理结果（生成/跳过及原因）。
// source/custom 为该页显式选择的讲稿来源；为空时回退默认行为（见 selectDraftInput）。
// renderKeys 为 slideId → 页面 PNG 对象键（来自渲染清单），用于视觉锚点取图。
// polisher/vision 由调用方按任务解析一次后复用（避免每页查网关库）。
func (h *ScriptDraftHandler) ensureDraft(ctx context.Context, tenantID, projectID string, revisionNo int, language string, mode narration.ScriptMode, pg parsedPage, source, custom string, snap ScriptDraftSnapshot, renderKeys map[string]string, polisher llm.TextRewriter, vision llm.VisionExtractor) (draftOutcome, string, string, error) {
	slideID := pg.SlideID
	if slideID == "" {
		return draftSkippedNoText, "", "", nil
	}
	rev, err := h.scripts.Get(ctx, tenantID, projectID, revisionNo, slideID, language)
	if err == nil && len(rev.Segments) > 0 && !snap.Overwrite {
		return draftSkippedExisting, "", "", nil // 已存在实际分段，不覆盖用户稿（仅显式"重新生成讲稿"时 overwrite=true）。
	} else if err != nil && !errors.Is(err, narration.ErrNotFound) {
		return "", "", "", err
	} else if err != nil {
		rev = nil
	}

	// in 同时给出文本与它的**实际来源**：提示词必须据此描述输入，
	// 否则会告诉模型"把当前讲稿润色一遍"，而实际喂进去的是 PPT 页面文字。
	in := selectDraftInput(pg, source, custom, snap.SourceMode, mode, snap.Overwrite, revisionText(rev))
	if in.Text == "" {
		return draftSkippedNoText, "", "", nil // 无正文/备注，不生成空讲稿。
	}
	anchorSource := sourceForAnchors(in.Kind, source)
	anchors, facts := h.slideInsight(ctx, vision, tenantID, projectID, revisionNo, language, pg, anchorSource, renderKeys)
	refs := sourceRefsFromAnchors(slideID, anchors)
	if len(refs) == 0 {
		refs = []string{slideID}
	}
	if in.Kind == inputExisting || in.Kind == inputCustom {
		facts = nil
	}
	res, err := h.draftTextDetail(ctx, tenantID, projectID, slideID, language, mode, in, facts, snap, polisher)
	if err != nil {
		return "", "", "", err
	}
	displayText, spokenText, degraded := res.Display, res.Spoken, res.Degraded
	if rev == nil {
		rev, err = h.scripts.EnsureExists(ctx, tenantID, projectID, revisionNo, slideID, language, mode)
		if err != nil {
			return "", "", "", fmt.Errorf("script_draft: ensure script: %w", err)
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
	if _, err := h.scripts.Update(ctx, tenantID, projectID, revisionNo, slideID, language, rev.Revision, []*narration.Segment{segment}); err != nil {
		return "", "", "", fmt.Errorf("script_draft: write draft: %w", err)
	}
	if degraded {
		// 稿子写进去了，但内容是未经加工的原始素材。返回 degraded 让调用方把它记为
		// degraded 步骤、界面给出"待人工确认"，而不是"成稿完成"。
		return draftDegraded, res.Reason, "", nil
	}
	return draftGenerated, "", res.Note, nil
}

// truncateRunes 按**字符**截断（不是字节）：步骤原因里全是中文，按字节截会把
// 最后一个字切成半个 UTF-8，前端显示成乱码。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max]) + "…"
}

// markPageStep 登记单页处理结果。步骤写失败不影响已完成的成稿（计数缺一条好过整任务失败）。
//
// step_key 以 slideId 为幂等键：同一任务的同一页重复执行只留一条步骤（job_steps 有
// UNIQUE(job_id, step_key)），重试不会把计数翻倍。
// pageStepState 把单页处理结果映射为任务步骤状态。
//
// 降级必须单列一类：记为 success 会让界面把"原始素材当稿"误报为正常生成（A26）。
func pageStepState(outcome draftOutcome) pipeline.JobStepState {
	switch outcome {
	case draftGenerated:
		return pipeline.StepSuccess
	case draftDegraded:
		return pipeline.StepDegraded
	default:
		return pipeline.StepSkipped
	}
}

func (h *ScriptDraftHandler) markPageStep(ctx context.Context, job *pipeline.Job, pg parsedPage, outcome draftOutcome, reason, note string) {
	// 降级原因优先（那才是"这页为什么不能用"）；成功页的 note 是"已放行但请核对"。
	if reason == "" {
		reason = note
	}
	h.markPageStepState(ctx, job, pg, pageStepState(outcome), reason)
}

// markPageStepState 以显式状态登记单页步骤（供 outcome 映射与失败页记录复用）。
func (h *ScriptDraftHandler) markPageStepState(ctx context.Context, job *pipeline.Job, pg parsedPage, state pipeline.JobStepState, reason string) {
	if h.steps == nil || pg.SlideID == "" {
		return
	}
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID,
		StepType: stepTypePage,
		StepKey:  pageStepKey(pg.SlideID),
		State:    state,
		// ResultRef 存失败/降级原因，或成功页的"已放行但请核对"提示（短文本）：
		// 用户必须能看到「为什么这页没成稿」以及「哪个数字是模型自己数出来的」，
		// 而不是只看到一个 degraded 标记再去猜。
		ResultRef: truncateRunes(reason, 240),
	}
	_ = h.steps.MarkStep(ctx, step)
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

// guardInstructions 是第二轮（定点修补）的指令。
//
// 只列**具体违规项**，并要求最小改动：把违规清单精确摆到模型面前，比让它重新读一遍原文
// 更可预期——后者每轮都在重新猜，命不命中靠运气。抽成纯函数是为了在没有 LLM 的机器上
// 也能锁定「指令里必须出现哪些信息」。
func guardInstructions(report validation.Report) string {
	parts := []string{"以下是需要修正的文案，它违反了数字/单位/型号保持规则："}
	if missing := validation.FormatEntitiesHuman(report.Missing); len(missing) > 0 {
		parts = append(parts, "遗漏了原素材中的「"+strings.Join(missing, "、")+"」，必须补回；")
	}
	inserted := validation.FormatEntitiesHuman(report.Inserted)
	if len(inserted) > 0 {
		parts = append(parts, "出现了原素材没有的「"+strings.Join(inserted, "、")+"」，必须删除；")
	}
	if len(report.Missing) == 0 && len(report.Inserted) == 0 {
		// 调用方只在 !OK() 时才会走到这里；留一个无死角的兜底，避免指令成为半句话。
		parts = append(parts, "其中的数字、单位与型号与原素材不一致；")
	}
	parts = append(parts, "除这些实体外不要改动其它措辞与结构，只做最小必要修改。")
	// 内部标记与思考过程必须显式禁止：模型会把 number:3 当成正文内容原样抄进去。
	parts = append(parts, "直接输出修正后的讲解正文：不要解释修改过程，不要输出 number/date/model "+
		"这类内部标记，也不要把思考过程写进正文。")
	return strings.Join(parts, "")
}

// entityFixSuffix 只返回「必须保留的事实」这几句，没有违规项时为空。
//
// 用于与 rewriteGuardInstructions 拼接：先要求把素材重写成讲稿，
// 再点名哪些数字/单位/型号不能少、哪些不能凭空多出来。
func entityFixSuffix(report validation.Report) string {
	parts := make([]string, 0, 2)
	if missing := validation.FormatEntitiesHuman(report.Missing); len(missing) > 0 {
		parts = append(parts, "务必保留原素材中的「"+strings.Join(missing, "、")+"」，不能遗漏；")
	}
	if inserted := validation.FormatEntitiesHuman(report.Inserted); len(inserted) > 0 {
		parts = append(parts, "不要出现原素材没有的「"+strings.Join(inserted, "、")+"」。")
	}
	return strings.Join(parts, "")
}

// toleratedNote 把「按策略放行的插入数字」转成一句给用户看的提示。
//
// 为什么必须提示：这条路径是**明知可能数错仍放行**——模型写「4 类场景」时，校验器
// 无法确认页面真的只有 4 条。沉默放行等于让用户失去核对机会，与 A26 的"不得假成功"
// 只差一步；写进步骤原因，代价很小。
//
// 只提示 warn 模式：allow 表示运营方明确不要提示（见 rules.json 的 counts）。
func toleratedNote(r validation.Report, text string) string {
	if !r.HasWarnings() {
		return ""
	}
	policy := validation.CountsPolicy()
	counts, ordinals := make([]validation.Entity, 0, len(r.Warnings)), make([]validation.Entity, 0, 2)
	for _, e := range r.Warnings {
		if e.Form == validation.FormOrdinal {
			ordinals = append(ordinals, e)
			continue
		}
		counts = append(counts, e)
	}
	parts := make([]string, 0, 2)
	if len(counts) > 0 && policy.InsertedCounts == validation.ModeWarn {
		parts = append(parts, "新增"+strings.Join(validation.FormatEntitiesHuman(counts), "、")+
			numberContextHint(counts, text)+"，属于对页面条目的概括而非实测数值")
	}
	if len(ordinals) > 0 && policy.InsertedOrdinals == validation.ModeWarn {
		parts = append(parts, "新增序号"+strings.Join(validation.FormatEntitiesHuman(ordinals), "、")+
			numberContextHint(ordinals, text))
	}
	if len(parts) == 0 {
		return ""
	}
	return "提示：" + strings.Join(parts, "；") + "。已放行，请核对是否与页面一致。"
}

// numberContextHint 从产出里截取"新增数字"周围的原文（最多两条，各留 8 字上下文）。
//
// 降级原因如果只写「新增数字 3」，用户没法判断这是模型在数页面列出的条目
// （「4 类场景」），还是凭空编了一个数——前者该放宽规则，后者该换模型或手改。
func numberContextHint(inserted []validation.Entity, text string) string {
	runes := []rune(text)
	hints := make([]string, 0, 2)
	for _, e := range inserted {
		if e.Kind != "number" || len(hints) >= 2 {
			continue
		}
		// 中文数词的实体值是归一后的阿拉伯数字（「三个」→ 3），但正文里并没有 "3"。
		// 这种情况下必须按原文写法定位，否则会截取到毫不相干的另一个 3。
		needle := e.Text
		if e.Surface != "" {
			needle = e.Surface
		}
		idx := strings.Index(text, needle)
		if idx < 0 {
			continue
		}
		pre := len([]rune(text[:idx]))
		from := pre - 8
		if from < 0 {
			from = 0
		}
		to := pre + len([]rune(e.Text)) + 8
		if to > len(runes) {
			to = len(runes)
		}
		snippet := strings.TrimSpace(strings.ReplaceAll(string(runes[from:to]), "\n", " "))
		hints = append(hints, "…"+snippet+"…")
	}
	if len(hints) == 0 {
		return ""
	}
	return "（" + strings.Join(hints, "；") + "）"
}

// rewriteGuardInstructions 是"上一版根本不是讲稿"时的第二轮指令。
//
// 与 guardInstructions 的区别：那里是**定点修补**（只补/删实体，其余一字不动），
// 这里要求**重新写成讲稿**。用错指令会得到反向结果——对一份照抄的页面文字说
// "除这些实体外不要改动其它措辞与结构"，模型只会更坚决地照抄。
func rewriteGuardInstructions(echo bool) string {
	var b strings.Builder
	if echo {
		b.WriteString("上一版把页面文字原样照抄了，不是讲解稿。请把素材改写成可直接朗读的口语化讲稿：")
		b.WriteString("保留其中的数字、单位、型号与专有名词，")
		b.WriteString("但不要保留版式的分行、标题罗列与标签堆叠，要用连贯的句子讲出来。")
	} else {
		b.WriteString("上一版的正文不干净（混入了思考过程或内部标记）。请重新把素材写成可直接朗读的讲解稿：")
	}
	b.WriteString("直接输出讲稿正文，不要解释修改过程，不要输出 number/date/model 这类内部标记，")
	b.WriteString("也不要把思考过程写进正文。")
	return b.String()
}

// factCitationPat 匹配讲稿正文中的事实引用标记：〔fact-xx〕。
//
// **允许未闭合**：推理型模型可能恰好停在标记中间（2026-11-02 实测：目标 60 秒的稿子
// 被截断在「〔fact-c」，长度只有 69 字）。只匹配闭合标记会把残片留在正文里，
// 用户照着念就会念出「左方括号 fact 横线 c」。残片只剥离、不计入引用集合。
var factCitationPat = regexp.MustCompile(`〔fact-[a-z0-9-]*〕?`)

// extractFactCitations 从模型产出中提取并剥离事实引用标记，返回纯正文
// 与引用的 ID 集合（保序、去重）。未匹配任何标记 = 纯正文原样返回。
//
// 只剥离两种情况：① 闭合且 ID 合法；② 停在文末的合法残片（真截断）。
// 「〔fact-〕」「〔fact-©〕」「〔fact-a b〕」这类畸形标记一律原样保留——
// 宁可漏判，也不要把用户正文里的方括号内容误删。
func extractFactCitations(text string) (clean string, cited []string) {
	seen := map[string]bool{}
	cited = []string{}
	var b strings.Builder
	last := 0
	for _, loc := range factCitationPat.FindAllStringIndex(text, -1) {
		m := text[loc[0]:loc[1]]
		closed := strings.HasSuffix(m, "〕")
		id := strings.TrimSuffix(strings.TrimPrefix(m, "〔"), "〕")
		if id == "" {
			continue
		}
		inner := strings.TrimPrefix(id, "fact-")
		if !factCitationIDOK(inner) {
			continue
		}
		truncatedTail := !closed && strings.TrimSpace(text[loc[1]:]) == ""
		if !closed && !truncatedTail {
			continue // 行中间未闭合 → 更像畸形标记，保留原文
		}
		b.WriteString(text[last:loc[0]])
		last = loc[1]
		if closed && !seen[id] {
			seen[id] = true
			cited = append(cited, id)
		}
	}
	b.WriteString(text[last:])
	return strings.TrimSpace(b.String()), cited
}

// factCitationIDOK 校验 fact- 后缀只含小写字母/数字/连字符（与 factID/
// structured fact 的合法字符集一致）。
func factCitationIDOK(suffix string) bool {
	for _, r := range suffix {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return suffix != ""
}

// unknownFactCitations 返回 cited 中不属于 available 的 ID（幻觉引用）。
// 空 available 时任何引用都视为无依据（但有提示词约束，正常不会发生）。
func unknownFactCitations(cited []string, available []draftFact) []string {
	if len(cited) == 0 {
		return nil
	}
	known := make(map[string]bool, len(available))
	for _, f := range available {
		known[f.ID] = true
	}
	unknown := make([]string, 0, len(cited))
	for _, id := range cited {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	return unknown
}

// factCitationInstruction 生成引用约束指令（仅在存在可引用 facts 时追加）：
// 引用必须真实、可选、且只允许出现在【结构化事实】给出的候选 ID 内。
func factCitationInstruction(available []draftFact) string {
	if len(available) == 0 {
		return ""
	}
	ids := make([]string, 0, len(available))
	for _, f := range available {
		ids = append(ids, f.ID)
	}
	return "若某句讲解确实依据某条【结构化事实】，可在句尾标注其编号（形如 〔fact-a〕）。" +
		"只能标注本页事实清单中真实存在的编号（" + strings.Join(ids, "/") + "），不得虚构。标注可选，宁缺毋滥；标注会用于溯源校验。"
}

// citationGuardInstructions 生成针对幻觉引用的第二轮修补指令。
func citationGuardInstructions(unknown []string) string {
	if len(unknown) == 0 {
		return ""
	}
	return "你标注的引用编号不存在于本页事实清单中（" + strings.Join(unknown, "、") +
		"），必须改成真实存在的编号或直接删除标注；只做最小必要修改，只输出修正后的正文。"
}

// draftText 产出单页讲稿正文，返回 (displayText, spokenText, degraded, error)。
//
// degraded=true 表示数字/单位/型号校验两轮仍不过，已**保守回退为原始素材原文**（未经 AI 加工）：
// 落库的是页面要点片段而非讲解稿。该标志必须向上传递——当作成功会让界面把要点片段报告成
// "成稿完成"（A26：不得假成功）。
// internalEntityLiteralPat 匹配**只应存在于校验器内部**的实体标记（number:3 / date:2026-01-01）。
// 它们一旦出现在正文里，说明模型把修补指令当成了要复述的内容。
var internalEntityLiteralPat = regexp.MustCompile(`(?i)\b(?:number|date|model)\s*:`)

// chainOfThoughtMarkers 是"模型把思考过程写进正文"的典型片段。宁可漏判也不能误杀，
// 所以只收录语义上不可能出现在正常讲解稿里的句子开头。
var chainOfThoughtMarkers = []string{
	"The user wants", "The user says", "The user is asking", "Let me ",
	"I need to ", "I'm realizing", "Looking at the", "I should ",
	"用户要求我", "用户在要求我", "让我仔细阅读", "我应该假设", "我在尝试理解",
}

// draftOutputSuspect 判断模型产出是不是**不可用产出**：内部实体标记泄漏，或正文里
// 混入了思考过程。这类文本即便数字集合恰好对得上，也绝不能落库——它看起来像稿子，
// 实际是模型写完稿之前的中间状态，用户拿到会直接念出来（A26：不得假成功）。
//
// language 用于限定英文占比检查：英文讲稿本来就以英文为主，只在中文目标下判"英文过多"。
func draftOutputSuspect(language, text string) bool {
	if internalEntityLiteralPat.MatchString(text) {
		return true
	}
	for _, marker := range chainOfThoughtMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "zh") {
		if letters, total := asciiLetterCount(text); total > 200 && float64(letters)/float64(total) > 0.5 {
			return true
		}
	}
	return false
}

// draftTooShort 判断产出是否明显短于目标时长——即"残稿"。
//
// 中文讲解约 4 字/秒，这里按 2 字/秒取下限：只拦明显不完整的产出
// （2026-11-02 实测：目标 60 秒的稿子只落库 69 字，模型在 800 token 预算里
// 把大部分花在了思考上），不评判风格是否精炼。targetSeconds<=0 时不检查。
func draftTooShort(text string, targetSeconds int) bool {
	if targetSeconds <= 0 {
		return false
	}
	return utf8.RuneCountInString(strings.TrimSpace(text)) < targetSeconds*2
}

// draftEchoesSource 判断产出是不是**把页面文字原样回显**。
//
// 2026-11-02 真机：slide-266 落库的"讲稿"就是源形状按行拼接的结果
// （MULTI-PLATFORM / Web / 桌面端…每行原封不动，只少了页码），却记为 success。
// 实体校验对它毫无办法——逐字相同的文本不可能漏事实，于是"没改写"反而成了
// 最安全的通过方式。这不是成稿，按 A26 不得报成功。
//
// 两条判据：
//  1. 归一化（去空白/标点、统一小写）后与素材完全一致；
//  2. 素材至少 3 行，且八成以上的行在产出里逐行原样出现——版式结构被照搬。
//
// 素材不足 20 字时不做判据 1：封面单标题页润色后可能确实只剩一句话。
func draftEchoesSource(source, out string) bool {
	srcNorm := normalizeDraftText(source)
	if len([]rune(srcNorm)) >= 20 && srcNorm == normalizeDraftText(out) {
		return true
	}
	lines := nonEmptyLines(source)
	if len(lines) < 3 {
		return false
	}
	outLines := make(map[string]bool, len(lines))
	for _, l := range nonEmptyLines(out) {
		outLines[normalizeDraftText(l)] = true
	}
	hit := 0
	for _, l := range lines {
		if outLines[normalizeDraftText(l)] {
			hit++
		}
	}
	return float64(hit)/float64(len(lines)) >= 0.8
}

// normalizeDraftText 只保留字母与数字（含汉字），用于"是否同一段文字"的比较。
// 标点与空白不计：模型照抄时常常改写标点、把两行合并成一行。
func normalizeDraftText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func nonEmptyLines(s string) []string {
	out := make([]string, 0, 8)
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// asciiLetterCount 统计非空白字符数与其中的 ASCII 字母数，用于粗略判断语言是否跑偏。
func asciiLetterCount(text string) (letters, total int) {
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	return letters, total
}

// draftTextResult 是单页成稿的产出与**降级原因**。
//
// Reason 只在 Degraded 时有值，写入 job_steps.result_ref：用户看到「已保留原文」
// 时必须知道是哪条规则没过（缺了哪个数字、多了哪个数字、还是模型吐了思考过程），
// 否则只能反复重试，无法判断该改页面、该换模型还是该改配置。
type draftTextResult struct {
	Display  string
	Spoken   string
	Degraded bool
	Reason   string
	// Note 是"已放行但请核对"的提示（当前仅按策略放行的概括计数）。它不影响落库，
	// 只随步骤记录带给用户：这类数字是模型自己数出来的，界面不该假装它是原文事实。
	Note string
}

// draftText 保留原签名，供既有调用方与测试使用；需要降级原因时用 draftTextDetail。
func (h *ScriptDraftHandler) draftText(ctx context.Context, tenantID, projectID, slideID, language string, mode narration.ScriptMode, in draftInput, facts []draftFact, snap ScriptDraftSnapshot, polisher llm.TextRewriter) (string, string, bool, error) {
	res, err := h.draftTextDetail(ctx, tenantID, projectID, slideID, language, mode, in, facts, snap, polisher)
	return res.Display, res.Spoken, res.Degraded, err
}

func (h *ScriptDraftHandler) draftTextDetail(ctx context.Context, tenantID, projectID, slideID, language string, mode narration.ScriptMode, in draftInput, facts []draftFact, snap ScriptDraftSnapshot, polisher llm.TextRewriter) (draftTextResult, error) {
	source := in.Text
	if mode == narration.ModeOriginal {
		return draftTextResult{Display: source, Spoken: source}, nil
	}
	if polisher == nil {
		return draftTextResult{}, errors.New("script_draft: LLM draft mode requires configured text rewriter")
	}
	instructions := draftInstructions(mode, in.Kind, snap)
	promptSource, validationSource := augmentDraftSource(source, facts)
	instructions += factCitationInstruction(facts)
	// 内容寻址缓存：相同（模式+语言+指令+素材）直接复用上次产出，避免重复调用模型。
	// 同一份稿子/同一批参数反复生成（补页、重试、改投影前后）时命中率很高。
	var cacheKey objectstore.ObjectKey
	if !h.disableCache {
		cacheKey = h.scriptCacheKey(tenantID, mode, language, instructions, promptSource)
		// 缓存也可能是脏的（历史版本把 number:3 写进过正文，截断成 69 字残稿，
		// 或干脆把页面文字原样存了下来）：命中后仍要过质量判定，
		// 否则一次坏产出会被缓存永久复用。
		if cached, ok := h.loadScriptCache(ctx, cacheKey); ok &&
			!draftOutputSuspect(language, cached) && !draftTooShort(cached, snap.TargetSeconds) &&
			!draftEchoesSource(source, cached) {
			return draftTextResult{Display: cached, Spoken: cached}, nil
		}
	}
	opID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode)
	if err := h.reserveLLMTokens(ctx, tenantID, opID, promptSource); err != nil {
		return draftTextResult{}, err
	}
	result, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID:  opID,
		Mode:         string(mode),
		Language:     language,
		SourceText:   promptSource,
		Instructions: instructions,
	})
	if err != nil {
		return draftTextResult{}, classifyLLMError(fmt.Errorf("script_draft: generate text: %w", err))
	}
	h.settleLLMTokens(ctx, tenantID, opID, result)
	clean, cited := extractFactCitations(result.Text)
	unknown := unknownFactCitations(cited, facts)
	report := validation.CheckPreserved(validationSource, clean)
	suspectFirst := draftOutputSuspect(language, clean)
	shortFirst := draftTooShort(clean, snap.TargetSeconds)
	echoFirst := draftEchoesSource(source, clean)
	if !suspectFirst && !shortFirst && !echoFirst && report.OK() && len(unknown) == 0 {
		if !h.disableCache {
			h.saveScriptCache(ctx, cacheKey, clean)
		}
		return draftTextResult{Display: clean, Spoken: clean, Note: toleratedNote(report, clean)}, nil
	}
	// 降级原因：两轮各自为什么没过，逐条留痕。
	why := make([]string, 0, 4)
	appendReport := func(tag string, r validation.Report, text string) {
		if ms := validation.FormatEntitiesHuman(r.Missing); len(ms) > 0 {
			why = append(why, tag+"遗漏「"+strings.Join(ms, "、")+"」")
		}
		if ins := validation.FormatEntitiesHuman(r.Inserted); len(ins) > 0 {
			why = append(why, tag+"新增「"+strings.Join(ins, "、")+"」"+numberContextHint(r.Inserted, text))
		}
	}
	if suspectFirst {
		why = append(why, "第一版含内部标记或思考过程")
	}
	if shortFirst {
		why = append(why, fmt.Sprintf("第一版过短（%d 字，目标 %d 秒）",
			utf8.RuneCountInString(clean), snap.TargetSeconds))
	}
	if echoFirst {
		why = append(why, "第一版原样照抄页面文字，没有改写成讲稿")
	}
	appendReport("第一版", report, clean)
	if len(unknown) > 0 {
		why = append(why, "第一版引用了不存在的事实出处")
	}
	// 第二轮是**定点修补**：把上一版文案本身当作待修的稿子，只列出违规项。
	// 原先这里重发原文（SourceText: source），等于让模型从头再写一遍——第一版已经写好的
	// 部分被丢弃，很可能换一种方式再错，两轮下来只剩下回退原文一条路。
	guardOpID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode) + ":guard"
	guardText := clean
	if suspectFirst || echoFirst {
		// 上一版是思维链/字面量垃圾/照抄页面的产物：在它上面"修补"只会得到更长的垃圾。
		// 回到原始素材重写一次，让第二轮有一次真正的机会。
		guardText = source
	}
	guardInstructionsText := guardInstructions(report)
	if suspectFirst || echoFirst {
		// 此时 report 可能恰好 OK（照抄不会漏实体），用实体修补指令等于让模型继续照抄。
		guardInstructionsText = rewriteGuardInstructions(echoFirst) + entityFixSuffix(report)
	}
	if c := citationGuardInstructions(unknown); c != "" {
		guardInstructionsText += c
	}
	if err := h.reserveLLMTokens(ctx, tenantID, guardOpID, guardText); err != nil {
		return draftTextResult{}, err
	}
	fix, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID:  guardOpID,
		Mode:         string(mode),
		Language:     language,
		SourceText:   guardText, // ← 待修补的是**上一版产出**（已剥离引用标记），不是原始素材
		Instructions: guardInstructionsText,
	})
	if err == nil {
		h.settleLLMTokens(ctx, tenantID, guardOpID, fix)
		fixedClean, fixedCited := extractFactCitations(fix.Text)
		fixedUnknown := unknownFactCitations(fixedCited, facts)
		fixedReport := validation.CheckPreserved(validationSource, fixedClean)
		suspectSecond := draftOutputSuspect(language, fixedClean)
		shortSecond := draftTooShort(fixedClean, snap.TargetSeconds)
		echoSecond := draftEchoesSource(source, fixedClean)
		if !suspectSecond && !shortSecond && !echoSecond && fixedReport.OK() && len(fixedUnknown) == 0 {
			if !h.disableCache {
				h.saveScriptCache(ctx, cacheKey, fixedClean)
			}
			return draftTextResult{Display: fixedClean, Spoken: fixedClean, Note: toleratedNote(fixedReport, fixedClean)}, nil
		}
		if suspectSecond {
			why = append(why, "修补版含内部标记或思考过程")
		}
		if shortSecond {
			why = append(why, fmt.Sprintf("修补版过短（%d 字，目标 %d 秒）",
				utf8.RuneCountInString(fixedClean), snap.TargetSeconds))
		}
		if echoSecond {
			why = append(why, "修补版仍然照抄页面文字")
		}
		appendReport("修补版", fixedReport, fixedClean)
		if len(fixedUnknown) > 0 {
			why = append(why, "修补版引用了不存在的事实出处")
		}
	} else {
		why = append(why, "修补调用失败")
	}
	// 两轮校验都不过：保守回退为原始素材原文，并以 degraded=true 告知调用方——落库的是
	// 页面要点片段，界面必须显示"已保留原文、待人工确认"，不得报告成稿完成。
	reason := strings.Join(why, "；")
	if reason == "" {
		reason = "数字/单位/型号校验未通过"
	}
	return draftTextResult{Display: source, Spoken: source, Degraded: true, Reason: reason}, nil
}

// scriptDraftCacheAssetType 是讲稿内容缓存的资产类型（租户级共享缓存段）。
const scriptDraftCacheAssetType = "scriptdraft"

// scriptCacheKey 由「租户 + 模式 + 语言 + 指令 + 素材」派生内容地址。
// 指令已编码 audience/style/length/targetSeconds 等参数，故参数变化会自然落到不同缓存。
func (h *ScriptDraftHandler) scriptCacheKey(tenantID string, mode narration.ScriptMode, language, instructions, source string) objectstore.ObjectKey {
	sum := hashBytes([]byte(string(mode) + "\x00" + language + "\x00" + instructions + "\x00" + source))
	return objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: scriptDraftCacheAssetType, AssetID: sum, Ext: "json",
	}
}

type scriptDraftCacheEntry struct {
	Text string `json:"text"`
}

// loadScriptCache 命中返回缓存正文；未命中/读取失败返回 ("", false)（缓存不可用不影响生成）。
func (h *ScriptDraftHandler) loadScriptCache(ctx context.Context, key objectstore.ObjectKey) (string, bool) {
	if h.objects == nil {
		return "", false
	}
	rc, _, err := h.objects.Get(ctx, key)
	if err != nil {
		return "", false
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", false
	}
	var entry scriptDraftCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return "", false
	}
	if strings.TrimSpace(entry.Text) == "" {
		return "", false
	}
	return entry.Text, true
}

// saveScriptCache 写入内容缓存；失败静默（缓存只是优化，不影响正确性）。
func (h *ScriptDraftHandler) saveScriptCache(ctx context.Context, key objectstore.ObjectKey, text string) {
	if h.objects == nil || strings.TrimSpace(text) == "" {
		return
	}
	data, err := json.Marshal(scriptDraftCacheEntry{Text: text})
	if err != nil {
		return
	}
	_ = h.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(data), Size: int64(len(data)),
	})
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

// draftInstructions 生成发给模型的指令。**指令必须描述真实的输入来源**：
// 输入是页面文字/备注（版面要点片段）时，若仍说"把当前讲稿润色一遍"，模型会按
// "改写一份已有成稿"去理解，而它实际拿到的是碎片要点 —— 产出会偏离素材。
// 来源种类由 selectDraftInput 单点决定（本函数不重复做优先级判断）。
func draftInstructions(mode narration.ScriptMode, kind draftInputKind, snap ScriptDraftSnapshot) string {
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
	// 数字/单位/型号/量词的约束**与校验器同源**：等价写法与量词族说明直接取自 validation 包，
	// 避免出现「指令说可以这样写、校验器却拒绝」的假失败——模型照着指令写、仍被判违规，
	// 用户只看到整页回退原文，却查不出是谁的问题（M1/M3 的教训：同一件事两处各写一份必然漂移）。
	// 量词自 M16 起参与判违规（"3 页" ≠ "3 个项目"），因此必须由校验器一并说明。
	suffix := strings.Join(parts, "") +
		"硬约束：① 原文中的数字、单位、日期、型号必须逐一保留，不得改写、替换或省略；" +
		// ② 由校验器按 counts 策略生成：策略放开了概括计数，指令却仍写"不要计数"，
		// 模型只能把「四个用例」拆成别扭的无数字句子（M1/M3：同一件事两处各写一份必然漂移）。
		validation.FormatCountHint() +
		"③ 型号与缩写保持原样，不要擅自改写、展开或省略。" +
		validation.FormatMeasureHint() +
		validation.FormatEquivalenceHint() +
		"只输出正文。"

	// 输入是已有讲稿（未显式指定来源时的沿用/润色路径）。
	if kind == inputExisting {
		if mode == narration.ModeAIGenerated {
			return "基于当前讲稿生成一段更完整、自然、适合客户演示的讲解稿；可以补足衔接和解释，但不得引入当前讲稿没有支持的事实。" + suffix
		}
		return "把当前讲稿润色为更适合 PPT 演示讲解的自然口播稿；" + suffix
	}

	// 输入是原始素材（页面文字 / 备注 / 自定义文本），不是成稿，必须说清是"整理要点"。
	material := "以下是本页 PPT 的页面文字（版面上的要点片段，不是成稿句子）"
	switch kind {
	case inputNotes:
		material = "以下是本页 PPT 的演讲者备注（讲稿要点，不是成稿句子）"
	case inputCustom:
		material = "以下是用户为本页提供的内容（讲稿要点，不是成稿句子）"
	}
	flow := "讲解要先形成清晰主线：开场点明本页要解决的问题或核心结论，中段说明关键关系、因果、对比或取舍，收束到听众应带走的判断；不要按版面顺序逐条复述，也不要输出标题、项目符号或显式结构标签。"
	if mode == narration.ModeAIGenerated {
		return material + "。请据此撰写一段完整、自然、适合客户演示的口播讲解稿：" + flow + "把要点组织成连贯的语句，可以补足衔接与必要解释，但不得引入这些内容没有支持的事实。" + suffix
	}
	return material + "。请把其中的要点整理成一段适合 PPT 演示讲解的自然口播稿：" + flow + "保持原意、要点齐全，不添加这些内容以外的信息。" + suffix
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
	retry := &pipeline.RetryError{Err: err}
	if retryable.RetryAfter > 0 {
		retry.At = time.Now().Add(retryable.RetryAfter)
		return retry
	}
	// 429 速率/配额限流：多数供应商按分钟窗口恢复。带 Retry-After 的按它退避；没有的
	// 用固定退避重试（成稿任务并行度高，单次突发打爆窗口很常见；一击失败会把整单挂掉）。
	var httpErr interface{ HTTPStatus() int }
	if errors.As(retryable.Err, &httpErr) && httpErr.HTTPStatus() == 429 {
		retry.At = time.Now().Add(llm429Backoff)
	}
	return retry
}

// isRateLimitError 判断错误是否为供应商 429（速率/配额限流）。
// 判断走错误链，而不是匹配文案：供应商文案千差万别，状态码是唯一稳定契约。
func isRateLimitError(err error) bool {
	var httpErr interface{ HTTPStatus() int }
	return errors.As(err, &httpErr) && httpErr.HTTPStatus() == 429
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

// augmentDraftSource 把已抽取的原子事实（draftFact）汇入讲稿生成输入，返回两份基准：
//   - promptSource：发给模型的输入。在原素材后追加【结构化事实】块——表格/图表/图片/
//     备注等异构元素的事实带 [fact-xx] 编号供模型组织讲解，编号在讲稿正文中不得输出。
//   - validationSource：供数字/单位/型号守门校验（CheckPreserved）的基准。必须**与 prompt 对称**，
//     否则模型按事实引用了某个数字（如表格里的 128GB）而校验基准只有原素材 → 被判「新增实体」
//     违规。正是这段对称，也是 M11 的教训：提示词与校验器各说一套就会漂移。
//
// 注意：facts 是**结构化/视觉观察的快照**（slideInsight 缓存），不随每次生成重新提取——
// 校验永远对着同一份事实，避免「今天校验通过、明天重新调视觉模型又失败」的非稳定行为。
// PromptOnly 的事实（视觉场景描述等自由文本）只进 prompt、不进校验基准：它们是低置信度、
// 非确定性的辅助上下文，硬性要求模型复述其中的数字会误杀（M11）。
//
// 无事实时（纯文本页 / inputExisting / inputCustom）双基准都等于原素材，行为与以往完全一致。
func augmentDraftSource(source string, facts []draftFact) (promptSource string, validationSource string) {
	source = strings.TrimSpace(source)
	if len(facts) == 0 {
		return source, source
	}
	var prompt strings.Builder
	if source != "" {
		prompt.WriteString(source)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("【结构化事实】以下事实来自本页 PPT 的结构解析或视觉观察，仅用于组织讲解；方括号编号只用于内部溯源，不要在讲稿正文中输出。\n")
	values := make([]string, 0, len(facts)+1)
	if source != "" {
		values = append(values, source)
	}
	elementKinds := map[string]bool{}
	for _, fact := range facts {
		value := strings.TrimSpace(fact.Value)
		if value == "" {
			continue
		}
		prompt.WriteString("- [")
		prompt.WriteString(fact.ID)
		prompt.WriteString("] ")
		prompt.WriteString(value)
		prompt.WriteByte('\n')
		if !fact.PromptOnly {
			values = append(values, value)
		}
		if fact.ElementKind != "" {
			elementKinds[fact.ElementKind] = true
		}
	}
	// 页面级编排（第五阶段）：同一页含多个异构元素（表格/图表/图片/文本）时，
	// 引导模型按讲解价值组织成连贯段落，而不是逐个元素机械罗列。该指令只进
	// prompt、不进校验基准——它没有可校验的事实数字，混入会污染 CheckPreserved。
	if len(elementKinds) > 1 {
		prompt.WriteString("\n【编排】本页同时包含：")
		keys := make([]string, 0, len(elementKinds))
		for k := range elementKinds {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		prompt.WriteString(strings.Join(keys, "、"))
		prompt.WriteString("。请把它们组织成一段连贯讲解：先讲本页最想传达的核心结论，再按逻辑需要穿插各元素佐证；" +
			"同一页面上的次要元素一句话带过即可，不要把每个元素各讲一遍“然后我们看看……”式地机械罗列。")
	}
	// 按类型模板（NarrationTemplate 的轻量版）：给出该类型通用的讲解结构骨架，
	// 让模型按骨架组织而非自由发挥。结构说明不含可校验数字，故只进 prompt。
	tpl := elementTemplatePrompt(elementKinds)
	if tpl != "" {
		prompt.WriteString("\n")
		prompt.WriteString(tpl)
	}
	return strings.TrimSpace(prompt.String()), strings.TrimSpace(strings.Join(values, "\n"))
}

// elementTemplatePrompt 按页面含有的元素类型返回讲解结构骨架（模板层）。
// 每种类型只给"该讲什么、按什么顺序"，不给具体数字——数字由 facts 提供并被校验。
func elementTemplatePrompt(kinds map[string]bool) string {
	var parts []string
	if kinds["chart"] {
		parts = append(parts, "图表：优先讲趋势/对比/极值，而不是画面构图；结合数值事实讲“变化”与“谁最大/最小”。")
	}
	if kinds["table"] {
		parts = append(parts, "表格：优先讲结构含义（行列关系、表头语义）、关键列的比较与排名，而非逐格复述。")
	}
	if kinds["image"] {
		parts = append(parts, "图片：讲这张图展示的对象/场景及其与当前主题的关联；如有识别出的文字或对象，挑关键细节，不逐个描述。")
	}
	if kinds["code"] {
		parts = append(parts, "代码：讲这段代码做什么（意图）、关键实现方式及其与主题的关系，不要逐行解释语法。")
	}
	if kinds["notes"] {
		parts = append(parts, "备注：作为讲解主线素材使用，内容优先于版面元素。")
	}
	if len(parts) == 0 {
		return ""
	}
	return "【讲解结构】" + strings.Join(parts, "")
}

// SourceShapeText 是「原文素材」计算所需的形状最小视图（只用到种类与文字）。
//
// 单独定义而不是直接吃 project.Shape：成稿读的是 document.json 的最小视图
// （parsedShape），并排对照读的是 project.Document——两者字段不同，但
// 「什么算原文、按什么优先级取」必须**只有一份实现**，否则对照面板显示的
// "原文"会和成稿实际吃进去的素材不一致。
type SourceShapeText struct {
	Kind string
	Text string
}

// IsLayoutOnlyNumber 判断一个形状是否只是**版式编号**（页脚页码 "03"、目录序号 "12"），
// 而不是页面内容。判定刻意保守：纯 ASCII 数字、1~3 位、且不是标题形状。
// 4 位以上的纯数字（年份 2026、编号 10086）一律当作内容保留——误删页面数据的代价
// 比多留一个页码大得多。
//
// 为什么必须剔除：一键成稿以 page_content 为来源时，页脚页码会进入
// validation.ExtractEntities，被当成"必须保留的事实"。讲解稿不朗读页码，于是每页都
// 判 missing，两轮修补后回退原文（degraded）——这正是 2026-11-02 那次 14 页任务里
// 5 页降级的根因。
func IsLayoutOnlyNumber(kind, text string) bool {
	if strings.EqualFold(strings.TrimSpace(kind), "title") {
		return false
	}
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 3 {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return false
		}
	}
	return true
}

// PageSourceText 按页面级来源取文本，**并返回最终胜出的来源种类**（page/notes/custom）。
// source 指定时只取该来源对应文本；source=custom 用 custom 文本；source=notes 仅用备注。
// 这是页面级取值的唯一实现——文本与来源种类必须由同一处产生，
// 否则"返回的文本算在一处、来源判定写在另一处"会重新漂移。
func PageSourceText(shapes []SourceShapeText, notes, source, custom string) (string, string) {
	source = strings.TrimSpace(source)
	switch source {
	case "notes":
		return "notes", strings.TrimSpace(notes)
	case "custom":
		if t := strings.TrimSpace(custom); t != "" {
			return "custom", t
		}
		return "notes", strings.TrimSpace(notes)
	}
	var parts []string
	for _, sh := range shapes {
		if source == "title" && !strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		if source == "body" && strings.EqualFold(strings.TrimSpace(sh.Kind), "title") {
			continue
		}
		// 版式编号（页脚页码、目录序号）不是页面内容：讲解稿不会朗读"03"，
		// 若把它拼进校验基准，模型每页都"漏掉一个数字"，两轮修补后只能回退原文。
		if IsLayoutOnlyNumber(sh.Kind, sh.Text) {
			continue
		}
		if t := strings.TrimSpace(sh.Text); t != "" {
			parts = append(parts, t)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		// 形状文字为空 → 回退备注：来源种类也必须是备注，不能仍标成"页面文字"。
		return "notes", strings.TrimSpace(notes)
	}
	return "page", text
}

// pgInput 是 SourceShapeText 版本的页面级取值：把 parsedShape 投影成最小视图后
// 交给 PageSourceText，保证成稿与对照面板共用同一套来源优先级。
func pgInput(pg parsedPage, source, custom string) draftInput {
	shapes := make([]SourceShapeText, 0, len(pg.Shapes))
	for _, sh := range pg.Shapes {
		shapes = append(shapes, SourceShapeText{Kind: sh.Kind, Text: sh.Text})
	}
	kind, text := PageSourceText(shapes, pg.NotesText, source, custom)
	return draftInput{Kind: draftInputKind(kind), Text: text}
}

// pgInputForMode 在页面级来源之上叠加批量来源（sourceMode，来自一键成稿）。
func pgInputForMode(pg parsedPage, source, custom, sourceMode string) draftInput {
	switch strings.TrimSpace(sourceMode) {
	case "notes_first":
		if notes := strings.TrimSpace(pg.NotesText); notes != "" {
			return draftInput{Kind: inputNotes, Text: notes}
		}
		return pgInput(pg, "layout", "")
	case "notes_only":
		return draftInput{Kind: inputNotes, Text: strings.TrimSpace(pg.NotesText)}
	case "page_content":
		return pgInput(pg, "layout", "")
	default:
		return pgInput(pg, source, custom)
	}
}

// selectDraftInput 决定单页成稿的输入：**文本 + 它的实际来源**（Text 为空 = 该页没有可用素材，应跳过）。
// 返回 Kind 而不是只返回文本，是为了让提示词能据此描述输入（见 draftInstructions）；
// 若分成两个函数各判一次来源，两份判断迟早会不一致。
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
func selectDraftInput(pg parsedPage, source, custom, sourceMode string, mode narration.ScriptMode, overwrite bool, existing string) draftInput {
	explicit := strings.TrimSpace(sourceMode) != "" || strings.TrimSpace(source) != ""
	if !explicit {
		if mode == narration.ModeOriginal {
			return draftInput{Kind: inputNotes, Text: strings.TrimSpace(pg.NotesText)}
		}
		// 显式重新生成时，润色/AI 生成以用户当前讲稿为输入，而不是重新从 PPT 版面抽取。
		if overwrite {
			if current := strings.TrimSpace(existing); current != "" {
				return draftInput{Kind: inputExisting, Text: current}
			}
		}
	}
	return pgInputForMode(pg, source, custom, sourceMode)
}
