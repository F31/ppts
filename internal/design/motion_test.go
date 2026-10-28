package design

import (
	"reflect"
	"testing"
)

func TestDefaultMotionValid(t *testing.T) {
	if err := DefaultMotion().Validate(); err != nil {
		t.Fatalf("默认运动档案非法: %v", err)
	}
}

func TestStaggerSequential(t *testing.T) {
	got := StaggerOffsets(4, 75, StaggerSequential)
	want := []int64{0, 75, 150, 225}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("顺序模式 = %v，期望 %v", got, want)
	}
	// 确定性：两次调用必须一致。
	if again := StaggerOffsets(4, 75, StaggerSequential); !reflect.DeepEqual(got, again) {
		t.Error("同一输入两次调用结果不一致")
	}
}

func TestStaggerCenter(t *testing.T) {
	cases := []struct {
		n    int
		want []int64
	}{
		{1, []int64{0}},
		{3, []int64{60, 0, 60}},
		{4, []int64{120, 60, 60, 120}},
		{5, []int64{120, 60, 0, 60, 120}},
	}
	for _, c := range cases {
		if got := StaggerOffsets(c.n, 60, StaggerCenter); !reflect.DeepEqual(got, c.want) {
			t.Errorf("中心扩散 n=%d = %v，期望 %v", c.n, got, c.want)
		}
	}
	if StaggerOffsets(0, 60, StaggerCenter) != nil {
		t.Error("n=0 应返回 nil")
	}
	if got := StaggerOffsets(2, -10, StaggerSequential); got[1] != 0 {
		t.Errorf("负间隔应被夹到 0，实际 %v", got)
	}
}

func TestEasingEndpointsAndMonotonicity(t *testing.T) {
	m := DefaultMotion()
	for _, e := range m.Easings {
		if got := e.At(0); got != 0 {
			t.Errorf("%s 的 At(0) = %v，期望 0", e.Name, got)
		}
		if got := e.At(1); got != 1 {
			t.Errorf("%s 的 At(1) = %v，期望 1", e.Name, got)
		}
		if got := e.At(-0.5); got != 0 {
			t.Errorf("%s 的 At(-0.5) 应夹到 0，实际 %v", e.Name, got)
		}
		// 非回弹曲线必须单调不减。
		if e.Name == "overshoot" {
			continue
		}
		prev := 0.0
		for i := 1; i <= 32; i++ {
			v := e.At(float64(i) / 32)
			if v < prev-1e-9 {
				t.Errorf("%s 在 t=%.3f 处回退: %.6f < %.6f", e.Name, float64(i)/32, v, prev)
			}
			prev = v
		}
	}
}

func TestOvershootWithinMaxAmplitude(t *testing.T) {
	m := DefaultMotion()
	e, ok := m.Easing("overshoot")
	if !ok {
		t.Fatal("缺少 overshoot 曲线")
	}
	peak := e.Peak(128)
	if peak <= 1.001 {
		t.Errorf("overshoot 峰值 %.4f，没有过冲", peak)
	}
	if peak-1 > m.Overshoot.MaxAmplitude+0.005 {
		t.Errorf("过冲 %.4f 超过上限 %.2f", peak-1, m.Overshoot.MaxAmplitude)
	}
}

func TestEasingPeakForNonOvershoot(t *testing.T) {
	m := DefaultMotion()
	e, _ := m.Easing("standard")
	if peak := e.Peak(64); peak > 1.0001 {
		t.Errorf("standard 不应过冲，峰值 %.4f", peak)
	}
}

func TestMotionRejectsBadParameters(t *testing.T) {
	cases := map[string]func(*MotionProfile){
		"停顿过短":   func(m *MotionProfile) { m.HoldMs = 300 },
		"回弹过大":   func(m *MotionProfile) { m.Overshoot.MaxAmplitude = 0.5 },
		"分层缺失":   func(m *MotionProfile) { delete(m.SpeedTiers, TierSmall) },
		"分层反了":   func(m *MotionProfile) { m.SpeedTiers[TierLarge] = Range{Min: 200, Max: 300} },
		"缓动缺失":   func(m *MotionProfile) { m.Easings = nil },
		"缺命名缓动":  func(m *MotionProfile) { m.Easings = m.Easings[:3] },
		"预备占比过高": func(m *MotionProfile) { m.Anticipation.Ratio = 0.8 },
	}
	for name, mutate := range cases {
		m := DefaultMotion()
		m.SpeedTiers = map[string]Range{
			TierLarge: m.SpeedTiers[TierLarge], TierMedium: m.SpeedTiers[TierMedium], TierSmall: m.SpeedTiers[TierSmall],
		}
		m.Easings = append([]Easing(nil), m.Easings...)
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

func TestTierAndDuration(t *testing.T) {
	m := DefaultMotion()
	if got := m.TierFor(0.40); got != TierLarge {
		t.Errorf("面积 40%% 应为 %s，实际 %s", TierLarge, got)
	}
	if got := m.TierFor(0.10); got != TierMedium {
		t.Errorf("面积 10%% 应为 %s，实际 %s", TierMedium, got)
	}
	if got := m.TierFor(0.01); got != TierSmall {
		t.Errorf("面积 1%% 应为 %s，实际 %s", TierSmall, got)
	}
	if got := m.DurationFor(0.40, 0); got != 750 {
		t.Errorf("大元素推荐时长 = %d，期望 750", got)
	}
	if got := m.DurationFor(0.40, 500); got != 500 {
		t.Errorf("兜底时长应压到 500，实际 %d", got)
	}
}
