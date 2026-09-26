package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/textnorm"
)

// textNormStoreStub 实现 TextNormDictStore，模拟租户词典 + 平台种子。
type textNormStoreStub struct {
	tenant      pronunciation.Rules
	platform    pronunciation.Rules
	tenantErr   error
	platformErr error
}

func (s *textNormStoreStub) LoadTenantDefault(context.Context, string) (pronunciation.Rules, error) {
	return s.tenant, s.tenantErr
}

func (s *textNormStoreStub) LoadPlatformDefault(context.Context) (pronunciation.Rules, error) {
	return s.platform, s.platformErr
}

// dictMetricsRecorder 记录 R1 词典事件。
type dictMetricsRecorder struct {
	events []string
}

func (r *dictMetricsRecorder) OnDictEvent(_ context.Context, name string) {
	r.events = append(r.events, name)
}

// captureTTSProvider 记录最后一次合成请求，供断言 Read 标记/停顿映射。
type captureTTSProvider struct {
	testTTSProvider
	lastReq tts.SynthesisRequest
}

func (p *captureTTSProvider) Synthesize(ctx context.Context, req tts.SynthesisRequest) (tts.SynthesisResult, error) {
	p.lastReq = req
	return p.testTTSProvider.Synthesize(ctx, req)
}

func newTextNormHandler(t *testing.T, store *narrationStoreStub, meta TextNormDictStore, seed bool, metrics TextNormMetrics) (*NarrationHandler, *captureTTSProvider, *stepRecorder, objectstore.ObjectStore) {
	t.Helper()
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	base := providerForTests()
	provider := &captureTTSProvider{testTTSProvider: *base}
	handler := NewNarrationHandler(store, steps, objects, provider)
	if meta != nil {
		handler = handler.WithTextNorm(staticTextNorm{
			meta: meta, seed: seed, metrics: metrics,
			logger: slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
		})
	}
	return handler, provider, steps, objects
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type staticTextNorm struct {
	meta    TextNormDictStore
	seed    bool
	metrics TextNormMetrics
	logger  *slog.Logger
}

func (s staticTextNorm) Build(ctx context.Context, _, lang string) (*textnorm.Engine, error) {
	return NewTextNormEngine(ctx, s.meta, "tenant-1", lang, s.seed, s.metrics, s.logger)
}

func segmentsWithMarkers() []*narration.Segment {
	return []*narration.Segment{
		{SegmentID: "seg-1", DisplayText: "RTX5090〔读：x〕‖性能强劲", SpokenText: "RTX5090〔读：x〕‖性能强劲"},
	}
}

// TestTextNormReadsMarkersInEffectiveText 朗读/停顿标记被引擎处理：TTS 输入不含定界符，
// 停顿事件映射进 SpeechControl。
func TestTextNormReadsMarkersInEffectiveText(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(segmentsWithMarkers()...)}
	handler, provider, _, _ := newTextNormHandler(t, store, &textNormStoreStub{}, false, nil)

	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	req := provider.lastReq
	if strings.Contains(req.Text, "〔") || strings.Contains(req.Text, "‖") {
		t.Fatalf("TTS input still contains markers: %q", req.Text)
	}
	if !strings.Contains(req.Text, "性能强劲") {
		t.Fatalf("reading marker content lost: %q", req.Text)
	}
	if len(req.SpeechControl.Pauses) == 0 {
		t.Fatalf("SpeechControl.Pauses empty, want pause for ‖")
	}
}

// TestTextNormSubtitleTextStripMarkers 字幕文本不得含标记语法（R6：字幕零污染）。
func TestTextNormSubtitleTextStripMarkers(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(segmentsWithMarkers()...)}
	handler, _, steps, objects := newTextNormHandler(t, store, &textNormStoreStub{}, false, nil)
	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	bundle := readTimelineBundle(t, objects, steps)
	if len(bundle.Timeline.Subtitles) == 0 {
		t.Fatalf("no subtitles")
	}
	for _, sub := range bundle.Timeline.Subtitles {
		if strings.Contains(sub.Text, "〔") || strings.Contains(sub.Text, "‖") {
			t.Fatalf("subtitle contains marker syntax: %q", sub.Text)
		}
	}
}

// TestTextNormSubtitleCleanWhenDisabled textnorm 关闭时（默认）字幕保持原样——零行为回退。
func TestTextNormSubtitleCleanWhenDisabled(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(segmentsWithMarkers()...)}
	// 不注入 textnorm：走既有 pronunciation.Apply 路径，字幕保持 DisplayText 原样。
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	provider := providerForTests()
	handler := NewNarrationHandler(store, steps, objects, provider)
	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	bundle := readTimelineBundle(t, objects, steps)
	for _, sub := range bundle.Timeline.Subtitles {
		if !strings.Contains(sub.Text, "‖") {
			t.Fatalf("disabled engine should keep original subtitle text, got %q", sub.Text)
		}
	}
}

