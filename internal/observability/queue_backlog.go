package observability

import (
	"context"
	"log"
	"time"
)

// QueueGauge exposes the queue backlog gauge.
type QueueGauge interface {
	SetQueueOldestWait(seconds float64)
}

// QueueAgeFunc reports the age of the oldest runnable queued job.
type QueueAgeFunc func(ctx context.Context) (time.Duration, error)

// QueueBacklogReporter periodically updates the oldest-wait gauge (G3-8).
type QueueBacklogReporter struct {
	gauge    QueueGauge
	age      QueueAgeFunc
	logger   *log.Logger
	interval time.Duration
}

// NewQueueBacklogReporter creates a queue backlog reporter.
func NewQueueBacklogReporter(gauge QueueGauge, age QueueAgeFunc, interval time.Duration, logger *log.Logger) *QueueBacklogReporter {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &QueueBacklogReporter{gauge: gauge, age: age, logger: logger, interval: interval}
}

// Run reports immediately, then on a fixed interval until ctx is canceled.
func (r *QueueBacklogReporter) Run(ctx context.Context) {
	r.report(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.report(ctx)
		}
	}
}

func (r *QueueBacklogReporter) report(ctx context.Context) {
	if r.gauge == nil || r.age == nil {
		return
	}
	age, err := r.age(ctx)
	if err != nil {
		if r.logger != nil {
			r.logger.Printf("queue backlog: read failed: %v", err)
		}
		return
	}
	r.gauge.SetQueueOldestWait(age.Seconds())
}
