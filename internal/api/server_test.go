package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/upload"
	"github.com/F31/ppts/internal/usage"
)

type fakeScriptStore struct {
	revision     *narration.Revision
	lastTenantID string
	lastLanguage string
}

type fakeProjectStore struct {
	projects      []*project.Project
	createdTenant string
	createdOwner  string
	nextRev       int
}

func (s *fakeProjectStore) CreateProject(_ context.Context, tenantID, owner, title string) (*project.Project, error) {
	p := &project.Project{ID: "project-new", TenantID: tenantID, OwnerUser: owner, Title: title, CreatedAt: time.Unix(100, 0)}
	s.createdTenant, s.createdOwner = tenantID, owner
	s.projects = append([]*project.Project{p}, s.projects...)
	return p, nil
}

func (s *fakeProjectStore) GetProject(_ context.Context, tenantID, id string) (*project.Project, error) {
	for _, p := range s.projects {
		if p.TenantID == tenantID && p.ID == id {
			return p, nil
		}
	}
	return nil, project.ErrProjectNotFound
}

func (s *fakeProjectStore) ListProjects(context.Context, string, string, int) ([]*project.Project, string, error) {
	return s.projects, "", nil
}

func (s *fakeProjectStore) ArchiveProject(_ context.Context, tenantID, id string) (*project.Project, error) {
	p, err := s.GetProject(context.Background(), tenantID, id)
	if err != nil {
		return nil, err
	}
	p.Archived = true
	return p, nil
}

func (s *fakeProjectStore) CreateSourceRevision(ctx context.Context, tenantID string, in project.NewSourceRevision) (*project.SourceRevision, error) {
	if _, err := s.GetProject(ctx, tenantID, in.ProjectID); err != nil {
		return nil, err
	}
	s.nextRev++
	return &project.SourceRevision{
		ID: "rev-" + itoa(s.nextRev), ProjectID: in.ProjectID, TenantID: tenantID,
		RevisionNo: s.nextRev, SourceHash: in.SourceHash, ObjectKey: in.ObjectKey,
		ParserVersion: in.ParserVersion, CreatedAt: time.Unix(int64(s.nextRev), 0),
	}, nil
}

func (*fakeProjectStore) GetSourceRevision(context.Context, string, string, int) (*project.SourceRevision, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) ListSourceRevisions(context.Context, string, string) ([]*project.SourceRevision, error) {
	return nil, nil
}

// ---- #94 标签+分组体系：测试桩（未使用，返回零值以满足 project.Store 接口） ----
func (*fakeProjectStore) ListTags(context.Context, string) ([]*project.Tag, error) {
	return nil, nil
}
func (*fakeProjectStore) CreateTag(context.Context, string, string, string) (*project.Tag, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) RenameTag(context.Context, string, string, string, string) (*project.Tag, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) DeleteTag(context.Context, string, string) error {
	return errors.New("not used")
}
func (*fakeProjectStore) AttachTag(context.Context, string, string, string) error {
	return errors.New("not used")
}
func (*fakeProjectStore) DetachTag(context.Context, string, string, string) error {
	return errors.New("not used")
}
func (*fakeProjectStore) ListFolders(context.Context, string) ([]*project.Folder, error) {
	return nil, nil
}
func (*fakeProjectStore) CreateFolder(context.Context, string, string, string) (*project.Folder, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) RenameFolder(context.Context, string, string, string) (*project.Folder, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) DeleteFolder(context.Context, string, string) error {
	return errors.New("not used")
}
func (*fakeProjectStore) MoveProject(context.Context, string, string, string) error {
	return errors.New("not used")
}
func (*fakeProjectStore) ListProjectOrganization(context.Context, string) ([]*project.ProjectOrg, error) {
	return nil, nil
}

