package observability

import (
	"expvar"
	"testing"
)

// TestTextNormShadowDiffCounter 锁定影子不一致计数器的指标名与累加语义。
func TestTextNormShadowDiffCounter(t *testing.T) {
	m := expvar.Get("ppts_textnorm_shadow_diff_total")
	if m == nil {
		t.Fatal("ppts_textnorm_shadow_diff_total not registered")
	}
	shadowMap, ok := m.(*expvar.Map)
	if !ok {
		t.Fatalf("metric is %T, want *expvar.Map", m)
	}
	before := shadowKeyValue(shadowMap, "diverged")
	TextNormShadowDiff("diverged")
	TextNormShadowDiff("diverged")
	TextNormShadowDiff("error")
	after := shadowKeyValue(shadowMap, "diverged")
	if after-before != 2 {
		t.Fatalf("diverged delta = %d, want 2", after-before)
	}
	if shadowKeyValue(shadowMap, "error") != 1 {
		t.Fatal("error counter should be 1")
	}
}

func shadowKeyValue(m *expvar.Map, key string) int64 {
	v := m.Get(key)
	if v == nil {
		return 0
	}
	if iv, ok := v.(*expvar.Int); ok {
		return iv.Value()
	}
	return 0
}
