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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

const ttsAdapterVersion = "v1"

// NarrationSnapshot fixes all inputs used by a narration job. A later script
// edit must enqueue a new job rather than changing an in-flight job's inputs.
type NarrationSnapshot struct {
	Slides           []NarrationSlideSnapshot `json:"slides"`
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
	objects  objectstore.ObjectStore
	provider tts.TTSProvider
}

// NewNarrationHandler creates a segmented TTS handler.
func NewNarrationHandler(scripts narration.Store, steps interface {
	MarkStep(context.Context, pipeline.JobStep) error
}, objects objectstore.ObjectStore, provider tts.TTSProvider) *NarrationHandler {
	return &NarrationHandler{scripts: scripts, steps: steps, objects: objects, provider: provider}
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

	capabilities, err := h.provider.Capabilities(ctx, snapshot.VoiceID)
	if err != nil {
		return classifyTTSError(err)
	}
	if len(capabilities.Languages) > 0 && !slices.Contains(capabilities.Languages, snapshot.Language) {
		return fmt.Errorf("narration job: voice %q does not support language %q", snapshot.VoiceID, snapshot.Language)
	}
	type plannedSlide struct {
		snapshot NarrationSlideSnapshot
		revision *narration.Revision
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
		revision, err := h.scripts.Get(ctx, job.TenantID, job.ProjectID, slide.SlideID, snapshot.Language)
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
			if _, exists := seenSegments[segment.SegmentID]; exists {
				return fmt.Errorf("narration job: duplicate segment %s", segment.SegmentID)
			}
			seenSegments[segment.SegmentID] = struct{}{}
			if capabilities.MaxInputChars > 0 && utf8.RuneCountInString(segment.SpokenText) > capabilities.MaxInputChars {
				return fmt.Errorf("narration job: segment %s exceeds voice input limit", segment.SegmentID)
			}
		}
		planned = append(planned, plannedSlide{snapshot: slide, revision: revision})
	}
	timelineSlides := make([]media.SlideInput, 0, len(planned))
	for _, slide := range planned {
		timelineSlide := media.SlideInput{SlideID: slide.snapshot.SlideID}
		for _, segment := range slide.revision.Segments {
			if err := ctx.Err(); err != nil {
				return err
			}
			asset, err := h.synthesizeSegment(ctx, job, snapshot, slide.snapshot, capabilities, segment)
			if err != nil {
				return err
			}
			timelineSlide.Segments = append(timelineSlide.Segments, media.SegmentInput{
				SegmentID: segment.SegmentID, DisplayText: segment.DisplayText,
				AudioKey: asset.AudioKey, DurationMS: asset.DurationMS, Alignment: asset.Alignment,
			})
		}
		timelineSlides = append(timelineSlides, timelineSlide)
	}
	return h.publishTimeline(ctx, job, snapshot.Timing, timelineSlides)
}

func (h *NarrationHandler) synthesizeSegment(ctx context.Context, job *pipeline.Job, snapshot NarrationSnapshot, slide NarrationSlideSnapshot, capabilities tts.VoiceCapabilities, segment *narration.Segment) (*SegmentAsset, error) {
	configHash, err := synthesisHash(snapshot, capabilities, segment.SpokenText)
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
		step.State, step.ResultRef = pipeline.StepSuccess, manifestKey.String()
		if err := h.steps.MarkStep(ctx, step); err != nil {
			return nil, err
		}
		return manifest, nil
	} else if manifest != nil {
		return nil, errors.New("narration job: invalid cached segment manifest")
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
		VoiceID:     snapshot.VoiceID, Text: segment.SpokenText, Language: snapshot.Language,
		SpeechControl: snapshot.SpeechControl, SampleRate: snapshot.SampleRate,
	}
	result, err := h.provider.Synthesize(ctx, request)
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
	audioKey := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: "cache",
		AssetType: "audio", AssetID: manifestID, Ext: ext,
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
	if err != nil || audioKey.TenantID != key.TenantID || audioKey.ProjectID != key.ProjectID {
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

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
