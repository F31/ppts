package render

// SLARecommendedSlots 是「30s 正片 P95 ≤ 120s」所需的单任务渲染位数。
//
// 实测口径（10 页 × 3s / 1080p30 / 4 vCPU，M5 冷渲染 5 次）：
// 4 位 49.3s、2 位 133.9s。目标线 120s 落在两者之间，所以 2 位不达标——
// 这条常量存在的意义是让「不够」在启动日志里被说出来，而不是等交付时才发现。
const SLARecommendedSlots = 4

// RenderSlots 是一次渲染任务内的并发渲染位预算（ADR-16）：
//
//	渲染位 = 页并发 × 帧并发
//
// 它**不含任务数**。服务同时跑几个任务由 ServiceConfig.MaxConcurrent 决定，而
// cmd/renderer 恰好把同一个 -concurrency 同时用作「帧并发」与「任务并发上限」，于是：
//
//	单任务位 = Page × Frame
//	满载位   = Page × Frame × MaxConcurrent
//
// 部署文档过去把这两个口径混在一列里比：默认配置写「2 位」，但默认满载其实是 4 位。
// 结果是「同样 4 个位，为什么切开方式差 30%」这种看起来矛盾、实则量的是两种东西的结论。
// 比较 P95 只认单任务位——P95 是单个任务的墙钟，跟同时排了几个任务无关。
type RenderSlots struct {
	// Page 是同时渲染的页数（Remotion --page-concurrency）。
	Page int
	// Frame 是页内的帧并发（Remotion --concurrency）。
	Frame int
}

// Total 返回单个任务的渲染位数。
func (s RenderSlots) Total() int {
	if s.Page <= 0 || s.Frame <= 0 {
		return 0
	}
	return s.Page * s.Frame
}

// Fleet 返回 tasks 个任务同时满载时的渲染位数。tasks 即 MaxConcurrent。
func (s RenderSlots) Fleet(tasks int) int {
	if tasks <= 0 {
		return 0
	}
	return s.Total() * tasks
}

// DefaultRenderSlots 按可用 CPU 给出默认预算。
//
// cpus 传 runtime.GOMAXPROCS(0) 而不是 runtime.NumCPU()：Go 1.25 起 GOMAXPROCS
// 会读容器的 CPU 配额，而 NumCPU 在容器里仍是宿主机的核数——照 NumCPU 定并发，
// 会把「限额 2 核」的边端当成 32 核来开浏览器。
func DefaultRenderSlots(cpus int) RenderSlots {
	if cpus <= 2 {
		// 2 核：4 位实测 OOM（4 个 Chrome + 4 份帧缓冲压不住），2 位是这台机型的上限。
		// 因此 2 核**达不到** 120s（实测 133.9s）。这里不粉饰，由调用方打印警告。
		//
		// 选「2 页 × 1 帧」而不是「1 页 × 2 帧」：页并行的优势来自每页独立标签页、
		// 解码与合成器不互相排队（4 核实测快 30%）。2 核没有单独量过这两种切法，
		// 位预算才是硬约束，切法只是把已知方向外推。
		return RenderSlots{Page: 2, Frame: 1}
	}
	// 4 核及以上：1 任务 × 4 页 × 1 帧 = 4 位，实测 49.3s（达标且最快）。
	// 核数更多时不继续加位：没有实测数据，且位是线性吃内存的，加大只会把
	// 「没量过的乘数」变成默认值——ADR-16 反对的正是这件事。
	return RenderSlots{Page: 4, Frame: 1}
}
