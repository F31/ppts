package validation

import (
	"reflect"
	"testing"
)

func TestCheckPreservedOK(t *testing.T) {
	report := CheckPreserved("吞吐提升 23.5%，延迟 12ms，A100 在 2026-09-13 上线。",
		"这套方案把吞吐提升 23.5%，延迟控制在 12ms，并将在 2026年09月13日支持 A100。")
	if !report.OK() {
		t.Fatalf("report = %+v missing=%v inserted=%v", report, FormatEntities(report.Missing), FormatEntities(report.Inserted))
	}
}

func TestCheckPreservedFlagsMissingAndInserted(t *testing.T) {
	report := CheckPreserved("端口升级到 PCIe 5.0，容量 80GB。", "端口升级到 PCIe 6.0，容量 96GB。")
	if report.OK() {
		t.Fatalf("expected violations")
	}
	if !reflect.DeepEqual(FormatEntities(report.Missing), []string{"number:5.0", "number:80gb"}) {
		t.Fatalf("missing = %v", FormatEntities(report.Missing))
	}
	if !reflect.DeepEqual(FormatEntities(report.Inserted), []string{"number:6.0", "number:96gb"}) {
		t.Fatalf("inserted = %v", FormatEntities(report.Inserted))
	}
}
