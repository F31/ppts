package observability

import (
	"expvar"
	"testing"
	"time"

	"github.com/F31/ppts/internal/pipeline"
)

func TestJobClaimedRecordsQueueWait(t *testing.T) {
	job := &pipeline.Job{
		TenantID:  "tenant-1",
		Kind:      pipeline.KindNarration,
		CreatedAt: time.Now().Add(-2 * time.Second),
	}
	NewPipelineMetrics().JobClaimed(job)

	wait, ok := expvar.Get("ppts_worker_queue_wait_ms_total").(*expvar.Map)
	if !ok {
		t.Fatalf("queue wait metric not registered as expvar.Map")
	}
	key := jobKey(job, "claimed")
	var got int64 = -1
	wait.Do(func(kv expvar.KeyValue) {
		if kv.Key == key {
			got = kv.Value.(*expvar.Int).Value()
		}
	})
	if got < 1000 {
		t.Fatalf("queue wait for %s = %d ms, want >= 1000", key, got)
	}
}

func TestJobClaimedWithoutCreatedAtSkipsWait(t *testing.T) {
	job := &pipeline.Job{TenantID: "tenant-2", Kind: pipeline.KindParse}
	NewPipelineMetrics().JobClaimed(job)

	wait, _ := expvar.Get("ppts_worker_queue_wait_ms_total").(*expvar.Map)
	key := jobKey(job, "claimed")
	found := false
	wait.Do(func(kv expvar.KeyValue) {
		if kv.Key == key {
			found = true
		}
	})
	if found {
		t.Fatalf("unexpected queue wait recorded for job without CreatedAt")
	}
}
