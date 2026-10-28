package app

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

// countingProvider 记录最大在飞并发合成数，用于验证有界并发语义。
type countingProvider struct {
	caps        tts.VoiceCapabilities
	fake        *tts.FakeProvider
	delay       time.Duration
	mu          sync.Mutex
	inFlight    int
	maxInflight int
	calls       int
}

func (p *countingProvider) Capabilities(context.Context, string) (tts.VoiceCapabilities, error) {
	return p.caps, nil
}

func (p *countingProvider) Synthesize(ctx context.Context, req tts.SynthesisRequest) (tts.SynthesisResult, error) {
	p.mu.Lock()
	p.calls++
	p.inFlight++
	if p.inFlight > p.maxInflight {
		p.maxInflight = p.inFlight
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()
	if p.delay > 0 {
		select {
		case <-ctx.Done():
			return tts.SynthesisResult{}, ctx.Err()
		case <-time.After(p.delay):
		}
	}
	return p.fake.Synthesize(ctx, req)
}

// narrationConcurrencyHandler 构造启用指定并发上限的 handler（供应商并发由 caps 声明）。
func narrationConcurrencyHandler(t *testing.T, p *countingProvider, operatorCap int) *NarrationHandler {
	t.Helper()
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	h := NewNarrationHandler(&narrationStoreStub{revision: approvedConcurrentRevision()}, steps, objects, p)
	if operatorCap > 0 {
		h.WithSegmentConcurrency(operatorCap)
	}
	return h
}

func approvedConcurrentRevision() *narration.Revision {
	return &narration.Revision{
		ProjectID: "project-1", SlideID: "slide-1", Language: "zh-CN",
		Status: narration.StatusApproved, Revision: 4,
		Segments: []*narration.Segment{
			{SegmentID: "seg-1", DisplayText: "one", SpokenText: "one"},
			{SegmentID: "seg-2", DisplayText: "two", SpokenText: "two"},
			{SegmentID: "seg-3", DisplayText: "three", SpokenText: "three"},
			{SegmentID: "seg-4", DisplayText: "four", SpokenText: "four"},
			{SegmentID: "seg-5", DisplayText: "five", SpokenText: "five"},
		},
	}
}

func narrationMultiJob(t *testing.T, jobID string, revision int64) *pipeline.Job {
	t.Helper()
	snapshot, err := json.Marshal(NarrationSnapshot{
		Slides:   []NarrationSlideSnapshot{{SlideID: "slide-1", ScriptRevision: revision}},
		Language: "zh-CN", VoiceID: "voice-1", SampleRate: 16000,
		BypassCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pipeline.Job{
		ID: jobID, TenantID: "tenant-1", ProjectID: "project-1",
		Kind: pipeline.KindNarration, InputSnapshot: string(snapshot),
	}
}

// TestSegmentConcurrencyDefaultIsSerial 供应商未声明并发（MaxConcurrentSegments=0）
// 恒为串行：最大在飞必须为 1。
func TestSegmentConcurrencyDefaultIsSerial(t *testing.T) {
	p := &countingProvider{
		caps: tts.VoiceCapabilities{
			VoiceID: "voice-1", Languages: []string{"zh-CN"}, MaxInputChars: 100,
			ModelID: "fake-wav", Region: "dev-fake",
		},
		fake: tts.NewFakeProvider(), delay: 10 * time.Millisecond,
	}
	h := narrationConcurrencyHandler(t, p, 0)
	if err := h.Handle(context.Background(), narrationMultiJob(t, "job-serial", 4)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if p.maxInflight != 1 {
		t.Fatalf("undeclared provider ran with concurrency %d, want 1 (serial)", p.maxInflight)
	}
	if p.calls != 5 {
		t.Fatalf("calls = %d, want 5", p.calls)
	}
}

// TestSegmentConcurrencyBoundedParallel 供应商声明并发 3：在飞并发 ≤3、全部合成、顺序保持。
func TestSegmentConcurrencyBoundedParallel(t *testing.T) {
	p := &countingProvider{
		caps: tts.VoiceCapabilities{
			VoiceID: "voice-1", Languages: []string{"zh-CN"}, MaxInputChars: 100,
			ModelID: "fake-wav", Region: "dev-fake", MaxConcurrentSegments: 3,
		},
		fake: tts.NewFakeProvider(), delay: 10 * time.Millisecond,
	}
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	h := NewNarrationHandler(&narrationStoreStub{revision: approvedConcurrentRevision()}, steps, objects, p)
	job := narrationMultiJob(t, "job-par", 4)
	if err := h.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if p.maxInflight < 2 {
		t.Fatalf("declared concurrency not utilized: max in-flight = %d, want >= 2", p.maxInflight)
	}
	if p.maxInflight > 3 {
		t.Fatalf("exceeded declared concurrency: max in-flight = %d, want <= 3", p.maxInflight)
	}
	if p.calls != 5 {
		t.Fatalf("calls = %d, want 5", p.calls)
	}
	// 时间轴段序必须保持 slide-1 seg-1..seg-5 原序。
	tl := timelineFromSteps(t, steps, objects)
	got := make([]string, 0, 5)
	for _, s := range tl.Slides[0].Segments {
		got = append(got, s.SegmentID)
	}
	for i, want := range []string{"slide-1:seg-1", "slide-1:seg-2", "slide-1:seg-3", "slide-1:seg-4", "slide-1:seg-5"} {
		if got[i] != want {
			t.Fatalf("timeline order[%d] = %q, want %q (full %v)", i, got[i], want, got)
		}
	}
}

// TestSegmentConcurrencyOperatorOnlyLowers 操作员上限只允许调低供应商声明：
// 供应商=4、操作员=2 → 2；供应商=0、操作员=4 → 仍串行。
func TestSegmentConcurrencyOperatorOnlyLowers(t *testing.T) {
	// 供应商声明 4、操作员设 2 → 实际 2。
	p := &countingProvider{
		caps: tts.VoiceCapabilities{
			VoiceID: "voice-1", Languages: []string{"zh-CN"}, MaxInputChars: 100,
			ModelID: "fake-wav", Region: "dev-fake", MaxConcurrentSegments: 4,
		},
		fake: tts.NewFakeProvider(), delay: 10 * time.Millisecond,
	}
	h := narrationConcurrencyHandler(t, p, 2)
	if err := h.Handle(context.Background(), narrationMultiJob(t, "job-lower", 4)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if p.maxInflight != 2 {
		t.Fatalf("operator cap 2 with provider 4 = %d, want 2", p.maxInflight)
	}

	// 供应商未声明并发（0）、操作员设 4 → 仍串行（不能强行并发）。
	p2 := &countingProvider{
		caps: tts.VoiceCapabilities{
			VoiceID: "voice-1", Languages: []string{"zh-CN"}, MaxInputChars: 100,
			ModelID: "fake-wav", Region: "dev-fake", // MaxConcurrentSegments 未声明
		},
		fake: tts.NewFakeProvider(), delay: 10 * time.Millisecond,
	}
	h2 := narrationConcurrencyHandler(t, p2, 4)
	if err := h2.Handle(context.Background(), narrationMultiJob(t, "job-noraise", 4)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if p2.maxInflight != 1 {
		t.Fatalf("operator raised undeclared provider to %d, want 1 (serial)", p2.maxInflight)
	}
}

// timelineFromSteps 从成功的时间轴步骤记录里回读 bundle 并返回其中 Timeline。
func timelineFromSteps(t *testing.T, steps *stepRecorder, objects objectstore.ObjectStore) *media.Timeline {
	t.Helper()
	for _, step := range steps.latest {
		if step.State != pipeline.StepSuccess || step.ResultRef == "" || step.StepType != "timeline" {
			continue
		}
		key, err := objectstore.Parse(step.ResultRef)
		if err != nil {
			t.Fatalf("timeline key: %v", err)
		}
		r, _, err := objects.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("timeline get: %v", err)
		}
		data, _ := io.ReadAll(r)
		r.Close()
		var bundle TimelineAsset
		if err := json.Unmarshal(data, &bundle); err != nil {
			t.Fatalf("timeline decode: %v", err)
		}
		if bundle.Timeline == nil {
			t.Fatal("timeline bundle missing timeline")
		}
		return bundle.Timeline
	}
	t.Fatal("no successful timeline step found")
	return nil
}
