package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// fakeACLProjectStore 是一个满足 ProjectStore 接口、支持 ACL 过滤的假实现。
type fakeACLProjectStore struct {
	projects      map[string]*project.Project
	collaborators map[string]map[string]string
}

func newFakeACLProjectStore() *fakeACLProjectStore {
	return &fakeACLProjectStore{
		projects:      make(map[string]*project.Project),
		collaborators: make(map[string]map[string]string),
	}
}

func (s *fakeACLProjectStore) addProject(tenantID, owner, id, title string) {
	s.projects[id] = &project.Project{
		ID:        id,
		TenantID:  tenantID,
		OwnerUser: owner,
		Title:     title,
		Archived:  false,
	}
}

func (s *fakeACLProjectStore) addCollaborator(projectID, userID, role string) {
	if s.collaborators[projectID] == nil {
		s.collaborators[projectID] = make(map[string]string)
	}
	s.collaborators[projectID][userID] = role
}

func (s *fakeACLProjectStore) GetProject(_ context.Context, tenantID, userID, id string) (*project.Project, error) {
	p, ok := s.projects[id]
	if !ok || p.TenantID != tenantID {
		return nil, project.ErrProjectNotFound
	}
	// 与真实 store 的 SQL 语义一致：空 userID = 管理旁路（不过滤）。
	if userID != "" && p.OwnerUser != userID && s.collaborators[id][userID] == "" {
		return nil, project.ErrProjectNotFound
	}
	return p, nil
}

func (s *fakeACLProjectStore) ListProjects(_ context.Context, tenantID, userID, _ string, _ int) ([]*project.Project, string, error) {
	out := make([]*project.Project, 0)
	for _, p := range s.projects {
		if p.TenantID != tenantID || p.Archived {
			continue
		}
		if userID != "" && p.OwnerUser != userID && s.collaborators[p.ID][userID] == "" {
			continue
		}
		out = append(out, p)
	}
	return out, "", nil
}

func (s *fakeACLProjectStore) ArchiveProject(_ context.Context, tenantID, userID, id string) (*project.Project, error) {
	p, err := s.GetProject(context.Background(), tenantID, userID, id)
	if err != nil {
		return nil, err
	}
	p.Archived = true
	return p, nil
}

func (s *fakeACLProjectStore) CreateProject(_ context.Context, _, _, _ string) (*project.Project, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) CreateSourceRevision(_ context.Context, _ string, _ project.NewSourceRevision) (*project.SourceRevision, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) GetSourceRevision(_ context.Context, _, _ string, _ int) (*project.SourceRevision, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) ListSourceRevisions(_ context.Context, _, _ string) ([]*project.SourceRevision, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) UpdateSourceRevisionPageCount(_ context.Context, _ string, _ string, _ int, _ int) error { return nil }
func (s *fakeACLProjectStore) DeleteSourceRevision(_ context.Context, _ string, _ string, _ int) error                  { return nil }
func (s *fakeACLProjectStore) ListTags(_ context.Context, _ string) ([]*project.Tag, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) CreateTag(_ context.Context, _, _, _ string) (*project.Tag, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) RenameTag(_ context.Context, _, _, _, _ string) (*project.Tag, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) DeleteTag(_ context.Context, _, _ string) error    { return nil }
func (s *fakeACLProjectStore) AttachTag(_ context.Context, _, _, _ string) error { return nil }
func (s *fakeACLProjectStore) DetachTag(_ context.Context, _, _, _ string) error { return nil }
func (s *fakeACLProjectStore) ListFolders(_ context.Context, _ string) ([]*project.Folder, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) CreateFolder(_ context.Context, _, _, _, _ string) (*project.Folder, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) RenameFolder(_ context.Context, _, _, _ string) (*project.Folder, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) DeleteFolder(_ context.Context, _, _ string) error   { return nil }
func (s *fakeACLProjectStore) MoveProject(_ context.Context, _, _, _ string) error { return nil }
func (s *fakeACLProjectStore) ListProjectOrganization(_ context.Context, _, _ string) ([]*project.ProjectOrg, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) ListCollaborators(_ context.Context, _, _ string) ([]*project.Collaborator, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) InviteCollaborator(_ context.Context, _, _, _, _, _ string) (*project.Collaborator, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) UpdateCollaboratorRole(_ context.Context, _, _, _, _ string) (*project.Collaborator, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) RemoveCollaborator(_ context.Context, _, _, _ string) error { return nil }
func (s *fakeACLProjectStore) CreateShareLink(_ context.Context, _, _, _, _, _ string, _ *time.Time) (*project.ShareLink, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) ListShareLinks(_ context.Context, _, _ string) ([]*project.ShareLink, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) RevokeShareLink(_ context.Context, _, _ string) error { return nil }
func (s *fakeACLProjectStore) GetShareLinkByToken(_ context.Context, _ string) (*project.ShareLink, error) {
	return nil, nil
}
func (s *fakeACLProjectStore) ShareLinkPasswordHash(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (s *fakeACLProjectStore) TouchShareLinkAccess(_ context.Context, _, _ string) error { return nil }

func TestRequireProjectAccess_OwnerCanAccess(t *testing.T) {
	store := newFakeACLProjectStore()
	store.addProject("tenant-1", "user-owner", "proj-1", "My Project")
	store.addCollaborator("proj-1", "user-collab", "editor")

	tests := []struct {
		name       string
		userID     string
		wantStatus int
	}{
		{"owner", "user-owner", http.StatusOK},
		{"collaborator", "user-collab", http.StatusOK},
		{"unknown_user", "user-stranger", http.StatusNotFound},
		{"same_tenant_not_collab", "user-other", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 直接调 GetProject，绕开 ServeMux 的 pathValue 机制
			proj, err := store.GetProject(context.Background(), "tenant-1", tt.userID, "proj-1")
			if tt.wantStatus == http.StatusOK {
				if err != nil {
					t.Fatalf("expected access, got err=%v", err)
				}
				if proj == nil {
					t.Fatal("expected non-nil project")
				}
			} else {
				if err == nil {
					t.Fatalf("expected denial, got project=%s", proj.ID)
				}
				if proj != nil {
					t.Fatalf("expected nil project on denial")
				}
			}
		})
	}
}

