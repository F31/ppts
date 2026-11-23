package observability

import (
	"context"
	"expvar"
	"sort"
	"sync"
	"time"
)

// 导出墙钟拆开：渲染器 22s、任务 134s 时，差额在拉页图、混音、下载、拼接、上传，
// 不在 gaze 画框。阶段名是低基数标签，均值 = duration_sum / jobs。
const (
	ExportStageTimeline = "timeline"
	ExportStageAssets   = "assets"
	ExportStageMixAudio = "mix_audio"
	ExportStageAssemble = "assemble"
	ExportStageCompile  = "compile"
	ExportStageRender   = "render"
	ExportStageDownload = "download"
	ExportStageConcat   = "concat"
	ExportStageEncode   = "encode"
	ExportStagePack     = "pack"
	ExportStagePublish  = "publish"
	ExportStageOther    = "other"
	ExportStageTotal    = "total"
)

var (
	exportStageDurationMS = expvar.NewMap("ppts_export_stage_duration_ms_total")
	exportStageJobsTotal  = expvar.NewMap("ppts_export_stage_jobs_total")
	exportDurationMS      = expvar.NewMap("ppts_export_duration_ms_total")
)

type exportClockKey struct{}

// ExportClock 把一次导出拆成互不重叠的阶段。Finish 把未记账的时间记到 other。
type ExportClock struct {
	mu     sync.Mutex
	start  time.Time
	stages map[string]time.Duration
	order  []string
	done   bool
}

// ExportClockSnapshot 是一次导出的墙钟拆分，供日志与测试。
type ExportClockSnapshot struct {
	Total  time.Duration
	Other  time.Duration
	Stages []ExportStageDur
}

// ExportStageDur 是一个已记账阶段。
type ExportStageDur struct {
	Name     string
	Duration time.Duration
}

// NewExportClock 从现在起算整次导出。
func NewExportClock() *ExportClock {
	return &ExportClock{start: time.Now(), stages: map[string]time.Duration{}}
}

// WithExportClock 把钟挂到 ctx，Handle 里挂一次，下游 Stage 从 ctx 取。
func WithExportClock(ctx context.Context, c *ExportClock) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, exportClockKey{}, c)
}

// ExportStage 给 name 计一段墙钟。ctx 没有钟时是空操作。done 只生效一次。
func ExportStage(ctx context.Context, name string) func() {
	if name == "" {
		return func() {}
	}
	c, _ := ctx.Value(exportClockKey{}).(*ExportClock)
	if c == nil {
		return func() {}
	}
	start := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() { c.add(name, time.Since(start)) })
	}
}

func (c *ExportClock) add(name string, d time.Duration) {
	if c == nil || name == "" || d < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	if _, ok := c.stages[name]; !ok {
		c.order = append(c.order, name)
	}
	c.stages[name] += d
}

// Finish 结算 total / other，并写入 expvar。可重复调用，只结算一次。
func (c *ExportClock) Finish(format string) ExportClockSnapshot {
	if c == nil {
		return ExportClockSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	total := time.Since(c.start)
	if total < 0 {
		total = 0
	}
	var accounted time.Duration
	out := make([]ExportStageDur, 0, len(c.order))
	for _, name := range c.order {
		d := c.stages[name]
		accounted += d
		out = append(out, ExportStageDur{Name: name, Duration: d})
	}
	other := total - accounted
	if other < 0 {
		other = 0
	}
	snap := ExportClockSnapshot{Total: total, Other: other, Stages: out}
	if c.done {
		return snap
	}
	c.done = true
	recordExportClock(format, snap)
	return snap
}

func recordExportClock(format string, snap ExportClockSnapshot) {
	if format == "" {
		format = "unknown"
	}
	exportDurationMS.Add("format="+clean(format), snap.Total.Milliseconds())
	exportStageJobsTotal.Add("stage="+ExportStageTotal, 1)
	exportStageDurationMS.Add("stage="+ExportStageTotal, snap.Total.Milliseconds())
	if snap.Other > 0 {
		exportStageJobsTotal.Add("stage="+ExportStageOther, 1)
		exportStageDurationMS.Add("stage="+ExportStageOther, snap.Other.Milliseconds())
	}
	for _, s := range snap.Stages {
		if s.Duration <= 0 {
			continue
		}
		key := "stage=" + clean(s.Name)
		exportStageJobsTotal.Add(key, 1)
		exportStageDurationMS.Add(key, s.Duration.Milliseconds())
	}
}

// Attrs 是 slog 用的扁平键：total_ms、other_ms、assets_ms… 顺序稳定。
func (s ExportClockSnapshot) Attrs() []any {
	attrs := []any{"total_ms", s.Total.Milliseconds(), "other_ms", s.Other.Milliseconds()}
	names := make([]string, 0, len(s.Stages))
	byName := make(map[string]int64, len(s.Stages))
	for _, st := range s.Stages {
		names = append(names, st.Name)
		byName[st.Name] = st.Duration.Milliseconds()
	}
	sort.Strings(names)
	for _, name := range names {
		attrs = append(attrs, name+"_ms", byName[name])
	}
	return attrs
}
