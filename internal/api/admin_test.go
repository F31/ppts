package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/F31/ppts/internal/tenant"
)

type adminTenantStub struct {
	suspended string
	resumed   string
	list      []tenant.Summary
}

func (s *adminTenantStub) ListTenants(_ context.Context, _ int) ([]tenant.Summary, error) {
	return s.list, nil
}
func (s *adminTenantStub) Suspend(_ context.Context, id string) error { s.suspended = id; return nil }
func (s *adminTenantStub) Resume(_ context.Context, id string) error  { s.resumed = id; return nil }

func TestAdminListTenants(t *testing.T) {
	stub := &adminTenantStub{list: []tenant.Summary{
		{ID: "t1", Name: "Acme", Type: "organization", Status: tenant.StatusSuspended, CreatedAt: time.Unix(1, 0)},
	}}
	d := &adminDeps{operatorIDs: map[string]bool{"op": true}, tenants: stub}
	req := httptest.NewRequest(http.MethodGet, "/admin/tenants", nil)
	rr := httptest.NewRecorder()
	d.listTenants(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out struct {
		Tenants []map[string]any `json:"tenants"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tenants) != 1 || out.Tenants[0]["id"] != "t1" || out.Tenants[0]["status"] != "suspended" {
		t.Fatalf("tenants = %+v", out.Tenants)
	}
}

func TestAdminSuspendResume(t *testing.T) {
	stub := &adminTenantStub{}
	d := &adminDeps{operatorIDs: map[string]bool{"op": true}, tenants: stub}

	req := httptest.NewRequest(http.MethodPost, "/admin/tenants/t9/suspend", nil)
	req.SetPathValue("id", "t9")
	rr := httptest.NewRecorder()
	d.suspendTenant(rr, req)
	if rr.Code != http.StatusOK || stub.suspended != "t9" {
		t.Fatalf("suspend status=%d id=%q", rr.Code, stub.suspended)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/admin/tenants/t9/resume", nil)
	req2.SetPathValue("id", "t9")
	rr2 := httptest.NewRecorder()
	d.resumeTenant(rr2, req2)
	if rr2.Code != http.StatusOK || stub.resumed != "t9" {
		t.Fatalf("resume status=%d id=%q", rr2.Code, stub.resumed)
	}
}

func TestAuthSettingsFromEnvRegistration(t *testing.T) {
	t.Setenv("PPTS_REGISTRATION_ENABLED", "false")
	t.Setenv("PPTS_REGISTRATION_INVITE_CODE", "letmein")
	t.Setenv("PPTS_REGISTER_DAILY_PER_IP", "2")
	t.Setenv("PPTS_DEFAULT_GEN_SECONDS_LIMIT", "120")
	t.Setenv("PPTS_OPERATOR_USER_IDS", "u1, u2 ,")
	s := authSettingsFromEnv("v1")
	if s.registrationEnabled {
		t.Fatal("registration should be disabled")
	}
	if s.inviteCode != "letmein" {
		t.Fatalf("invite = %q", s.inviteCode)
	}
	if s.registerDailyPerIP != 2 {
		t.Fatalf("daily = %d", s.registerDailyPerIP)
	}
	if s.defaultGenSeconds != 120 {
		t.Fatalf("default quota = %v", s.defaultGenSeconds)
	}
	if !s.isOperator("u1") || !s.isOperator("u2") || s.isOperator("u3") {
		t.Fatalf("operators = %+v", s.operatorIDs)
	}
}

func TestAuthSettingsDefaults(t *testing.T) {
	// 清空相关 env（t.Setenv 会在用例结束后恢复）。
	for _, k := range []string{"PPTS_REGISTRATION_ENABLED", "PPTS_REGISTRATION_INVITE_CODE", "PPTS_REGISTER_DAILY_PER_IP", "PPTS_DEFAULT_GEN_SECONDS_LIMIT", "PPTS_OPERATOR_USER_IDS"} {
		t.Setenv(k, "")
	}
	s := authSettingsFromEnv("")
	if !s.registrationEnabled {
		t.Fatal("registration should default enabled")
	}
	if s.inviteCode != "" {
		t.Fatalf("invite should default empty: %q", s.inviteCode)
	}
	if s.registerDailyPerIP != 20 || s.defaultGenSeconds != 600 {
		t.Fatalf("defaults = daily:%d gen:%v", s.registerDailyPerIP, s.defaultGenSeconds)
	}
	if len(s.operatorIDs) != 0 {
		t.Fatalf("operators should default empty: %+v", s.operatorIDs)
	}
}
