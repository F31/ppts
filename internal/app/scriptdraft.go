package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
		done     atomic.Int64
		total    = int64(len(targets))
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
			outcome, err := h.ensureDraft(runCtx, job.TenantID, snap.ProjectID, snap.RevisionNo, snap.Language, mode, pg, source, custom, snap, renderKeys, polisher, vision)
			if err != nil {
				// 单页失败**不牵连其它页**：记该页失败步骤后继续。整任务最终仍返回错误，
				// 借助「重试跳过已完成页」补跑，避免一页超时就让已完成的页白跑一遍。
				if runCtx.Err() != nil {
					return // worker 停机/取消：不写终态，靠租约重领
				}
				dbMu.Lock()
				h.markPageStepState(runCtx, job, pg, pipeline.StepFailed)
				dbMu.Unlock()
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				reportDone()
				return
			}
			dbMu.Lock()
			h.markPageStep(runCtx, job, pg, outcome)
			dbMu.Unlock()
			reportDone()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		if retry := pipeline.AsRetry(firstErr); retry != nil && job.Attempt >= maxScriptDraftRetryAttempts {
			return retry.Err
		}
		return firstErr
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
// renderKeys 为 slideId → 页面 PNG 对象键（来自渲染清单），用于视觉锚点取图。
// polisher/vision 由调用方按任务解析一次后复用（避免每页查网关库）。
func (h *ScriptDraftHandler) ensureDraft(ctx context.Context, tenantID, projectID string, revisionNo int, language string, mode narration.ScriptMode, pg parsedPage, source, custom string, snap ScriptDraftSnapshot, renderKeys map[string]string, polisher llm.TextRewriter, vision llm.VisionExtractor) (draftOutcome, error) {
	slideID := pg.SlideID
	if slideID == "" {
		return draftSkippedNoText, nil
	}
	rev, err := h.scripts.Get(ctx, tenantID, projectID, revisionNo, slideID, language)
	if err == nil && len(rev.Segments) > 0 && !snap.Overwrite {
		return draftSkippedExisting, nil // 已存在实际分段，不覆盖用户稿（仅显式"重新生成讲稿"时 overwrite=true）。
	} else if err != nil && !errors.Is(err, narration.ErrNotFound) {
		return "", err
	} else if err != nil {
		rev = nil
	}

	// in 同时给出文本与它的**实际来源**：提示词必须据此描述输入，
	// 否则会告诉模型"把当前讲稿润色一遍"，而实际喂进去的是 PPT 页面文字。
	in := selectDraftInput(pg, source, custom, snap.SourceMode, mode, snap.Overwrite, revisionText(rev))
	if in.Text == "" {
		return draftSkippedNoText, nil // 无正文/备注，不生成空讲稿。
	}
	anchors := pgAnchors(pg, source)
	anchors = append(anchors, h.visualAnchors(ctx, vision, tenantID, projectID, language, pg, renderKeys)...)
	refs := sourceRefsFromAnchors(slideID, anchors)
	if len(refs) == 0 {
		refs = []string{slideID}
	}
	displayText, spokenText, degraded, err := h.draftText(ctx, tenantID, projectID, slideID, language, mode, in, snap, polisher)
	if err != nil {
		return "", err
	}
	if rev == nil {
		rev, err = h.scripts.EnsureExists(ctx, tenantID, projectID, revisionNo, slideID, language, mode)
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
	if _, err := h.scripts.Update(ctx, tenantID, projectID, revisionNo, slideID, language, rev.Revision, []*narration.Segment{segment}); err != nil {
		return "", fmt.Errorf("script_draft: write draft: %w", err)
	}
	if degraded {
		// 稿子写进去了，但内容是未经加工的原始素材。返回 degraded 让调用方把它记为
		// degraded 步骤、界面给出"待人工确认"，而不是"成稿完成"。
		return draftDegraded, nil
	}
	return draftGenerated, nil
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

func (h *ScriptDraftHandler) markPageStep(ctx context.Context, job *pipeline.Job, pg parsedPage, outcome draftOutcome) {
	h.markPageStepState(ctx, job, pg, pageStepState(outcome))
}

// markPageStepState 以显式状态登记单页步骤（供 outcome 映射与失败页记录复用）。
func (h *ScriptDraftHandler) markPageStepState(ctx context.Context, job *pipeline.Job, pg parsedPage, state pipeline.JobStepState) {
	if h.steps == nil || pg.SlideID == "" {
		return
	}
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID,
		StepType: stepTypePage,
		StepKey:  pageStepKey(pg.SlideID),
		State:    state,
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
	if missing := validation.FormatEntities(report.Missing); len(missing) > 0 {
		parts = append(parts, "遗漏了原素材中的 "+strings.Join(missing, "、")+"，必须补回；")
	}
	inserted := validation.FormatEntities(report.Inserted)
	if len(inserted) > 0 {
		parts = append(parts, "出现了原素材没有的 "+strings.Join(inserted, "、")+"，必须删除；")
	}
	if len(report.Missing) == 0 && len(report.Inserted) == 0 {
		// 调用方只在 !OK() 时才会走到这里；留一个无死角的兜底，避免指令成为半句话。
		parts = append(parts, "其中的数字、单位与型号与原素材不一致；")
	}
	parts = append(parts, "除这些实体外不要改动其它措辞与结构，只做最小必要修改。只输出修正后的正文。")
	return strings.Join(parts, "")
}

// draftText 产出单页讲稿正文，返回 (displayText, spokenText, degraded, error)。
//
// degraded=true 表示数字/单位/型号校验两轮仍不过，已**保守回退为原始素材原文**（未经 AI 加工）：
// 落库的是页面要点片段而非讲解稿。该标志必须向上传递——当作成功会让界面把要点片段报告成
// "成稿完成"（A26：不得假成功）。
func (h *ScriptDraftHandler) draftText(ctx context.Context, tenantID, projectID, slideID, language string, mode narration.ScriptMode, in draftInput, snap ScriptDraftSnapshot, polisher llm.TextRewriter) (string, string, bool, error) {
	source := in.Text
	if mode == narration.ModeOriginal {
		return source, source, false, nil
	}
	if polisher == nil {
		return "", "", false, errors.New("script_draft: LLM draft mode requires configured text rewriter")
	}
	instructions := draftInstructions(mode, in.Kind, snap)
	// 内容寻址缓存：相同（模式+语言+指令+素材）直接复用上次产出，避免重复调用模型。
	// 同一份稿子/同一批参数反复生成（补页、重试、改投影前后）时命中率很高。
	var cacheKey objectstore.ObjectKey
	if !h.disableCache {
		cacheKey = h.scriptCacheKey(tenantID, mode, language, instructions, source)
		if cached, ok := h.loadScriptCache(ctx, cacheKey); ok {
			return cached, cached, false, nil
		}
	}
	opID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode)
	if err := h.reserveLLMTokens(ctx, tenantID, opID, source); err != nil {
		return "", "", false, err
	}
	result, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID:  opID,
		Mode:         string(mode),
		Language:     language,
		SourceText:   source,
		Instructions: instructions,
	})
	if err != nil {
		return "", "", false, classifyLLMError(fmt.Errorf("script_draft: generate text: %w", err))
	}
	h.settleLLMTokens(ctx, tenantID, opID, result)
	text := strings.TrimSpace(result.Text)
	report := validation.CheckPreserved(source, text)
	if report.OK() {
		if !h.disableCache {
			h.saveScriptCache(ctx, cacheKey, text)
		}
		return text, text, false, nil
	}
	// 第二轮是**定点修补**：把上一版文案本身当作待修的稿子，只列出违规项。
	// 原先这里重发原文（SourceText: source），等于让模型从头再写一遍——第一版已经写好的
	// 部分被丢弃，很可能换一种方式再错，两轮下来只剩下回退原文一条路。
	guardOpID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode) + ":guard"
	if err := h.reserveLLMTokens(ctx, tenantID, guardOpID, text); err != nil {
		return "", "", false, err
	}
	fix, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID:  guardOpID,
		Mode:         string(mode),
		Language:     language,
		SourceText:   text, // ← 待修补的是**上一版产出**，不是原始素材
		Instructions: guardInstructions(report),
	})
	if err == nil {
		h.settleLLMTokens(ctx, tenantID, guardOpID, fix)
		fixed := strings.TrimSpace(fix.Text)
		if validation.CheckPreserved(source, fixed).OK() {
			if !h.disableCache {
				h.saveScriptCache(ctx, cacheKey, fixed)
			}
			return fixed, fixed, false, nil
		}
	}
	// 两轮校验都不过：保守回退为原始素材原文，并以 degraded=true 告知调用方——落库的是
	// 页面要点片段，界面必须显示"已保留原文、待人工确认"，不得报告成稿完成。
	return source, source, true, nil
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
		"② 原文没有出现的数字一律不得新增——不要自行编号或计数（例如不要写「第 1 步」「3 个要点」）；" +
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
	if mode == narration.ModeAIGenerated {
		return material + "。请据此撰写一段完整、自然、适合客户演示的口播讲解稿：把要点组织成连贯的语句，可以补足衔接与必要解释，但不得引入这些内容没有支持的事实。" + suffix
	}
	return material + "。请把其中的要点整理成一段适合 PPT 演示讲解的自然口播稿：保持原意、要点齐全，不添加这些内容以外的信息。" + suffix
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

// pgInput 按页面级来源取文本，**并标出最终胜出的来源**。
// source 指定时只取该来源对应文本；source=custom 用 custom 文本；source=notes 仅用备注。
// 这是页面级取值的唯一实现——文本与来源种类必须由同一处产生，
// 否则"返回的文本算在一处、来源判定写在另一处"会重新漂移。
func pgInput(pg parsedPage, source, custom string) draftInput {
	switch source {
	case "notes":
		return draftInput{Kind: inputNotes, Text: strings.TrimSpace(pg.NotesText)}
	case "custom":
		if t := strings.TrimSpace(custom); t != "" {
			return draftInput{Kind: inputCustom, Text: t}
		}
		return draftInput{Kind: inputNotes, Text: strings.TrimSpace(pg.NotesText)}
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
		// 形状文字为空 → 回退备注：来源种类也必须是备注，不能仍标成"页面文字"。
		return draftInput{Kind: inputNotes, Text: strings.TrimSpace(pg.NotesText)}
	}
	return draftInput{Kind: inputPage, Text: text}
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