// ---- #95 私密分享与协作者：测试桩未覆盖这些端点，统一返回"未使用" ----
func (*fakeProjectStore) ListCollaborators(context.Context, string, string) ([]*project.Collaborator, error) {
	return nil, nil
}
func (*fakeProjectStore) InviteCollaborator(context.Context, string, string, string, string, string) (*project.Collaborator, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) UpdateCollaboratorRole(context.Context, string, string, string, string) (*project.Collaborator, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) RemoveCollaborator(context.Context, string, string, string) error {
	return errors.New("not used")
}
func (*fakeProjectStore) CreateShareLink(context.Context, string, string, string, string, string, *time.Time) (*project.ShareLink, error) {
	return nil, errors.New("not used")
}
func (*fakeProjectStore) ListShareLinks(context.Context, string, string) ([]*project.ShareLink, error) {
	return nil, nil
}
func (*fakeProjectStore) RevokeShareLink(context.Context, string, string) error {
	return errors.New("not used")
}
func (*fakeProjectStore) GetShareLinkByToken(context.Context, string) (*project.ShareLink, error) {
	return nil, project.ErrShareLinkNotFound
}
func (*fakeProjectStore) ShareLinkPasswordHash(context.Context, string, string) (string, error) {
	return "", project.ErrShareLinkNotFound
}
func (*fakeProjectStore) TouchShareLinkAccess(context.Context, string, string) error {
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

type fakeUploadStore struct {
	sessions map[string]*upload.UploadSession
}

func newFakeUploadStore() *fakeUploadStore {
	return &fakeUploadStore{sessions: map[string]*upload.UploadSession{}}
}

func (s *fakeUploadStore) Create(_ context.Context, in upload.NewUpload) (*upload.UploadSession, error) {
	now := time.Unix(1, 0)
	ses := &upload.UploadSession{
		ID: in.ID, TenantID: in.TenantID, ProjectID: in.ProjectID,
		Filename: in.Filename, ContentType: in.ContentType, ObjectKey: in.ObjectKey,
		SizeBytes: in.SizeBytes, DeleteSourceAfter: in.DeleteSourceAfter,
		State: upload.StatePending, CreatedAt: now, UpdatedAt: now,
	}
	s.sessions[in.ID] = ses
	return ses, nil
}

func (s *fakeUploadStore) Get(_ context.Context, tenantID, uploadID string) (*upload.UploadSession, error) {
	ses, ok := s.sessions[uploadID]
	if !ok || ses.TenantID != tenantID {
		return nil, upload.ErrNotFound
	}
	return ses, nil
}

func (s *fakeUploadStore) Complete(_ context.Context, tenantID, uploadID, sourceRevisionID, jobID string) (*upload.UploadSession, error) {
	ses, err := s.Get(context.Background(), tenantID, uploadID)
	if err != nil {
		return nil, err
	}
	switch ses.State {
	case upload.StateCompleted:
		return ses, nil
	case upload.StateAborted:
		return nil, upload.ErrAlreadyAborted
	}
	ses.State = upload.StateCompleted
	ses.SourceRevisionID, ses.JobID = sourceRevisionID, jobID
	return ses, nil
}

func (s *fakeUploadStore) Abort(_ context.Context, tenantID, uploadID string) (*upload.UploadSession, error) {
	ses, err := s.Get(context.Background(), tenantID, uploadID)
	if err != nil {
		return nil, err
	}
	switch ses.State {
	case upload.StateAborted:
		return ses, nil
	case upload.StateCompleted:
		return nil, upload.ErrAlreadyCompleted
	}
	ses.State = upload.StateAborted
	return ses, nil
}

type jobCreatorStub struct {
	job            *pipeline.Job
	err            error
	tenantID       string
	projectID      string
	idempotencyKey string
	inputSnapshot  string
	listJobs       []*pipeline.Job
	listNext       string
	activeCount    int
	byIdem         *pipeline.Job
	byIdemErr      error
	watchEvents    []pipeline.JobEvent
}

type fakeArtifactStore struct {
	artifact *artifact.Artifact
}

type fakeAuditStore struct {
	events []audit.Event
	err    error
	filter audit.Filter
}

func (f *fakeAuditStore) Record(context.Context, audit.Event) error {
	return f.err
}

func (f *fakeAuditStore) List(_ context.Context, _ string, filter audit.Filter) ([]audit.Event, error) {
	f.filter = filter
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

func (f *fakeAuditStore) DeleteBefore(context.Context, string, time.Time) (int64, error) {
	return 0, f.err
}

type fakeTenantStatusChecker struct {
	active bool
	err    error
}

func (f fakeTenantStatusChecker) TenantActive(context.Context, string) (bool, error) {
	return f.active, f.err
}

func (s *fakeArtifactStore) Create(context.Context, string, artifact.NewArtifact) (*artifact.Artifact, error) {
	return nil, errors.New("not used")
}

func (s *fakeArtifactStore) Get(_ context.Context, tenantID, id string) (*artifact.Artifact, error) {
	if s.artifact == nil || s.artifact.ID != id || s.artifact.TenantID != tenantID {
		return nil, artifact.ErrNotFound
	}
	return s.artifact, nil
}

// ListByProject 对应 B3-M1 新增的产物列表能力（GET /projects/{pid}/artifacts）。
// 测试桩按租户 + 项目过滤单条产物，保持与 PGStore 一致的"无匹配即空列表"语义。
func (s *fakeArtifactStore) ListByProject(_ context.Context, tenantID, projectID string) ([]*artifact.Artifact, error) {
	if s.artifact == nil || s.artifact.TenantID != tenantID || s.artifact.ProjectID != projectID {
		return nil, nil
	}
	return []*artifact.Artifact{s.artifact}, nil
}

// ListAll 对应 B5-M2 跨项目成品库（artifact.Store 接口新增方法）。
// 桩按租户过滤单条产物；无匹配返回空列表（与 PGStore 语义一致）。
func (s *fakeArtifactStore) ListAll(_ context.Context, tenantID string) ([]*artifact.Artifact, error) {
	if s.artifact == nil || s.artifact.TenantID != tenantID {
		return nil, nil
	}
	return []*artifact.Artifact{s.artifact}, nil
}

func testObjects(t *testing.T) objectstore.ObjectStore {
	t.Helper()
	return objectstore.NewLocal(t.TempDir(), []byte("download-secret"))
}

func (s *jobCreatorStub) Create(_ context.Context, tenantID, projectID, _ string, idempotencyKey, snapshot string, _ time.Time) (*pipeline.Job, error) {
	s.tenantID, s.projectID = tenantID, projectID
	s.idempotencyKey, s.inputSnapshot = idempotencyKey, snapshot
	if s.err != nil {
		return nil, s.err
	}
	if s.job != nil {
		return s.job, nil
	}
	return &pipeline.Job{ID: "job-1", InputSnapshot: snapshot}, nil
}

func (s *jobCreatorStub) LatestSucceededJob(context.Context, string, string, string) (*pipeline.Job, error) {
	return nil, pipeline.ErrNoSucceededJob
}

func (s *jobCreatorStub) StepResultRef(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *jobCreatorStub) CountActive(context.Context, string) (int, error) {
	return s.activeCount, nil
}

func (s *jobCreatorStub) ByIdempotency(context.Context, string, string, string) (*pipeline.Job, error) {
	if s.byIdemErr != nil {
		return nil, s.byIdemErr
	}
	if s.byIdem == nil {
		return nil, pipeline.ErrJobNotFound
	}
	return s.byIdem, nil
}

func (s *jobCreatorStub) EventsSince(_ context.Context, _, _ string, afterSeq int64, _ int) ([]pipeline.JobEvent, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []pipeline.JobEvent
	for _, ev := range s.watchEvents {
		if ev.Seq > afterSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *jobCreatorStub) Get(context.Context, string, string) (*pipeline.Job, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.job == nil {
		return nil, pipeline.ErrJobNotFound
	}
	return s.job, nil
}

func (s *jobCreatorStub) List(context.Context, string, string, string, string, int) ([]*pipeline.Job, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return s.listJobs, s.listNext, nil
}

func (s *jobCreatorStub) Cancel(context.Context, string, string) (*pipeline.Job, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.job == nil {
		return nil, pipeline.ErrJobNotFound
	}
	return s.job, nil
}

func (s *jobCreatorStub) RetryFailed(context.Context, string, string) (*pipeline.Job, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.job == nil {
		return nil, pipeline.ErrJobNotFound
	}
	return s.job, nil
}

func (s *fakeScriptStore) Get(_ context.Context, tenantID, _, _, language string) (*narration.Revision, error) {
	s.lastTenantID, s.lastLanguage = tenantID, language
	if s.revision == nil {
		return nil, narration.ErrNotFound
	}
	return s.revision, nil
}

func (s *fakeScriptStore) Update(_ context.Context, tenantID, _, _, language string, expected int64, segments []*narration.Segment) (*narration.Revision, error) {
	s.lastTenantID, s.lastLanguage = tenantID, language
	if s.revision == nil {
		return nil, narration.ErrNotFound
	}
	if expected != s.revision.Revision {
		return nil, &narration.ErrConflict{Latest: s.revision}
	}
	s.revision.Revision++
	s.revision.Segments = segments
	return s.revision, nil
}

func (s *fakeScriptStore) SetStatus(_ context.Context, tenantID, _, _, language string, status narration.ScriptStatus) (*narration.Revision, error) {
	s.lastTenantID, s.lastLanguage = tenantID, language
	if s.revision == nil {
		return nil, narration.ErrNotFound
	}
	if s.revision.Status == narration.StatusLocked {
		return nil, narration.ErrLocked
	}
	s.revision.Status = status
	return s.revision, nil
}

func (s *fakeScriptStore) EnsureExists(context.Context, string, string, string, string, narration.ScriptMode) (*narration.Revision, error) {
	return nil, errors.New("not used")
}

func (*fakeScriptStore) CountDraftSegments(context.Context, string, string) (int, error) {
	return 0, nil
}

func (*fakeScriptStore) MarkAudioRevision(context.Context, string, string, string, string, int64) error {
	return nil
}

func (*fakeScriptStore) ListByProject(context.Context, string, string, string) ([]*narration.Revision, error) {
	return nil, nil
}

func newTestRevision() *narration.Revision {
	return &narration.Revision{
		ProjectID: "project-1",
		SlideID:   "slide-1",
		Language:  "zh-CN",
		Mode:      narration.ModeOriginal,
		Status:    narration.StatusDraft,
		Revision:  3,
		UpdatedAt: time.Unix(100, 0),
		Segments: []*narration.Segment{{
			SegmentID: "seg-1", DisplayText: "第一页", SpokenText: "第一页",
			SourceRefs: []string{"slide-1/shape-1"},
			SourceAnchors: []narration.SourceAnchor{{
				SlideID: "slide-1", ShapeID: "shape-1", Kind: "shape_text", Raw: "第一页", Confidence: 1,
			}},
			Status: narration.StatusDraft,
		}},
	}
}

func authRequest[T any](msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set(tenantHeader, "tenant-1")
	req.Header().Set(userHeader, "user-1")
	return req
}

func TestHandlerHealthAndAuthentication(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(server.URL + "/debug/vars")
	if err != nil {
		t.Fatalf("debug vars: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("debug vars status = %d", resp.StatusCode)
	}

	client := pptsv1connect.NewScriptServiceClient(http.DefaultClient, server.URL)
	_, err = client.Get(context.Background(), connect.NewRequest(&pptsv1.GetScriptRequest{
		ProjectId: "project-1", SlideId: "slide-1",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestHandlerRejectsInactiveTenant(t *testing.T) {
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{TenantStatus: fakeTenantStatusChecker{active: false}}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewProjectServiceClient(http.DefaultClient, server.URL)

	_, err := client.Create(context.Background(), authRequest(&pptsv1.CreateProjectRequest{Title: "blocked"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("inactive tenant code = %v err=%v", connect.CodeOf(err), err)
	}
}

func TestProjectServiceCreateListAndArchive(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "旧项目", CreatedAt: time.Unix(90, 0),
	}}}
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewProjectServiceClient(http.DefaultClient, server.URL)

	created, err := client.Create(context.Background(), authRequest(&pptsv1.CreateProjectRequest{Title: "新项目"}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Msg.GetProject().GetOwner() != "user-1" || projects.createdTenant != "tenant-1" {
		t.Fatalf("created = %+v tenant=%q", created.Msg.GetProject(), projects.createdTenant)
	}
	listed, err := client.List(context.Background(), authRequest(&pptsv1.ListProjectsRequest{PageSize: 10}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed.Msg.GetProjects()) != 2 || listed.Msg.GetProjects()[0].GetId() != "project-new" {
		t.Fatalf("listed = %+v", listed.Msg.GetProjects())
	}
	archived, err := client.Archive(context.Background(), authRequest(&pptsv1.ArchiveProjectRequest{Id: "project-new"}))
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if !archived.Msg.GetArchived() {
		t.Fatalf("archived = %+v", archived.Msg)
	}
}

func TestScriptGetUsesAuthenticatedTenantAndLanguage(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewScriptServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.GetScriptRequest{ProjectId: "project-1", SlideId: "slide-1"})
	req.Header().Set("Accept-Language", "en-US;q=0.9,zh-CN;q=0.8")
	resp, err := client.Get(context.Background(), req)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if store.lastTenantID != "tenant-1" || store.lastLanguage != "en-US" {
		t.Fatalf("identity/language = %q/%q", store.lastTenantID, store.lastLanguage)
	}
	if resp.Msg.GetRevision() != 3 || resp.Msg.GetSegments()[0].GetSlideId() != "slide-1" {
		t.Fatalf("response = %+v", resp.Msg)
	}
	anchors := resp.Msg.GetSegments()[0].GetSourceAnchors()
	if len(anchors) != 1 || anchors[0].GetShapeId() != "shape-1" || anchors[0].GetConfidence() != 1 {
		t.Fatalf("anchors = %+v", anchors)
	}
}

func TestScriptUpdateReturnsLatestOnConflict(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewScriptServiceClient(http.DefaultClient, server.URL)

	resp, err := client.Update(context.Background(), authRequest(&pptsv1.UpdateScriptRequest{
		ProjectId: "project-1", SlideId: "slide-1", ExpectedRevision: 2,
		Segments: []*pptsv1.Segment{{SegmentId: "seg-2", DisplayText: "changed"}},
	}))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !resp.Msg.GetConflict() || resp.Msg.GetLatest().GetRevision() != 3 {
		t.Fatalf("conflict response = %+v", resp.Msg)
	}
}

func TestScriptValidationAndIrreversibleLock(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewScriptServiceClient(http.DefaultClient, server.URL)

	_, err := client.Update(context.Background(), authRequest(&pptsv1.UpdateScriptRequest{
		ProjectId: "project-1", SlideId: "slide-1", ExpectedRevision: 3,
		Segments: []*pptsv1.Segment{{SegmentId: "dup"}, {SegmentId: "dup"}},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("duplicate segment code = %v, err=%v", connect.CodeOf(err), err)
	}

	_, err = client.Lock(context.Background(), authRequest(&pptsv1.LockScriptRequest{
		ProjectId: "project-1", SlideId: "slide-1", Lock: false,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unlock code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestCreateGenerationPersistsRevisionBoundSnapshot(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	store.revision.Status = narration.StatusApproved
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{
		ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1",
		RatePercent: 120, LockConfirmedOnly: true,
	})
	req.Header().Set("Idempotency-Key", "narration-1")
	resp, err := client.CreateGeneration(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if resp.Msg.GetJobId() != "job-1" || !resp.Msg.GetWithinBudget() {
		t.Fatalf("response = %+v", resp.Msg)
	}
	if jobs.tenantID != "tenant-1" || jobs.projectID != "project-1" || jobs.idempotencyKey != "narration-1" {
		t.Fatalf("job identity = %q/%q idem=%q", jobs.tenantID, jobs.projectID, jobs.idempotencyKey)
	}
	var snapshot app.NarrationSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Slides) != 1 || snapshot.Slides[0].ScriptRevision != 3 || snapshot.SpeechControl.RatePercent != 120 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestCreateGenerationRejectsIdempotencyKeyReuseWithDifferentSnapshot(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	jobs := &jobCreatorStub{job: &pipeline.Job{ID: "existing", InputSnapshot: `{"different":true}`}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{
		ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1",
	})
	req.Header().Set("Idempotency-Key", "reused")
	_, err := client.CreateGeneration(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
	}
}

// fakeQuotaManager 记录预占/释放，验证 G3-2 配额接线。
type fakeQuotaManager struct {
	reserveErr error
	reserved   []usage.Reservation
	released   []string
}

func (f *fakeQuotaManager) Reserve(_ context.Context, tenantID, logicalOperationID string, kind usage.Kind, units float64) (*usage.Reservation, error) {
	if f.reserveErr != nil {
		return nil, f.reserveErr
	}
	r := &usage.Reservation{
		ID: "res-1", TenantID: tenantID, LogicalOperationID: logicalOperationID,
		Kind: kind, ReservedUnits: units, State: "reserved", Created: true,
	}
	f.reserved = append(f.reserved, *r)
	return r, nil
}

func (f *fakeQuotaManager) Release(_ context.Context, tenantID, logicalOperationID string, kind usage.Kind) error {
	f.released = append(f.released, logicalOperationID)
	return nil
}

func TestCreateGenerationReservesQuota(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	jobs := &jobCreatorStub{}
	quota := &fakeQuotaManager{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{Quota: quota}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1"})
	req.Header().Set("Idempotency-Key", "quota-1")
	resp, err := client.CreateGeneration(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	if !resp.Msg.GetWithinBudget() {
		t.Fatalf("within_budget should be true")
	}
	if len(quota.reserved) != 1 || quota.reserved[0].LogicalOperationID != "quota-1" || quota.reserved[0].ReservedUnits <= 0 {
		t.Fatalf("reservation = %+v", quota.reserved)
	}
}

func TestCreateGenerationQuotaExceeded(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	quota := &fakeQuotaManager{reserveErr: usage.ErrInsufficientQuota}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{Quota: quota}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1"})
	req.Header().Set("Idempotency-Key", "quota-2")
	_, err := client.CreateGeneration(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
	}
	if len(quota.reserved) != 0 {
		t.Fatalf("no reservation should be recorded on rejection")
	}
}

func TestCreateGenerationReleasesQuotaOnJobFailure(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	quota := &fakeQuotaManager{}
	jobs := &jobCreatorStub{err: errors.New("db down")}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{Quota: quota}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1"})
	req.Header().Set("Idempotency-Key", "quota-3")
	_, err := client.CreateGeneration(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
	}
	if len(quota.released) != 1 || quota.released[0] != "quota-3" {
		t.Fatalf("expected release on job failure, got %+v", quota.released)
	}
}

func TestCreateGenerationRejectsWhenTenantConcurrentLimitReached(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	quota := &fakeQuotaManager{}
	jobs := &jobCreatorStub{activeCount: 1}
	policy := &fakeTenantPolicy{policy: &tenant.Policy{MaxConcurrentJobs: 1}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{Quota: quota, Policy: policy}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1"})
	req.Header().Set("Idempotency-Key", "limit-1")
	_, err := client.CreateGeneration(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
	}
	if len(quota.reserved) != 0 || jobs.idempotencyKey != "" {
		t.Fatalf("limit should reject before reserve/create: reserved=%+v idem=%q", quota.reserved, jobs.idempotencyKey)
	}
}

func TestCreateGenerationAllowsIdempotentReplayWhenTenantConcurrentLimitReached(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	snapshot := app.NarrationSnapshot{
		Language: defaultLanguage, VoiceID: "voice-1",
		SpeechControl: tts.SpeechControl{RatePercent: 100}, SampleRate: 16000,
		Slides: []app.NarrationSlideSnapshot{{SlideID: "slide-1", ScriptRevision: 3}},
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	quota := &fakeQuotaManager{}
	jobs := &jobCreatorStub{
		activeCount: 1,
		byIdem:      &pipeline.Job{ID: "existing", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindNarration, InputSnapshot: string(snapshotBytes)},
	}
	policy := &fakeTenantPolicy{policy: &tenant.Policy{MaxConcurrentJobs: 1}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{Quota: quota, Policy: policy}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1"})
	req.Header().Set("Idempotency-Key", "limit-replay")
	resp, err := client.CreateGeneration(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateGeneration replay: %v", err)
	}
	if resp.Msg.GetJobId() != "existing" || len(quota.reserved) != 0 || jobs.idempotencyKey != "" {
		t.Fatalf("replay response=%+v reserved=%+v idem=%q", resp.Msg, quota.reserved, jobs.idempotencyKey)
	}
}

// fakeAuditRecorder 记录审计事件，验证 G3-4 接线。
type fakeAuditRecorder struct {
	events []audit.Event
}

func (f *fakeAuditRecorder) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

func (f *fakeAuditRecorder) List(context.Context, string, audit.Filter) ([]audit.Event, error) {
	return f.events, nil
}

func (f *fakeAuditRecorder) DeleteBefore(context.Context, string, time.Time) (int64, error) {
	return 0, nil
}

func TestJobServiceCancelWritesAudit(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindNarration,
		State: pipeline.StateCanceled, IDempotencyKey: "idem-1",
	}
	recorder := &fakeAuditRecorder{}
	jobs := &jobCreatorStub{job: job}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{Audit: recorder}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	if _, err := client.Cancel(context.Background(), authRequest(&pptsv1.CancelJobRequest{JobId: "job-1"})); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("audit events = %+v", recorder.events)
	}
	ev := recorder.events[0]
	if ev.Action != "job.cancel" || ev.ResourceID != "job-1" || ev.ActorUser != "user-1" || ev.TenantID != "tenant-1" {
		t.Fatalf("audit event = %+v", ev)
	}
}

func TestJobServiceGetListCancelRetry(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindNarration,
		State: pipeline.StateRunning, Attempt: 2, Progress: 40, InputSnapshot: "snap",
		CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(20, 0),
		LastError: &pipeline.JobError{Code: "throttled", Retryable: true, RetryAfterSeconds: 5},
	}
	jobs := &jobCreatorStub{job: job, listJobs: []*pipeline.Job{job}, listNext: "cursor-2"}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	got, err := client.Get(context.Background(), authRequest(&pptsv1.GetJobRequest{JobId: "job-1"}))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Msg.GetJobId() != "job-1" || got.Msg.GetState() != pptsv1.JobState_JOB_STATE_RUNNING ||
		got.Msg.GetAttempt() != 2 || got.Msg.GetProgressPercent() != 40 || got.Msg.GetLastError().GetRetryable() != true {
		t.Fatalf("job = %+v", got.Msg)
	}

	list, err := client.List(context.Background(), authRequest(&pptsv1.ListJobsRequest{
		ProjectId: "project-1", State: pptsv1.JobState_JOB_STATE_RUNNING, PageSize: 10,
	}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Msg.GetJobs()) != 1 || list.Msg.GetNextCursor().GetValue() != "cursor-2" {
		t.Fatalf("list = %+v", list.Msg)
	}

	canceled, err := client.Cancel(context.Background(), authRequest(&pptsv1.CancelJobRequest{JobId: "job-1"}))
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if canceled.Msg.GetJobId() != "job-1" {
		t.Fatalf("cancel = %+v", canceled.Msg)
	}

	retried, err := client.RetryFailed(context.Background(), authRequest(&pptsv1.RetryFailedRequest{JobId: "job-1"}))
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if retried.Msg.GetJobId() != "job-1" {
		t.Fatalf("retry = %+v", retried.Msg)
	}
}

func TestJobServiceListWithoutProjectReturnsTenantJobs(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", ProjectID: "project-1",
		Kind: pipeline.KindParse, State: pipeline.StateQueued,
	}
	jobs := &jobCreatorStub{listJobs: []*pipeline.Job{job}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	// 任务中心全局视图：不带 project_id 也应返回租户任务，而非报 invalid_argument。
	list, err := client.List(context.Background(), authRequest(&pptsv1.ListJobsRequest{PageSize: 20}))
	if err != nil {
		t.Fatalf("List without project: %v", err)
	}
	if len(list.Msg.GetJobs()) != 1 || list.Msg.GetJobs()[0].GetJobId() != "job-1" {
		t.Fatalf("list = %+v", list.Msg)
	}
}

func TestJobServiceCancelReleasesNarrationReservation(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindNarration,
		State: pipeline.StateCanceled, IDempotencyKey: "idem-1",
	}
	quota := &fakeQuotaManager{}
	jobs := &jobCreatorStub{job: job}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{Quota: quota}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	if _, err := client.Cancel(context.Background(), authRequest(&pptsv1.CancelJobRequest{JobId: "job-1"})); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(quota.released) != 1 || quota.released[0] != "idem-1" {
		t.Fatalf("released = %+v", quota.released)
	}
}

func TestJobServiceWatchEventsStreamsUpdates(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindNarration,
		State: pipeline.StateRunning, Progress: 40,
	}
	jobs := &jobCreatorStub{watchEvents: []pipeline.JobEvent{{Seq: 7, Job: job}}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.WatchEvents(ctx, authRequest(&pptsv1.WatchEventsRequest{ProjectId: "project-1"}))
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("Receive: %v", stream.Err())
	}
	ev := stream.Msg()
	if ev.GetSeq() != 7 || ev.GetJob().GetJobId() != "job-1" ||
		ev.GetJob().GetState() != pptsv1.JobState_JOB_STATE_RUNNING {
		t.Fatalf("event = %+v", ev)
	}
	cancel()
}

func TestJobServiceWatchEventsRequiresProject(t *testing.T) {
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := client.WatchEvents(ctx, authRequest(&pptsv1.WatchEventsRequest{}))
	if err == nil {
		if stream.Receive() {
			err = nil
		} else {
			err = stream.Err()
		}
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("WatchEvents missing project code = %v err=%v", connect.CodeOf(err), err)
	}
}

func TestJobServiceNotFoundAndNotCancelable(t *testing.T) {
	jobs := &jobCreatorStub{} // job == nil → ErrJobNotFound
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	if _, err := client.Get(context.Background(), authRequest(&pptsv1.GetJobRequest{JobId: "missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get missing code = %v", connect.CodeOf(err))
	}

	notCancelable := &jobCreatorStub{err: pipeline.ErrJobNotCancelable}
	server2 := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, notCancelable, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server2.Close)
	client2 := pptsv1connect.NewJobServiceClient(http.DefaultClient, server2.URL)
	if _, err := client2.Cancel(context.Background(), authRequest(&pptsv1.CancelJobRequest{JobId: "job-1"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Cancel not-cancelable code = %v", connect.CodeOf(err))
	}
}

// fakeTenantUsage / fakeTenantPolicy 用于 TenantService 只读接口测试。
type fakeTenantUsage struct {
	quota        *usage.Quota
	seconds      float64
	cost         float64
	supplierCost float64
	err          error
}

func (f *fakeTenantUsage) GetQuota(_ context.Context, tenantID string, kind usage.Kind) (*usage.Quota, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.quota == nil {
		return &usage.Quota{TenantID: tenantID, Kind: kind, LimitUnits: -1}, nil
	}
	return f.quota, nil
}

func (f *fakeTenantUsage) UsageSummary(context.Context, string, string) (float64, float64, float64, error) {
	if f.err != nil {
		return 0, 0, 0, f.err
	}
	return f.seconds, f.cost, f.supplierCost, nil
}

func (f *fakeTenantUsage) Currency() string { return "CNY" }

func (f *fakeTenantUsage) ProjectUsage(context.Context, string, string) (usage.ProjectUsage, error) {
	if f.err != nil {
		return usage.ProjectUsage{}, f.err
	}
	return usage.ProjectUsage{ProjectID: "project-1", Seconds: f.seconds, JobCount: 3}, nil
}

type fakeTenantPolicy struct {
	policy *tenant.Policy
	err    error
}

type fakeTenantStorage struct {
	usage *tenant.StorageUsage
	err   error
}

func (f *fakeTenantStorage) StorageUsage(context.Context, string) (*tenant.StorageUsage, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.usage == nil {
		return &tenant.StorageUsage{}, nil
	}
	return f.usage, nil
}

func (f *fakeTenantPolicy) GetPolicy(context.Context, string) (*tenant.Policy, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.policy == nil {
		return &tenant.Policy{}, nil
	}
	return f.policy, nil
}

// fakeMembershipReader 提供 TenantService.Members/Roles 的成员读取能力。
type fakeMembershipReader struct {
	members []membership.Member
}

func (f *fakeMembershipReader) GetRole(context.Context, string, string) (membership.Role, error) {
	return "", membership.ErrNotFound
}

func (f *fakeMembershipReader) List(context.Context, string) ([]membership.Member, error) {
	return f.members, nil
}

func (f *fakeMembershipReader) SetRole(_ context.Context, _, userID string, role membership.Role) error {
	for i := range f.members {
		if f.members[i].UserID == userID {
			f.members[i].Role = role
			return nil
		}
	}
	f.members = append(f.members, membership.Member{UserID: userID, Role: role})
	return nil
}

func (f *fakeMembershipReader) Remove(_ context.Context, _, userID string) error {
	for i := range f.members {
		if f.members[i].UserID == userID {
			f.members = append(f.members[:i], f.members[i+1:]...)
			return nil
		}
	}
	return membership.ErrNotFound
}

func (f *fakeMembershipReader) SaveProfile(_ context.Context, _ string, _ membership.Profile) error {
	return nil
}

func TestTenantServiceMembersAndRoles(t *testing.T) {
	m := &fakeMembershipReader{members: []membership.Member{
		{UserID: "user-1", Role: membership.RoleOwner},
		{UserID: "user-2", Role: membership.RoleReviewer},
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Usage: &fakeTenantUsage{}, Policy: &fakeTenantPolicy{policy: &tenant.Policy{}}, Members: m}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	members, err := client.Members(context.Background(), authRequest(&pptsv1.GetMembersRequest{}))
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members.Msg.GetMembers()) != 2 || members.Msg.GetMembers()[0].GetRole() != pptsv1.Role_ROLE_OWNER {
		t.Fatalf("members = %+v", members.Msg.GetMembers())
	}

	roles, err := client.Roles(context.Background(), authRequest(&pptsv1.GetRolesRequest{}))
	if err != nil {
		t.Fatalf("Roles: %v", err)
	}
	if roles.Msg.GetUserRoles()["user-1"] != pptsv1.Role_ROLE_OWNER ||
		roles.Msg.GetUserRoles()["user-2"] != pptsv1.Role_ROLE_REVIEWER {
		t.Fatalf("roles = %+v", roles.Msg.GetUserRoles())
	}
}

func TestTenantServiceMemberManagementEnforcesRoles(t *testing.T) {
	members := &fakeRoleReader{roles: map[string]membership.Role{"user-1": membership.RoleReviewer}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Usage: &fakeTenantUsage{}, Policy: &fakeTenantPolicy{policy: &tenant.Policy{}}, Members: members}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	if _, err := client.SetMemberRole(context.Background(), authRequest(&pptsv1.SetMemberRoleRequest{UserId: "user-2", Role: pptsv1.Role_ROLE_VIEWER})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("reviewer set member code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.roles["user-1"] = membership.RoleAdmin
	if _, err := client.SetMemberRole(context.Background(), authRequest(&pptsv1.SetMemberRoleRequest{UserId: "user-2", Role: pptsv1.Role_ROLE_VIEWER})); err != nil {
		t.Fatalf("admin set viewer: %v", err)
	}
	if members.roles["user-2"] != membership.RoleViewer {
		t.Fatalf("user-2 role = %q want viewer", members.roles["user-2"])
	}
	if _, err := client.SetMemberRole(context.Background(), authRequest(&pptsv1.SetMemberRoleRequest{UserId: "user-3", Role: pptsv1.Role_ROLE_OWNER})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("admin set owner code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.roles["user-1"] = membership.RoleOwner
	if _, err := client.SetMemberRole(context.Background(), authRequest(&pptsv1.SetMemberRoleRequest{UserId: "user-3", Role: pptsv1.Role_ROLE_OWNER})); err != nil {
		t.Fatalf("owner set owner: %v", err)
	}
	members.roles["user-1"] = membership.RoleAdmin
	if _, err := client.RemoveMember(context.Background(), authRequest(&pptsv1.RemoveMemberRequest{UserId: "user-2"})); err != nil {
		t.Fatalf("admin remove viewer: %v", err)
	}
	if _, ok := members.roles["user-2"]; ok {
		t.Fatalf("user-2 still present after remove")
	}
}

func TestTenantServiceProjectUsage(t *testing.T) {
	u := &fakeTenantUsage{seconds: 120}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Usage: u, Policy: &fakeTenantPolicy{policy: &tenant.Policy{}}}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	resp, err := client.ProjectUsage(context.Background(), authRequest(&pptsv1.GetProjectUsageRequest{ProjectId: "project-1"}))
	if err != nil {
		t.Fatalf("ProjectUsage: %v", err)
	}
	if resp.Msg.GetSeconds() != 120 || resp.Msg.GetJobCount() != 3 || resp.Msg.GetProjectId() != "project-1" {
		t.Fatalf("project usage = %+v", resp.Msg)
	}
	if _, err := client.ProjectUsage(context.Background(), authRequest(&pptsv1.GetProjectUsageRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing project_id code = %v want InvalidArgument", connect.CodeOf(err))
	}
}

func TestTenantServiceStorageUsage(t *testing.T) {
	storage := &fakeTenantStorage{usage: &tenant.StorageUsage{
		SourceBytes: 30, ArtifactBytes: 7, OtherBytes: 5, TotalBytes: 42, SourceObjects: 2, ArtifactObjects: 1, OtherObjects: 1,
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Usage: &fakeTenantUsage{}, Policy: &fakeTenantPolicy{policy: &tenant.Policy{}}, Storage: storage}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	resp, err := client.StorageUsage(context.Background(), authRequest(&pptsv1.GetStorageUsageRequest{}))
	if err != nil {
		t.Fatalf("StorageUsage: %v", err)
	}
	if resp.Msg.GetSourceBytes() != 30 || resp.Msg.GetArtifactBytes() != 7 || resp.Msg.GetOtherBytes() != 5 ||
		resp.Msg.GetTotalBytes() != 42 || resp.Msg.GetSourceObjects() != 2 || resp.Msg.GetArtifactObjects() != 1 ||
		resp.Msg.GetOtherObjects() != 1 {
		t.Fatalf("storage usage = %+v", resp.Msg)
	}
}

type fakeTenantArchive struct {
	files []tenant.ArchiveFile
	err   error
	limit int
}

func (f *fakeTenantArchive) ListAuditArchives(context.Context, string, int) ([]tenant.ArchiveFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.files, nil
}

func TestTenantServiceListAuditArchivesRequiresAdmin(t *testing.T) {
	members := &fakeRoleReader{roles: map[string]membership.Role{"user-1": membership.RoleReviewer}}
	archive := &fakeTenantArchive{files: []tenant.ArchiveFile{
		{ObjectKey: "t/audit/archive/audit/x.jsonl", SizeBytes: 10, UpdatedAt: time.Unix(200, 0)},
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Usage: &fakeTenantUsage{}, Policy: &fakeTenantPolicy{policy: &tenant.Policy{}}, Members: members, Archive: archive}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	if _, err := client.ListAuditArchives(context.Background(), authRequest(&pptsv1.ListAuditArchivesRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("reviewer list archives code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.roles["user-1"] = membership.RoleAdmin
	resp, err := client.ListAuditArchives(context.Background(), authRequest(&pptsv1.ListAuditArchivesRequest{Limit: 10}))
	if err != nil {
		t.Fatalf("admin list archives: %v", err)
	}
	if len(resp.Msg.GetFiles()) != 1 || resp.Msg.GetFiles()[0].GetObjectKey() != "t/audit/archive/audit/x.jsonl" ||
		resp.Msg.GetFiles()[0].GetSizeBytes() != 10 || resp.Msg.GetFiles()[0].GetUpdatedAtUnix() != 200 {
		t.Fatalf("archive files = %+v", resp.Msg.GetFiles())
	}
}

func TestTenantServiceListAuditEventsRequiresAdmin(t *testing.T) {
	members := &fakeRoleReader{roles: map[string]membership.Role{"user-1": membership.RoleReviewer}}
	audits := &fakeAuditStore{events: []audit.Event{{
		ID: "audit-1", ActorUser: "user-2", Action: "job.cancel", ResourceType: "job", ResourceID: "job-1",
		Metadata: map[string]any{"reason": "test"}, CreatedAt: time.Unix(100, 0),
	}}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Usage: &fakeTenantUsage{}, Policy: &fakeTenantPolicy{policy: &tenant.Policy{}}, Members: members, Audit: audits}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.ListAuditEventsRequest{Action: "job.cancel", ResourceType: "job", SinceUnix: 90, PageSize: 5})
	if _, err := client.ListAuditEvents(context.Background(), req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("reviewer list audit code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.roles["user-1"] = membership.RoleAdmin
	resp, err := client.ListAuditEvents(context.Background(), req)
	if err != nil {
		t.Fatalf("admin list audit: %v", err)
	}
	if audits.filter.Action != "job.cancel" || audits.filter.ResourceType != "job" || audits.filter.Limit != 5 || audits.filter.Since.Unix() != 90 {
		t.Fatalf("filter = %+v", audits.filter)
	}
	if len(resp.Msg.GetEvents()) != 1 || !strings.Contains(resp.Msg.GetEvents()[0].GetMetadataJson(), "reason") {
		t.Fatalf("events = %+v", resp.Msg.GetEvents())
	}
}

// fakeRoleReader 提供可配置角色的成员读取，验证 G3-3 授权门禁。
type fakeRoleReader struct {
	role  membership.Role
	roles map[string]membership.Role
	err   error
}

func (f *fakeRoleReader) GetRole(_ context.Context, _, userID string) (membership.Role, error) {
	if f.roles != nil {
		role, ok := f.roles[userID]
		if !ok {
			return "", membership.ErrNotFound
		}
		return role, f.err
	}
	return f.role, f.err
}

func (f *fakeRoleReader) List(context.Context, string) ([]membership.Member, error) {
	return nil, nil
}

func (f *fakeRoleReader) SetRole(_ context.Context, _, userID string, role membership.Role) error {
	if f.roles == nil {
		f.roles = map[string]membership.Role{}
	}
	f.roles[userID] = role
	return f.err
}

func (f *fakeRoleReader) Remove(_ context.Context, _, userID string) error {
	if f.err != nil {
		return f.err
	}
	if f.roles != nil {
		if _, ok := f.roles[userID]; !ok {
			return membership.ErrNotFound
		}
		delete(f.roles, userID)
	}
	return nil
}

// SaveProfile 是 membership.Store 的必需方法（#94 前已加入）；fakeRoleReader 不依赖档案，返回 nil。
func (f *fakeRoleReader) SaveProfile(_ context.Context, _ string, _ membership.Profile) error {
	return f.err
}

type fakeTenantLifecycle struct {
	manifest *tenant.ExportManifest
	deleted  int64
	err      error
}

func (f *fakeTenantLifecycle) ExportTenant(_ context.Context, tenantID string, _ objectstore.ObjectStore) (*tenant.ExportManifest, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.manifest == nil {
		return &tenant.ExportManifest{TenantID: tenantID, ManifestKey: "m", Files: []tenant.ExportFile{{Table: "uploads", ObjectKey: "uploads.jsonl", Rows: 2}}}, nil
	}
	return f.manifest, nil
}

func (f *fakeTenantLifecycle) PurgeTenant(context.Context, string, objectstore.ObjectStore) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.deleted, nil
}

func TestTenantServiceExportPurgeRequireOwner(t *testing.T) {
	members := &fakeRoleReader{roles: map[string]membership.Role{"user-1": membership.RoleEditor}}
	lifecycle := &fakeTenantLifecycle{deleted: 7}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Usage: &fakeTenantUsage{}, Policy: &fakeTenantPolicy{policy: &tenant.Policy{}}, Members: members, Lifecycle: lifecycle}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	if _, err := client.ExportTenant(context.Background(), authRequest(&pptsv1.ExportTenantRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("editor export code = %v want PermissionDenied", connect.CodeOf(err))
	}
	if _, err := client.PurgeTenant(context.Background(), authRequest(&pptsv1.PurgeTenantRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("editor purge code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.roles["user-1"] = membership.RoleOwner
	exported, err := client.ExportTenant(context.Background(), authRequest(&pptsv1.ExportTenantRequest{}))
	if err != nil {
		t.Fatalf("owner export: %v", err)
	}
	if exported.Msg.GetManifestObjectKey() == "" || len(exported.Msg.GetFiles()) != 1 || exported.Msg.GetFiles()[0].GetTable() != "uploads" {
		t.Fatalf("export = %+v", exported.Msg)
	}
	purged, err := client.PurgeTenant(context.Background(), authRequest(&pptsv1.PurgeTenantRequest{}))
	if err != nil {
		t.Fatalf("owner purge: %v", err)
	}
	if purged.Msg.GetDeletedRows() != 7 {
		t.Fatalf("purge deleted = %d want 7", purged.Msg.GetDeletedRows())
	}
}

func TestJobServiceCancelEnforcesRole(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindNarration,
		State: pipeline.StateQueued,
	}
	members := &fakeRoleReader{role: membership.RoleViewer}
	jobs := &jobCreatorStub{job: job}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t), nil, Options{Members: members}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	if _, err := client.Cancel(context.Background(), authRequest(&pptsv1.CancelJobRequest{JobId: "job-1"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer cancel code = %v want PermissionDenied", connect.CodeOf(err))
	}

	members.role = membership.RoleEditor
	if _, err := client.Cancel(context.Background(), authRequest(&pptsv1.CancelJobRequest{JobId: "job-1"})); err != nil {
		t.Fatalf("editor cancel: %v", err)
	}
}

func TestCreateGenerationRequiresEditorRole(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	members := &fakeRoleReader{role: membership.RoleViewer}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{Members: members}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewNarrationServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateGenerationRequest{ProjectId: "project-1", SlideIds: []string{"slide-1"}, VoiceId: "voice-1"})
	req.Header().Set("Idempotency-Key", "role-1")
	if _, err := client.CreateGeneration(context.Background(), req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer generation code = %v want PermissionDenied", connect.CodeOf(err))
	}
}

func TestProjectWriteOperationsEnforceRoles(t *testing.T) {
	members := &fakeRoleReader{role: membership.RoleViewer}
	projects := &fakeProjectStore{projects: []*project.Project{{ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo"}}}
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{Members: members}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewProjectServiceClient(http.DefaultClient, server.URL)

	if _, err := client.Create(context.Background(), authRequest(&pptsv1.CreateProjectRequest{Title: "New"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer create project code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.role = membership.RoleEditor
	if _, err := client.Create(context.Background(), authRequest(&pptsv1.CreateProjectRequest{Title: "New"})); err != nil {
		t.Fatalf("editor create project: %v", err)
	}
	if _, err := client.Archive(context.Background(), authRequest(&pptsv1.ArchiveProjectRequest{Id: "project-1"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("editor archive project code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.role = membership.RoleAdmin
	if _, err := client.Archive(context.Background(), authRequest(&pptsv1.ArchiveProjectRequest{Id: "project-1"})); err != nil {
		t.Fatalf("admin archive project: %v", err)
	}
}

func TestScriptWriteOperationsEnforceRoles(t *testing.T) {
	members := &fakeRoleReader{role: membership.RoleViewer}
	store := &fakeScriptStore{revision: newTestRevision()}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{Members: members}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewScriptServiceClient(http.DefaultClient, server.URL)
	segment := &pptsv1.Segment{SegmentId: "seg-1", DisplayText: "第一页", SpokenText: "第一页"}

	if _, err := client.Update(context.Background(), authRequest(&pptsv1.UpdateScriptRequest{ProjectId: "project-1", SlideId: "slide-1", ExpectedRevision: 3, Segments: []*pptsv1.Segment{segment}})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer update script code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.role = membership.RoleReviewer
	if _, err := client.Approve(context.Background(), authRequest(&pptsv1.ApproveScriptRequest{ProjectId: "project-1", SlideId: "slide-1"})); err != nil {
		t.Fatalf("reviewer approve script: %v", err)
	}
	if _, err := client.Update(context.Background(), authRequest(&pptsv1.UpdateScriptRequest{ProjectId: "project-1", SlideId: "slide-1", ExpectedRevision: 3, Segments: []*pptsv1.Segment{segment}})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("reviewer update script code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.role = membership.RoleEditor
	if _, err := client.Update(context.Background(), authRequest(&pptsv1.UpdateScriptRequest{ProjectId: "project-1", SlideId: "slide-1", ExpectedRevision: 3, Segments: []*pptsv1.Segment{segment}})); err != nil {
		t.Fatalf("editor update script: %v", err)
	}
}

func TestUploadAndExportWriteOperationsEnforceRoles(t *testing.T) {
	members := &fakeRoleReader{role: membership.RoleViewer}
	projects := &fakeProjectStore{projects: []*project.Project{{ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo"}}}
	objects := testObjects(t)
	artifacts := &fakeArtifactStore{artifact: &artifact.Artifact{
		ID: "artifact-1", TenantID: "tenant-1", ProjectID: "project-1",
		Format: artifact.FormatSRT, ObjectKey: "tenant-1/project-1/artifact/artifact/hash.srt",
	}}
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, artifacts, objects, nil, Options{Members: members}))
	t.Cleanup(server.Close)
	uploadClient := pptsv1connect.NewUploadServiceClient(http.DefaultClient, server.URL)
	exportClient := pptsv1connect.NewExportServiceClient(http.DefaultClient, server.URL)

	if _, err := uploadClient.CreateUpload(context.Background(), authRequest(&pptsv1.CreateUploadRequest{ProjectId: "project-1", Filename: "demo.pptx", SizeBytes: 3})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer create upload code = %v want PermissionDenied", connect.CodeOf(err))
	}
	createExport := authRequest(&pptsv1.CreateExportRequest{ProjectId: "project-1", Format: pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT, TimelineKey: "tenant-1/project-1/narration/timeline/tl.json"})
	createExport.Header().Set("Idempotency-Key", "export-role-1")
	if _, err := exportClient.CreateExport(context.Background(), createExport); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer create export code = %v want PermissionDenied", connect.CodeOf(err))
	}
	members.role = membership.RoleEditor
	if _, err := uploadClient.CreateUpload(context.Background(), authRequest(&pptsv1.CreateUploadRequest{ProjectId: "project-1", Filename: "demo.pptx", SizeBytes: 3})); err != nil {
		t.Fatalf("editor create upload: %v", err)
	}
	createExport = authRequest(&pptsv1.CreateExportRequest{ProjectId: "project-1", Format: pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT, TimelineKey: "tenant-1/project-1/narration/timeline/tl.json"})
	createExport.Header().Set("Idempotency-Key", "export-role-2")
	if _, err := exportClient.CreateExport(context.Background(), createExport); err != nil {
		t.Fatalf("editor create export: %v", err)
	}
}

func TestTenantServiceQuotaUsagePolicy(t *testing.T) {
	u := &fakeTenantUsage{
		quota:   &usage.Quota{TenantID: "tenant-1", Kind: usage.KindGenSeconds, LimitUnits: 3600, ConsumedUnits: 120, ReservedUnits: 30},
		seconds: 120, cost: 1.2, supplierCost: 0.48,
	}
	p := &fakeTenantPolicy{policy: &tenant.Policy{
		StorageBackend: "s3", StorageRegion: "cn-north-1", SourceRetentionDays: 30,
		StorageTransitionDays: 45, StorageExpirationDays: 365,
		MaxConcurrentJobs: 4, MaxStorageBytes: 1 << 30,
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{Usage: u, Policy: p}))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewTenantServiceClient(http.DefaultClient, server.URL)

	quota, err := client.Quota(context.Background(), authRequest(&pptsv1.GetQuotaRequest{}))
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if quota.Msg.GetMonthlySeconds() != 3600 || quota.Msg.GetUsedSeconds() != 120 ||
		quota.Msg.GetMaxConcurrentJobs() != 4 || quota.Msg.GetMaxStorageBytes() != 1<<30 {
		t.Fatalf("quota = %+v", quota.Msg)
	}

	usageResp, err := client.Usage(context.Background(), authRequest(&pptsv1.GetUsageRequest{Month: "2026-09"}))
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usageResp.Msg.GetSecondsUsed() != 120 || usageResp.Msg.GetUserAmount() != 1.2 ||
		usageResp.Msg.GetSupplierCost() != 0.48 || usageResp.Msg.GetCurrency() != "CNY" {
		t.Fatalf("usage = %+v", usageResp.Msg)
	}

	policy, err := client.Policy(context.Background(), authRequest(&pptsv1.GetPolicyRequest{}))
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if policy.Msg.GetStorageBackend() != "s3" || policy.Msg.GetStorageRegion() != "cn-north-1" ||
		policy.Msg.GetSourceRetentionDays() != 30 || policy.Msg.GetStorageTransitionDays() != 45 ||
		policy.Msg.GetStorageExpirationDays() != 365 {
		t.Fatalf("policy = %+v", policy.Msg)
	}

	// Members/Roles 依赖 G3-3，暂返回 Unimplemented。
	if _, err := client.Members(context.Background(), authRequest(&pptsv1.GetMembersRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Members code = %v want Unimplemented", connect.CodeOf(err))
	}
}

func TestCreateExportPersistsFixedSnapshotAndRejectsCrossTenantKeys(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	jobs := &jobCreatorStub{}
	objects := testObjects(t)
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewExportServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateExportRequest{
		ProjectId: "project-1", Format: pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT,
		TimelineKey: "tenant-1/project-1/narration/timeline/tl.json",
	})
	req.Header().Set("Idempotency-Key", "export-1")
	resp, err := client.CreateExport(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	if resp.Msg.GetJobId() != "job-1" || jobs.idempotencyKey != "export-1" {
		t.Fatalf("response/job = %+v idem=%q", resp.Msg, jobs.idempotencyKey)
	}
	var snapshot app.ExportSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Format != artifact.FormatSRT || snapshot.TimelineKey == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	badReq := authRequest(&pptsv1.CreateExportRequest{
		ProjectId: "project-1", Format: pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT,
		TimelineKey: "other/project-1/narration/timeline/tl.json",
	})
	badReq.Header().Set("Idempotency-Key", "export-2")
	_, err = client.CreateExport(context.Background(), badReq)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("cross tenant code=%v err=%v", connect.CodeOf(err), err)
	}
}

func TestCreateExportSupportsWebProjectFormat(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	jobs := &jobCreatorStub{}
	objects := testObjects(t)
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewExportServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.CreateExportRequest{
		ProjectId: "project-1", Format: pptsv1.ArtifactFormat_ARTIFACT_FORMAT_WEB_PROJECT,
		TimelineKey: "tenant-1/project-1/narration/timeline/tl.json",
	})
	req.Header().Set("Idempotency-Key", "export-web-1")
	if _, err := client.CreateExport(context.Background(), req); err != nil {
		t.Fatalf("CreateExport web_project: %v", err)
	}
	var snapshot app.ExportSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Format != artifact.FormatWebProject {
		t.Fatalf("snapshot format = %q want web_project", snapshot.Format)
	}

	bad := authRequest(&pptsv1.CreateExportRequest{
		ProjectId: "project-1", Format: pptsv1.ArtifactFormat_ARTIFACT_FORMAT_UNSPECIFIED,
		TimelineKey: "tenant-1/project-1/narration/timeline/tl.json",
	})
	bad.Header().Set("Idempotency-Key", "export-web-2")
	if _, err := client.CreateExport(context.Background(), bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unspecified format code=%v err=%v", connect.CodeOf(err), err)
	}
}

func TestCreateDownloadSignsArtifactObject(t *testing.T) {
	objects := testObjects(t)
	key := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "artifact", AssetType: "artifact", AssetID: "hash", Ext: "srt"}
	if err := objects.Put(context.Background(), key, bytes.NewReader([]byte("srt")), objectstore.ObjectMeta{Size: 3}); err != nil {
		t.Fatal(err)
	}
	artifacts := &fakeArtifactStore{artifact: &artifact.Artifact{
		ID: "artifact-1", TenantID: "tenant-1", ProjectID: "project-1", Format: artifact.FormatSRT,
		ObjectKey: key.String(), ContentHash: "hash", SizeBytes: 3, CreatedAt: time.Unix(1, 0),
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, artifacts, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewExportServiceClient(http.DefaultClient, server.URL)

	resp, err := client.CreateDownload(context.Background(), authRequest(&pptsv1.CreateDownloadRequest{ArtifactId: "artifact-1", TtlSeconds: 60}))
	if err != nil {
		t.Fatalf("CreateDownload: %v", err)
	}
	if resp.Msg.GetSignedUrl() == "" || resp.Msg.GetExpiresAtUnix() <= time.Now().Unix() {
		t.Fatalf("download = %+v", resp.Msg)
	}
	if !strings.HasPrefix(resp.Msg.GetSignedUrl(), "/ppts/object/") {
		t.Fatalf("local download url not rewritten: %q", resp.Msg.GetSignedUrl())
	}
}

// TestGlobalArtifactsRouteMounted 守护 B5-M2 跨项目成品库路由（GET /artifacts）确实被挂载。
// 回归背景：globalArtifacts handler 写完但漏挂路由，请求落到 SPA 兜底返回 index.html，
// 前端 JSON 解析报 `Unexpected token '<', "<!doctype"`（见项目风险 R-10）。
// 本用例特意挂上 SPA 兜底（WebRoot + index.html）复现单二进制部署形态：
// 路由未挂载时响应体必为 HTML；挂载正确则为 JSON。
func TestGlobalArtifactsRouteMounted(t *testing.T) {
	members := &fakeRoleReader{role: membership.RoleOwner}
	artifacts := &fakeArtifactStore{artifact: &artifact.Artifact{
		ID: "artifact-1", TenantID: "tenant-1", ProjectID: "project-1", ProjectName: "Demo",
		Format: artifact.FormatSRT, SizeBytes: 3, CreatedAt: time.Unix(1, 0),
	}}
	webRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html><html>app</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(
		&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{},
		artifacts, testObjects(t), nil, Options{Members: members, WebRoot: webRoot},
	))
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/artifacts", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /artifacts: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	if strings.Contains(strings.ToLower(string(body)), "<!doctype") {
		t.Fatalf("GET /artifacts 命中 SPA 兜底返回了 HTML（路由未挂载）: %s", body)
	}
	var payload struct {
		Artifacts []struct {
			ID          string `json:"id"`
			ProjectName string `json:"projectName"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("响应不是 JSON: %v body = %s", err, body)
	}
	if len(payload.Artifacts) != 1 || payload.Artifacts[0].ProjectName != "Demo" {
		t.Fatalf("artifacts = %+v", payload.Artifacts)
	}
}

func TestPlaybackManifestSignsAllRequiredResources(t *testing.T) {
	objects := testObjects(t)
	timeline, err := media.BuildTimeline([]media.SlideInput{{
		SlideID:  "slide-1",
		Segments: []media.SegmentInput{{SegmentID: "seg-1", DisplayText: "字幕", AudioKey: "tenant-1/project-1/cache/audio/a.wav", DurationMS: 100}},
	}}, media.Timing{})
	if err != nil {
		t.Fatal(err)
	}
	srtKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "subtitle", AssetID: "sub", Ext: "srt"}
	vttKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "subtitle", AssetID: "sub", Ext: "vtt"}
	timelineKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "timeline", AssetID: "tl", Ext: "json"}
	pageKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "render", AssetType: "render", AssetID: "slide-1", Ext: "png"}
	audioKey, _ := objectstore.Parse(timeline.Slides[0].Segments[0].AudioKey)
	putAPIObject(t, objects, srtKey, []byte("srt"), "application/x-subrip")
	putAPIObject(t, objects, vttKey, []byte("vtt"), "text/vtt")
	putAPIObject(t, objects, pageKey, []byte("png"), "image/png")
	putAPIObject(t, objects, audioKey, []byte("wav"), "audio/wav")
	bundle, _ := json.Marshal(app.TimelineAsset{Timeline: timeline, SRTKey: srtKey.String(), VTTKey: vttKey.String()})
	putAPIObject(t, objects, timelineKey, bundle, "application/json")
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewPlaybackServiceClient(http.DefaultClient, server.URL)

	resp, err := client.GetManifest(context.Background(), authRequest(&pptsv1.GetPlaybackManifestRequest{
		ProjectId: "project-1", TimelineKey: timelineKey.String(), PagePngKeys: []string{pageKey.String()}, TtlSeconds: 60,
	}))
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if resp.Msg.GetTimelineJson() == "" || resp.Msg.GetExpiresAtUnix() <= time.Now().Unix() || len(resp.Msg.GetResources()) != 5 {
		t.Fatalf("manifest = %+v", resp.Msg)
	}
	counts := map[pptsv1.PlaybackResourceType]int{}
	for _, res := range resp.Msg.GetResources() {
		if res.GetSignedUrl() == "" || res.GetKey() == "" {
			t.Fatalf("resource missing url/key: %+v", res)
		}
		if !strings.HasPrefix(res.GetSignedUrl(), "/ppts/object/") {
			t.Fatalf("local resource url not rewritten: %+v", res)
		}
		counts[res.GetType()]++
	}
	if counts[pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_TIMELINE] != 1 || counts[pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_PAGE_PNG] != 1 || counts[pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_AUDIO] != 1 {
		t.Fatalf("counts = %v", counts)
	}

	// 空页面图：渲染未就绪时允许（音频+字幕仍可返回，无 PAGE_PNG 资源）。
	respNoPage, err := client.GetManifest(context.Background(), authRequest(&pptsv1.GetPlaybackManifestRequest{
		ProjectId: "project-1", TimelineKey: timelineKey.String(), TtlSeconds: 60,
	}))
	if err != nil {
		t.Fatalf("GetManifest no-page: %v", err)
	}
	if len(respNoPage.Msg.GetResources()) != 4 {
		t.Fatalf("no-page resources = %d: %+v", len(respNoPage.Msg.GetResources()), respNoPage.Msg)
	}
	for _, res := range respNoPage.Msg.GetResources() {
		if res.GetType() == pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_PAGE_PNG {
			t.Fatalf("unexpected PAGE_PNG resource without page keys: %+v", res)
		}
	}

	// 数量不匹配（仅提供部分页面）仍拒绝。
	_, err = client.GetManifest(context.Background(), authRequest(&pptsv1.GetPlaybackManifestRequest{
		ProjectId: "project-1", TimelineKey: timelineKey.String(),
		PagePngKeys: []string{pageKey.String(), pageKey.String()}, TtlSeconds: 60,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("page count mismatch code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func putAPIObject(t *testing.T, objects objectstore.ObjectStore, key objectstore.ObjectKey, data []byte, contentType string) {
	t.Helper()
	if err := objects.Put(context.Background(), key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: contentType, ContentHash: "hash", Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
}

func TestPlaybackGetNarration(t *testing.T) {
	objects := testObjects(t)
	jobs := &narrationJobStub{
		job:         &pipeline.Job{ID: "job-narr", Kind: pipeline.KindNarration},
		stepRef:     "tenant-1/project-1/narration-job-narr/timeline/abc.json",
		jobNotFound: false,
	}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewPlaybackServiceClient(http.DefaultClient, server.URL)

	resp, err := client.GetNarration(context.Background(), authRequest(&pptsv1.GetNarrationRequest{ProjectId: "project-1"}))
	if err != nil {
		t.Fatalf("GetNarration: %v", err)
	}
	if !resp.Msg.GetReady() || resp.Msg.GetTimelineKey() != "tenant-1/project-1/narration-job-narr/timeline/abc.json" {
		t.Fatalf("response = %+v", resp.Msg)
	}

	// 无成功配音 → ready=false。
	notReady := &narrationJobStub{jobNotFound: true}
	server2 := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, notReady, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server2.Close)
	client2 := pptsv1connect.NewPlaybackServiceClient(http.DefaultClient, server2.URL)
	resp2, err := client2.GetNarration(context.Background(), authRequest(&pptsv1.GetNarrationRequest{ProjectId: "project-1"}))
	if err != nil {
		t.Fatalf("GetNarration#2: %v", err)
	}
	if resp2.Msg.GetReady() {
		t.Fatalf("expected not ready: %+v", resp2.Msg)
	}
}

func TestPlaybackGetNarrationIncludesPageKeys(t *testing.T) {
	objects := testObjects(t)
	timeline, err := media.BuildTimeline([]media.SlideInput{{
		SlideID:  "slide-1",
		Segments: []media.SegmentInput{{SegmentID: "seg-1", DisplayText: "字幕", AudioKey: "tenant-1/project-1/cache/audio/a.wav", DurationMS: 100}},
	}}, media.Timing{})
	if err != nil {
		t.Fatal(err)
	}
	srtKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "subtitle", AssetID: "sub", Ext: "srt"}
	vttKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "subtitle", AssetID: "sub", Ext: "vtt"}
	timelineKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration", AssetType: "timeline", AssetID: "tl", Ext: "json"}
	pageKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "src-01", AssetType: "render", AssetID: "page-0001", Ext: "png"}
	manifestKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "src-01", AssetType: "render", AssetID: "pages", Ext: "json"}
	putAPIObject(t, objects, srtKey, []byte("srt"), "application/x-subrip")
	putAPIObject(t, objects, vttKey, []byte("vtt"), "text/vtt")
	putAPIObject(t, objects, pageKey, []byte("png"), "image/png")
	bundle, _ := json.Marshal(app.TimelineAsset{Timeline: timeline, SRTKey: srtKey.String(), VTTKey: vttKey.String()})
	putAPIObject(t, objects, timelineKey, bundle, "application/json")
	manifest, _ := json.Marshal(app.PageManifest{RevisionNo: 1, Pages: []app.PageEntry{{SlideID: "slide-1", Index: 0, Key: pageKey.String()}}})
	putAPIObject(t, objects, manifestKey, manifest, "application/json")

	jobs := &narrationJobStub{
		job:      &pipeline.Job{ID: "job-narr", Kind: pipeline.KindNarration},
		stepRef:  timelineKey.String(),
		parseJob: &pipeline.Job{ID: "job-parse", Kind: pipeline.KindParse},
		pageRef:  manifestKey.String(),
	}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewPlaybackServiceClient(http.DefaultClient, server.URL)

	resp, err := client.GetNarration(context.Background(), authRequest(&pptsv1.GetNarrationRequest{ProjectId: "project-1"}))
	if err != nil {
		t.Fatalf("GetNarration: %v", err)
	}
	if !resp.Msg.GetReady() || len(resp.Msg.GetPagePngKeys()) != 1 || resp.Msg.GetPagePngKeys()[0] != pageKey.String() {
		t.Fatalf("response = %+v", resp.Msg)
	}
}

// narrationJobStub 是 GetNarration 专用 stub：精确控制最近成功任务与 timeline ref。
type narrationJobStub struct {
	job         *pipeline.Job
	stepRef     string
	jobNotFound bool
	parseJob    *pipeline.Job
	pageRef     string
}

func (s *narrationJobStub) Create(_ context.Context, tenantID, projectID, _ string, idemKey, snapshot string, _ time.Time) (*pipeline.Job, error) {
	job := s.job
	if job == nil {
		job = &pipeline.Job{ID: "job-narr", InputSnapshot: snapshot}
	}
	return job, nil
}

func (s *narrationJobStub) LatestSucceededJob(_ context.Context, _, _, kind string) (*pipeline.Job, error) {
	if kind == string(pipeline.KindParse) {
		if s.parseJob == nil {
			return nil, pipeline.ErrNoSucceededJob
		}
		return s.parseJob, nil
	}
	if s.jobNotFound || s.job == nil {
		return nil, pipeline.ErrNoSucceededJob
	}
	return s.job, nil
}

func (s *narrationJobStub) StepResultRef(_ context.Context, _ string, stepType string) (string, error) {
	if stepType == "pages" {
		return s.pageRef, nil
	}
	return s.stepRef, nil
}

func (s *narrationJobStub) Get(context.Context, string, string) (*pipeline.Job, error) {
	return nil, pipeline.ErrJobNotFound
}

func (s *narrationJobStub) List(context.Context, string, string, string, string, int) ([]*pipeline.Job, string, error) {
	return nil, "", nil
}

func (s *narrationJobStub) Cancel(context.Context, string, string) (*pipeline.Job, error) {
	return nil, pipeline.ErrJobNotFound
}

func (s *narrationJobStub) RetryFailed(context.Context, string, string) (*pipeline.Job, error) {
	return nil, pipeline.ErrJobNotFound
}

func TestProjectGetSlidesReadsParsedDocument(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "解析项目", CurrentRevision: 2, CreatedAt: time.Unix(90, 0),
	}}}
	objects := testObjects(t)
	docKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1",
		Revision: "src-02", AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	doc := `{"schemaVersion":"1.0","pages":[
		{"index":0,"slideId":"slide-1","name":"首页","notesText":"开场介绍", "featureFlags":["picture"]},
		{"index":1,"slideId":"slide-2","shapes":[{"text":"PCIe 5.0 性能"}]}
	],"features":{"pageCount":2}}`
	putAPIObject(t, objects, docKey, []byte(doc), "application/json")
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewProjectServiceClient(http.DefaultClient, server.URL)

	// 未指定版本 → 使用项目当前版本 2。
	resp, err := client.GetSlides(context.Background(), authRequest(&pptsv1.GetSlidesRequest{ProjectId: "project-1"}))
	if err != nil {
		t.Fatalf("GetSlides: %v", err)
	}
	if resp.Msg.GetRevisionNo() != 2 || len(resp.Msg.GetSlides()) != 2 {
		t.Fatalf("response = %+v", resp.Msg)
	}
	if resp.Msg.GetSlides()[0].GetSlideId() != "slide-1" || resp.Msg.GetSlides()[0].GetPreview() != "开场介绍" ||
		len(resp.Msg.GetSlides()[0].GetFeatureFlags()) != 1 {
		t.Fatalf("slide[0] = %+v", resp.Msg.GetSlides()[0])
	}
	if resp.Msg.GetSlides()[1].GetSlideId() != "slide-2" || resp.Msg.GetSlides()[1].GetPreview() != "PCIe 5.0 性能" {
		t.Fatalf("slide[1] = %+v", resp.Msg.GetSlides()[1])
	}

	// 指定版本读取不存在 → NotFound。
	_, err = client.GetSlides(context.Background(), authRequest(&pptsv1.GetSlidesRequest{ProjectId: "project-1", RevisionNo: 99}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing revision code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestGenerateDraftEnqueuesOriginalModeJob(t *testing.T) {
	store := &fakeScriptStore{}
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewScriptServiceClient(http.DefaultClient, server.URL)

	req := authRequest(&pptsv1.GenerateDraftRequest{
		ProjectId: "project-1", SlideIds: []string{"slide-1"}, Mode: pptsv1.ScriptMode_SCRIPT_MODE_ORIGINAL,
	})
	req.Header().Set("Idempotency-Key", "draft-1")
	resp, err := client.GenerateDraft(context.Background(), req)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if jobs.idempotencyKey != "draft-1" || jobs.projectID != "project-1" {
		t.Fatalf("job identity = %q/%q", jobs.tenantID, jobs.projectID)
	}
	var snap app.ScriptDraftSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snap); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.ProjectID != "project-1" || snap.Mode != "original" || len(snap.SlideIDs) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if resp.Msg.GetJobId() != "job-1" || !resp.Msg.GetFullySupported() {
		t.Fatalf("response = %+v", resp.Msg)
	}

	// polish 模式进入 G2：允许入队，由 worker 侧 LLM 配置决定执行能力。
	polishReq := authRequest(&pptsv1.GenerateDraftRequest{
		ProjectId: "project-1", Mode: pptsv1.ScriptMode_SCRIPT_MODE_POLISH,
	})
	polishReq.Header().Set("Idempotency-Key", "draft-2")
	if _, err = client.GenerateDraft(context.Background(), polishReq); err != nil {
		t.Fatalf("GenerateDraft polish: %v", err)
	}
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snap); err != nil {
		t.Fatalf("polish snapshot: %v", err)
	}
	if snap.Mode != "polish" {
		t.Fatalf("polish snapshot = %+v", snap)
	}

	aiReq := authRequest(&pptsv1.GenerateDraftRequest{
		ProjectId: "project-1", Mode: pptsv1.ScriptMode_SCRIPT_MODE_AI_GENERATED,
	})
	aiReq.Header().Set("Idempotency-Key", "draft-3")
	if _, err = client.GenerateDraft(context.Background(), aiReq); err != nil {
		t.Fatalf("GenerateDraft ai_generated: %v", err)
	}
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snap); err != nil {
		t.Fatalf("ai snapshot: %v", err)
	}
	if snap.Mode != "ai_generated" {
		t.Fatalf("ai snapshot = %+v", snap)
	}
}

func TestUploadDirectFlow(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "上传项目", CreatedAt: time.Unix(90, 0),
	}}}
	jobs := &jobCreatorStub{}
	objects := testObjects(t)
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewUploadServiceClient(http.DefaultClient, server.URL)

	created, err := client.CreateUpload(context.Background(), authRequest(&pptsv1.CreateUploadRequest{
		ProjectId: "project-1", Filename: "demo.pptx", SizeBytes: 8,
	}))
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	uploadID := created.Msg.GetUploadId()
	if uploadID == "" || created.Msg.GetObjectKey() == "" || len(created.Msg.GetSignedUploadUrls()) != 1 {
		t.Fatalf("create = %+v", created.Msg)
	}
	url := created.Msg.GetSignedUploadUrls()[0]
	if !strings.HasPrefix(url, "/ppts/object/") {
		t.Fatalf("local upload url not rewritten: %q", url)
	}

	// 通过签名对象端点写入正文。
	put, err := http.NewRequest(http.MethodPut, server.URL+url, bytes.NewReader([]byte("PPTXDATA")))
	if err != nil {
		t.Fatal(err)
	}
	put.Header.Set("Content-Type", "application/octet-stream")
	presp, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d", presp.StatusCode)
	}

	sum := sha256.Sum256([]byte("PPTXDATA"))
	hash := hex.EncodeToString(sum[:])
	completed, err := client.CompleteUpload(context.Background(), authRequest(&pptsv1.CompleteUploadRequest{
		UploadId: uploadID, ExpectedHash: hash, SizeBytes: 8,
	}))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if completed.Msg.GetSourceRevisionId() != "rev-1" || completed.Msg.GetJobId() != "job-1" {
		t.Fatalf("complete = %+v", completed.Msg)
	}
	if jobs.idempotencyKey != uploadID {
		t.Fatalf("parse idem key = %q want upload id %q", jobs.idempotencyKey, uploadID)
	}
	var snap app.ParseSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snap); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.SourceRevisionID != "rev-1" || snap.ObjectKey == "" {
		t.Fatalf("snapshot = %+v", snap)
	}

	// 幂等：再次完成返回同一结果。
	again, err := client.CompleteUpload(context.Background(), authRequest(&pptsv1.CompleteUploadRequest{
		UploadId: uploadID, ExpectedHash: hash, SizeBytes: 8,
	}))
	if err != nil {
		t.Fatalf("CompleteUpload#2: %v", err)
	}
	if again.Msg.GetSourceRevisionId() != "rev-1" || again.Msg.GetJobId() != "job-1" {
		t.Fatalf("idempotent complete = %+v", again.Msg)
	}
}

func TestUploadRejectsHashMismatchAndAbort(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "上传项目", CreatedAt: time.Unix(90, 0),
	}}}
	jobs := &jobCreatorStub{}
	objects := testObjects(t)
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects, nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewUploadServiceClient(http.DefaultClient, server.URL)

	created, err := client.CreateUpload(context.Background(), authRequest(&pptsv1.CreateUploadRequest{
		ProjectId: "project-1", Filename: "demo.pptx", SizeBytes: 8,
	}))
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	url := created.Msg.GetSignedUploadUrls()[0]
	put, _ := http.NewRequest(http.MethodPut, server.URL+url, bytes.NewReader([]byte("PPTXDATA")))
	presp, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	presp.Body.Close()

	// 错误哈希 → InvalidArgument。
	_, err = client.CompleteUpload(context.Background(), authRequest(&pptsv1.CompleteUploadRequest{
		UploadId: created.Msg.GetUploadId(), ExpectedHash: "ffff", SizeBytes: 8,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("hash mismatch code = %v, err=%v", connect.CodeOf(err), err)
	}

	// 大小不符 → InvalidArgument。
	_, err = client.CompleteUpload(context.Background(), authRequest(&pptsv1.CompleteUploadRequest{
		UploadId: created.Msg.GetUploadId(), ExpectedHash: "ffff", SizeBytes: 999,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("size mismatch code = %v, err=%v", connect.CodeOf(err), err)
	}

	// 中止后不能再完成。
	if _, err := client.AbortUpload(context.Background(), authRequest(&pptsv1.AbortUploadRequest{UploadId: created.Msg.GetUploadId()})); err != nil {
		t.Fatalf("AbortUpload: %v", err)
	}
	sum := sha256.Sum256([]byte("PPTXDATA"))
	_, err = client.CompleteUpload(context.Background(), authRequest(&pptsv1.CompleteUploadRequest{
		UploadId: created.Msg.GetUploadId(), ExpectedHash: hex.EncodeToString(sum[:]), SizeBytes: 8,
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("complete aborted code = %v, err=%v", connect.CodeOf(err), err)
	}
}

// TestSPAFallbackServesWebRoot 覆盖单二进制部署下前端静态资源的托管语义：
// 客户端路由回退 index.html、哈希资源强缓存、缺失资源 404 而非误回退。
func TestSPAFallbackServesWebRoot(t *testing.T) {
	webRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<html>app</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(webRoot, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "assets", "app-abc123.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := spaFallbackHandler(os.DirFS(webRoot))

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
		wantCache  string
	}{
		{"客户端路由回退 index.html", http.MethodGet, "/projects/abc/timeline", http.StatusOK, "<html>app</html>", ""},
		{"哈希资源命中并强缓存", http.MethodGet, "/assets/app-abc123.js", http.StatusOK, "console.log(1)", "public, max-age=31536000, immutable"},
		{"缺失的静态资源 404 而不回退", http.MethodGet, "/assets/missing.js", http.StatusNotFound, "", ""},
		{"系统前缀不参与回退", http.MethodGet, "/healthz", http.StatusNotFound, "", ""},
		{"HEAD 与 GET 同权（同 nginx 拓扑）", http.MethodHead, "/assets/app-abc123.js", http.StatusOK, "", "public, max-age=31536000, immutable"},
		{"HEAD 客户端路由同样回退", http.MethodHead, "/projects/abc/timeline", http.StatusOK, "", ""},
		{"非 GET/HEAD 一律不服务", http.MethodPost, "/assets/app-abc123.js", http.StatusNotFound, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body = %q, want contains %q", rec.Body.String(), tc.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.wantCache)
			}
		})
	}
}

// TestSPAFallbackServesEmbeddedFS 覆盖内嵌前端产物（单二进制分发）的托管语义：
// WebFS 与 WebRoot 走同一 handler，且 NewHandler 优先使用 WebRoot、空时退回 WebFS。
func TestSPAFallbackServesEmbeddedFS(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":          {Data: []byte("<html>embedded</html>")},
		"assets/app-def456.js": {Data: []byte("console.log(2)")},
	}
	handler := spaFallbackHandler(dist)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/console/settings", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html>embedded</html>") {
		t.Fatalf("spa route status = %d body = %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/assets/app-def456.js", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("asset status = %d cache = %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	// NewHandler 在 WebRoot 为空时挂载 WebFS。
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{WebFS: dist}))
	t.Cleanup(server.Close)
	resp, err := http.Get(server.URL + "/console/settings")
	if err != nil {
		t.Fatalf("spa route: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html>embedded</html>") {
		t.Fatalf("spa route status = %d body = %q", resp.StatusCode, body)
	}
}

// TestHandlerMountsSPAFallbackWithoutShadowingAPI 断言 WebRoot 接线不会遮蔽已注册路由。
func TestHandlerMountsSPAFallbackWithoutShadowingAPI(t *testing.T) {
	webRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<html>app</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil, Options{WebRoot: webRoot}))
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}

	spaResp, err := http.Get(server.URL + "/projects/some-id/timeline")
	if err != nil {
		t.Fatalf("spa route: %v", err)
	}
	defer spaResp.Body.Close()
	body, _ := io.ReadAll(spaResp.Body)
	if spaResp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html>app</html>") {
		t.Fatalf("spa route status = %d body = %q", spaResp.StatusCode, body)
	}

	// 未配置 WebRoot 时保持旧行为：无 SPA 兜底，客户端路由 404。
	plain := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(plain.Close)
	plainResp, err := http.Get(plain.URL + "/projects/some-id/timeline")
	if err != nil {
		t.Fatalf("plain spa route: %v", err)
	}
	defer plainResp.Body.Close()
	if plainResp.StatusCode != http.StatusNotFound {
		t.Fatalf("plain spa route status = %d, want 404", plainResp.StatusCode)
	}
}
