package observability

import (
	"context"
	"testing"
	"time"
)

func TestExportClockFinishSplitsOther(t *testing.T) {
	c := NewExportClock()
	ctx := WithExportClock(context.Background(), c)
	done := ExportStage(ctx, ExportStageAssets)
	time.Sleep(20 * time.Millisecond)
	done()
	time.Sleep(15 * time.Millisecond)
	snap := c.Finish("mp4")
	if snap.Total < 30*time.Millisecond {
		t.Fatalf("total = %s, want >= 30ms", snap.Total)
	}
	if len(snap.Stages) != 1 || snap.Stages[0].Name != ExportStageAssets {
		t.Fatalf("stages = %+v", snap.Stages)
	}
	if snap.Stages[0].Duration < 15*time.Millisecond {
		t.Fatalf("assets = %s", snap.Stages[0].Duration)
	}
	if snap.Other < 10*time.Millisecond {
		t.Fatalf("other = %s, want the sleep after assets", snap.Other)
	}
	if snap.Other+snap.Stages[0].Duration > snap.Total+5*time.Millisecond {
		t.Fatalf("accounted %s + other %s > total %s", snap.Stages[0].Duration, snap.Other, snap.Total)
	}
	second := c.Finish("mp4")
	if second.Total < snap.Total {
		t.Fatalf("second Finish shrank total")
	}
	key := "stage=" + ExportStageAssets
	if got := expvarMapValue("ppts_export_stage_duration_ms_total", key); got < 15 {
		t.Fatalf("stage duration[%s]=%d", key, got)
	}
	if got := expvarMapValue("ppts_export_duration_ms_total", "format=mp4"); got < 30 {
		t.Fatalf("export duration[mp4]=%d", got)
	}
}

func TestExportStageWithoutClockIsNoop(t *testing.T) {
	done := ExportStage(context.Background(), ExportStageRender)
	done()
	done()
}

func TestExportClockSnapshotAttrsStable(t *testing.T) {
	snap := ExportClockSnapshot{
		Total: 100 * time.Millisecond,
		Other: 10 * time.Millisecond,
		Stages: []ExportStageDur{
			{Name: ExportStageRender, Duration: 60 * time.Millisecond},
			{Name: ExportStageAssets, Duration: 30 * time.Millisecond},
		},
	}
	attrs := snap.Attrs()
	if len(attrs)%2 != 0 {
		t.Fatalf("odd attr count: %v", attrs)
	}
	got := map[string]int64{}
	for i := 0; i < len(attrs); i += 2 {
		k, _ := attrs[i].(string)
		v, _ := attrs[i+1].(int64)
		got[k] = v
	}
	if got["total_ms"] != 100 || got["other_ms"] != 10 || got["render_ms"] != 60 || got["assets_ms"] != 30 {
		t.Fatalf("attrs = %v", got)
	}
}
