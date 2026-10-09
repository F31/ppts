package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/usage"
)

const ttsAdapterVersion = "v1"

// sharedCacheProject 是租户级共享缓存的伪项目段（G2-7 内容哈希去重），
// 音频与分段清单按内容寻址存放，跨项目复用同一对象；仍受租户前缀隔离。
const sharedCacheProject = "shared"

// NarrationSnapshot fixes all inputs used by a narration job. A later script
// edit must enqueue a new job rather than changing an in-flight job's inputs.
type NarrationSnapshot struct {
	RevisionNo       int                      `json:"revisionNo,omitempty"`
	Slides           []NarrationSlideSnapshot `json:"slides"`
	SegmentIDs       []string                 `json:"segmentIds,omitempty"`       // G2-5 空=全量；非空=仅重生成指定分段
	TargetDurationMS int64                    `json:"targetDurationMs,omitempty"` // G2-5 目标总时长(ms)，0=不限
	Language         string                   `json:"language"`
	VoiceID          string                   `json:"voiceId"`
	RequireConfirmed bool                     `json:"requireConfirmed"`
	SpeechControl    tts.SpeechControl        `json:"speechControl"`
	SampleRate       int                      `json:"sampleRate"`
	Timing           media.Timing             `json:"timing"`
	// BypassCache 表示用户显式要求"重新生成"：跳过内容哈希缓存读取，每段真实调用语音合成，
	// 并把新音频写回同一 configHash 键（覆盖旧音频对象）。
	//
	// 之所以"跳过读取但仍写回同一键"，是为了让强制重生成的结果成为该配置的新正本：
	// 若改用掺 nonce 的新键，旧音频仍在共享缓存里，后续一次普通（增量）运行又会命中旧音频，
	// 把用户刚刚强制重做的成果悄悄换回去——那比不绕过更糟。代价是同一 configHash 键下的字节被
	// 刷新（内容寻址键此前假定"同配置=同字节"，TTS 非严格确定性时才会有毫秒级时长漂移）。
	BypassCache bool `json:"bypassCache,omitempty"`
}

// NarrationSlideSnapshot binds one slide to the script revision being voiced.
type NarrationSlideSnapshot struct {
	SlideID        string `json:"slideId"`
	ScriptRevision int64  `json:"scriptRevision"`
}

// SegmentAsset is the immutable manifest consumed by timeline assembly.
// alignmentCacheVersion 标识字级对齐算法的缓存版本；提升它会让既有分段清单失效，并触发
// "仅重算对齐"（复用缓存音频，不重新合成）。P2（estimated_vad 增强）对应版本 2，
// P-音节核锚定对应版本 3。
const alignmentCacheVersion = 3

// SegmentAsset 是单段合成产物（音频 + 字级对齐）的缓存清单。
type SegmentAsset struct {
	SegmentID         string         `json:"segmentId"`
	AudioKey          string         `json:"audioKey"`
	AudioHash         string         `json:"audioHash"`
	Format            string         `json:"format"`
	DurationMS        int64          `json:"durationMs"`
	Alignment         *tts.Alignment `json:"alignment"`
	ProviderRequestID string         `json:"providerRequestId,omitempty"`
	BillingUnit       string         `json:"billingUnit,omitempty"`
	BillingQuantity   int64          `json:"billingQuantity,omitempty"`
	Warnings          []string       `json:"warnings,omitempty"`
}

// TimelineAsset is written last and acts as the publication manifest for a
// playable narration revision.
// SynthesisStats 记录一次配音任务的音频来源构成。
//
// 存在即是为了"诚实反馈"：G2-7 内容哈希缓存让「全部（重新生成并覆盖）」在音色/讲稿/语速
// 均未变化时命中缓存、零次 TTS 调用——时间轴重建了，音频却仍是原对象。若界面无条件报成功，
// 用户会把"缓存命中、什么都没重新生成"误读为"已重新合成"，且二者外观完全相同（A26）。
// 故把构成随 bundle 持久化，由状态接口透出。
type SynthesisStats struct {
	Segments    int `json:"segments"`    // 本次任务纳入时间轴的分段总数
	Synthesized int `json:"synthesized"` // 真实调用 TTS 合成的分段数
	Cached      int `json:"cached"`      // 命中 project/shared 缓存、复用既有音频的分段数
}

type TimelineAsset struct {
	Timeline *media.Timeline `json:"timeline"`
	SRTKey   string          `json:"srtKey"`
	VTTKey   string          `json:"vttKey"`
}

// NarrationHandler synthesizes and publishes each segment independently.
type NarrationHandler struct {
	scripts narration.Store
	steps   interface {
		MarkStep(context.Context, pipeline.JobStep) error
	}
	objects     objectstore.ObjectStore
	provider    tts.TTSProvider
	usage       UsageSettler
	metrics     TTSMetrics
	dictLoader  DictionaryLoader
	textNorm    TextPreparationFactory
	providerFor func(ctx context.Context, tenantID string) (tts.TTSProvider, error)
	// sourceRevisions 是记录时间轴来源所需的窄能力（见 WithSourceRevisions）；nil 时跳过来源记录。
	sourceRevisions SourceRevisionLookup
	// segmentConcurrency 是配音分段合成的有界并发上限（operator 覆盖）。
	// 0＝跟随供应商能力声明（Capabilities.MaxConcurrentSegments，0 即串行）；
	// >0＝操作员显式上限，只允许**调低**供应商声明，不允许调高（防真实限流/串行供应商被并行打爆）。
	segmentConcurrency int
	// stepMu 串行化步骤记录 DB 写：SQLite 并发写会 BUSY（与 scriptdraft 的 dbMu 同源）。
	stepMu sync.Mutex
}

