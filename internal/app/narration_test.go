package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	if provider.calls != 2 || len(steps.latest) != 3 {
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
	if provider.calls != 2 {
		t.Fatalf("cached run called provider: calls=%d", provider.calls)
	}
	store.revision.Revision = 5
	if err := handler.Handle(context.Background(), narrationJobAt(t, "job-2", 5)); err != nil {
		t.Fatalf("new revision Handle: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("unchanged segments were not reused across script revisions: calls=%d", provider.calls)
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

func TestNarrationHandlerRejectsRevisionDrift(t *testing.T) {
	revision := approvedRevision(&narration.Segment{SegmentID: "seg-1", SpokenText: "内容"})
	revision.Revision = 5
	provider := providerForTests()
	handler := NewNarrationHandler(&narrationStoreStub{revision: revision}, &stepRecorder{}, objectstore.NewLocal(t.TempDir(), nil), provider)

	if err := handler.Handle(context.Background(), narrationJob(t)); err == nil || provider.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, provider.calls)
	}
}
