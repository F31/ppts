// Package observability exposes minimal in-process metrics for development and CI.
package observability

import (
	"expvar"
	"os"
	"strings"
	"time"

	"github.com/F31/ppts/internal/pipeline"
)

var (
	workerJobsTotal            = expvar.NewMap("ppts_worker_jobs_total")
	workerJobMillisTotal       = expvar.NewMap("ppts_worker_job_duration_ms_total")
	workerQueueWaitMillisTotal = expvar.NewMap("ppts_worker_queue_wait_ms_total")
	queueOldestWaitSeconds     = expvar.NewFloat("ppts_worker_queue_oldest_wait_seconds")
	ttsSynthesisTotal          = expvar.NewMap("ppts_tts_synthesis_total")
	ttsSynthesisMillisTotal    = expvar.NewMap("ppts_tts_synthesis_duration_ms_total")
	ttsThrottledTotal          = expvar.NewMap("ppts_tts_throttled_total")
	ttsCacheHitTotal           = expvar.NewMap("ppts_tts_cache_hit_total")
)

// includeTenantLabel 控制指标键是否携带租户维度。默认关闭，避免以租户 UUID 作为
// 高基数标签导致指标维度爆炸；排查单租户问题时经 PPTS_METRICS_TENANT_LABELS=true 开启。
var includeTenantLabel = func() bool {
	v := strings.TrimSpace(os.Getenv("PPTS_METRICS_TENANT_LABELS"))
	return strings.EqualFold(v, "true") || v == "1"
}()

// PipelineMetrics records worker lifecycle counters with bounded labels.
type PipelineMetrics struct{}

// NewPipelineMetrics creates a pipeline metrics recorder.
func NewPipelineMetrics() *PipelineMetrics { return &PipelineMetrics{} }

func (m *PipelineMetrics) JobClaimed(job *pipeline.Job) {
	workerJobsTotal.Add(jobKey(job, "claimed"), 1)
	// 队列等待：领取时距任务创建的时长；平均等待 = sum / claimed 计数。
	if job != nil && !job.CreatedAt.IsZero() {
		workerQueueWaitMillisTotal.Add(jobKey(job, "claimed"), time.Since(job.CreatedAt).Milliseconds())
	}
}

func (m *PipelineMetrics) JobCompleted(job *pipeline.Job, state pipeline.JobState, duration time.Duration) {
	workerJobsTotal.Add(jobKey(job, string(state)), 1)
	workerJobMillisTotal.Add(jobKey(job, string(state)), duration.Milliseconds())
}

func (m *PipelineMetrics) JobRetryScheduled(job *pipeline.Job) {
	workerJobsTotal.Add(jobKey(job, "retry_scheduled"), 1)
}

func (m *PipelineMetrics) JobCancelRequested(job *pipeline.Job) {
	workerJobsTotal.Add(jobKey(job, "cancel_requested"), 1)
}

func (m *PipelineMetrics) JobLeaseLost(job *pipeline.Job) {
	workerJobsTotal.Add(jobKey(job, "lease_lost"), 1)
}

// SetQueueOldestWait updates the queue backlog gauge with the age of the oldest runnable queued job.
func (m *PipelineMetrics) SetQueueOldestWait(seconds float64) {
	queueOldestWaitSeconds.Set(seconds)
}

// SegmentSynthesized records TTS provider synthesis outcomes.
func (m *PipelineMetrics) SegmentSynthesized(job *pipeline.Job, retryable, throttled bool, duration time.Duration, err error) {
	event := "succeeded"
	if err != nil {
		event = "failed"
		if retryable {
			event = "retryable_failed"
		}
	}
	key := jobKey(job, event)
	ttsSynthesisTotal.Add(key, 1)
	ttsSynthesisMillisTotal.Add(key, duration.Milliseconds())
	if throttled {
		ttsThrottledTotal.Add(jobKey(job, "429"), 1)
	}
}

// SegmentCacheHit records a segment audio served from cache instead of the
// provider (G2-7 内容哈希去重命中率观测：hit/(hit+succeeded) 即命中率).
func (m *PipelineMetrics) SegmentCacheHit(job *pipeline.Job, scope string) {
	ttsCacheHitTotal.Add(jobKey(job, scope), 1)
}

func jobKey(job *pipeline.Job, event string) string {
	kind := "unknown"
	if job != nil && job.Kind != "" {
		kind = string(job.Kind)
	}
	key := "kind=" + clean(kind) + ",event=" + clean(event)
	if includeTenantLabel && job != nil && job.TenantID != "" {
		key = "tenant=" + clean(job.TenantID) + "," + key
	}
	return key
}

func clean(s string) string {
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, "=", "_")
	return s
}
