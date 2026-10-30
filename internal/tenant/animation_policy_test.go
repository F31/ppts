package tenant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

// 这三条守的是「nil 与 false 不可混淆」。一旦用零值 bool 表达策略，
// 「没配过」与「明确封禁」就合并了：灰度扩到 100% 时被封禁的租户会重新被打开。
func TestAnimationPolicyNilVersusFalse(t *testing.T) {
	// 未设置：序列化后不出现 enabled 键；解析回来仍是 nil。
	unset := Policy{Animation: &animation.AnimationPolicy{}}
	raw, err := json.Marshal(unset)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); strings.Contains(got, `"enabled"`) {
		t.Fatalf("未设置不应写出 enabled 键：%s", got)
	}
	var back Policy
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Animation == nil || back.Animation.Enabled != nil {
		t.Fatalf("未设置解析回来应是 nil 指针：%+v", back.Animation)
	}

	// 显式 false：必须写出键，解析回来是非 nil 的 false。
	off := false
	raw, err = json.Marshal(Policy{Animation: &animation.AnimationPolicy{Enabled: &off}})
	if err != nil {
		t.Fatal(err)
	}
	back = Policy{}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Animation == nil || back.Animation.Enabled == nil || *back.Animation.Enabled {
		t.Fatalf("显式 false 丢了：%+v", back.Animation)
	}
}

func TestAnimationPolicyAbsentInLegacyPolicy(t *testing.T) {
	var p Policy
	if err := json.Unmarshal([]byte(`{"storage_backend":"s3","render_retention_days":7}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Animation != nil {
		t.Fatalf("旧策略没有 animation 键时必须是 nil（= 没配过，走部署默认）：%+v", p.Animation)
	}
	if p.StorageBackend != "s3" || p.RenderRetentionDays != 7 {
		t.Fatalf("既有字段被破坏：%+v", p)
	}
}

func TestAnimationPolicyRoundTripFull(t *testing.T) {
	yes, no, pct := true, false, 20
	in := Policy{Animation: &animation.AnimationPolicy{
		Enabled: &yes, TrackEditor: &no, Preview: &yes, MaxMinutes: 120, RolloutPercent: &pct,
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Policy
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	a := out.Animation
	if a == nil || a.Enabled == nil || !*a.Enabled {
		t.Fatalf("enabled 丢失：%+v", a)
	}
	if a.TrackEditor == nil || *a.TrackEditor {
		t.Fatalf("track_editor 丢失：%+v", a)
	}
	if a.Preview == nil || !*a.Preview {
		t.Fatalf("preview 丢失：%+v", a)
	}
	if a.MaxMinutes != 120 {
		t.Fatalf("max_minutes 丢失：%+v", a)
	}
	if a.RolloutPercent == nil || *a.RolloutPercent != 20 {
		t.Fatalf("rollout_percent 丢失：%+v", a)
	}
}
