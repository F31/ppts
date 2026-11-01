package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/render"
)

// TestBundleClipsPassesRenderFlags 固定「编译器字段必须原样进清单」这条透传契约。
//
// 为什么值得单独测：share-baseline 直接往包里写 share.Clip，**绕过了 bundleClips**，所以
// 端到端的 V19 门禁覆盖不到这条路径——漏传一个字段时门禁照样全绿（夹具自己就是对的）。
// 同样要固定反面：ContinuationSource 是编译器内部标记，不应出现在清单里。
func TestBundleClipsPassesRenderFlags(t *testing.T) {
	ease := animation.EasingCurves[animation.DefaultEasing]
	op := []animation.MotionProp{{Name: animation.PropOpacity, From: 0, To: 1}}
	carryProps := []animation.MotionProp{
		{Name: animation.PropScaleX, From: 1.2, To: 1},
		{Name: animation.PropTranslateX, From: 26, To: 0},
	}

	in := []render.Motion{
		{ // 本页源段：内部标记不进清单
			ClipID: "c1", Effect: "fadeIn", StartMs: 0, DurationMs: 8000,
			Easing: ease, Props: op, ContinuationSource: true,
		},
		{ // 静态盒补偿
			ClipID: "c1@carry", Effect: "carry", StartMs: 0, DurationMs: 500,
			Easing: ease, Props: carryProps, Carry: true, ElementID: "shape-1",
		},
		{ // 次页延续段
			ClipID: "c1", Effect: "fadeIn", StartMs: -6000, DurationMs: 8000,
			Easing: ease, Props: op,
			IsContinuation: true, ContinuationOffsetMs: 6000, ElementID: "shape-1",
		},
	}

	out := bundleClips(in)
	if len(out) != len(in) {
		t.Fatalf("段数 = %d，want %d", len(out), len(in))
	}

	// 源段：三个延续字段与 carry 都必须为零值——否则非延续产物不再逐字节稳定。
	if out[0].IsContinuation || out[0].ContinuationOffsetMs != 0 || out[0].ElementID != "" || out[0].Carry {
		t.Fatalf("源段不应携带延续/补偿字段: %+v", out[0])
	}
	raw, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"isContinuation", "continuationOffsetMs", "elementId", "carry"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("非延续段的清单里出现了可选键 %q：%s", key, raw)
		}
	}

	// 补偿段：carry 与 elementId 必须透传，否则门禁无法把补偿段排除出「源段配对」。
	if !out[1].Carry || out[1].ElementID != "shape-1" || out[1].DurationMs != 500 {
		t.Fatalf("补偿段透传不完整: %+v", out[1])
	}
	if out[1].IsContinuation {
		t.Fatalf("补偿段不是延续段，不该标 isContinuation: %+v", out[1])
	}

	// 延续段：三字段齐全。
	if !out[2].IsContinuation || out[2].ContinuationOffsetMs != 6000 ||
		out[2].StartMs != -6000 || out[2].ElementID != "shape-1" {
		t.Fatalf("延续段透传不完整: %+v", out[2])
	}
}
