package tts

import (
	"expvar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/observability"
)

func TestVADAlignmentMetrics(t *testing.T) {
	const rate = 16000
	wav := burstWAV(t, rate, speechSegments(), 12000)
	silent := wav16FromSamples(t, rate, make([]int16, rate))
	continuous := wav16FromSamples(t, rate, func() []int16 {
		samples := make([]int16, rate)
		for i := range samples {
			samples[i] = 12000
		}
		return samples
	}())
	cases := []struct {
		name, text, reason string
		audio              []byte
		duration           int64
		method             AlignmentMethod
	}{
		{"punctuated", "今天天气很好，我们出去走走吧。", "none", wav, durationMSOfWAV(wav, rate), AlignEstimateVAD},
		{"unpunctuated", "今天天气很好我们出去走走吧", "none", wav, durationMSOfWAV(wav, rate), AlignEstimateVAD},
		{"duration", "你好", "invalid_duration", wav, 0, AlignEstimate},
		{"format", "你好", "unsupported_wav", []byte("not WAV"), 1000, AlignEstimate},
		{"silent", "你好", "no_usable_speech", silent, 1000, AlignEstimate},
		{"empty", "", "empty_text", wav, durationMSOfWAV(wav, rate), AlignEstimate},
		{"continuous", "今天天气很好我们出去走走吧", "no_pauses_or_nuclei", continuous, 1000, AlignEstimate},
	}
	m := expvar.Get("ppts_tts_vad_alignment_total").(*expvar.Map)
	count := func(key string) int64 {
		if v := m.Get(key); v != nil {
			return v.(*expvar.Int).Value()
		}
		return 0
	}
	total := func() (n int64) {
		m.Do(func(kv expvar.KeyValue) { n += kv.Value.(*expvar.Int).Value() })
		return
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "method=" + string(tc.method) + ",reason=" + tc.reason
			before, allBefore := count(key), total()
			a := buildEstimatedVADAlignment(tc.text, tc.audio, tc.duration)
			if a.Method != tc.method || count(key)-before != 1 || total()-allBefore != 1 {
				t.Fatalf("method=%s, key delta=%d, total delta=%d", a.Method, count(key)-before, total()-allBefore)
			}
		})
	}
	// 对照组不是生产算法，不计入生产命中率。
	before := total()
	buildEstimatedAudioAlignment("你好", wav, durationMSOfWAV(wav, rate), false)
	if total() != before {
		t.Fatal("control computation emitted production metrics")
	}
	rr := httptest.NewRecorder()
	observability.PrometheusHandler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rr.Body.String(), `ppts_tts_vad_alignment_total{method="estimated",reason="unsupported_wav"}`) {
		t.Fatal("alignment metric missing from Prometheus output")
	}
}