// SourceRevisionLookup 是 narration 记录时间轴来源所需的窄能力（避免直接依赖整个 project.Store）：
// 按项目 + 版本号取源版本展示名，供成品库展示"PPT 名称 + 版本"。
type SourceRevisionLookup interface {
	GetSourceRevision(ctx context.Context, tenantID, projectID string, revisionNo int) (*project.SourceRevision, error)
}

// WithSourceRevisions 注入源版本查询能力；未注入时跳过来源记录（降级：成品仅显示项目名称）。
func (h *NarrationHandler) WithSourceRevisions(s SourceRevisionLookup) *NarrationHandler {
	h.sourceRevisions = s
	return h
}

// UsageSettler 是配音完成后按实际时长结算额度所需的窄能力（G3-2）。
type UsageSettler interface {
	Settle(ctx context.Context, tenantID, logicalOperationID string, kind usage.Kind, actualUnits float64, priceVersion string) error
}

// TTSMetrics 记录供应商合成可观测性（G3-8）。
type TTSMetrics interface {
	SegmentSynthesized(job *pipeline.Job, retryable, throttled bool, duration time.Duration, err error)
	// SegmentCacheHit 记录分段音频从缓存命中的次数（G2-7 去重命中率）。
	SegmentCacheHit(job *pipeline.Job, scope string)
}

// NewNarrationHandler creates a segmented TTS handler.
func NewNarrationHandler(scripts narration.Store, steps interface {
	MarkStep(context.Context, pipeline.JobStep) error
}, objects objectstore.ObjectStore, provider tts.TTSProvider) *NarrationHandler {
	return &NarrationHandler{scripts: scripts, steps: steps, objects: objects, provider: provider}
}

// WithUsage 注入额度结算能力；未注入时跳过结算（测试/私有化）。
func (h *NarrationHandler) WithUsage(u UsageSettler) *NarrationHandler {
	h.usage = u
	return h
}

// WithTTSMetrics 注入供应商合成指标记录器。
func (h *NarrationHandler) WithTTSMetrics(m TTSMetrics) *NarrationHandler {
	h.metrics = m
	return h
}

// DictionaryLoader 按租户加载发音词典规则；未配置词典时返回空集。
type DictionaryLoader interface {
	LoadTenantDefault(ctx context.Context, tenantID string) (pronunciation.Rules, error)
}

// WithDictionary 注入发音词典加载器；未注入时合成不替换任何文本。
func (h *NarrationHandler) WithDictionary(dl DictionaryLoader) *NarrationHandler {
	h.dictLoader = dl
	return h
}

// WithTenantProvider 注入按租户解析的 TTS 供应商（模型网关）；
// 设置后优先于 NewNarrationHandler 传入的固定 provider。
func (h *NarrationHandler) WithTenantProvider(f func(ctx context.Context, tenantID string) (tts.TTSProvider, error)) *NarrationHandler {
	h.providerFor = f
	return h
}

func (h *NarrationHandler) providerForTenant(ctx context.Context, tenantID string) (tts.TTSProvider, error) {
	if h.providerFor != nil {
		return h.providerFor(ctx, tenantID)
	}
	if h.provider == nil {
		return nil, errors.New("narration job: no TTS provider configured")
	}
	return h.provider, nil
}

// WithSegmentConcurrency 设置配音分段合成的有界并发上限（默认跟随供应商能力声明）。
// 只允许调低、不允许调高：真实供应商未声明并发（MaxConcurrentSegments=0）时，
// 即使这里设了 N>0 也保持串行，绝不假定供应商能承受并行调用。
func (h *NarrationHandler) WithSegmentConcurrency(n int) *NarrationHandler {
	h.segmentConcurrency = n
	return h
}

// effectiveSegmentConcurrency 计算一次配音任务实际使用的分段合成并发度。
// 结果 = min(供应商声明 MaxConcurrentSegments, 操作员上限)，下限 1。
// 供应商未声明（0）→ 恒为 1（串行），操作员无法借此强行并发。
func (h *NarrationHandler) effectiveSegmentConcurrency(capabilities tts.VoiceCapabilities) int {
	n := capabilities.MaxConcurrentSegments
	if n < 1 {
		return 1
	}
	if h.segmentConcurrency > 0 && h.segmentConcurrency < n {
		return h.segmentConcurrency
	}
	return n
}

// markStep 串行化步骤记录 DB 写：并发合成分段的 goroutine 会同时调用 steps.MarkStep，
// SQLite 并发写会 BUSY（与 scriptdraft 的 dbMu 同源）。publishTimeline 等串行路径
// 也走此函数，统一保护，互斥开销可忽略。
func (h *NarrationHandler) markStep(ctx context.Context, step pipeline.JobStep) error {
	h.stepMu.Lock()
	defer h.stepMu.Unlock()
	return h.steps.MarkStep(ctx, step)
}

type plannedSlide struct {
	snapshot NarrationSlideSnapshot
	revision *narration.Revision
}

// nalInstruction 是一次配音任务里单个"页 × 段"的合成工作单元，固定顺序（预分配槽位）。
type nalInstruction struct {
	idx     int // 最终结果槽位索引（= 展平序）
	slide   plannedSlide
	segment *narration.Segment
}

// nalOutcome 是单个工作单元的合成结果（槽位由 instruction 索引，天然有序）。
type nalOutcome struct {
	asset  *SegmentAsset
	cached bool
}

// timelineAssembly 是一次合成的有序时间轴输入、统计与总时长（组装阶段串行累计）。
type timelineAssembly struct {
	slides  []media.SlideInput
	stats   SynthesisStats
	totalMS int64
}

