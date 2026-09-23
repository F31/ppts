package observability

import (
	"strings"
	"testing"
)

func TestWritePrometheusScalar(t *testing.T) {
	var b strings.Builder
	writePrometheus(&b, "ppts_test_gauge", "42")
	out := b.String()
	if !strings.Contains(out, "# TYPE ppts_test_gauge gauge") || !strings.Contains(out, "ppts_test_gauge 42") {
		t.Fatalf("scalar render = %q", out)
	}
}

func TestWritePrometheusMapLabels(t *testing.T) {
	var b strings.Builder
	writePrometheus(&b, "ppts_worker_jobs_total", `{"kind=narration,event=claimed":5,"kind=narration,event=succeeded":4}`)
	out := b.String()
	if !strings.Contains(out, `ppts_worker_jobs_total{kind="narration",event="claimed"} 5`) {
		t.Fatalf("map render = %q", out)
	}
	if !strings.Contains(out, `ppts_worker_jobs_total{kind="narration",event="succeeded"} 4`) {
		t.Fatalf("map render = %q", out)
	}
}

func TestWritePrometheusString(t *testing.T) {
	var b strings.Builder
	writePrometheus(&b, "ppts_build_info", `"v1.2.3"`)
	out := b.String()
	if !strings.Contains(out, `ppts_build_info_info{value="\"v1.2.3\""} 1`) {
		t.Fatalf("string render = %q", out)
	}
}

func TestSanitizeNames(t *testing.T) {
	if got := sanitizeMetricName("ppts.a-b"); got != "ppts_a_b" {
		t.Fatalf("metric name = %q", got)
	}
	if got := sanitizeLabelName("kind"); got != "kind" {
		t.Fatalf("label name = %q", got)
	}
}
