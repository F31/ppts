package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

type narrationStoreStub struct {
	revision *narration.Revision
	err      error
}

func (s *narrationStoreStub) Get(context.Context, string, string, string, string) (*narration.Revision, error) {
	return s.revision, s.err
}

func (*narrationStoreStub) Update(context.Context, string, string, string, string, int64, []*narration.Segment) (*narration.Revision, error) {
	return nil, errors.New("not implemented")
}

func (*narrationStoreStub) SetStatus(context.Context, string, string, string, string, narration.ScriptStatus) (*narration.Revision, error) {
	return nil, errors.New("not implemented")
}

func (*narrationStoreStub) EnsureExists(context.Context, string, string, string, string, narration.ScriptMode) (*narration.Revision, error) {
	return nil, errors.New("not implemented")
}

type stepRecorder struct {
	latest  map[string]pipeline.JobStep
	history []pipeline.JobStep
}

func (s *stepRecorder) MarkStep(_ context.Context, step pipeline.JobStep) error {
	if s.latest == nil {
		s.latest = make(map[string]pipeline.JobStep)
	}
	s.latest[step.StepKey] = step
	s.history = append(s.history, step)
	return nil
}

type testTTSProvider struct {
	caps  tts.VoiceCapabilities
	err   error
	calls int
	fake  *tts.FakeProvider
}

type ttsMetricsRecorder struct {
	total     int
	failed    int
	retryable bool
	throttled bool
}

func (r *ttsMetricsRecorder) SegmentSynthesized(_ *pipeline.Job, retryable, throttled bool, _ time.Duration, err error) {
	r.total++
	if err != nil {
		r.failed++
	}
	r.retryable = r.retryable || retryable
	r.throttled = r.throttled || throttled
}

func (r *ttsMetricsRecorder) SegmentCacheHit(_ *pipeline.Job, _ string) {
	r.total++
}

type httpStatusErr struct{ status int }

func (e httpStatusErr) Error() string   { return "http status" }
func (e httpStatusErr) HTTPStatus() int { return e.status }

func (p *testTTSProvider) Capabilities(context.Context, string) (tts.VoiceCapabilities, error) {
	return p.caps, nil
}

func (p *testTTSProvider) Synthesize(ctx context.Context, req tts.SynthesisRequest) (tts.SynthesisResult, error) {
	p.calls++
	if p.err != nil {
		return tts.SynthesisResult{}, p.err
	}
	return p.fake.Synthesize(ctx, req)
}

func approvedRevision(segments ...*narration.Segment) *narration.Revision {
	return &narration.Revision{
		ProjectID: "project-1", SlideID: "slide-1", Language: "zh-CN",
		Status: narration.StatusApproved, Revision: 4, Segments: segments,
	}
}

func narrationJob(t *testing.T) *pipeline.Job {
	return narrationJobAt(t, "job-1", 4)
}

func narrationJobAt(t *testing.T, jobID string, revision int64) *pipeline.Job {
	t.Helper()
	snapshot, err := json.Marshal(NarrationSnapshot{
		Slides:   []NarrationSlideSnapshot{{SlideID: "slide-1", ScriptRevision: revision}},
		Language: "zh-CN", VoiceID: "voice-1", SampleRate: 16000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pipeline.Job{
		ID: jobID, TenantID: "tenant-1", ProjectID: "project-1",
		Kind: pipeline.KindNarration, InputSnapshot: string(snapshot),
	}
}

func providerForTests() *testTTSProvider {
	return &testTTSProvider{
		caps: tts.VoiceCapabilities{
			VoiceID: "voice-1", Languages: []string{"zh-CN"}, MaxInputChars: 100,
			ModelID: "fake-wav", Region: "dev-fake",
		},
		fake: tts.NewFakeProvider(),
	}
}

func TestNarrationHandlerPublishesAndReusesSegments(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(
		&narration.Segment{SegmentID: "seg-1", DisplayText: "第一段", SpokenText: "相同文本"},
		&narration.Segment{SegmentID: "seg-2", DisplayText: "第二段", SpokenText: "相同文本"},
	)}
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	provider := providerForTests()
	handler := NewNarrationHandler(store, steps, objects, provider)

	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// G2-7 内容哈希去重：两段文本相同，第二段命中租户共享缓存，只合成一次。
	if provider.calls != 1 || len(steps.latest) != 3 {
		t.Fatalf("calls=%d steps=%d", provider.calls, len(steps.latest))
	}
	manifestRefs := make(map[string]struct{}, 2)
	var timelineRef string
	for _, step := range steps.latest {
		if step.State != pipeline.StepSuccess || step.ResultRef == "" {
			t.Fatalf("step = %+v", step)
		}
		if step.StepType == "timeline" {
			timelineRef = step.ResultRef
			continue
		}
		manifestRefs[step.ResultRef] = struct{}{}
		manifestKey, err := objectstore.Parse(step.ResultRef)
		if err != nil {
			t.Fatalf("manifest key: %v", err)
		}
		r, _, err := objects.Get(context.Background(), manifestKey)
		if err != nil {
			t.Fatalf("manifest get: %v", err)
		}
		data, _ := io.ReadAll(r)
		r.Close()
		var manifest SegmentAsset
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("manifest decode: %v", err)
		}
		if manifest.DurationMS <= 0 || manifest.Alignment == nil || manifest.AudioHash == "" {
			t.Fatalf("manifest = %+v", manifest)
		}
		audioKey, _ := objectstore.Parse(manifest.AudioKey)
		audio, _, err := objects.Get(context.Background(), audioKey)
		if err != nil {
			t.Fatalf("audio get: %v", err)
		}
		audio.Close()
	}
	if len(manifestRefs) != 2 {
		t.Fatalf("different segments shared manifest: %v", manifestRefs)
	}
	timelineKey, err := objectstore.Parse(timelineRef)
	if err != nil {
		t.Fatalf("timeline key: %v", err)
	}
	r, _, err := objects.Get(context.Background(), timelineKey)
	if err != nil {
		t.Fatalf("timeline get: %v", err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	var bundle TimelineAsset
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("timeline decode: %v", err)
	}
	if bundle.Timeline == nil || bundle.Timeline.DurationUS != 2_800_000 || bundle.SRTKey == "" || bundle.VTTKey == "" {
		t.Fatalf("timeline bundle = %+v", bundle)
	}
	for _, subtitleRef := range []string{bundle.SRTKey, bundle.VTTKey} {
		key, err := objectstore.Parse(subtitleRef)
		if err != nil {
			t.Fatalf("subtitle key: %v", err)
		}
		asset, _, err := objects.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("subtitle get: %v", err)
		}
		asset.Close()
	}

	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	// 幂等重跑命中 per-project manifest 缓存，不再调用供应商。
	if provider.calls != 1 {
		t.Fatalf("cached run called provider: calls=%d", provider.calls)
	}
	store.revision.Revision = 5
	if err := handler.Handle(context.Background(), narrationJobAt(t, "job-2", 5)); err != nil {
		t.Fatalf("new revision Handle: %v", err)
	}
	// 跨讲稿 revision 复用未修改分段音频（G2-7 共享缓存）。
	if provider.calls != 1 {
		t.Fatalf("unchanged segments were not reused across script revisions: calls=%d", provider.calls)
	}
}

