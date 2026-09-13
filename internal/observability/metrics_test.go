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

func TestSegmentSynthesizedRecordsTTSMetrics(t *testing.T) {
	job := &pipeline.Job{TenantID: "tenant-tts", Kind: pipeline.KindNarration}
	NewPipelineMetrics().SegmentSynthesized(job, true, true, 1500*time.Millisecond, assertErr("429"))

	key := jobKey(job, "retryable_failed")
	if got := expvarMapValue("ppts_tts_synthesis_total", key); got < 1 {
		t.Fatalf("tts total[%s]=%d want >=1", key, got)
	}
	if got := expvarMapValue("ppts_tts_synthesis_duration_ms_total", key); got < 1500 {
		t.Fatalf("tts duration[%s]=%d want >=1500", key, got)
	}
	throttleKey := jobKey(job, "429")
	if got := expvarMapValue("ppts_tts_throttled_total", throttleKey); got < 1 {
		t.Fatalf("tts throttled[%s]=%d want >=1", throttleKey, got)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func expvarMapValue(name, key string) int64 {
	m, _ := expvar.Get(name).(*expvar.Map)
	if m == nil {
		return 0
	}
	var got int64
	m.Do(func(kv expvar.KeyValue) {
		if kv.Key == key {
			got = kv.Value.(*expvar.Int).Value()
		}
	})
	return got
}