// aclRoleReader 是固定角色的 membership.Reader 测试桩（方案 A 管理旁路测试用）。
type aclRoleReader struct {
	role membership.Role
	err  error
}

func (f *aclRoleReader) GetRole(context.Context, string, string) (membership.Role, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.role, nil
}

func (f *aclRoleReader) List(context.Context, string) ([]membership.Member, error) {
	return nil, nil
}

// 方案 A：租户 admin/owner 应获得管理旁路（override=true，userID 为空=不过滤）。
func TestProjectAccessUser_AdminOverride(t *testing.T) {
	cases := []struct {
		name         string
		role         membership.Role
		members      membership.Reader
		wantOverride bool
		wantUserID   string
	}{
		{"owner_override", membership.RoleOwner, &aclRoleReader{role: membership.RoleOwner}, true, ""},
		{"admin_override", membership.RoleAdmin, &aclRoleReader{role: membership.RoleAdmin}, true, ""},
		{"editor_no_override", membership.RoleEditor, &aclRoleReader{role: membership.RoleEditor}, false, "user-1"},
		{"viewer_no_override", membership.RoleViewer, &aclRoleReader{role: membership.RoleViewer}, false, "user-1"},
		{"nil_members_strict", "", nil, false, "user-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-1"})
			userID, override := projectAccessUser(ctx, tc.members)
			if override != tc.wantOverride {
				t.Fatalf("override: want %v got %v", tc.wantOverride, override)
			}
			if userID != tc.wantUserID {
				t.Fatalf("userID: want %q got %q", tc.wantUserID, userID)
			}
		})
	}
}

// 方案 A：admin 旁路可访问非协作者项目；editor 非协作者被拒。
func TestRequireProjectAccess_AdminOverrideHTTP(t *testing.T) {
	store := newFakeACLProjectStore()
	store.addProject("tenant-1", "user-owner", "proj-1", "My Project")

	cases := []struct {
		name       string
		role       membership.Role
		userID     string
		wantStatus int
	}{
		{"admin_sees_others_project", membership.RoleAdmin, "user-admin", http.StatusOK},
		{"owner_sees_others_project", membership.RoleOwner, "user-admin", http.StatusOK},
		{"editor_not_collab_denied", membership.RoleEditor, "user-other", http.StatusNotFound},
		{"owner_of_project_ok", membership.RoleEditor, "user-owner", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/projects/proj-1/artifacts", nil)
			req.SetPathValue("pid", "proj-1")
			req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{
				TenantID: "tenant-1", UserID: tc.userID,
			}))
			rec := httptest.NewRecorder()
			_, ok := requireProjectAccess(rec, req, store, &aclRoleReader{role: tc.role}, nil)
			if tc.wantStatus == http.StatusOK {
				if !ok {
					t.Fatalf("expected access, got status %d body=%s", rec.Code, rec.Body.String())
				}
			} else {
				if ok {
					t.Fatal("expected denial, got access")
				}
				if rec.Code != tc.wantStatus {
					t.Fatalf("expected %d, got %d", tc.wantStatus, rec.Code)
				}
			}
		})
	}
}

