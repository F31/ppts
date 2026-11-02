package render

import "testing"

// 默认预算必须跟着机型走：位不够就是达不到 SLA，位太多就是 OOM。
// 这两条都曾经作为「默认值」被踩过，所以用表驱动钉住。
func TestDefaultRenderSlots(t *testing.T) {
	cases := []struct {
		cpus      int
		want      RenderSlots
		wantTotal int
		sla       bool
		why       string
	}{
		{cpus: 0, want: RenderSlots{Page: 2, Frame: 1}, wantTotal: 2, sla: false,
			why: "读不到核数时按最小机型处理，宁可慢也不能 OOM"},
		{cpus: 1, want: RenderSlots{Page: 2, Frame: 1}, wantTotal: 2, sla: false,
			why: "单核不能开 4 个位"},
		{cpus: 2, want: RenderSlots{Page: 2, Frame: 1}, wantTotal: 2, sla: false,
			why: "2 核开 4 位实测 OOM；2 位达不到 120s，这是硬约束不是配置问题"},
		{cpus: 3, want: RenderSlots{Page: 4, Frame: 1}, wantTotal: 4, sla: true,
			why: "3 核已能容纳 4 位（实测机型 4 vCPU）"},
		{cpus: 4, want: RenderSlots{Page: 4, Frame: 1}, wantTotal: 4, sla: true,
			why: "参考部署：4 位实测 P95 49.3s"},
		{cpus: 8, want: RenderSlots{Page: 4, Frame: 1}, wantTotal: 4, sla: true,
			why: "核多不加位：没有实测数据，位是线性吃内存的"},
		{cpus: 64, want: RenderSlots{Page: 4, Frame: 1}, wantTotal: 4, sla: true,
			why: "同上，按位预算封顶"},
	}
	for _, c := range cases {
		got := DefaultRenderSlots(c.cpus)
		if got != c.want {
			t.Errorf("DefaultRenderSlots(%d) = %+v，期望 %+v（%s）", c.cpus, got, c.want, c.why)
		}
		if got.Total() != c.wantTotal {
			t.Errorf("DefaultRenderSlots(%d).Total() = %d，期望 %d", c.cpus, got.Total(), c.wantTotal)
		}
		if met := got.Total() >= SLARecommendedSlots; met != c.sla {
			t.Errorf("DefaultRenderSlots(%d) 达标判定 = %v，期望 %v（%s）", c.cpus, met, c.sla, c.why)
		}
	}
}

// 位是乘积不是加法；任务数只在 Fleet 里乘一次，别和帧并发混成一个数。
func TestRenderSlotsTotalIsProduct(t *testing.T) {
	cases := []struct {
		slots RenderSlots
		want  int
	}{
		{RenderSlots{Page: 1, Frame: 1}, 1},
		{RenderSlots{Page: 4, Frame: 1}, 4},
		{RenderSlots{Page: 1, Frame: 2}, 2},
		{RenderSlots{Page: 2, Frame: 2}, 4},
		{RenderSlots{Page: 0, Frame: 4}, 0},
		{RenderSlots{Page: 4, Frame: 0}, 0},
		{RenderSlots{Page: -1, Frame: 2}, 0},
	}
	for _, c := range cases {
		if got := c.slots.Total(); got != c.want {
			t.Errorf("RenderSlots%+v.Total() = %d，期望 %d", c.slots, got, c.want)
		}
	}
}

// Fleet 只负责乘任务数：任务数由 MaxConcurrent 单独决定，不参与 Total。
func TestRenderSlotsFleet(t *testing.T) {
	s := RenderSlots{Page: 4, Frame: 1}
	if got := s.Fleet(0); got != 0 {
		t.Errorf("Fleet(0) = %d，期望 0", got)
	}
	if got := s.Fleet(2); got != 8 {
		t.Errorf("Fleet(2) = %d，期望 8（2 任务 × 4 位）", got)
	}
	if got := (RenderSlots{}).Fleet(3); got != 0 {
		t.Errorf("空预算 Fleet(3) = %d，期望 0", got)
	}
}
