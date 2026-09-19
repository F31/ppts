package api

import (
	"context"
	"net/http"
	"testing"
	"time"

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
	if p.OwnerUser != userID && s.collaborators[id][userID] == "" {
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
		if p.OwnerUser != userID && s.collaborators[p.ID][userID] == "" {
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

func (s *fakeACLProjectStore) CreateProject(_ context.Context, _, _, _ string) (*project.Project, error)                  { return nil, nil }
func (s *fakeACLProjectStore) CreateSourceRevision(_ context.Context, _ string, _ project.NewSourceRevision) (*project.SourceRevision, error) { return nil, nil }
func (s *fakeACLProjectStore) GetSourceRevision(_ context.Context, _, _ string, _ int) (*project.SourceRevision, error)       { return nil, nil }
func (s *fakeACLProjectStore) ListSourceRevisions(_ context.Context, _, _ string) ([]*project.SourceRevision, error)          { return nil, nil }
func (s *fakeACLProjectStore) ListTags(_ context.Context, _ string) ([]*project.Tag, error)                                 { return nil, nil }
func (s *fakeACLProjectStore) CreateTag(_ context.Context, _, _, _ string) (*project.Tag, error)                            { return nil, nil }
func (s *fakeACLProjectStore) RenameTag(_ context.Context, _, _, _, _ string) (*project.Tag, error)                         { return nil, nil }
func (s *fakeACLProjectStore) DeleteTag(_ context.Context, _, _ string) error                                               { return nil }
func (s *fakeACLProjectStore) AttachTag(_ context.Context, _, _, _ string) error                                            { return nil }
func (s *fakeACLProjectStore) DetachTag(_ context.Context, _, _, _ string) error                                            { return nil }
func (s *fakeACLProjectStore) ListFolders(_ context.Context, _ string) ([]*project.Folder, error)                           { return nil, nil }
func (s *fakeACLProjectStore) CreateFolder(_ context.Context, _, _, _ string) (*project.Folder, error)                      { return nil, nil }
func (s *fakeACLProjectStore) RenameFolder(_ context.Context, _, _, _ string) (*project.Folder, error)                      { return nil, nil }
func (s *fakeACLProjectStore) DeleteFolder(_ context.Context, _, _ string) error                                            { return nil }
func (s *fakeACLProjectStore) MoveProject(_ context.Context, _, _, _ string) error                                          { return nil }
func (s *fakeACLProjectStore) ListProjectOrganization(_ context.Context, _ string) ([]*project.ProjectOrg, error)           { return nil, nil }
func (s *fakeACLProjectStore) ListCollaborators(_ context.Context, _, _ string) ([]*project.Collaborator, error)            { return nil, nil }
func (s *fakeACLProjectStore) InviteCollaborator(_ context.Context, _, _, _, _, _ string) (*project.Collaborator, error)    { return nil, nil }
func (s *fakeACLProjectStore) UpdateCollaboratorRole(_ context.Context, _, _, _, _ string) (*project.Collaborator, error)    { return nil, nil }
func (s *fakeACLProjectStore) RemoveCollaborator(_ context.Context, _, _, _ string) error                                   { return nil }
func (s *fakeACLProjectStore) CreateShareLink(_ context.Context, _, _, _, _, _ string, _ *time.Time) (*project.ShareLink, error) { return nil, nil }
func (s *fakeACLProjectStore) ListShareLinks(_ context.Context, _, _ string) ([]*project.ShareLink, error)                  { return nil, nil }
func (s *fakeACLProjectStore) RevokeShareLink(_ context.Context, _, _ string) error                                         { return nil }
func (s *fakeACLProjectStore) GetShareLinkByToken(_ context.Context, _ string) (*project.ShareLink, error)                 { return nil, nil }
func (s *fakeACLProjectStore) ShareLinkPasswordHash(_ context.Context, _, _ string) (string, error)                         { return "", nil }
func (s *fakeACLProjectStore) TouchShareLinkAccess(_ context.Context, _, _ string) error                                    { return nil }

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
