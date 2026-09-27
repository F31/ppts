package observability

import (
	"context"
	"log"
	"time"

	"github.com/F31/ppts/internal/pipeline"
)

// QueueDepthReader 提供按 job kind 的队列积压快照。
//
// 接口在**消费方**定义（不是生产方），这样 pipeline 包不必为观测而依赖 observability，
// 也只有真正需要读的那些 store 才实现它 —— 与 signedURLParser 同一手法。
type QueueDepthReader interface {
	QueueDepthByKind(ctx context.Context, tenantID string) ([]pipeline.QueueDepthStat, error)
}

// QueueDepthReporter 周期性把按 kind 的队列快照灌进 expvar（P2-B3）。
//
// 为什么是轮询而不是事件驱动：队列长度是**存量**，领取/完成事件只能推出增减，
// 一旦某个实例漏一个事件，存量就会永久偏移。直接读表取真值虽然贵一点（30s 一次），
// 但不会漂，而且多副本共享同一个数据源 —— 每个实例各记一份的结果会把 N 副本算成 N 倍。
type QueueDepthReporter struct {
	reader   QueueDepthReader
	tenantID string
	logger   *log.Logger
	interval time.Duration
}

// NewQueueDepthReporter 创建按 kind 的队列快照采集器。tenantID 为空表示单租户本地 profile
// （此时队列视图即该租户的视图）；多副本部署时每个实例都会读到同一份真值。
func NewQueueDepthReporter(reader QueueDepthReader, tenantID string, interval time.Duration, logger *log.Logger) *QueueDepthReporter {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &QueueDepthReporter{reader: reader, tenantID: tenantID, logger: logger, interval: interval}
}

// Run 立即采样一次，再按固定间隔直到 ctx 取消。
func (r *QueueDepthReporter) Run(ctx context.Context) {
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

func (r *QueueDepthReporter) report(ctx context.Context) {
	if r.reader == nil {
		return
	}
	stats, err := r.reader.QueueDepthByKind(ctx, r.tenantID)
	if err != nil {
		if r.logger != nil {
			r.logger.Printf("queue depth: read failed: %v", err)
		}
		return
	}
	for _, st := range stats {
		SetQueueDepthByKind(st.Kind, int64(st.Waiting), int64(st.Running), int64(st.NotReady))
		// 没有排队任务时**不写**等待时长：此时的最老等待是"未定义"而不是 0 秒。
		// 写 0 会让"积压已消化"和"从没排过队"看起来一样，而后者往往是采集链路断了。
		if st.Waiting > 0 && !st.OldestRunAt.IsZero() {
			SetQueueOldestWaitByKind(st.Kind, time.Since(st.OldestRunAt).Seconds())
		}
	}
}
