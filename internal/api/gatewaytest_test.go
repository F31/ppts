package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/membership"
)

// recordingGatewayStore 只加观察点：记录探活实际读的是哪一层租户，并可注入解密失败。
type recordingGatewayStore struct {
	*fakeGatewayStore
	resolvedTenants []string
	resolveErr      error
}

func (s *recordingGatewayStore) ResolveNamed(ctx context.Context, tenantID, name string, kind gateway.Kind) (*gateway.Config, error) {
	s.resolvedTenants = append(s.resolvedTenants, tenantID)
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return s.fakeGatewayStore.ResolveNamed(ctx, tenantID, name, kind)
}

func newGatewayTestStore(t *testing.T) *recordingGatewayStore {
	t.Helper()
	s := &recordingGatewayStore{fakeGatewayStore: newFakeGatewayStore()}
	// 平台共享行：租户列表会合并显示，但写入路径只认本租户 (kind,name)。
	if err := s.Create(context.Background(), &gateway.Gateway{
		TenantID: gateway.PlatformTenantID, Name: "plat-llm", Kind: gateway.KindLLM,
		Provider: "openai_compatible", Model: "probe-model", IsDefault: true, Enabled: true,
	}, "sk-platform"); err != nil {
		t.Fatalf("create platform row: %v", err)
	}
	return s
}

// 平台共享行对租户是只读，但「不能改」不等于「不能验证」。修复前租户作用域点测试
// 只会在本租户查 (kind,name)，必然 404，于是界面干脆把按钮藏了。
func TestGatewayTestPlatformSharedInTenantScope(t *testing.T) {
	store := newGatewayTestStore(t)
	h := gwHandler(t, store, membership.RoleAdmin)

	rec := gwDo(t, h, "POST", "/api/model-gateways/plat-llm/test?kind=llm&source=platform", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("test platform row = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.resolvedTenants) != 1 || store.resolvedTenants[0] != gateway.PlatformTenantID {
		t.Fatalf("探活读了 %v，应为平台租户", store.resolvedTenants)
	}
	// 探活本身用不可达 BaseURL，结果 ok=false 是预期的；这里只保证返回结构完整。
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["ok"]; !ok {
		t.Fatalf("响应缺 ok 字段: %s", rec.Body.String())
	}
}

func TestGatewayTestPlatformSharedRejectsInvisibleRows(t *testing.T) {
	cases := []struct {
		name          string
		path          string
		shadow        bool
		wantStatus    int
		expectResolve bool
	}{
		{"平台不存在的名字", "/api/model-gateways/ghost/test?kind=llm&source=platform", false, http.StatusNotFound, false},
		{"本租户同名行影子掉平台行", "/api/model-gateways/plat-llm/test?kind=llm&source=platform", true, http.StatusConflict, false},
		// 不带 source 是合法请求，只是按本租户查必然 404；它会走到 ResolveNamed。
		{"未声明 source 时保持原语义", "/api/model-gateways/plat-llm/test?kind=llm", false, http.StatusNotFound, true},
		{"非法 source", "/api/model-gateways/plat-llm/test?kind=llm&source=other", false, http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newGatewayTestStore(t)
			if tc.shadow {
				if err := store.Create(context.Background(), &gateway.Gateway{
					TenantID: "00000000-0000-0000-0000-000000000001", Name: "plat-llm", Kind: gateway.KindLLM,
					Provider: "openai_compatible", Model: "own-model", Enabled: true,
				}, "sk-own"); err != nil {
					t.Fatalf("create tenant row: %v", err)
				}
			}
			h := gwHandler(t, store, membership.RoleAdmin)
			rec := gwDo(t, h, "POST", tc.path, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !tc.expectResolve && len(store.resolvedTenants) != 0 {
				t.Fatalf("被拒绝的请求不应触发探活，却解析了 %v", store.resolvedTenants)
			}
			if tc.expectResolve && (len(store.resolvedTenants) != 1 || store.resolvedTenants[0] != "00000000-0000-0000-0000-000000000001") {
				t.Fatalf("未声明 source 应只读本租户，实际解析了 %v", store.resolvedTenants)
			}
		})
	}
}

// 密钥不匹配时不能伪装成 404「找不到网关」，管理员需要知道该修密钥而不是查名字。
func TestGatewayTestReportsUnreadableCreds(t *testing.T) {
	store := newGatewayTestStore(t)
	store.resolveErr = errors.New("gateway: decrypt creds: cipher: message authentication failed")
	h := gwHandler(t, store, membership.RoleAdmin)

	rec := gwDo(t, h, "POST", "/api/model-gateways/plat-llm/test?kind=llm&source=platform", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "credential_unreadable" {
		t.Fatalf("code = %q want credential_unreadable, body=%s", resp.Code, rec.Body.String())
	}
}
