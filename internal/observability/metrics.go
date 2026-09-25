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
	// 降级 / 开发模式的显式信号。
	// 这几种模式的共同特征是：系统照常返回成功，但产出并非用户预期（静音音频、
	// 无页面图、身份无校验）。没有指标就等于"假成功"——看板全绿而链路失真，
	// 因此每处降级都必须配一个 Gauge，供 Prometheus 告警直接引用。
	ttsFakeProviderActive = expvar.NewInt("ppts_tts_fake_provider_active")
	renderPagesDisabled   = expvar.NewInt("ppts_render_pages_disabled")
	authDevHeadersActive  = expvar.NewInt("ppts_auth_dev_headers_active")
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

// SetTTSFakeProviderActive 标记 TTS 运行在假供应商模式（产出静音 WAV）。
// 1=降级中；用于把"配音成功但音频无声"从"用户投诉后才发现"提前到看板告警。
func SetTTSFakeProviderActive(active bool) { setGauge(ttsFakeProviderActive, active) }

// SetRenderPagesDisabled 标记页面渲染不可用（LibreOffice / poppler 缺失）。
// 1=解析会成功但没有页面图，预览、播放帧与 MP4 导出素材均无来源。
func SetRenderPagesDisabled(disabled bool) { setGauge(renderPagesDisabled, disabled) }

// SetAuthDevHeadersActive 标记服务接受 X-PPTS-Tenant-ID / X-PPTS-User-ID 开发头
// （即无真实认证）。1=任何能访问端口的人都可冒充任意租户任意用户，仅限本地开发。
func SetAuthDevHeadersActive(active bool) { setGauge(authDevHeadersActive, active) }

func setGauge(v *expvar.Int, on bool) {
	if on {
		v.Set(1)
		return
	}
	v.Set(0)
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
