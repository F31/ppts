package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

// TestNarrationPassesSnapshotModelToProvider 守护"任务点名的模型真的传给了供应商解析"。
//
// 这条断的是链路中段：gateway.ResolveModel 单测证明"按名能查到正确配置"，
// 但只有这里能证明 worker 真的把 snapshot.Model 递下去了。少了这一步，
// 解析函数再正确也不会被调用——界面换模型仍然无声无效。
func TestNarrationPassesSnapshotModelToProvider(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(
		&narration.Segment{SegmentID: "seg-1", DisplayText: "第一段", SpokenText: "一段文本"},
	)}
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	provider := providerForTests()
	handler := NewNarrationHandler(store, steps, objects, provider)

	var gotTenant, gotModel string
	handler.WithTenantProvider(func(_ context.Context, tenantID, model string) (tts.TTSProvider, error) {
		gotTenant, gotModel = tenantID, model
		return provider, nil
	})

	snapshot, err := json.Marshal(NarrationSnapshot{
		Slides:   []NarrationSlideSnapshot{{SlideID: "slide-1", ScriptRevision: 4}},
		Language: "zh-CN", VoiceID: "voice-1", SampleRate: 16000,
		Model: "model-b",
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	job := &pipeline.Job{
		ID: "job-model", TenantID: "tenant-1", ProjectID: "project-1",
		Kind: pipeline.KindNarration, InputSnapshot: string(snapshot),
	}
	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if gotModel != "model-b" {
		t.Fatalf("providerFor model = %q want model-b（快照里的模型没传到解析层）", gotModel)
	}
	if gotTenant != "tenant-1" {
		t.Fatalf("providerFor tenant = %q want tenant-1", gotTenant)
	}
}

// TestNarrationEmptySnapshotModelKeepsDefault 固化向后兼容：
// 快照里没有模型（旧任务、或调用方不指定）时传空串，由实现走租户默认。
func TestNarrationEmptySnapshotModelKeepsDefault(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(
		&narration.Segment{SegmentID: "seg-1", DisplayText: "第一段", SpokenText: "一段文本"},
	)}
	steps := &stepRecorder{}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	provider := providerForTests()
	handler := NewNarrationHandler(store, steps, objects, provider)

	var calls int
	var gotModel string
	handler.WithTenantProvider(func(_ context.Context, _, model string) (tts.TTSProvider, error) {
		calls++
		gotModel = model
		return provider, nil
	})

	if err := handler.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if calls != 1 {
		t.Fatalf("providerFor calls = %d want 1", calls)
	}
	if gotModel != "" {
		t.Fatalf("providerFor model = %q want 空串（未指定时不得凭空造一个模型名）", gotModel)
	}
}
