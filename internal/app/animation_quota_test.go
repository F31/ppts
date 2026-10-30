package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/usage"
)

type settleCall struct {
	kind  usage.Kind
	units float64
}

type recordingSettler struct {
	calls []settleCall
	err   error
}

func (s *recordingSettler) Settle(_ context.Context, _, _ string, kind usage.Kind, actualUnits float64, _ string) error {
	s.calls = append(s.calls, settleCall{kind: kind, units: actualUnits})
	return s.err
}

// 结算口径是"真的渲了动画的页"，不是整片时长：
// 降级页播的是静态段，没花渲染分钟数，按整片收等于在渲染服务最不稳的时候按满额收费。
func TestSettleAnimationMinutesChargesOnlyAnimatedPages(t *testing.T) {
	settler := &recordingSettler{}
	h := NewExportHandler(nil, nil, nil, nil).WithUsage(settler)
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", IDempotencyKey: "op-1",
		CreatedAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC),
	}
	durations := []int64{10_000, 20_000, 30_000}
	av := &AnimatedVideo{Degraded: []int{1}}

	h.settleAnimationMinutes(context.Background(), job, durations, av)

	if len(settler.calls) != 1 {
		t.Fatalf("settle calls = %d, want 1", len(settler.calls))
	}
	wantKind := usage.KindAnimationMinutes.Period("2026-10")
	if settler.calls[0].kind != wantKind {
		t.Fatalf("kind = %q, want %q", settler.calls[0].kind, wantKind)
	}
	// 10s + 30s = 40s = 0.6667 分钟；第 2 页（20s）降级不计。
	if got, want := settler.calls[0].units, 40.0/60.0; got != want {
		t.Fatalf("units = %v, want %v", got, want)
	}
}

// 整体退回静态（av == nil）时必须结算 0 而不是"不结算"：
// 不结算就把受理时占住的额度锁死到账期结束，用户看到"没额度了"，实际一次都没渲。
func TestSettleAnimationMinutesReleasesHoldWhenFullyStatic(t *testing.T) {
	settler := &recordingSettler{}
	h := NewExportHandler(nil, nil, nil, nil).WithUsage(settler)
	job := &pipeline.Job{ID: "job-2", TenantID: "tenant-1", IDempotencyKey: "op-2",
		CreatedAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)}

	h.settleAnimationMinutes(context.Background(), job, []int64{10_000, 20_000}, nil)

	if len(settler.calls) != 1 || settler.calls[0].units != 0 {
		t.Fatalf("calls = %+v, want one settle with 0", settler.calls)
	}
}

// 账期按**任务创建时间**：月末提交、月初跑完的任务必须结算到受理那个账期，
// 否则找不到预占，这笔用量就成了无主账。
func TestSettleAnimationMinutesUsesJobCreationPeriod(t *testing.T) {
	settler := &recordingSettler{}
	h := NewExportHandler(nil, nil, nil, nil).WithUsage(settler)
	job := &pipeline.Job{ID: "job-3", TenantID: "tenant-1", IDempotencyKey: "op-3",
		CreatedAt: time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)}

	h.settleAnimationMinutes(context.Background(), job, []int64{60_000}, &AnimatedVideo{})

	want := usage.KindAnimationMinutes.Period("2026-09")
	if len(settler.calls) != 1 || settler.calls[0].kind != want {
		t.Fatalf("kind = %+v, want %q", settler.calls, want)
	}
}

// 结算失败不能让导出失败：成片已经做出来了，钱的事由日志与对账兜。
func TestSettleAnimationMinutesSwallowsStoreErrors(t *testing.T) {
	settler := &recordingSettler{err: errors.New("db down")}
	h := NewExportHandler(nil, nil, nil, nil).WithUsage(settler)
	job := &pipeline.Job{ID: "job-4", TenantID: "tenant-1", IDempotencyKey: "op-4",
		CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}

	h.settleAnimationMinutes(context.Background(), job, []int64{60_000}, &AnimatedVideo{})
	if len(settler.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(settler.calls))
	}
}

// 没有注入账本（本地 profile / 单测）时不结算，也不 panic。
func TestSettleAnimationMinutesWithoutUsageIsNoop(t *testing.T) {
	h := NewExportHandler(nil, nil, nil, nil)
	h.settleAnimationMinutes(context.Background(),
		&pipeline.Job{ID: "job-5", TenantID: "tenant-1", IDempotencyKey: "op-5"},
		[]int64{60_000}, &AnimatedVideo{})
}
