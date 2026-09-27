package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// 探活分项结论。
//
// 三态而非两态是刻意的：`unsupported` 与 `up` 必须可区分。
// 若把"这个适配器没法探活"合并进 `up`，探活端点就会对一个从未被真正检查过的依赖
// 报健康——这与把失败压成空态是同一类不诚实（A26）。
const (
	checkUp          = "up"
	checkDown        = "down"
	checkUnsupported = "unsupported"
)

// DependencyPinger 是对象存适配器的**可选**能力。
//
// 之所以设计成可选接口而不是给 ObjectStore 接口加方法：实现一共有本地文件系统与
// S3 两个后端，加方法会让"无法廉价探活"的实现被迫写出假实现（返回 nil 假装健康）。
// 未实现者由探活端点显式报 `unsupported`，而不是被静默当作健康。
type DependencyPinger interface {
	Ping(ctx context.Context) error
}

// DBPinger 是数据库可达性探测所需的最小能力。
//
// 刻意用窄接口而不是 *pgxpool.Pool：探活只需要 Ping，用具体类型会把"验证 DB 行为"
// 绑死在真实连接池上，而连一个必然失败的地址在某些网络环境下表现为**超时而非拒绝**
// （本地沙箱实测如此），测试就会挂住。窄接口让假实现可以在毫秒级返回失败。
type DBPinger interface {
	Ping(ctx context.Context) error
}

// HealthChecker 汇总各依赖项的可达性。各项**独立探测**：一项失败不得掩盖其余分项结论，
// 否则排障时只能看到"第一个挂了的依赖"，看不到同时挂着的第二个。
//
// Pool 为接口类型，赋值处必须判 nil 后再赋（见 NewHandler）：把 (*pgxpool.Pool)(nil)
// 直接赋给接口会得到一个非 nil 接口值，随后调用 Ping 会 panic。
type HealthChecker struct {
	Pool    DBPinger
	Objects objectstore.ObjectStore
	// Timeout 是单项探测上限；<=0 时用默认值。探活本身挂死比依赖不可用更糟——
	// 负载均衡会以它为准做摘除判定，一个不返回的探活等于永久在册。
	Timeout time.Duration
	// Logger 接收真实错误详情。错误原文**不进 HTTP 响应**：/healthz 免鉴权，
	// 连接串、桶名、堆栈等细节只能进日志。
	Logger *log.Logger
}

type checkResult struct {
	State string `json:"state"`
}

// HealthReport 是探活结论。checks 只含 `state`，不含任何错误细节。
type HealthReport struct {
	Status string                 `json:"status"`
	Checks map[string]checkResult `json:"checks"`
}

// Degraded 报告是否存在已确认不可用的依赖项。unsupported 不计入——它表示"没查"，
// 不等于"查了没问题"，也不等于"坏了"。
func (r HealthReport) Degraded() bool {
	for _, c := range r.Checks {
		if c.State == checkDown {
			return true
		}
	}
	return false
}

func (h HealthChecker) Check(ctx context.Context) HealthReport {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rep := HealthReport{Status: "ok", Checks: make(map[string]checkResult)}

	// 依赖为 nil 表示本次部署没有该依赖（如 SQLite profile 无 PG 连接池），
	// 不登记分项，也不算故障。
	if h.Pool != nil {
		rep.Checks["database"] = h.probe(checkCtx, "database", func(ctx context.Context) error {
			return h.Pool.Ping(ctx)
		})
	}
	if h.Objects != nil {
		pinger, ok := h.Objects.(DependencyPinger)
		if !ok {
			rep.Checks["object"] = checkResult{State: checkUnsupported}
		} else {
			rep.Checks["object"] = h.probe(checkCtx, "object", pinger.Ping)
		}
	}
	if rep.Degraded() {
		rep.Status = "unhealthy"
	}
	return rep
}

func (h HealthChecker) probe(ctx context.Context, name string, fn func(context.Context) error) checkResult {
	if err := fn(ctx); err != nil {
		// 超时与被取消同样算故障：探活按时得不到结论，就不能宣称健康。
		if h.Logger != nil {
			h.Logger.Printf("healthz: %s check failed: %v", name, err)
		}
		return checkResult{State: checkDown}
	}
	return checkResult{State: checkUp}
}

// healthHandler 返回探活端点。任一已探测依赖不可用即 503 —— 这与"探活永远 200"的差别
// 正是它能用于摘除判定的前提：一个恒真的探活等于没有探活。
func healthHandler(checker HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rep := checker.Check(r.Context())
		status := http.StatusOK
		if rep.Degraded() {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(rep)
	}
}