// synthesizeAllSegments 有界并发合成 planned 内所有分段，并按页/段原序组装时间轴。
//
// 并发语义（防止"TTS 不支持并发调用"的供应商被并行打爆）：
//   - 并发度 = effectiveSegmentConcurrency(capabilities)：供应商未声明
//     MaxConcurrentSegments（0）→ 恒为 1，纯串行，行为与逐段完全一致；
//   - 操作员上限只允许调低供应商声明，不允许调高；
//
// 进度：合成**逐个完成时**增量上报（与改动前逐段上报语义一致）；segmentFilter 非空时
// 仅统计目标分段（totalTargeted）。并发路径下进度 DB 写经 stepMu 串行化。
//
// 错误：串行路径首个错误立即返回；并发路径首个错误触发 runCtx 取消让其余尽快失败，
// 收敛后返回首个错误。MarkStep（DB 写）经 markStep 互斥串行化，避免 SQLite 并发写 BUSY。
func (h *NarrationHandler) synthesizeAllSegments(ctx context.Context, job *pipeline.Job, snapshot NarrationSnapshot, provider tts.TTSProvider, capabilities tts.VoiceCapabilities, planned []plannedSlide, segmentFilter map[string]struct{}, dictRules pronunciation.Rules, textNormEngine TextPreparer) (timelineAssembly, error) {
	concurrency := h.effectiveSegmentConcurrency(capabilities)

	totalTargeted := 0
	for _, s := range planned {
		for _, seg := range s.revision.Segments {
			if segmentFilter == nil {
				totalTargeted++
			} else if _, ok := segmentFilter[seg.SegmentID]; ok {
				totalTargeted++
			}
		}
	}
	// 进度累计（并发路径用原子计数；DB 写归 stepMu）。
	isTargeted := func(seg *narration.Segment) bool {
		if segmentFilter == nil {
			return true
		}
		_, ok := segmentFilter[seg.SegmentID]
		return ok
	}
	var completed atomic.Int64
	reportProgress := func() {
		if totalTargeted <= 0 {
			return
		}
		n := completed.Load()
		pct := int(n * 100 / int64(totalTargeted))
		if pct > 100 {
			pct = 100
		}
		h.stepMu.Lock()
		_ = pipeline.ReportProgress(ctx, pct)
		h.stepMu.Unlock()
	}

	// 1) 展开全部 (页, 段) 工作单元，idx 即结果槽位（天然有序）。
	n := 0
	for _, s := range planned {
		n += len(s.revision.Segments)
	}
	instructions := make([]nalInstruction, 0, n)
	for _, s := range planned {
		for _, seg := range s.revision.Segments {
			instructions = append(instructions, nalInstruction{idx: len(instructions), slide: s, segment: seg})
		}
	}
	outcomes := make([]nalOutcome, len(instructions))

	if concurrency <= 1 {
		// 串行保底路径：语义与改动前一致（错误立即返回，逐段报进度）。
		for _, ins := range instructions {
			if err := ctx.Err(); err != nil {
				return timelineAssembly{}, err
			}
			asset, cached, err := h.synthesizeSegment(ctx, job, snapshot, ins.slide.snapshot, provider, capabilities, ins.segment, dictRules, textNormEngine)
			if err != nil {
				return timelineAssembly{}, err
			}
			outcomes[ins.idx] = nalOutcome{asset: asset, cached: cached}
			if isTargeted(ins.segment) {
				completed.Add(1)
				reportProgress()
			}
		}
	} else {
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		var errMu sync.Mutex
		var firstErr error
		for _, ins := range instructions {
			sem <- struct{}{} // 等待并发槽位；runCtx 取消后 slot 会被提前释放，不会死锁
			if err := runCtx.Err(); err != nil {
				<-sem
				break
			}
			wg.Add(1)
			go func(ins nalInstruction) {
				defer wg.Done()
				defer func() { <-sem }()
				asset, cached, err := h.synthesizeSegment(runCtx, job, snapshot, ins.slide.snapshot, provider, capabilities, ins.segment, dictRules, textNormEngine)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
				outcomes[ins.idx] = nalOutcome{asset: asset, cached: cached}
				if isTargeted(ins.segment) {
					completed.Add(1)
					reportProgress()
				}
			}(ins)
		}
		wg.Wait()
		if firstErr != nil {
			return timelineAssembly{}, firstErr
		}
	}

	// 2) 串行组装时间轴：保持页/段原序，统计与累计不涉共享写。
	asm := timelineAssembly{slides: make([]media.SlideInput, 0, len(planned))}
	currentSlideID := ""
	for _, ins := range instructions {
		out := outcomes[ins.idx]
		asm.stats.Segments++
		if out.cached {
			asm.stats.Cached++
		} else {
			asm.stats.Synthesized++
		}
		asm.totalMS += out.asset.DurationMS
		displayForTimeline := ins.segment.DisplayText
		if textNormEngine != nil {
			// 字幕/导出文本派生：剥离读/停标记（候选引擎 StripLegacyMarkers，V3.1 §10.2）。
			displayForTimeline = textNormEngine.Display(ins.segment.DisplayText)
		}
		// 精确逐字高亮（N2 数值展开）：显示字符 → 朗读 token 下标；不可用时回落比例近似。
		charToToken := buildCharToToken(ctx, textNormEngine, ins.segment.SpokenText, snapshot.Language, out.asset.Alignment)
		if currentSlideID != ins.slide.snapshot.SlideID {
			asm.slides = append(asm.slides, media.SlideInput{SlideID: ins.slide.snapshot.SlideID})
			currentSlideID = ins.slide.snapshot.SlideID
		}
		last := &asm.slides[len(asm.slides)-1]
		last.Segments = append(last.Segments, media.SegmentInput{
			SegmentID: scopedSegmentID(ins.slide.snapshot.SlideID, ins.segment.SegmentID), DisplayText: displayForTimeline,
			AudioKey: out.asset.AudioKey, DurationMS: out.asset.DurationMS, Alignment: out.asset.Alignment,
			CharToToken: charToToken,
		})
	}
	return asm, nil
}

