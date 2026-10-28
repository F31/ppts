package animation

import (
	"math"
	"testing"
)

// TestEveryCatalogEffectHasProperties 是 catalog 与 plan.go 的同步断言。
// 新增一种动效却忘了写属性展开时，这条用例会红——这正是 IR"后端不认识动效名"的前提。
func TestEveryCatalogEffectHasProperties(t *testing.T) {
	for _, eff := range Catalog().Effects() {
		plan, err := PlanMotion(eff.ID, Params{}, "easeOut", 1)
		if err != nil {
			t.Fatalf("effect %q has no property expansion: %v", eff.ID, err)
		}
		if len(plan.Props) == 0 {
			t.Fatalf("effect %q expands to zero properties", eff.ID)
		}
		for _, p := range plan.Props {
			if !ValidProp(p.Name) {
				t.Fatalf("effect %q emits unknown property %q", eff.ID, p.Name)
			}
		}
	}
}

func TestPlanMotionRejectsUnknown(t *testing.T) {
	if _, err := PlanMotion("wobble", Params{}, "easeOut", 1); err == nil {
		t.Fatal("未知动效应报错")
	}
	if _, err := PlanMotion("fadeIn", Params{}, "bounce", 1); err == nil {
		t.Fatal("未知缓动应报错")
	}
}

func TestSlideInDirectionAndIntensity(t *testing.T) {
	tests := []struct {
		name     string
		params   Params
		intens   float64
		wantName string
		wantFrom float64
	}{
		{"up", Params{Direction: "up", Offset: 40}, 1, PropTranslateY, 40},
		{"down", Params{Direction: "down", Offset: 40}, 1, PropTranslateY, -40},
		{"left", Params{Direction: "left", Offset: 40}, 1, PropTranslateX, 40},
		{"right", Params{Direction: "right", Offset: 40}, 1, PropTranslateX, -40},
		{"default direction", Params{Offset: 40}, 1, PropTranslateY, 40},
		{"intensity 1.5", Params{Direction: "up", Offset: 40}, 1.5, PropTranslateY, 60},
		{"intensity 0.5", Params{Direction: "up", Offset: 40}, 0.5, PropTranslateY, 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanMotion("slideIn", tc.params, "easeOut", tc.intens)
			if err != nil {
				t.Fatal(err)
			}
			var got *MotionProp
			for i := range plan.Props {
				if plan.Props[i].Name == tc.wantName {
					got = &plan.Props[i]
				}
			}
			if got == nil {
				t.Fatalf("缺少属性 %s，实际 %+v", tc.wantName, plan.Props)
			}
			if math.Abs(got.From-tc.wantFrom) > 1e-9 {
				t.Fatalf("from = %v, want %v", got.From, tc.wantFrom)
			}
			if got.To != 0 {
				t.Fatalf("to = %v, want 0", got.To)
			}
		})
	}
}

// TestIntensityScalesAmplitudeNotBase 固定一个容易写错的语义：
// 强度作用于"幅度"（1-scaleFrom），不是 scaleFrom 本身。
// 若写成 scaleFrom×intensity，intensity=0 会把元素缩到 88% 而不是不缩放。
func TestIntensityScalesAmplitudeNotBase(t *testing.T) {
	plan, err := PlanMotion("pop", Params{ScaleFrom: 0.8}, "easeOut", 1.5)
	if err != nil {
		t.Fatal(err)
	}
	scale := findProp(t, plan.Props, PropScale)
	// 1 - (1-0.8)*1.5 = 0.7
	if math.Abs(scale.From-0.7) > 1e-9 {
		t.Fatalf("scale from = %v, want 0.7", scale.From)
	}

	// 强度 0.5：幅度减半，1 - 0.2*0.5 = 0.9。
	plan, err = PlanMotion("pop", Params{ScaleFrom: 0.8}, "easeOut", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	scale = findProp(t, plan.Props, PropScale)
	if math.Abs(scale.From-0.9) > 1e-9 {
		t.Fatalf("scale from = %v, want 0.9", scale.From)
	}

	// 0 表示"未设置"，按 1 处理；否则 intensity=0 会把元素缩到 80% 而不是不缩放。
	plan, err = PlanMotion("pop", Params{ScaleFrom: 0.8}, "easeOut", 0)
	if err != nil {
		t.Fatal(err)
	}
	scale = findProp(t, plan.Props, PropScale)
	if math.Abs(scale.From-0.8) > 1e-9 {
		t.Fatalf("scale from = %v, want 0.8 (未设置强度即不缩放)", scale.From)
	}
}

func TestGrowAxis(t *testing.T) {
	y, err := PlanMotion("grow", Params{}, "easeOut", 1)
	if err != nil {
		t.Fatal(err)
	}
	findProp(t, y.Props, PropScaleY)

	x, err := PlanMotion("grow", Params{Axis: "x"}, "easeOut", 1)
	if err != nil {
		t.Fatal(err)
	}
	findProp(t, x.Props, PropScaleX)
}

func TestEasingCurves(t *testing.T) {
	for name := range EasingCurves {
		c, err := EasingCurve(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.X1 < 0 || c.X1 > 1 || c.X2 < 0 || c.X2 > 1 {
			t.Fatalf("%s: x control point outside [0,1]: %+v", name, c)
		}
	}
	if _, err := EasingCurve(""); err != nil {
		t.Fatalf("空缓动名应回退到默认: %v", err)
	}
	// 回弹的过冲必须小于视觉基线声明的 8% 上限（曲线与档案不能各说一套）。
	if c := EasingCurves["spring"]; c.Y1 > 1.45 {
		t.Fatalf("spring 控制点 %v 过冲可能超过 8%%", c.Y1)
	}
}

func findProp(t *testing.T, props []MotionProp, name string) MotionProp {
	t.Helper()
	for _, p := range props {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("缺少属性 %q（实际 %+v）", name, props)
	return MotionProp{}
}