func TestNarrationHandlerDedupsAcrossProjects(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(
		&narration.Segment{SegmentID: "seg-1", DisplayText: "第一段", SpokenText: "相同文本"},
	)}
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	provider := providerForTests()
	handler := NewNarrationHandler(store, steps, objects, provider)

	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("first project Handle: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("first project calls=%d", provider.calls)
	}

	// G2-7 跨项目去重：同租户、同合成配置 → 命中共享缓存，零供应商调用。
	job2 := narrationJobAt(t, "job-2", 4)
	job2.ProjectID = "project-2"
	if err := handler.Handle(context.Background(), job2); err != nil {
		t.Fatalf("second project Handle: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("cross-project dedup failed: calls=%d", provider.calls)
	}
	// project-2 的 per-project manifest 已写入。
	var found bool
	for _, step := range steps.latest {
		if step.StepType == "tts_segment" && strings.Contains(step.ResultRef, "project-2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("project-2 manifest not written: %+v", steps.latest)
	}
}

func TestNarrationHandlerPreflightsAllSegments(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(
		&narration.Segment{SegmentID: "seg-1", SpokenText: "一"},
		&narration.Segment{SegmentID: "seg-2", SpokenText: "超出"},
	)}
	provider := providerForTests()
	provider.caps.MaxInputChars = 1
	handler := NewNarrationHandler(store, &stepRecorder{}, objectstore.NewLocal(t.TempDir(), nil), provider)

	err := handler.Handle(context.Background(), narrationJob(t))
	if err == nil || provider.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, provider.calls)
	}
}

func TestNarrationHandlerClassifiesRetryableProviderError(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(
		&narration.Segment{SegmentID: "seg-1", SpokenText: "重试"},
	)}
	steps := &stepRecorder{}
	provider := providerForTests()
	provider.err = &tts.RetryableError{Err: errors.New("rate limited"), RetryAfter: time.Second}
	handler := NewNarrationHandler(store, steps, objectstore.NewLocal(t.TempDir(), nil), provider)

	err := handler.Handle(context.Background(), narrationJob(t))
	retry := pipeline.AsRetry(err)
	if retry == nil || retry.At.Before(time.Now()) {
		t.Fatalf("retry error = %#v", err)
	}
	for _, step := range steps.latest {
		if step.State != pipeline.StepFailed {
			t.Fatalf("step = %+v", step)
		}
	}
}

func TestNarrationHandlerRecordsTTSMetrics(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(&narration.Segment{SegmentID: "seg-1", DisplayText: "一段", SpokenText: "一段"})}
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	provider := providerForTests()
	metrics := &ttsMetricsRecorder{}
	handler := NewNarrationHandler(store, steps, objects, provider).WithTTSMetrics(metrics)

	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("Handle success: %v", err)
	}
	if metrics.total != 1 || metrics.failed != 0 || metrics.retryable || metrics.throttled {
		t.Fatalf("success metrics = %+v", metrics)
	}

	provider = providerForTests()
	provider.err = &tts.RetryableError{Err: httpStatusErr{status: http.StatusTooManyRequests}, RetryAfter: time.Second}
	metrics = &ttsMetricsRecorder{}
	handler = NewNarrationHandler(store, &stepRecorder{}, objectstore.NewLocal(t.TempDir(), nil), provider).WithTTSMetrics(metrics)
	if err := handler.Handle(context.Background(), narrationJob(t)); err == nil {
		t.Fatalf("Handle 429 should fail")
	}
	if metrics.total != 1 || metrics.failed != 1 || !metrics.retryable || !metrics.throttled {
		t.Fatalf("429 metrics = %+v", metrics)
	}
}

func TestNarrationHandlerRejectsRevisionDrift(t *testing.T) {
	revision := approvedRevision(&narration.Segment{SegmentID: "seg-1", SpokenText: "内容"})
	revision.Revision = 5
	provider := providerForTests()
	handler := NewNarrationHandler(&narrationStoreStub{revision: revision}, &stepRecorder{}, objectstore.NewLocal(t.TempDir(), nil), provider)

	if err := handler.Handle(context.Background(), narrationJob(t)); err == nil || provider.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, provider.calls)
	}
}
