package observability

import (
	"encoding/json"
	"expvar"
	"strings"
	"testing"
)

// prefillMapSeries 读出某个 expvar.Map 指标当前的键值。
//
// 这些指标是进程级全局量（同 authEventsTotal 等），测试之间共享同一份计数，
// 所以断言一律用「前后差值」，不要写死绝对值——写死会在 `go test -shuffle` 下随机翻红。
func prefillMapSeries(t *testing.T, name string) map[string]float64 {
	t.Helper()
	raw := ""
	expvar.Do(func(kv expvar.KeyValue) {
		if kv.Key == name {
			raw = kv.Value.String()
		}
	})
	if raw == "" {
		t.Fatalf("%s 没有注册到 expvar", name)
	}
	var m map[string]float64
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("%s 不是标签 map：%v（原始值 %q）", name, err, raw)
	}
	return m
}

func prefillScalar(t *testing.T, name string) float64 {
	t.Helper()
	raw := ""
	expvar.Do(func(kv expvar.KeyValue) {
		if kv.Key == name {
			raw = kv.Value.String()
		}
	})
	if raw == "" {
		t.Fatalf("%s 没有注册到 expvar", name)
	}
	var f float64
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("%s 不是数字：%v", name, err)
	}
	return f
}

func TestPrefillRunRendersRouteAndBand(t *testing.T) {
	const key = "route=model,band=low-confidence,reason=script"
	before := prefillMapSeries(t, "ppts_animation_prefill_runs_total")[key]
	PrefillRun("model", "low-confidence", "script")
	PrefillRun("model", "low-confidence", "script")
	after := prefillMapSeries(t, "ppts_animation_prefill_runs_total")[key]
	if got := after - before; got != 2 {
		t.Fatalf("两次 PrefillRun 后计数增量 = %v，期望 2", got)
	}

	// 标签必须真的渲染成 label= 而不是塞进指标名：运维靠 {reason="script"} 过滤，
	// 一旦键名被清洗成 route_model，指标名字就会随取值一起变。
	var b strings.Builder
	writePrometheus(&b, "ppts_animation_prefill_runs_total",
		`{"route=model,band=low-confidence,reason=script":3}`)
	if out := b.String(); !strings.Contains(out,
		`ppts_animation_prefill_runs_total{route="model",band="low-confidence",reason="script"} 3`) {
		t.Fatalf("渲染结果 = %q", out)
	}
}

func TestPrefillRunSanitizesValuesNotTheWholeKey(t *testing.T) {
	// 值是外部来的（route/band/reason 目前是枚举，但标签拼接一旦写成 clean(整串)，
	// 逗号会把三个标签挤成一个）。这条用例钉住「先清洗值，再拼标签」。
	const key = "route=model_x,band=low-confidence,reason=script"
	before := prefillMapSeries(t, "ppts_animation_prefill_runs_total")[key]
	PrefillRun("model,x", "low-confidence", "script")
	after := prefillMapSeries(t, "ppts_animation_prefill_runs_total")[key]
	if after-before != 1 {
		t.Fatalf("带逗号的取值没有落成单个标签值，键集合 = %v", prefillMapSeries(t, "ppts_animation_prefill_runs_total"))
	}
}

func TestPrefillModelFallbackSkipsEmptyReason(t *testing.T) {
	// 空串是「模型正常产出」或「这一页本就不该问」，不是回退。
	// 记进去会把分母变成全部任务，回退率看起来永远是 0。
	PrefillModelFallback("")
	if _, ok := prefillMapSeries(t, "ppts_animation_prefill_model_fallback_total")["reason="]; ok {
		t.Fatal("空 reason 被记成了回退")
	}

	before := prefillMapSeries(t, "ppts_animation_prefill_model_fallback_total")["reason=quota"]
	PrefillModelFallback("quota")
	after := prefillMapSeries(t, "ppts_animation_prefill_model_fallback_total")["reason=quota"]
	if after-before != 1 {
		t.Fatalf("quota 回退计数增量 = %v，期望 1", after-before)
	}
}

func TestPrefillModelCallAccumulatesCountAndLatency(t *testing.T) {
	callsBefore := prefillScalar(t, "ppts_animation_prefill_model_calls_total")
	msBefore := prefillScalar(t, "ppts_animation_prefill_model_latency_ms_total")

	PrefillModelCall(1200)
	PrefillModelCall(0) // 没有耗时（例如缓存命中却仍走到这里）不该污染平均值

	if got := prefillScalar(t, "ppts_animation_prefill_model_calls_total") - callsBefore; got != 2 {
		t.Fatalf("调用次数增量 = %v，期望 2", got)
	}
	// 平均值靠 总和/次数 算：0 也被计入次数会让均值偏低，所以次数照加、总和只加正数。
	if got := prefillScalar(t, "ppts_animation_prefill_model_latency_ms_total") - msBefore; got != 1200 {
		t.Fatalf("耗时总和增量 = %v，期望 1200", got)
	}
}

func TestPrefillCacheEventsAreSeparated(t *testing.T) {
	series := prefillMapSeries(t, "ppts_animation_prefill_cache_total")
	before := series["result=retry_after_failure"]
	PrefillCache(PrefillCacheRetryAfterFailed)
	after := prefillMapSeries(t, "ppts_animation_prefill_cache_total")["result=retry_after_failure"]
	if after-before != 1 {
		t.Fatalf("retry_after_failure 计数增量 = %v，期望 1", after-before)
	}
	// 四个事件必须互不相同：合成一个 cache_miss 就看不出「每次打开都重算」
	// 是没算过，还是算过但失败了。
	seen := map[string]bool{}
	for _, e := range []string{PrefillCacheHit, PrefillCacheMiss, PrefillCacheRetryAfterFailed, PrefillCacheReadError} {
		if seen[e] {
			t.Fatalf("缓存事件重复：%q", e)
		}
		seen[e] = true
	}
}