// 审计旁路：admin override 必须写出审计事件。
type recordingAudit struct {
	events []audit.Event
}

func (r *recordingAudit) Record(_ context.Context, e audit.Event) error {
	r.events = append(r.events, e)
	return nil
}

func TestRequireProjectAccess_AdminOverrideAudited(t *testing.T) {
	store := newFakeACLProjectStore()
	store.addProject("tenant-1", "user-owner", "proj-1", "My Project")
	rec := &recordingAudit{}

	req := httptest.NewRequest("GET", "/projects/proj-1/artifacts", nil)
	req.SetPathValue("pid", "proj-1")
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{
		TenantID: "tenant-1", UserID: "user-admin",
	}))
	w := httptest.NewRecorder()
	if _, ok := requireProjectAccess(w, req, store, &aclRoleReader{role: membership.RoleAdmin}, rec); !ok {
		t.Fatalf("admin should access, got %d", w.Code)
	}
	if len(rec.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(rec.events))
	}
	if rec.events[0].Action != "project.admin_override_access" || rec.events[0].ResourceID != "proj-1" {
		t.Fatalf("unexpected audit event: %+v", rec.events[0])
	}
}

// 非 admin 的合法协作者访问不应产生 override 审计。
func TestRequireProjectAccess_CollaboratorNoOverrideAudit(t *testing.T) {
	store := newFakeACLProjectStore()
	store.addProject("tenant-1", "user-owner", "proj-1", "My Project")
	store.addCollaborator("proj-1", "user-collab", "editor")
	rec := &recordingAudit{}

	req := httptest.NewRequest("GET", "/projects/proj-1/artifacts", nil)
	req.SetPathValue("pid", "proj-1")
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{
		TenantID: "tenant-1", UserID: "user-collab",
	}))
	w := httptest.NewRecorder()
	if _, ok := requireProjectAccess(w, req, store, &aclRoleReader{role: membership.RoleEditor}, rec); !ok {
		t.Fatalf("collaborator should access, got %d", w.Code)
	}
	if len(rec.events) != 0 {
		t.Fatalf("collaborator access must not be audited as override, got %d", len(rec.events))
	}
}

// 精度：owner 访问自己的项目不应记为管理越权（避免审计噪声）。
func TestRequireProjectAccess_OwnerSelfNoOverrideAudit(t *testing.T) {
	store := newFakeACLProjectStore()
	store.addProject("tenant-1", "user-owner", "proj-1", "My Project")
	rec := &recordingAudit{}

	req := httptest.NewRequest("GET", "/projects/proj-1/artifacts", nil)
	req.SetPathValue("pid", "proj-1")
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{
		TenantID: "tenant-1", UserID: "user-owner",
	}))
	w := httptest.NewRecorder()
	// 即使 tenant role 是 owner（≥admin），访问自己的项目也走严格路径，不记越权审计。
	if _, ok := requireProjectAccess(w, req, store, &aclRoleReader{role: membership.RoleOwner}, rec); !ok {
		t.Fatalf("owner should access, got %d", w.Code)
	}
	if len(rec.events) != 0 {
		t.Fatalf("owner accessing own project must not be audited as override, got %d", len(rec.events))
	}
}

// resolveProject：admin 非成员 → override=true；普通成员非协作者 → NotFound。
func TestResolveProject(t *testing.T) {
	store := newFakeACLProjectStore()
	store.addProject("tenant-1", "user-owner", "proj-1", "My Project")

	base := context.WithValue(context.Background(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-x"})

	// admin 非成员 → 旁路
	_, override, err := resolveProject(base, store, &aclRoleReader{role: membership.RoleAdmin}, "tenant-1", "user-x", "proj-1")
	if err != nil || !override {
		t.Fatalf("admin should override: err=%v override=%v", err, override)
	}
	// editor 非成员 → NotFound
	_, _, err = resolveProject(base, store, &aclRoleReader{role: membership.RoleEditor}, "tenant-1", "user-x", "proj-1")
	if !errors.Is(err, project.ErrProjectNotFound) {
		t.Fatalf("editor non-member should get NotFound, got %v", err)
	}
	// owner 本人 → 严格命中，无 override
	_, override, err = resolveProject(base, store, &aclRoleReader{role: membership.RoleEditor}, "tenant-1", "user-owner", "proj-1")
	if err != nil || override {
		t.Fatalf("owner should have direct access without override: err=%v override=%v", err, override)
	}
}