// TestTextNormOnlyDictChangeResynthesizes E2E：只改词典/规则、不改讲稿文本 → 新音频真实重新合成。
func TestTextNormOnlyDictChangeResynthesizes(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(
		&narration.Segment{SegmentID: "seg-1", DisplayText: "运行 CUDA 程序", SpokenText: "运行 CUDA 程序"},
	)}
	meta := &textNormStoreStub{tenant: pronunciation.Rules{{Pattern: "CUDA", Replacement: "库达", Enabled: true}}}
	handler, provider, _, _ := newTextNormHandler(t, store, meta, false, nil)

	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	callsAfterFirst := provider.calls
	if callsAfterFirst == 0 {
		t.Fatalf("first run should synthesize")
	}

	// 只改词典（同一讲稿文本、同一 revision，job-2）—— effectiveText 变化 → 必须真实重合成。
	meta.tenant = pronunciation.Rules{{Pattern: "CUDA", Replacement: "库达C", Enabled: true}}
	if err := handler.Handle(context.Background(), narrationJobAt(t, "job-2", 4)); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if provider.calls != callsAfterFirst+1 {
		t.Fatalf("dict change did NOT resynthesize: calls=%d after=%d", provider.calls, callsAfterFirst)
	}
}

// TestTextNormTenantIsolation 跨租户负测：A 租户字典不出现于 B（R3）。
func TestTextNormTenantIsolation(t *testing.T) {
	a := &textNormStoreStub{tenant: pronunciation.Rules{{Pattern: "SECRET", Replacement: "A词", Enabled: true}}}
	b := &textNormStoreStub{}
	egA, err := NewTextNormEngine(context.Background(), a, "tenant-A", "zh-CN", true, nil, nil)
	if err != nil {
		t.Fatalf("engine A: %v", err)
	}
	egB, err := NewTextNormEngine(context.Background(), b, "tenant-B", "zh-CN", true, nil, nil)
	if err != nil {
		t.Fatalf("engine B: %v", err)
	}
	if gotA := egA.Run("SECRET", "zh-CN").Text; gotA != "A词" {
		t.Fatalf("tenant A should substitute: %q", gotA)
	}
	if gotB := egB.Run("SECRET", "zh-CN").Text; gotB != "SECRET" {
		t.Fatalf("tenant B must NOT see tenant A rule: %q", gotB)
	}
}

// TestTextNormDictLoadErrorFailsJob R1：词典加载 error → 任务失败，不静默降空。
func TestTextNormDictLoadErrorFailsJob(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(segmentsWithMarkers()...)}
	meta := &textNormStoreStub{tenantErr: errors.New("db down")}
	handler, _, _, _ := newTextNormHandler(t, store, meta, false, nil)
	err := handler.Handle(context.Background(), narrationJob(t))
	if err == nil {
		t.Fatalf("expected job failure on dict load error, got nil")
	}
	if !strings.Contains(err.Error(), "load tenant dictionary") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTextNormEmptyUnexpectedSignal R1：expectedSeed=true 但合并结果为空 → empty_unexpected 事件。
func TestTextNormEmptyUnexpectedSignal(t *testing.T) {
	metrics := &dictMetricsRecorder{}
	meta := &textNormStoreStub{}
	eg, err := NewTextNormEngine(context.Background(), meta, "tenant-1", "zh-CN", true, metrics, nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if eg == nil {
		t.Fatalf("engine nil")
	}
	found := false
	for _, e := range metrics.events {
		if e == "empty_unexpected" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected empty_unexpected event, got %v", metrics.events)
	}
}

// TestTextNormPlatformSeedFallback 平台种子兜底：租户无自定义时回退平台规则。
func TestTextNormPlatformSeedFallback(t *testing.T) {
	meta := &textNormStoreStub{platform: pronunciation.Rules{{Pattern: "CUDA", Replacement: "库达", Enabled: true}}}
	eg, err := NewTextNormEngine(context.Background(), meta, "tenant-1", "zh-CN", true, nil, nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if got := eg.Run("CUDA 并行", "zh-CN").Text; got != "库达 并行" {
		t.Fatalf("platform fallback: got %q", got)
	}
}

// TestTextNormToSpeechControlPauses 停顿事件 → SpeechControl.Pauses 映射。
func TestTextNormToSpeechControlPauses(t *testing.T) {
	eg := textnorm.New()
	eg.Build()
	res := eg.Run("AB‖CD", "zh-CN")
	pauses := ToSpeechControlPauses(res.Pauses)
	if len(pauses) != 1 || pauses[0].AfterChars != 2 || pauses[0].DurationMS != 300 {
		t.Fatalf("pauses = %+v", pauses)
	}
}

// readTimelineBundle 从 timeline 步骤的 ResultRef 读取 TimelineAsset。
func readTimelineBundle(t *testing.T, objects objectstore.ObjectStore, steps *stepRecorder) *TimelineAsset {
	t.Helper()
	var timelineRef string
	for _, step := range steps.latest {
		if step.StepType == "timeline" && step.ResultRef != "" {
			timelineRef = step.ResultRef
			break
		}
	}
	if timelineRef == "" {
		t.Fatalf("no timeline step recorded")
	}
	key, err := objectstore.Parse(timelineRef)
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
		t.Fatalf("timeline bundle nil")
	}
	return &bundle
}
