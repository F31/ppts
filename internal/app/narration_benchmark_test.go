package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

// benchJob 构造配音任务（不依赖 *testing.T，供 benchmark 使用）。
func benchJob(jobID, projectID string) *pipeline.Job {
	snapshot, _ := json.Marshal(NarrationSnapshot{
		Slides:   []NarrationSlideSnapshot{{SlideID: "slide-1", ScriptRevision: 4}},
		Language: "zh-CN", VoiceID: "voice-1", SampleRate: 16000,
	})
	return &pipeline.Job{
		ID: jobID, TenantID: "tenant-1", ProjectID: projectID,
		Kind: pipeline.KindNarration, InputSnapshot: string(snapshot),
	}
}

// benchRevision 构造多段已批准 revision。
func benchRevision(segments int) *narration.Revision {
	revSegs := make([]*narration.Segment, 0, segments)
	for i := 0; i < segments; i++ {
		revSegs = append(revSegs, &narration.Segment{
			SegmentID:   "seg-" + string(rune('a'+i%26)),
			DisplayText: "基准测试分段",
			SpokenText:  "这是基准测试的第段文本，用于评估配音合成链路的性能。",
		})
	}
	rev := approvedRevision(revSegs...)
	rev.Revision = 4
	return rev
}

func benchmarkNarration(b *testing.B, segments int) {
	revision := benchRevision(segments)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store := &narrationStoreStub{revision: revision}
		steps := &stepRecorder{}
		objects := objectstore.NewLocal(b.TempDir(), nil)
		provider := providerForTests()
		handler := NewNarrationHandler(store, steps, objects, provider)
		if err := handler.Handle(context.Background(), benchJob("bench-job", "project-1")); err != nil {
			b.Fatalf("Handle: %v", err)
		}
	}
}

// BenchmarkNarrationSynthesis_SingleSegment 单段（真实 TTS 合成 + 对齐 + 对象写入）。
func BenchmarkNarrationSynthesis_SingleSegment(b *testing.B) { benchmarkNarration(b, 1) }

// BenchmarkNarrationSynthesis_MultiSegment 多段（覆盖段循环与时间轴组装）。
func BenchmarkNarrationSynthesis_MultiSegment(b *testing.B) { benchmarkNarration(b, 8) }

// BenchmarkNarrationCacheHit 幂等重跑：全部命中 per-project manifest 缓存，零 TTS 调用。
func BenchmarkNarrationCacheHit(b *testing.B) {
	revision := approvedRevision(
		&narration.Segment{SegmentID: "seg-1", DisplayText: "第一段", SpokenText: "相同文本"},
		&narration.Segment{SegmentID: "seg-2", DisplayText: "第二段", SpokenText: "相同文本"},
	)
	revision.Revision = 4
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store := &narrationStoreStub{revision: revision}
		steps := &stepRecorder{}
		objects := objectstore.NewLocal(b.TempDir(), nil)
		provider := providerForTests()
		handler := NewNarrationHandler(store, steps, objects, provider)
		ctx := context.Background()
		if err := handler.Handle(ctx, benchJob("bench-cache", "project-1")); err != nil {
			b.Fatalf("first Handle: %v", err)
		}
		if err := handler.Handle(ctx, benchJob("bench-cache-2", "project-1")); err != nil {
			b.Fatalf("cached Handle: %v", err)
		}
		if provider.calls != 1 {
			b.Fatalf("cached run called provider: %d", provider.calls)
		}
	}
}

// BenchmarkNarrationSharedCache 跨项目复用共享缓存音频（零 TTS 调用 + 对齐重算路径）。
func BenchmarkNarrationSharedCache(b *testing.B) {
	revision := approvedRevision(
		&narration.Segment{SegmentID: "seg-1", DisplayText: "第一段", SpokenText: "相同文本"},
	)
	revision.Revision = 4
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store := &narrationStoreStub{revision: revision}
		steps := &stepRecorder{}
		objects := objectstore.NewLocal(b.TempDir(), nil)
		provider := providerForTests()
		handler := NewNarrationHandler(store, steps, objects, provider)
		ctx := context.Background()
		if err := handler.Handle(ctx, benchJob("bench-shared-1", "project-1")); err != nil {
			b.Fatalf("first Handle: %v", err)
		}
		if err := handler.Handle(ctx, benchJob("bench-shared-2", "project-2")); err != nil {
			b.Fatalf("cross-project Handle: %v", err)
		}
		if provider.calls != 1 {
			b.Fatalf("cross-project dedup failed: %d", provider.calls)
		}
	}
}
