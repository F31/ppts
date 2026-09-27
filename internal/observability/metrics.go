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
	// P2-B1：限流计数退回到进程内后端。1=各副本各自计数，实际放行量 = 限额 × 副本数。
	// 与上面三个同构 —— 系统照常返回成功，但防护强度已经不是配置里那个数字。
	ratelimitDegraded = expvar.NewInt("ppts_ratelimit_degraded")
	// ratelimitBackend 记录当前生效的限流后端：shared（跨副本共享）/ memory（进程内）。
	// 降级时由调用方置为 "memory"，供 /metrics 与 /debug/vars 区分"有意单副本"与"共享存储挂了"。
	ratelimitBackend = expvar.NewString("ppts_ratelimit_backend")
	// ratelimitDegradeTotal 按 scope 累计降级次数。
	ratelimitDegradeTotal = expvar.NewMap("ppts_ratelimit_degrade_total")
	// P2-B3：按 job kind 的队列视图。所有 kind 共用一条队列，一个慢种类会把其它种类饿死，
	// 而"队列长度 = N"看不出是谁在堵 —— 这三个 Map 就是把 N 拆开。
	//
	// 键形如 kind=narration。**没有出现过的 kind 不会有键**：
	// "没见过"必须表现为键缺失，而不是 0 —— 否则"这个 kind 从没排过队"和
	// "这个 kind 现在没积压"在看板上长得一模一样（0 表示未知的老问题）。
	// 反向的坑：某个 kind 消失后，最后一次取值会留在 Map 里删不掉，
	// 因此"是否还在用某个 kind"要看 ppts_worker_jobs_total 的计数，别看这里。
	queueDepthByKind   = expvar.NewMap("ppts_worker_queue_depth_by_kind")
	queueRunningByKind = expvar.NewMap("ppts_worker_queue_running_by_kind")
	// queueOldestWaitByKind 存该 kind 最老排队任务的等待时长（秒，Float）。
	queueOldestWaitByKind = expvar.NewMap("ppts_worker_queue_oldest_wait_seconds_by_kind")
	// queueBackoffByKind 是退避等待（run_at 未到）的任务数：它**不是**积压 ——
	// 这类任务本来就没到点，混进 backlog 会让看板凭空报警。单列出来，
	// 才能把"调度慢"与"调用方自己排的未来任务"分开看。
	queueBackoffByKind = expvar.NewMap("ppts_worker_queue_backoff_by_kind")
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

// SetRateLimitDegraded 标记限流计数已退回到进程内后端。
// 1=各副本独立计数，防护强度随副本数线性稀释；此时 ppts_ratelimit_backend 应为 "memory"。
func SetRateLimitDegraded(degraded bool) { setGauge(ratelimitDegraded, degraded) }

// SetRateLimitBackend 记录当前生效的限流后端名（shared / memory）。
// 初始值与降级后的值由调用方设置，用于把"单副本部署本就用 memory"与
// "共享存储故障被迫降级"区分开 —— 两者指标值相同（backend=memory），
// 但只有后者会把 ppts_ratelimit_degraded 置 1。
func SetRateLimitBackend(name string) { ratelimitBackend.Set(name) }

// SetQueueDepthByKind 更新某个 kind 的队列快照（P2-B3）。
//
// waiting 真正可运行却还在排队；running 持租约在跑；notReady 还没到 run_at 的退避任务。
func SetQueueDepthByKind(kind string, waiting, running, notReady int64) {
	setMapInt(queueDepthByKind, kindKey(kind), waiting)
	setMapInt(queueRunningByKind, kindKey(kind), running)
	setMapInt(queueBackoffByKind, kindKey(kind), notReady)
}

// SetQueueOldestWaitByKind 更新某个 kind 的最老排队时长（秒）。
//
// 只在"确实有排队任务"时被设置：没有排队任务时，最老等待时长是**未定义**而不是 0 秒，
// 给 0 会让"刚看过还是 0，现在也是 0"和"一小时内从没排过队"无从区分。
func SetQueueOldestWaitByKind(kind string, seconds float64) {
	setMapFloat(queueOldestWaitByKind, kindKey(kind), seconds)
}

// expvar.Map 只能放 expvar.Var（没有 SetInt64 这类便捷方法），因此整值/浮点各包一层。
// 每次 Set 都是整体替换，故取到的永远是最后一次快照，不会像 Map.Add 那样累加。
func setMapInt(m *expvar.Map, key string, v int64) {
	i := new(expvar.Int)
	i.Set(v)
	m.Set(key, i)
}

func setMapFloat(m *expvar.Map, key string, v float64) {
	f := new(expvar.Float)
	f.Set(v)
	m.Set(key, f)
}

// kindKey 与 jobKey 的 kind 部分保持同一编码，方便 Prometheus 侧跨指标 join。
func kindKey(kind string) string { return "kind=" + clean(kind) }

// AddRateLimitDegrade 按限流 scope（端点名）累计一次降级。
func AddRateLimitDegrade(scope string) { ratelimitDegradeTotal.Add(clean(scope), 1) }

// DebugVarsPublic 返回 GET /debug/vars 是否允许匿名读取（PPTS_DEBUG_VARS_PUBLIC=true）。
// 与 SetAuthDevHeadersActive 同构：把"有意放行"与"忘了配 token"区分成两种后果。
// 该端点会导出完整命令行（可能含密钥）与全部业务计数，默认不可匿名访问。
func DebugVarsPublic() bool {
	v := strings.TrimSpace(os.Getenv("PPTS_DEBUG_VARS_PUBLIC"))
	return v == "1" || strings.EqualFold(v, "true")
}

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
