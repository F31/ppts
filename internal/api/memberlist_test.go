package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/membership"
)

// tenantScopedMemberStore 是 membership.Store 的假实现，**按真实语义实现租户归属**：
// 目标 userID 必须出现在该租户的成员表中，SaveProfile 才写入，否则返回 ErrNotFound。
//
// 这不是洁癖：如果假实现无条件返回 nil，那么"跨租户写入被拒"的用例会连同它们要守护的
// 缺陷一起通过——测试通过与否将不再说明任何问题（假成功的一种，见 A26）。
type tenantScopedMemberStore struct {
	members map[string]map[string]membership.Role // tenantID -> userID -> role
	saved   map[string]membership.Profile         // userID -> 已写入的档案
	savedBy string                                // 最近一次 SaveProfile 的调用方租户
}

func newTenantScopedMemberStore() *tenantScopedMemberStore {
	return &tenantScopedMemberStore{
		members: map[string]map[string]membership.Role{},
		saved:   map[string]membership.Profile{},
	}
}

func (s *tenantScopedMemberStore) addMember(tenantID, userID string, role membership.Role) {
	if s.members[tenantID] == nil {
		s.members[tenantID] = map[string]membership.Role{}
	}
	s.members[tenantID][userID] = role
}

func (s *tenantScopedMemberStore) GetRole(_ context.Context, tenantID, userID string) (membership.Role, error) {
	r, ok := s.members[tenantID][userID]
	if !ok {
		return "", membership.ErrNotFound
	}
	return r, nil
}

func (s *tenantScopedMemberStore) List(_ context.Context, tenantID string) ([]membership.Member, error) {
	var out []membership.Member
	for userID, role := range s.members[tenantID] {
		out = append(out, membership.Member{UserID: userID, Role: role})
	}
	return out, nil
}

func (s *tenantScopedMemberStore) SetRole(_ context.Context, tenantID, userID string, role membership.Role) error {
	s.addMember(tenantID, userID, role)
	return nil
}

func (s *tenantScopedMemberStore) Remove(_ context.Context, tenantID, userID string) error {
	if _, ok := s.members[tenantID][userID]; !ok {
		return membership.ErrNotFound
	}
	delete(s.members[tenantID], userID)
	return nil
}

// SaveProfile 复刻生产实现的归属约束：只在本租户成员表命中时才写入。
func (s *tenantScopedMemberStore) SaveProfile(_ context.Context, tenantID, userID string, p membership.Profile) error {
	if tenantID == "" || userID == "" {
		return membership.ErrNotFound
	}
	if _, ok := s.members[tenantID][userID]; !ok {
		return membership.ErrNotFound
	}
	s.savedBy = tenantID
	s.saved[userID] = p
	return nil
}

func newMemberRouteServer(t *testing.T, members membership.Store) *httptest.Server {
	t.Helper()
	// DevHeaders 只是把身份头变成可信输入；本文件要验证的是"有了身份之后还能不能越权"。
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{DevHeaders: true, Members: members}))
	t.Cleanup(server.Close)
	return server
}

func putMemberProfile(t *testing.T, server *httptest.Server, tenant, actor, target, fullName string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, server.URL+"/members/"+target,
		strings.NewReader(`{"full_name":"`+fullName+`"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenantHeader, tenant)
	req.Header.Set(userHeader, actor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put member profile: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

// TestMemberProfileUpdateRejectsCrossTenantTarget 守护 R4：PUT /members/{userId} 的目标
// 必须属于调用方租户。
//
// user_profiles 无租户列且不启用 RLS，旧实现 `SaveProfile(ctx, userID, p)` 甚至连 tenantID
// 参数都没有——签名本身让调用方无从约束归属，于是任一租户的 Admin 只要知道目标 userID，
// 就能改写其他租户用户的姓名/电话等档案。修复方式是把 tenantID 变成强制参数并在写入前
// 校验归属；本用例即该修复的回归门禁（旧实现在此会得到 200）。
func TestMemberProfileUpdateRejectsCrossTenantTarget(t *testing.T) {
	members := newTenantScopedMemberStore()
	members.addMember("tenant-1", "user-1", membership.RoleAdmin)  // 调用方：本租户管理员
	members.addMember("tenant-2", "victim", membership.RoleViewer) // 受害者属于另一租户
	server := newMemberRouteServer(t, members)

	resp, body := putMemberProfile(t, server, "tenant-1", "user-1", "victim", "被越权改写")
	if resp.StatusCode != http.StatusForbidden && connectErrorCode(body) != "permission_denied" {
		t.Fatalf("跨租户写入档案未被拒绝：status=%d body=%v", resp.StatusCode, body)
	}
	if _, ok := members.saved["victim"]; ok {
		t.Fatalf("跨租户档案写入竟然落库了：%#v", members.saved["victim"])
	}
	if members.savedBy != "" {
		t.Fatalf("SaveProfile 被调用，说明归属校验没有拦住：savedBy=%s", members.savedBy)
	}
}

// TestMemberProfileUpdateAllowsSameTenantAdmin 是上一条的对照：同租户 Admin 改写本租户成员
// 档案必须照常成功。只写"拒绝"测试会掩盖把功能一起封死的回归。
func TestMemberProfileUpdateAllowsSameTenantAdmin(t *testing.T) {
	members := newTenantScopedMemberStore()
	members.addMember("tenant-1", "user-1", membership.RoleAdmin)
	members.addMember("tenant-1", "user-2", membership.RoleViewer)
	server := newMemberRouteServer(t, members)

	resp, body := putMemberProfile(t, server, "tenant-1", "user-1", "user-2", "张三")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("同租户管理员写入档案应成功：status=%d body=%v", resp.StatusCode, body)
	}
	if members.savedBy != "tenant-1" {
		t.Fatalf("SaveProfile 必须带上调用方租户：savedBy=%q", members.savedBy)
	}
	if got := members.saved["user-2"].FullName; got != "张三" {
		t.Fatalf("档案未落库：full_name=%q", got)
	}
}

// TestMemberProfileUpdateRejectsNonAdmin 守护 admin 级门槛不会因为加了归属校验而丢失。
func TestMemberProfileUpdateRejectsNonAdmin(t *testing.T) {
	members := newTenantScopedMemberStore()
	members.addMember("tenant-1", "user-1", membership.RoleViewer)
	members.addMember("tenant-1", "user-2", membership.RoleViewer)
	server := newMemberRouteServer(t, members)

	resp, body := putMemberProfile(t, server, "tenant-1", "user-1", "user-2", "李四")
	if resp.StatusCode != http.StatusForbidden && connectErrorCode(body) != "permission_denied" {
		t.Fatalf("viewer 不应能改档案：status=%d body=%v", resp.StatusCode, body)
	}
	if _, ok := members.saved["user-2"]; ok {
		t.Fatalf("越权写入落库：%#v", members.saved["user-2"])
	}
}

// connectErrorCode 从 JSON 错误体里取 Connect 错误码字符串。
func connectErrorCode(body map[string]any) string {
	if body == nil {
		return ""
	}
	code, _ := body["code"].(string)
	return code
}
