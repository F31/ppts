package observability

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type gaugeFake struct{ seconds float64 }

func (g *gaugeFake) SetQueueOldestWait(seconds float64) { g.seconds = seconds }

func TestQueueBacklogReporterUpdatesGauge(t *testing.T) {
	var calls int32
	gauge := &gaugeFake{}
	reporter := NewQueueBacklogReporter(gauge, func(context.Context) (time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		return 12 * time.Second, nil
	}, time.Second, nil)
	reporter.report(context.Background())
	if gauge.seconds != 12 {
		t.Fatalf("gauge seconds = %v want 12", gauge.seconds)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls = %d want 1", calls)
	}
}

func TestQueueBacklogReporterIgnoresReadError(t *testing.T) {
	gauge := &gaugeFake{seconds: 5}
	reporter := NewQueueBacklogReporter(gauge, func(context.Context) (time.Duration, error) {
		return 0, errors.New("boom")
	}, time.Second, nil)
	reporter.report(context.Background())
	if gauge.seconds != 5 {
		t.Fatalf("gauge changed to %v despite error", gauge.seconds)
	}
}

func TestQueueBacklogReporterRunUntilCancel(t *testing.T) {
	gauge := &gaugeFake{}
	ctx, cancel := context.WithCancel(context.Background())
	reporter := NewQueueBacklogReporter(gauge, func(context.Context) (time.Duration, error) {
		return 1 * time.Second, nil
	}, time.Millisecond, nil)
	go reporter.Run(ctx)
	time.Sleep(5 * time.Millisecond)
	cancel()
}
