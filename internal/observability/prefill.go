package observability

import "expvar"

// 动效预填（animation_prefill）的观测。
//
// 只有 script 档会进任务，其余档在 API 侧毫秒级算完——所以这里计数的是
// 「值得问模型的那部分页面」，而不是全部页面。三组指标回答三个不同的问题：
//
//	runs_total{route,band,reason} 模型到底有没有被用上。route=rule 而 reason=script 就是没用上。
//	model_fallback_total{reason}   没用上的原因分布。quota 要充值、timeout 要调超时或查网络、
//	                               no_model 要找管理员；合成一个数就分不出该动哪里。
//	model_calls_total              真正的模型调用次数（缓存命中不算），配 latency 总和求均值。
//
// 为什么 band 与 reason 要分开标：band 只有 structured / low-confidence 两种，
// 而 low-confidence 里「讲稿没念结构」（值得问模型）与「版面本身没结构」（问也白问）
// 是同一档。只按 band 看会发现 route=rule 占多数，却分不出这是正常还是整档在空跑。
var (
	prefillRunsTotal          = expvar.NewMap("ppts_animation_prefill_runs_total")
	prefillModelFallbackTotal = expvar.NewMap("ppts_animation_prefill_model_fallback_total")
	prefillModelCallsTotal    = expvar.NewInt("ppts_animation_prefill_model_calls_total")
	// 单次耗时求和而不是直方图：expvar 没有桶，而「平均值是不是逼近 45s 超时」
	// 已经足够回答当前唯一的决策——超时该不该调。
	prefillModelLatencyMSTotal = expvar.NewFloat("ppts_animation_prefill_model_latency_ms_total")
	prefillCacheTotal          = expvar.NewMap("ppts_animation_prefill_cache_total")
)

// PrefillRun 记录一次预填任务最终走的通道、分档与分档原因。
func PrefillRun(route, band, reason string) {
	prefillRunsTotal.Add("route="+clean(route)+",band="+clean(band)+",reason="+clean(reason), 1)
}

// PrefillModelFallback 记录一次「模型没被用上」。
//
// reason 为空表示模型正常产出、或这一页本就不该问，都不算回退——不记。
// 把空串也记一笔会让「回退率」分母变成全部任务，看起来一切正常。
func PrefillModelFallback(reason string) {
	if reason == "" {
		return
	}
	prefillModelFallbackTotal.Add("reason="+clean(reason), 1)
}

// PrefillModelCall 记录一次真实的模型调用及其耗时。缓存命中不调用，故不进来。
func PrefillModelCall(latencyMS int64) {
	prefillModelCallsTotal.Add(1)
	if latencyMS > 0 {
		prefillModelLatencyMSTotal.Add(float64(latencyMS))
	}
}

// 结果缓存的四种处置。
const (
	PrefillCacheHit              = "hit"                 // 直接复用
	PrefillCacheMiss             = "miss"                // 没算过，要排队
	PrefillCacheRetryAfterFailed = "retry_after_failure" // 算过但模型没问成，丢弃重算
	PrefillCacheReadError        = "read_error"          // 读不出来，按没算过处理
)

// PrefillCache 记录一次结果缓存的处置。
//
// retry_after_failure 单独列出来，是因为它是「模型持续不可用时每次打开都排一个任务」
// 这个代价的计量：这个数随打开次数线性增长，就说明该加负缓存了。
func PrefillCache(event string) {
	prefillCacheTotal.Add("result="+clean(event), 1)
}