// Handle implements pipeline.HandlerFunc for narration jobs.
func (h *NarrationHandler) Handle(ctx context.Context, job *pipeline.Job) error {
	if job == nil || job.Kind != pipeline.KindNarration {
		return errors.New("narration job: unexpected job kind")
	}
	var snapshot NarrationSnapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snapshot); err != nil {
		return fmt.Errorf("narration job: invalid input snapshot: %w", err)
	}
	if job.TenantID == "" || job.ProjectID == "" || len(snapshot.Slides) == 0 || snapshot.Language == "" || snapshot.VoiceID == "" {
		return errors.New("narration job: incomplete input snapshot")
	}

	provider, err := h.providerForTenant(ctx, job.TenantID)
	if err != nil {
		return err
	}
	capabilities, err := provider.Capabilities(ctx, snapshot.VoiceID)
	if err != nil {
		return classifyTTSError(err)
	}
	if len(capabilities.Languages) > 0 && !slices.Contains(capabilities.Languages, snapshot.Language) {
		return fmt.Errorf("narration job: voice %q does not support language %q", snapshot.VoiceID, snapshot.Language)
	}
	// G2-4 加载租户发音词典；未配置时为空集（不替换）。
	var dictRules pronunciation.Rules
	if h.dictLoader != nil {
		if rules, err := h.dictLoader.LoadTenantDefault(ctx, job.TenantID); err == nil {
			dictRules = rules
		}
	}
	// 获取按租户/语言隔离的冻结引擎（构建器可有界缓存）；加载失败 → 任务失败（R1）。
	// V3.1 主引擎切换：候选引擎（模块 textnorm）负责输出与缓存键。
	var textNormEngine TextPreparer
	if h.textNorm != nil {
		engine, err := h.textNorm.Resolve(ctx, job.TenantID, snapshot.Language)
		if err != nil {
			return err
		}
		textNormEngine = engine
	}
	// G2-5 构建分段过滤集：非空时仅统计指定分段进度（全部分段仍走 synthesizeSegment 以复用缓存）。
	var segmentFilter map[string]struct{}
	if len(snapshot.SegmentIDs) > 0 {
		segmentFilter = make(map[string]struct{}, len(snapshot.SegmentIDs))
		for _, id := range snapshot.SegmentIDs {
			segmentFilter[id] = struct{}{}
		}
	}
	planned := make([]plannedSlide, 0, len(snapshot.Slides))
	seenSlides := make(map[string]struct{}, len(snapshot.Slides))
	seenSegments := make(map[string]struct{})
	for _, slide := range snapshot.Slides {
		if slide.SlideID == "" || slide.ScriptRevision < 0 {
			return errors.New("narration job: incomplete slide snapshot")
		}
		if _, exists := seenSlides[slide.SlideID]; exists {
			return fmt.Errorf("narration job: duplicate slide %s", slide.SlideID)
		}
		seenSlides[slide.SlideID] = struct{}{}
		revision, err := h.scripts.Get(ctx, job.TenantID, job.ProjectID, snapshot.RevisionNo, slide.SlideID, snapshot.Language)
		if err != nil {
			return fmt.Errorf("narration job: load script: %w", err)
		}
		if revision.Revision != slide.ScriptRevision {
			return fmt.Errorf("narration job: script revision changed: expected %d, got %d", slide.ScriptRevision, revision.Revision)
		}
		if snapshot.RequireConfirmed && revision.Status != narration.StatusApproved && revision.Status != narration.StatusLocked {
			return errors.New("narration job: script must be approved or locked")
		}
		if len(revision.Segments) == 0 {
			return fmt.Errorf("narration job: slide %s has no segments", slide.SlideID)
		}
		for _, segment := range revision.Segments {
			if segment == nil || segment.SegmentID == "" || strings.TrimSpace(segment.SpokenText) == "" {
				return errors.New("narration job: segment id and spoken text are required")
			}
			// 分段 id 只需在同一页内唯一；时间轴按"页:段"做全局键，避免不同页复用 seg-01/seg-02 时误判重复。
			scoped := scopedSegmentID(slide.SlideID, segment.SegmentID)
			if _, exists := seenSegments[scoped]; exists {
				return fmt.Errorf("narration job: duplicate segment %s", scoped)
			}
			seenSegments[scoped] = struct{}{}
			if capabilities.MaxInputChars > 0 && utf8.RuneCountInString(segment.SpokenText) > capabilities.MaxInputChars {
				return fmt.Errorf("narration job: segment %s exceeds voice input limit", segment.SegmentID)
			}
		}
		planned = append(planned, plannedSlide{snapshot: slide, revision: revision})
	}

	totalMS := int64(0)
	// 本次任务的音频来源构成（按 job 局部累计，绝不放 handler 字段：handler 跨任务共享，
	// 并发任务会互相串数据）。
	stats := SynthesisStats{}

	asm, err := h.synthesizeAllSegments(ctx, job, snapshot, provider, capabilities, planned, segmentFilter, dictRules, textNormEngine)
	if err != nil {
		return err
	}
	timelineSlides := asm.slides
	stats = asm.stats
	totalMS = asm.totalMS

	// G2-5 时长控制：首轮合成后若超出目标 ±10%，自动按比例调整速率重合成。
	if snapshot.TargetDurationMS > 0 && totalMS > 0 {
		ratio := float64(snapshot.TargetDurationMS) / float64(totalMS)
		if ratio > 1.1 || ratio < 0.9 {
			adjusted := snapshot
			currentRate := adjusted.SpeechControl.RatePercent
			if currentRate <= 0 {
				currentRate = 100
			}
			newRate := int(float64(currentRate) * ratio)
			if newRate < 50 {
				newRate = 50
			}
			if newRate > 200 {
				newRate = 200
			}
			if newRate != currentRate {
				adjusted.SpeechControl.RatePercent = newRate
				return h.retryWithAdjustedRate(ctx, job, adjusted, segmentFilter, dictRules, provider, capabilities, planned, textNormEngine)
			}
		}
	}

	if err := h.publishTimeline(ctx, job, snapshot.Timing, timelineSlides, snapshot.RevisionNo, stats); err != nil {
		return err
	}
	// 回写 audio_revision：标记每段最近一次配音对应的脚本修订号（stale 判定）。
	for _, slide := range planned {
		if err := h.scripts.MarkAudioRevision(ctx, job.TenantID, job.ProjectID, snapshot.RevisionNo, slide.snapshot.SlideID, snapshot.Language, slide.snapshot.ScriptRevision); err != nil {
			// 非致命：stale 信号缺失不影响已生成音频的可用性。
			continue
		}
	}
	// 按真实合成时长结算额度（幂等键与 API 预占一致）。
	if h.usage != nil && job.IDempotencyKey != "" {
		if err := h.usage.Settle(ctx, job.TenantID, job.IDempotencyKey, usage.KindGenSeconds, float64(totalMS)/1000.0, ""); err != nil {
			return fmt.Errorf("narration job: settle usage: %w", err)
		}
	}
	return nil
}

