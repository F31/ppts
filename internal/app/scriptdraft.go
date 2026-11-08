package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/usage"
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
