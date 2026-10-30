package usage

import (
	"testing"
	"time"
)

// 账期编进计量种类是 M4 额度"按月"的唯一实现方式（见 Kind.Period 的注释）。
// 这里守三件事：跨期是不同的行、基础种类能还原、边界时刻按 UTC 算。

func TestKindPeriodRoundTrip(t *testing.T) {
	periodic := KindAnimationMinutes.Period("2026-10")
	if got, want := string(periodic), "animation_minutes:2026-10"; got != want {
		t.Fatalf("period kind = %q, want %q", got, want)
	}
	if got := periodic.Base(); got != KindAnimationMinutes {
		t.Fatalf("Base() = %q, want %q", got, KindAnimationMinutes)
	}
	if got := periodic.PeriodOf(); got != "2026-10" {
		t.Fatalf("PeriodOf() = %q, want 2026-10", got)
	}
}

func TestKindPeriodEmptyIsUnchanged(t *testing.T) {
	// 空账期必须返回原值：gen_seconds 这类"不按月"的老口径继续走同一个函数。
	if got := KindGenSeconds.Period(""); got != KindGenSeconds {
		t.Fatalf("Period(\"\") = %q, want unchanged", got)
	}
	if got := KindGenSeconds.PeriodOf(); got != "" {
		t.Fatalf("PeriodOf() on plain kind = %q, want empty", got)
	}
	if got := KindGenSeconds.Base(); got != KindGenSeconds {
		t.Fatalf("Base() on plain kind = %q, want unchanged", got)
	}
}

func TestKindPeriodDistinguishesMonths(t *testing.T) {
	sep := KindAnimationMinutes.Period("2026-09")
	oct := KindAnimationMinutes.Period("2026-10")
	if string(sep) == string(oct) {
		t.Fatal("different periods must be different quota rows")
	}
	// 同一个月必须得到同一行：否则一次导出预占、一小时后就结算不到。
	if got := KindAnimationMinutes.Period("2026-10"); got != oct {
		t.Fatalf("same period produced %q and %q", oct, got)
	}
}

func TestMonthPeriodUsesUTC(t *testing.T) {
	// 2026-10-31 23:30 UTC 已经是 11 月 1 日 07:30 的东八区时间。
	// 账期必须按 UTC：否则"何时重置"取决于实例所在时区，
	// 同一个租户在不同节点上会在不同瞬间重置额度。
	local := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)
	if got := MonthPeriod(local); got != "2026-10" {
		t.Fatalf("MonthPeriod = %q, want 2026-10", got)
	}
	shanghai := time.FixedZone("CST", 8*3600)
	// 同一瞬间在 +08:00 下是 11 月 1 日 07:30，但按 UTC 仍属 10 月账期。
	if got := MonthPeriod(local.In(shanghai)); got != "2026-10" {
		t.Fatalf("MonthPeriod in +08:00 = %q, want 2026-10 (UTC-based)", got)
	}
}

func TestMonthReset(t *testing.T) {
	cases := map[string]string{
		"2026-10": "2026-11-01T00:00:00Z",
		"2026-12": "2027-01-01T00:00:00Z",
	}
	for period, want := range cases {
		got, err := MonthReset(period)
		if err != nil {
			t.Fatalf("MonthReset(%q): %v", period, err)
		}
		if got.UTC().Format(time.RFC3339) != want {
			t.Fatalf("MonthReset(%q) = %s, want %s", period, got.UTC().Format(time.RFC3339), want)
		}
	}
	if _, err := MonthReset("2026-13"); err == nil {
		t.Fatal("invalid period must be rejected")
	}
}