// synthesizeSegment 合成单个分段。返回的 cached 表示该段是否命中内容哈希缓存：
// true=复用既有音频（未调用 TTS），false=本次真实合成。调用方据此向用户如实反馈，
// 避免把"全部命中缓存、未重新生成"报成"已重新生成"。
//
// snapshot.BypassCache 为真时（用户显式要求重新生成）两级缓存一律不读，直接真实合成；
// 写回仍用同一 configHash 键，即新音频覆盖旧音频对象。
func (h *NarrationHandler) synthesizeSegment(ctx context.Context, job *pipeline.Job, snapshot NarrationSnapshot, slide NarrationSlideSnapshot, provider tts.TTSProvider, capabilities tts.VoiceCapabilities, segment *narration.Segment, dictRules pronunciation.Rules, textNormEngine TextPreparer) (*SegmentAsset, bool, error) {
	// 文本规范化：引擎启用时先规范化（剥离读/停标记 + 词典 + 停顿事件），否则沿用今日发音词典替换。
	// 规范化结果送 TTS 与对齐；DisplayText（字幕）在调用方按 StripMarkers 派生。
	effectiveText := pronunciation.Apply(segment.SpokenText, dictRules)
	pauses := []tts.Pause{}
	if textNormEngine != nil {
		res, err := textNormEngine.Prepare(ctx, segment.SpokenText, snapshot.Language)
		if err != nil {
			return nil, false, fmt.Errorf("narration job: text normalization: %w", err)
		}
		effectiveText = res.Text
		pauses = res.Pauses
	}
	// 停顿事件并入 SpeechControl（V2.8 §7.5 时间域 B）：支持停顿的供应商在音频留真实静音，
	// VAD 对齐自动重锚；不支持的供应商只回显 Warnings（provider 层判定）。
	sc := snapshot.SpeechControl
	sc.Pauses = append(sc.Pauses, pauses...)
	configHash, err := synthesisHash(snapshot, capabilities, effectiveText, sc)
	if err != nil {
		return nil, false, err
	}
	manifestID := hashBytes([]byte(slide.SlideID + ":" + segment.SegmentID + ":" + configHash + fmt.Sprintf(":a%d", alignmentCacheVersion)))
	manifestKey := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: "cache",
		AssetType: "alignment", AssetID: manifestID, Ext: "json",
	}
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID, StepType: "tts_segment",
		StepKey: "tts_segment:" + slide.SlideID + ":" + segment.SegmentID + ":" + configHash,
	}

	// 用户显式要求重新生成时跳过缓存读取（见 NarrationSnapshot.BypassCache），
	// 直接进入真实合成分支；写回路径不变，因此新音频覆盖同一 configHash 键。
	if !snapshot.BypassCache {
		if manifest, ok, err := h.loadPublishedSegment(ctx, manifestKey); err != nil {
			return nil, false, err
		} else if ok {
			h.recordCacheHit(job, "project")
			step.State, step.ResultRef = pipeline.StepSuccess, manifestKey.String()
			if err := h.markStep(ctx, step); err != nil {
				return nil, false, err
			}
			return manifest, true, nil
		} else if manifest != nil {
			return nil, false, errors.New("narration job: invalid cached segment manifest")
		}
	}

	// G2-7 内容哈希去重：相同合成配置（文本+音色+语率+模型）的音频按内容寻址存放在
	// 租户级共享缓存，跨页面/跨项目直接复用，零 TTS 调用。
	sharedKey := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "segments", AssetID: configHash, Ext: "json",
	}
	if !snapshot.BypassCache {
		if shared, ok, err := h.loadSharedSegment(ctx, sharedKey); err != nil {
			return nil, false, err
		} else if ok {
			h.recordCacheHit(job, "shared")
			// 对齐算法升级（alignmentCacheVersion）后：复用缓存音频，仅重算对齐，避免重新合成。
			if upgraded, ok := h.realignSharedSegment(ctx, shared, effectiveText); ok {
				shared = upgraded
			}
			manifestBytes, err := json.Marshal(shared)
			if err != nil {
				return nil, false, err
			}
			if err := h.objects.Put(ctx, manifestKey, bytes.NewReader(manifestBytes), objectstore.ObjectMeta{
				ContentType: "application/json", ContentHash: hashBytes(manifestBytes), Size: int64(len(manifestBytes)),
			}); err != nil {
				return nil, false, fmt.Errorf("narration job: publish manifest: %w", err)
			}
			step.State, step.ResultRef = pipeline.StepSuccess, manifestKey.String()
			if err := h.markStep(ctx, step); err != nil {
				return nil, false, err
			}
			return shared, true, nil
		}
	}

	step.State = pipeline.StepPending
	if err := h.markStep(ctx, step); err != nil {
		return nil, false, err
	}
	failStep := func(err error) (*SegmentAsset, bool, error) {
		step.State = pipeline.StepFailed
		_ = h.markStep(ctx, step)
		return nil, false, err
	}
	request := tts.SynthesisRequest{
		LogicalOpID: job.ID + ":" + slide.SlideID + ":" + segment.SegmentID + ":" + configHash,
		VoiceID:     snapshot.VoiceID, Text: effectiveText, Language: snapshot.Language,
		SpeechControl: sc, SampleRate: snapshot.SampleRate,
	}
	started := time.Now()
	result, err := provider.Synthesize(ctx, request)
	h.recordTTSSynthesis(job, started, err)
	if err != nil {
		return failStep(classifyTTSError(err))
	}
	ext, err := audioExtension(result.Format)
	if err != nil {
		return failStep(err)
	}
	if err := validateSynthesisResult(request.Text, result); err != nil {
		return failStep(err)
	}
	audioHash := hashBytes(result.Audio)
	warnings := result.Warnings
	if len(pauses) > 0 && !capabilities.SupportsPauses {
		// V2.8 §7.5 期望管理：结构化停顿未被供应商消费时，显式回显信号（G2 停顿功能层降级）。
		// 停顿不出现在真实音频中，但用户会在 Warnings 里看到该供应商"不支持停顿"。
		warnings = append(append([]string{}, warnings...), "pause_not_supported")
	}
	// G2-7 音频按内容寻址（configHash）存放在租户级共享路径，跨项目复用同一对象。
	audioKey := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "audio", AssetID: configHash, Ext: ext,
	}
	manifest := SegmentAsset{
		SegmentID: segment.SegmentID, AudioKey: audioKey.String(), AudioHash: audioHash,
		Format: result.Format, DurationMS: result.RealDurationMS, Alignment: result.Alignment,
		ProviderRequestID: result.ProviderRequestID, BillingUnit: result.BillingUnit,
		BillingQuantity: result.BillingQuantity, Warnings: warnings,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return failStep(err)
	}
	if err := h.objects.Put(ctx, audioKey, bytes.NewReader(result.Audio), objectstore.ObjectMeta{
		ContentType: result.Format, ContentHash: audioHash, Size: int64(len(result.Audio)),
	}); err != nil {
		return failStep(fmt.Errorf("narration job: publish audio: %w", err))
	}
	// 写共享分段清单，供后续相同内容复用。
	sharedManifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return failStep(err)
	}
	if err := h.objects.Put(ctx, sharedKey, bytes.NewReader(sharedManifestBytes), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(sharedManifestBytes), Size: int64(len(sharedManifestBytes)),
	}); err != nil {
		return failStep(fmt.Errorf("narration job: publish shared manifest: %w", err))
	}
	if err := h.objects.Put(ctx, manifestKey, bytes.NewReader(manifestBytes), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(manifestBytes), Size: int64(len(manifestBytes)),
	}); err != nil {
		return failStep(fmt.Errorf("narration job: publish manifest: %w", err))
	}
	step.State, step.ResultRef = pipeline.StepSuccess, manifestKey.String()
	if err := h.markStep(ctx, step); err != nil {
		return nil, false, err
	}
	return &manifest, false, nil
}

