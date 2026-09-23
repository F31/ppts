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
	"time"
	"unicode/utf8"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
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
}

// NarrationSlideSnapshot binds one slide to the script revision being voiced.
type NarrationSlideSnapshot struct {
	SlideID        string `json:"slideId"`
	ScriptRevision int64  `json:"scriptRevision"`
}

// SegmentAsset is the immutable manifest consumed by timeline assembly.
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
	providerFor func(ctx context.Context, tenantID string) (tts.TTSProvider, error)
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

type plannedSlide struct {
	snapshot NarrationSlideSnapshot
	revision *narration.Revision
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

	timelineSlides := make([]media.SlideInput, 0, len(planned))
	totalMS := int64(0)
	completedSegments := 0
	// G2-5 进度统计：有过滤集时仅统计目标分段。
	totalTargeted := 0
	for _, slide := range planned {
		for _, segment := range slide.revision.Segments {
			if segmentFilter == nil {
				totalTargeted++
			} else if _, ok := segmentFilter[segment.SegmentID]; ok {
				totalTargeted++
			}
		}
	}
	for _, slide := range planned {
		timelineSlide := media.SlideInput{SlideID: slide.snapshot.SlideID}
		for _, segment := range slide.revision.Segments {
			if err := ctx.Err(); err != nil {
				return err
			}
			// synthesizeSegment 内部按 content hash 做缓存，未修改分段直接命中缓存，开销极低。
			asset, err := h.synthesizeSegment(ctx, job, snapshot, slide.snapshot, provider, capabilities, segment, dictRules)
			if err != nil {
				return err
			}
			totalMS += asset.DurationMS
			timelineSlide.Segments = append(timelineSlide.Segments, media.SegmentInput{
				SegmentID: scopedSegmentID(slide.snapshot.SlideID, segment.SegmentID), DisplayText: segment.DisplayText,
				AudioKey: asset.AudioKey, DurationMS: asset.DurationMS, Alignment: asset.Alignment,
			})
			if segmentFilter == nil {
				completedSegments++
			} else if _, ok := segmentFilter[segment.SegmentID]; ok {
				completedSegments++
			}
			if totalTargeted > 0 {
				pct := completedSegments * 100 / totalTargeted
				if err := pipeline.ReportProgress(ctx, pct); err != nil {
					return err
				}
			}
		}
		timelineSlides = append(timelineSlides, timelineSlide)
	}

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
				return h.retryWithAdjustedRate(ctx, job, adjusted, segmentFilter, dictRules, provider, capabilities, planned)
			}
		}
	}

	if err := h.publishTimeline(ctx, job, snapshot.Timing, timelineSlides); err != nil {
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

func (h *NarrationHandler) synthesizeSegment(ctx context.Context, job *pipeline.Job, snapshot NarrationSnapshot, slide NarrationSlideSnapshot, provider tts.TTSProvider, capabilities tts.VoiceCapabilities, segment *narration.Segment, dictRules pronunciation.Rules) (*SegmentAsset, error) {
	// G2-4 应用发音词典替换，effectiveText 送 TTS。
	effectiveText := pronunciation.Apply(segment.SpokenText, dictRules)
	configHash, err := synthesisHash(snapshot, capabilities, effectiveText)
	if err != nil {
		return nil, err
	}
	manifestID := hashBytes([]byte(slide.SlideID + ":" + segment.SegmentID + ":" + configHash))
	manifestKey := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: "cache",
		AssetType: "alignment", AssetID: manifestID, Ext: "json",
	}
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID, StepType: "tts_segment",
		StepKey: "tts_segment:" + slide.SlideID + ":" + segment.SegmentID + ":" + configHash,
	}

	if manifest, ok, err := h.loadPublishedSegment(ctx, manifestKey); err != nil {
		return nil, err
	} else if ok {
		h.recordCacheHit(job, "project")
		step.State, step.ResultRef = pipeline.StepSuccess, manifestKey.String()
		if err := h.steps.MarkStep(ctx, step); err != nil {
			return nil, err
		}
		return manifest, nil
	} else if manifest != nil {
		return nil, errors.New("narration job: invalid cached segment manifest")
	}

	// G2-7 内容哈希去重：相同合成配置（文本+音色+语率+模型）的音频按内容寻址存放在
	// 租户级共享缓存，跨页面/跨项目直接复用，零 TTS 调用。
	sharedKey := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "segments", AssetID: configHash, Ext: "json",
	}
	if shared, ok, err := h.loadSharedSegment(ctx, sharedKey); err != nil {
		return nil, err
	} else if ok {
		h.recordCacheHit(job, "shared")
		manifestBytes, err := json.Marshal(shared)
		if err != nil {
			return nil, err
		}
		if err := h.objects.Put(ctx, manifestKey, bytes.NewReader(manifestBytes), objectstore.ObjectMeta{
			ContentType: "application/json", ContentHash: hashBytes(manifestBytes), Size: int64(len(manifestBytes)),
		}); err != nil {
			return nil, fmt.Errorf("narration job: publish manifest: %w", err)
		}
		step.State, step.ResultRef = pipeline.StepSuccess, manifestKey.String()
		if err := h.steps.MarkStep(ctx, step); err != nil {
			return nil, err
		}
		return shared, nil
	}

	step.State = pipeline.StepPending
	if err := h.steps.MarkStep(ctx, step); err != nil {
		return nil, err
	}
	failStep := func(err error) (*SegmentAsset, error) {
		step.State = pipeline.StepFailed
		_ = h.steps.MarkStep(ctx, step)
		return nil, err
	}
	request := tts.SynthesisRequest{
		LogicalOpID: job.ID + ":" + slide.SlideID + ":" + segment.SegmentID + ":" + configHash,
		VoiceID:     snapshot.VoiceID, Text: effectiveText, Language: snapshot.Language,
		SpeechControl: snapshot.SpeechControl, SampleRate: snapshot.SampleRate,
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
	// G2-7 音频按内容寻址（configHash）存放在租户级共享路径，跨项目复用同一对象。
	audioKey := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "audio", AssetID: configHash, Ext: ext,
	}
	manifest := SegmentAsset{
		SegmentID: segment.SegmentID, AudioKey: audioKey.String(), AudioHash: audioHash,
		Format: result.Format, DurationMS: result.RealDurationMS, Alignment: result.Alignment,
		ProviderRequestID: result.ProviderRequestID, BillingUnit: result.BillingUnit,
		BillingQuantity: result.BillingQuantity, Warnings: result.Warnings,
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
	if err := h.steps.MarkStep(ctx, step); err != nil {
		return nil, err
	}
	return &manifest, nil
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

func (h *NarrationHandler) publishTimeline(ctx context.Context, job *pipeline.Job, timing media.Timing, slides []media.SlideInput) error {
	timeline, err := media.BuildTimeline(slides, timing)
	if err != nil {
		return err
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
	if complete, err := h.objectsExist(ctx, srtKey, vttKey, bundleKey); err != nil {
		return err
	} else if complete {
		step.State = pipeline.StepSuccess
		return h.steps.MarkStep(ctx, step)
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
	if err := h.steps.MarkStep(ctx, step); err != nil {
		return err
	}
	failStep := func(err error) error {
		step.State = pipeline.StepFailed
		_ = h.steps.MarkStep(ctx, step)
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
	return h.steps.MarkStep(ctx, step)
}

// retryWithAdjustedRate 用调整后的语速重新执行合成（时长控制）。
func (h *NarrationHandler) retryWithAdjustedRate(ctx context.Context, job *pipeline.Job, adjusted NarrationSnapshot, segmentFilter map[string]struct{}, dictRules pronunciation.Rules, provider tts.TTSProvider, capabilities tts.VoiceCapabilities, planned []plannedSlide) error {
	timelineSlides := make([]media.SlideInput, 0, len(planned))
	totalMS := int64(0)
	for _, slide := range planned {
		timelineSlide := media.SlideInput{SlideID: slide.snapshot.SlideID}
		for _, segment := range slide.revision.Segments {
			if err := ctx.Err(); err != nil {
				return err
			}
			asset, err := h.synthesizeSegment(ctx, job, adjusted, slide.snapshot, provider, capabilities, segment, dictRules)
			if err != nil {
				return err
			}
			totalMS += asset.DurationMS
			timelineSlide.Segments = append(timelineSlide.Segments, media.SegmentInput{
				SegmentID: scopedSegmentID(slide.snapshot.SlideID, segment.SegmentID), DisplayText: segment.DisplayText,
				AudioKey: asset.AudioKey, DurationMS: asset.DurationMS, Alignment: asset.Alignment,
			})
		}
		timelineSlides = append(timelineSlides, timelineSlide)
	}
	if err := h.publishTimeline(ctx, job, adjusted.Timing, timelineSlides); err != nil {
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

func synthesisHash(snapshot NarrationSnapshot, capabilities tts.VoiceCapabilities, text string) (string, error) {
	input := struct {
		AdapterVersion string            `json:"adapterVersion"`
		Text           string            `json:"text"`
		Language       string            `json:"language"`
		VoiceID        string            `json:"voiceId"`
		ModelID        string            `json:"modelId"`
		Region         string            `json:"region"`
		SampleRate     int               `json:"sampleRate"`
		SpeechControl  tts.SpeechControl `json:"speechControl"`
	}{ttsAdapterVersion, text, snapshot.Language, snapshot.VoiceID, capabilities.ModelID, capabilities.Region, snapshot.SampleRate, snapshot.SpeechControl}
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

// scopedSegmentID 生成时间轴使用的全局分段键：分段 id 只需页内唯一，
// 但时间轴/字幕/播放清单要求全局唯一，因此统一以「页:段」作为键。
func scopedSegmentID(slideID, segmentID string) string {
	return slideID + ":" + segmentID
}
