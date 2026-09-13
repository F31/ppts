// Package observability exposes minimal in-process metrics for development and CI.
package observability

import (
	"expvar"
	"strings"
	"time"

	"github.com/F31/ppts/internal/pipeline"
)

var (
	workerJobsTotal            = expvar.NewMap("ppts_worker_jobs_total")
	workerJobMillisTotal       = expvar.NewMap("ppts_worker_job_duration_ms_total")
	workerQueueWaitMillisTotal = expvar.NewMap("ppts_worker_queue_wait_ms_total")
	ttsSynthesisTotal          = expvar.NewMap("ppts_tts_synthesis_total")
	ttsSynthesisMillisTotal    = expvar.NewMap("ppts_tts_synthesis_duration_ms_total")
	ttsThrottledTotal          = expvar.NewMap("ppts_tts_throttled_total")
)

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

func jobKey(job *pipeline.Job, event string) string {
	tenantID, kind := "unknown", "unknown"
	if job != nil {
		if job.TenantID != "" {
			tenantID = job.TenantID
		}
		if job.Kind != "" {
			kind = string(job.Kind)
		}
	}
	return "tenant=" + clean(tenantID) + ",kind=" + clean(kind) + ",event=" + clean(event)
}

func clean(s string) string {
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, "=", "_")
	return s
}