func (h *NarrationHandler) recordTTSSynthesis(job *pipeline.Job, started time.Time, err error) {
	if h.metrics == nil {
		return
	}
	retryable, throttled := ttsErrorFlags(err)
	h.metrics.SegmentSynthesized(job, retryable, throttled, time.Since(started), err)
}

// recordCacheHit 记录缓存命中（G2-7 去重命中率观测）。
func (h *NarrationHandler) recordCacheHit(job *pipeline.Job, scope string) {
	if h.metrics == nil {
		return
	}
	h.metrics.SegmentCacheHit(job, scope)
}

// SynthesisStatsKey 返回某次配音任务的音频来源统计对象键（按 job 独立寻址）。
func SynthesisStatsKey(tenantID, projectID, jobID string) objectstore.ObjectKey {
	return objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID, Revision: "narration-" + jobID,
		AssetType: "synth", AssetID: "stats", Ext: "json",
	}
}

// publishSynthesisStats 落一份本次任务的音频来源构成。
//
// 刻意不塞进 TimelineAsset：bundle 以时间轴内容寻址，且三个对象已存在时 publishTimeline
// 会提前返回、不重写（530 行附近）。把统计放进 bundle，相同时间轴的重复运行就会一直读到
// 首轮的旧统计——恰好在"全部命中缓存"这个最需要如实告知的场景里，报出上一次的真实合成数。
func (h *NarrationHandler) publishSynthesisStats(ctx context.Context, job *pipeline.Job, stats SynthesisStats) error {
	data, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	return h.objects.Put(ctx, SynthesisStatsKey(job.TenantID, job.ProjectID, job.ID), bytes.NewReader(data), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(data), Size: int64(len(data)),
	})
}

// buildCharToToken 为精确逐字高亮计算"显示字符 → 朗读 token 下标"映射（V2.8 §7.5 时间域 A）。
// 主引擎切换后以候选引擎（模块 textnorm）的 SourceMap 派生映射；引擎/对齐信息缺失或
// 长度不匹配时返回 nil（调用方回落 remapCharsToCount 比例近似）。
func buildCharToToken(ctx context.Context, engine TextPreparer, spoken, lang string, alignment *tts.Alignment) []int {
	if engine == nil || alignment == nil || len(alignment.Tokens) == 0 {
		return nil
	}
	res, err := engine.Prepare(ctx, spoken, lang)
	if err != nil {
		return nil
	}
	return res.DisplayToSpoken
}

