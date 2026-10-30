package animation

import (
	"fmt"
	"testing"
)

func boolPtr(b bool) *bool { return &b }
func intPtr(n int) *int    { return &n }

func TestResolveDeployGateOverridesTenantPolicy(t *testing.T) {
	p := AnimationPolicy{Enabled: boolPtr(true), TrackEditor: boolPtr(true)}
	c := p.Resolve("tenant-a", GateConfig{DeployEnabled: false, DefaultPercent: 100})
	if c.Enabled || c.TrackEditor || c.Preview {
		t.Fatalf("部署闸关闭时租户策略不得开启能力，得到 %+v", c)
	}
	if c.Source != SourceDeploy || c.Reason != ReasonDeployDisabled {
		t.Fatalf("来源/原因不符：%+v", c)
	}
}

func TestResolveExplicitTrueBypassesRollout(t *testing.T) {
	// rollout 0 + 显式 true：白名单必须在灰度没铺开时就能用。
	p := AnimationPolicy{Enabled: boolPtr(true)}
	c := p.Resolve("tenant-a", GateConfig{DeployEnabled: true, DefaultPercent: 0})
	if !c.Enabled || c.Source != SourceTenant {
		t.Fatalf("显式 true 应绕过灰度：%+v", c)
	}
	if c.TrackEditor {
		t.Fatalf("track_editor 未设置时应为 false：%+v", c)
	}
	if !c.Preview {
		t.Fatalf("preview 未设置时应随总开关为 true：%+v", c)
	}
}

func TestResolveExplicitFalseBeatsFullRollout(t *testing.T) {
	// rollout 100 + 显式 false：封禁不能因为"所有人在桶内"而失效。
	p := AnimationPolicy{Enabled: boolPtr(false), TrackEditor: boolPtr(true)}
	c := p.Resolve("tenant-a", GateConfig{DeployEnabled: true, DefaultPercent: 100})
	if c.Enabled {
		t.Fatalf("显式 false 应压过 100%% 灰度：%+v", c)
	}
	if c.Source != SourceTenant || c.Reason != ReasonTenantDisabled {
		t.Fatalf("来源/原因不符：%+v", c)
	}
	if c.TrackEditor || c.Preview {
		t.Fatalf("总开关关闭时子能力必须全灭：%+v", c)
	}
}

func TestResolveTenantRolloutWinsOverDefault(t *testing.T) {
	id := tenantWithBucket(t, 0, 20) // 桶在 [0,20)
	p := AnimationPolicy{RolloutPercent: intPtr(20)}
	c := p.Resolve(id, GateConfig{DeployEnabled: true, DefaultPercent: 0})
	if !c.Enabled || c.Source != SourceRollout {
		t.Fatalf("租户 rollout 应优先于部署默认：%+v", c)
	}

	miss := tenantWithBucket(t, 20, 100)
	c = p.Resolve(miss, GateConfig{DeployEnabled: true, DefaultPercent: 100})
	if c.Enabled || c.Reason != ReasonRolloutMiss {
		t.Fatalf("桶外租户不应因部署默认 100 而开启：%+v", c)
	}
	if c.Bucket < 20 || c.Bucket >= 100 {
		t.Fatalf("桶号不符：%d", c.Bucket)
	}
}

func TestResolveDefaultPercentWhenTenantUnset(t *testing.T) {
	in := tenantWithBucket(t, 0, 10)
	c := AnimationPolicy{}.Resolve(in, GateConfig{DeployEnabled: true, DefaultPercent: 10})
	if !c.Enabled || c.Source != SourceDefault || c.Reason != "" {
		t.Fatalf("未配策略且桶内应可用：%+v", c)
	}

	out := tenantWithBucket(t, 10, 100)
	c = AnimationPolicy{}.Resolve(out, GateConfig{DeployEnabled: true, DefaultPercent: 10})
	if c.Enabled || c.Source != SourceDefault || c.Reason != ReasonDefaultOff {
		t.Fatalf("未配策略且桶外应关闭：%+v", c)
	}
}

func TestResolveExplicitZeroRolloutIsNotUnset(t *testing.T) {
	// rollout 显式 0 与 nil 不同：前者是"本租户不参与"，必须压过部署默认 100。
	id := tenantWithBucket(t, 0, 100)
	p := AnimationPolicy{RolloutPercent: intPtr(0)}
	c := p.Resolve(id, GateConfig{DeployEnabled: true, DefaultPercent: 100})
	if c.Enabled || c.Reason != ReasonRolloutMiss {
		t.Fatalf("显式 0%% 不应回落到部署默认：%+v", c)
	}
}

func TestResolveEmptyTenantIsDenied(t *testing.T) {
	c := AnimationPolicy{Enabled: boolPtr(true)}.Resolve("  ", GateConfig{DeployEnabled: true, DefaultPercent: 100})
	if c.Enabled {
		t.Fatalf("空租户 ID 不应被分桶放行：%+v", c)
	}
	if c.Reason != ReasonUnknownTenant || c.Bucket != -1 {
		t.Fatalf("空租户的原因/桶号不符：%+v", c)
	}
}

func TestResolveClampsPercent(t *testing.T) {
	id := tenantWithBucket(t, 0, 100)
	if c := (AnimationPolicy{}).Resolve(id, GateConfig{DeployEnabled: true, DefaultPercent: 500}); !c.Enabled {
		t.Fatalf("超过 100 的百分比应夹到 100：%+v", c)
	}
	if c := (AnimationPolicy{}).Resolve(id, GateConfig{DeployEnabled: true, DefaultPercent: -5}); c.Enabled {
		t.Fatalf("负数百分比应夹到 0：%+v", c)
	}
}

func TestBucketIsStableAndBounded(t *testing.T) {
	first := Bucket("t_000123", "salt-1")
	for i := 0; i < 1000; i++ {
		got := Bucket("t_000123", "salt-1")
		if got != first {
			t.Fatalf("第 %d 次分桶漂移：%d != %d", i, got, first)
		}
	}
	if first < 0 || first >= 100 {
		t.Fatalf("桶号越界：%d", first)
	}
}

func TestBucketSaltSeparates(t *testing.T) {
	// 没有 \x00 分隔时 salt="ab"+id="c" 与 salt="a"+id="bc" 会撞。
	if Bucket("c", "ab") == Bucket("bc", "a") {
		t.Fatal("不同 salt/id 组合撞桶，说明分隔符失效")
	}
}

func TestBucketDistributionIsEven(t *testing.T) {
	const n = 10000
	bins := make([]int, 10)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t_%06d", i)
		bins[Bucket(id, "")/10]++
	}
	for i, count := range bins {
		share := float64(count) / float64(n) * 100
		if share < 7 || share > 13 {
			t.Fatalf("第 %d 个十分位占比 %.2f%%，超出 7%%–13%%（%v）", i, share, bins)
		}
	}
}

// tenantWithBucket 找一个桶号落在 [lo, hi) 的租户 ID。
func tenantWithBucket(t *testing.T, lo, hi int) string {
	t.Helper()
	for i := 0; i < 100000; i++ {
		id := fmt.Sprintf("t_%06d", i)
		if b := Bucket(id, ""); b >= lo && b < hi {
			return id
		}
	}
	t.Fatalf("找不到桶号在 [%d,%d) 的租户", lo, hi)
	return ""
}
