package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/tenant"
)

// 本文件守护 M4 的三件事：
//
//  1. 能力判定收敛到一处：写规格与导出选项不能得出两个答案；
//  2. 策略读失败与「没配过」必须区分（fail closed，但原因不同）；
//  3. 关闭是入口关闭，不是任务中止——已有规格仍可读。

type fakePolicyReader struct {
	policy *tenant.Policy
	err    error
}

func (f fakePolicyReader) GetPolicy(context.Context, string) (*tenant.Policy, error) {
	return f.policy, f.err
}

func TestTenantAnimationGateResolution(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name        string
		reader      TenantPolicyReader
		cfg         animation.GateConfig
		wantEnabled bool
		wantSource  string
		wantReason  string
	}{
		{
			name:        "explicit true bypasses zero rollout",
			reader:      fakePolicyReader{policy: &tenant.Policy{Animation: &animation.AnimationPolicy{Enabled: &yes}}},
			cfg:         animation.GateConfig{DeployEnabled: true, DefaultPercent: 0},
			wantEnabled: true, wantSource: animation.SourceTenant,
		},
		{
			name:       "explicit false beats full rollout",
			reader:     fakePolicyReader{policy: &tenant.Policy{Animation: &animation.AnimationPolicy{Enabled: &no}}},
			cfg:        animation.GateConfig{DeployEnabled: true, DefaultPercent: 100},
			wantSource: animation.SourceTenant, wantReason: animation.ReasonTenantDisabled,
		},
		{
			// 没有策略行（本地 profile / 新建租户）是「没配过」，不是「读失败」。
			name:        "missing tenant row falls back to default",
			reader:      fakePolicyReader{err: tenant.ErrTenantNotFound},
			cfg:         animation.GateConfig{DeployEnabled: true, DefaultPercent: 100},
			wantEnabled: true, wantSource: animation.SourceDefault,
		},
		{
			name:       "policy read failure fails closed with its own reason",
			reader:     fakePolicyReader{err: errors.New("db down")},
			cfg:        animation.GateConfig{DeployEnabled: true, DefaultPercent: 100},
			wantSource: animation.SourcePolicy, wantReason: animation.ReasonPolicyUnreadable,
		},
		{
			name:       "deploy gate wins over tenant policy",
			reader:     fakePolicyReader{policy: &tenant.Policy{Animation: &animation.AnimationPolicy{Enabled: &yes}}},
			cfg:        animation.GateConfig{DeployEnabled: false, DefaultPercent: 100},
			wantSource: animation.SourceDeploy, wantReason: animation.ReasonDeployDisabled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := newTenantAnimationGate(tc.reader, tc.cfg)
			c := gate.Resolve(context.Background(), "tenant-1")
			if c.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v (%+v)", c.Enabled, tc.wantEnabled, c)
			}
			if c.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q", c.Source, tc.wantSource)
			}
			if c.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", c.Reason, tc.wantReason)
			}
		})
	}
}

// mutableGate 让同一个 server 在两次请求之间切换能力，用来验证「关了写、还能读」。
type mutableGate struct{ cap animation.Capability }

func (g *mutableGate) Resolve(context.Context, string) animation.Capability { return g.cap }

func TestAnimationGateControlsWriteButNotRead(t *testing.T) {
	store := newAnimationTestStore(t)
	page := animationTestPage()
	resolver := &fakeAnimationResolver{
		page: page,
		pc: animation.PageContext{
			DurationMS: 6000,
			FPS:        30,
			Elements:   animdefault.PageAnchors(page),
			SegmentIDs: []string{"seg-1"},
		},
	}
	gate := &mutableGate{cap: animation.Capability{Enabled: true}}
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	NewAnimationHandlerWithGate(store, resolver, gate).Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// 开：写一份规格，作为「关闭后仍可读」的材料。
	if status := putAnimation(t, ts.URL, validPutBody(0)); status != http.StatusOK {
		t.Fatalf("open gate: PUT = %d, want 200", status)
	}

	gate.cap = animation.Capability{Enabled: false, Source: animation.SourceTenant, Reason: animation.ReasonTenantDisabled}

	if status := putAnimation(t, ts.URL, validPutBody(1)); status != http.StatusNotFound {
		t.Fatalf("closed gate: PUT = %d, want 404（不暴露能力存在）", status)
	}
	if status := getAnimation(t, ts.URL, animationURL()); status != http.StatusOK {
		t.Fatalf("closed gate: GET = %d, want 200（读路径必须仍可用）", status)
	}
	if status := getAnimation(t, ts.URL, animationURL()+"/anchors"); status != http.StatusOK {
		t.Fatalf("closed gate: anchors = %d, want 200（编辑器要能解释为什么不能改）", status)
	}
}

func TestAnimationOptionsReportsTenantCapabilities(t *testing.T) {
	no := false
	opts := Options{DevHeaders: true, Animation: newAnimationTestStore(t), AnimationEnabled: true}
	opts.AnimationProbe = &stubProbe{}
	opts.Policy = fakePolicyReader{policy: &tenant.Policy{
		Animation: &animation.AnimationPolicy{Enabled: &no},
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, opts))
	t.Cleanup(server.Close)

	resp := getAnimationOptions(t, server.URL, "")
	if resp.Enabled {
		t.Fatal("租户被关闭时导出选项必须不可用")
	}
	if resp.Reason != "animation_disabled_by_tenant" {
		t.Fatalf("reason = %q, want animation_disabled_by_tenant", resp.Reason)
	}
	if resp.Capabilities == nil {
		t.Fatal("响应必须带能力快照，否则前端只能显示冷冰冰的一句不可用")
	}
	c := resp.Capabilities
	if c.Enabled || c.TrackEditor || c.Preview {
		t.Fatalf("capabilities = %+v，关闭时子能力必须全灭", c)
	}
	if c.Source != animation.SourceTenant || c.Reason != animation.ReasonTenantDisabled {
		t.Fatalf("capabilities 来源/原因不符：%+v", c)
	}
	if c.Bucket < 0 || c.Bucket >= 100 {
		t.Fatalf("bucket = %d，应为 [0,100)", c.Bucket)
	}
}

func TestAnimationOptionsCapabilitiesExposeTrackEditor(t *testing.T) {
	yes := true
	opts := Options{DevHeaders: true, Animation: newAnimationTestStore(t), AnimationEnabled: true}
	opts.AnimationProbe = &stubProbe{}
	opts.Policy = fakePolicyReader{policy: &tenant.Policy{
		Animation: &animation.AnimationPolicy{Enabled: &yes, TrackEditor: &yes},
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, opts))
	t.Cleanup(server.Close)

	resp := getAnimationOptions(t, server.URL, "")
	if !resp.Enabled {
		t.Fatalf("显式开放应可用：%+v", resp.Capabilities)
	}
	if resp.Capabilities == nil || !resp.Capabilities.TrackEditor {
		t.Fatal("track_editor 显式开启时 capabilities.trackEditor 必须为真")
	}
	if !resp.Capabilities.Preview {
		t.Fatal("preview 未设置时应随总开关为真")
	}
}

func putAnimation(t *testing.T, baseURL string, body map[string]any) int {
	t.Helper()
	status, _ := doJSON(t, http.MethodPut, baseURL+animationURL(), body)
	return status
}

func getAnimation(t *testing.T, baseURL, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