// publishTimeline 发布播放时间轴（含精确逐字高亮映射；无映射时 timeline 内回落比例近似）。

func (h *NarrationHandler) publishTimeline(ctx context.Context, job *pipeline.Job, timing media.Timing, slides []media.SlideInput, revisionNo int, stats SynthesisStats) error {
	timeline, err := media.BuildTimeline(slides, timing)
	if err != nil {
		return err
	}
	// 记录时间轴来源（源版本号 + 展示名），供成品库按"PPT 名称 + 版本"精确展示：
	// 成品行与 source_revisions 无外键，故在生成时把来源冗余进时间轴 JSON，export 落库时再写入成品。
	if h.sourceRevisions != nil && revisionNo > 0 {
		if rev, lerr := h.sourceRevisions.GetSourceRevision(ctx, job.TenantID, job.ProjectID, revisionNo); lerr == nil && rev != nil {
			timeline.SourceRevisionNo = revisionNo
			timeline.SourceDisplayName = rev.DisplayName
		}
	}
	timelineBytes, err := json.Marshal(timeline)
	if err != nil {
		return err
	}
	assetID := hashBytes(timelineBytes)
	revision := "narration-" + job.ID
	srtKey := objectstore.ObjectKey{TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: revision, AssetType: "subtitle", AssetID: assetID, Ext: "srt"}
	vttKey := objectstore.ObjectKey{TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: revision, AssetType: "subtitle", AssetID: assetID, Ext: "vtt"}
	bundleKey := objectstore.ObjectKey{TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: revision, AssetType: "timeline", AssetID: assetID, Ext: "json"}
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID, StepType: "timeline",
		StepKey: "timeline:" + assetID, ResultRef: bundleKey.String(),
	}
	// 统计先于 objectsExist 短路写入：时间轴内容未变时 bundle 不重写，但本次的
	// 缓存/合成构成必须按 job 更新，否则重复运行会一直读到首轮统计。
	if err := h.publishSynthesisStats(ctx, job, stats); err != nil {
		return err
	}
	if complete, err := h.objectsExist(ctx, srtKey, vttKey, bundleKey); err != nil {
		return err
	} else if complete {
		step.State = pipeline.StepSuccess
		return h.markStep(ctx, step)
	}
	srt, err := media.RenderSRT(timeline.Subtitles)
	if err != nil {
		return err
	}
	vtt, err := media.RenderWebVTT(timeline.Subtitles)
	if err != nil {
		return err
	}
	bundleBytes, err := json.Marshal(TimelineAsset{Timeline: timeline, SRTKey: srtKey.String(), VTTKey: vttKey.String()})
	if err != nil {
		return err
	}
	step.State = pipeline.StepPending
	if err := h.markStep(ctx, step); err != nil {
		return err
	}
	failStep := func(err error) error {
		step.State = pipeline.StepFailed
		_ = h.markStep(ctx, step)
		return err
	}
	for _, asset := range []struct {
		key         objectstore.ObjectKey
		contentType string
		data        []byte
	}{
		{srtKey, "application/x-subrip", srt},
		{vttKey, "text/vtt; charset=utf-8", vtt},
		{bundleKey, "application/json", bundleBytes},
	} {
		if err := h.objects.Put(ctx, asset.key, bytes.NewReader(asset.data), objectstore.ObjectMeta{
			ContentType: asset.contentType, ContentHash: hashBytes(asset.data), Size: int64(len(asset.data)),
		}); err != nil {
			return failStep(fmt.Errorf("narration job: publish timeline asset: %w", err))
		}
	}
	step.State = pipeline.StepSuccess
	return h.markStep(ctx, step)
}

// retryWithAdjustedRate 用调整后的语速重新执行合成（时长控制）。
func (h *NarrationHandler) retryWithAdjustedRate(ctx context.Context, job *pipeline.Job, adjusted NarrationSnapshot, segmentFilter map[string]struct{}, dictRules pronunciation.Rules, provider tts.TTSProvider, capabilities tts.VoiceCapabilities, planned []plannedSlide, textNormEngine TextPreparer) error {
	asm, err := h.synthesizeAllSegments(ctx, job, adjusted, provider, capabilities, planned, nil, dictRules, textNormEngine)
	if err != nil {
		return err
	}
	timelineSlides := asm.slides
	totalMS := asm.totalMS
	stats := asm.stats
	if err := h.publishTimeline(ctx, job, adjusted.Timing, timelineSlides, adjusted.RevisionNo, stats); err != nil {
		return err
	}
	if h.usage != nil && job.IDempotencyKey != "" {
		if err := h.usage.Settle(ctx, job.TenantID, job.IDempotencyKey, usage.KindGenSeconds, float64(totalMS)/1000.0, ""); err != nil {
			return fmt.Errorf("narration job: settle usage: %w", err)
		}
	}
	return nil
}

func (h *NarrationHandler) objectsExist(ctx context.Context, keys ...objectstore.ObjectKey) (bool, error) {
	for _, key := range keys {
		r, _, err := h.objects.Get(ctx, key)
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if err := r.Close(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// loadPublishedSegment returns ok only when both the manifest and its audio exist.
func (h *NarrationHandler) loadPublishedSegment(ctx context.Context, key objectstore.ObjectKey) (*SegmentAsset, bool, error) {
	r, _, err := h.objects.Get(ctx, key)
	if errors.Is(err, objectstore.ErrObjectNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	var manifest SegmentAsset
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.AudioKey == "" || manifest.DurationMS <= 0 {
		return &manifest, false, nil
	}
	audioKey, err := objectstore.Parse(manifest.AudioKey)
	// G2-7 音频存放在租户级共享路径（shared 伪项目），清单跨项目引用时仅校验租户一致。
	if err != nil || audioKey.TenantID != key.TenantID {
		return &manifest, false, nil
	}
	audio, _, err := h.objects.Get(ctx, audioKey)
	if errors.Is(err, objectstore.ErrObjectNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := audio.Close(); err != nil {
		return nil, false, err
	}
	return &manifest, true, nil
}

// loadSharedSegment 加载租户级共享分段清单（G2-7 内容哈希去重）。
// 清单有效且音频存在时返回 true；跨项目复用同一音频对象，零 TTS 调用。
func (h *NarrationHandler) loadSharedSegment(ctx context.Context, key objectstore.ObjectKey) (*SegmentAsset, bool, error) {
	r, _, err := h.objects.Get(ctx, key)
	if errors.Is(err, objectstore.ErrObjectNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	var manifest SegmentAsset
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.AudioKey == "" || manifest.DurationMS <= 0 {
		return nil, false, nil
	}
	audioKey, err := objectstore.Parse(manifest.AudioKey)
	if err != nil || audioKey.TenantID != key.TenantID {
		return nil, false, nil
	}
	audio, _, err := h.objects.Get(ctx, audioKey)
	if errors.Is(err, objectstore.ErrObjectNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := audio.Close(); err != nil {
		return nil, false, err
	}
	return &manifest, true, nil
}

// realignSharedSegment 复用已缓存音频，用当前对齐算法重算字级时间戳（不重新合成、不额外计费）。
// 仅当原对齐为估算类（estimated/estimated_vad，即非供应商原生时间戳/强制对齐）且新算法确有
// 改进（VAD 生效）时才替换，避免把更可信的时间戳降级。
func (h *NarrationHandler) realignSharedSegment(ctx context.Context, seg *SegmentAsset, text string) (*SegmentAsset, bool) {
	if seg == nil || seg.Alignment == nil || seg.DurationMS <= 0 {
		return nil, false
	}
	switch seg.Alignment.Method {
	case tts.AlignEstimate, tts.AlignEstimateVAD:
		// 估算类才重算。
	default:
		return nil, false
	}
	audioKey, err := objectstore.Parse(seg.AudioKey)
	if err != nil {
		return nil, false
	}
	r, _, err := h.objects.Get(ctx, audioKey)
	if err != nil {
		return nil, false
	}
	wav, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		return nil, false
	}
	align := tts.RecomputeAlignment(text, wav, seg.DurationMS)
	if align.Method != tts.AlignEstimateVAD {
		return nil, false // VAD 未生效，保留原结果
	}
	upgraded := *seg
	upgraded.Alignment = &align
	return &upgraded, true
}

func synthesisHash(snapshot NarrationSnapshot, capabilities tts.VoiceCapabilities, text string, sc tts.SpeechControl) (string, error) {
	input := struct {
		AdapterVersion string            `json:"adapterVersion"`
		Text           string            `json:"text"`
		Language       string            `json:"language"`
		VoiceID        string            `json:"voiceId"`
		ModelID        string            `json:"modelId"`
		Region         string            `json:"region"`
		SampleRate     int               `json:"sampleRate"`
		SpeechControl  tts.SpeechControl `json:"speechControl"`
	}{ttsAdapterVersion, text, snapshot.Language, snapshot.VoiceID, capabilities.ModelID, capabilities.Region, snapshot.SampleRate, sc}
	b, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return hashBytes(b), nil
}

func validateSynthesisResult(text string, result tts.SynthesisResult) error {
	if len(result.Audio) == 0 || result.RealDurationMS <= 0 {
		return errors.New("narration job: provider returned empty audio or duration")
	}
	if result.Alignment == nil || result.Alignment.Text != text {
		return errors.New("narration job: provider returned missing or mismatched alignment")
	}
	limitUS := result.RealDurationMS * 1000
	var previousEnd int64
	for _, token := range result.Alignment.Tokens {
		if token.StartUS < previousEnd || token.EndUS <= token.StartUS || token.EndUS > limitUS {
			return errors.New("narration job: provider returned invalid alignment offsets")
		}
		previousEnd = token.EndUS
	}
	return nil
}

func audioExtension(format string) (string, error) {
	switch format {
	case "audio/wav":
		return "wav", nil
	case "audio/mpeg":
		return "mp3", nil
	default:
		return "", fmt.Errorf("narration job: unsupported audio format %q", format)
	}
}

func classifyTTSError(err error) error {
	var retryable *tts.RetryableError
	if !errors.As(err, &retryable) {
		return err
	}
	retry := &pipeline.RetryError{Err: err}
	if retryable.RetryAfter > 0 {
		retry.At = time.Now().Add(retryable.RetryAfter)
	}
	return retry
}

func ttsErrorFlags(err error) (retryable bool, throttled bool) {
	if err == nil {
		return false, false
	}
	var retry *tts.RetryableError
	retryable = errors.As(err, &retry)
	var status interface{ HTTPStatus() int }
	if errors.As(err, &status) && status.HTTPStatus() == http.StatusTooManyRequests {
		throttled = true
	}
	return retryable, throttled
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashReader 流式计算 reader 内容的 SHA-256 与字节数，供导出等大文件路径在不上浮内存的前提下
// 得到内容寻址键与元数据尺寸。
func hashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// scopedSegmentID 生成时间轴使用的全局分段键：分段 id 只需页内唯一，
// 但时间轴/字幕/播放清单要求全局唯一，因此统一以「页:段」作为键。
func scopedSegmentID(slideID, segmentID string) string {
	return slideID + ":" + segmentID
}
