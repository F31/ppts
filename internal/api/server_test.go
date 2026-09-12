package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
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
}

type fakeArtifactStore struct {
	artifact *artifact.Artifact
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
			SourceRefs: []string{"slide-1/shape-1"}, Status: narration.StatusDraft,
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}

	client := pptsv1connect.NewScriptServiceClient(http.DefaultClient, server.URL)
	_, err = client.Get(context.Background(), connect.NewRequest(&pptsv1.GetScriptRequest{
		ProjectId: "project-1", SlideId: "slide-1",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestProjectServiceCreateListAndArchive(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "旧项目", CreatedAt: time.Unix(90, 0),
	}}}
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
}

func TestScriptUpdateReturnsLatestOnConflict(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), Options{Quota: quota}))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), Options{Quota: quota}))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t), Options{Quota: quota}))
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

func TestJobServiceGetListCancelRetry(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindNarration,
		State: pipeline.StateRunning, Attempt: 2, Progress: 40, InputSnapshot: "snap",
		CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(20, 0),
		LastError: &pipeline.JobError{Code: "throttled", Retryable: true, RetryAfterSeconds: 5},
	}
	jobs := &jobCreatorStub{job: job, listJobs: []*pipeline.Job{job}, listNext: "cursor-2"}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t)))
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

func TestJobServiceNotFoundAndNotCancelable(t *testing.T) {
	jobs := &jobCreatorStub{} // job == nil → ErrJobNotFound
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, testObjects(t)))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewJobServiceClient(http.DefaultClient, server.URL)

	if _, err := client.Get(context.Background(), authRequest(&pptsv1.GetJobRequest{JobId: "missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get missing code = %v", connect.CodeOf(err))
	}

	notCancelable := &jobCreatorStub{err: pipeline.ErrJobNotCancelable}
	server2 := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, notCancelable, &fakeArtifactStore{}, testObjects(t)))
	t.Cleanup(server2.Close)
	client2 := pptsv1connect.NewJobServiceClient(http.DefaultClient, server2.URL)
	if _, err := client2.Cancel(context.Background(), authRequest(&pptsv1.CancelJobRequest{JobId: "job-1"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Cancel not-cancelable code = %v", connect.CodeOf(err))
	}
}

// fakeTenantUsage / fakeTenantPolicy 用于 TenantService 只读接口测试。
type fakeTenantUsage struct {
	quota   *usage.Quota
	seconds float64
	cost    float64
	err     error
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

func (f *fakeTenantUsage) UsageSummary(context.Context, string, string) (float64, float64, error) {
	if f.err != nil {
		return 0, 0, f.err
	}
	return f.seconds, f.cost, nil
}

type fakeTenantPolicy struct {
	policy *tenant.Policy
	err    error
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

func TestTenantServiceQuotaUsagePolicy(t *testing.T) {
	u := &fakeTenantUsage{
		quota:   &usage.Quota{TenantID: "tenant-1", Kind: usage.KindGenSeconds, LimitUnits: 3600, ConsumedUnits: 120, ReservedUnits: 30},
		seconds: 120, cost: 0,
	}
	p := &fakeTenantPolicy{policy: &tenant.Policy{
		StorageBackend: "s3", StorageRegion: "cn-north-1", SourceRetentionDays: 30,
		MaxConcurrentJobs: 4, MaxStorageBytes: 1 << 30,
	}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), Options{Usage: u, Policy: p}))
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
	if usageResp.Msg.GetSecondsUsed() != 120 {
		t.Fatalf("usage = %+v", usageResp.Msg)
	}

	policy, err := client.Policy(context.Background(), authRequest(&pptsv1.GetPolicyRequest{}))
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if policy.Msg.GetStorageBackend() != "s3" || policy.Msg.GetStorageRegion() != "cn-north-1" ||
		policy.Msg.GetSourceRetentionDays() != 30 {
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, objects))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, artifacts, objects))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, objects))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects))
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
	server2 := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, notReady, &fakeArtifactStore{}, testObjects(t)))
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

// narrationJobStub 是 GetNarration 专用 stub：精确控制最近成功任务与 timeline ref。
type narrationJobStub struct {
	job         *pipeline.Job
	stepRef     string
	jobNotFound bool
}

func (s *narrationJobStub) Create(_ context.Context, tenantID, projectID, _ string, idemKey, snapshot string, _ time.Time) (*pipeline.Job, error) {
	job := s.job
	if job == nil {
		job = &pipeline.Job{ID: "job-narr", InputSnapshot: snapshot}
	}
	return job, nil
}

func (s *narrationJobStub) LatestSucceededJob(context.Context, string, string, string) (*pipeline.Job, error) {
	if s.jobNotFound || s.job == nil {
		return nil, pipeline.ErrNoSucceededJob
	}
	return s.job, nil
}

func (s *narrationJobStub) StepResultRef(context.Context, string, string) (string, error) {
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
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, objects))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), store, jobs, &fakeArtifactStore{}, testObjects(t)))
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

	// polish 模式在 G1 未实现 → Unimplemented。
	polishReq := authRequest(&pptsv1.GenerateDraftRequest{
		ProjectId: "project-1", Mode: pptsv1.ScriptMode_SCRIPT_MODE_POLISH,
	})
	polishReq.Header().Set("Idempotency-Key", "draft-2")
	_, err = client.GenerateDraft(context.Background(), polishReq)
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("polish mode code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestUploadDirectFlow(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "上传项目", CreatedAt: time.Unix(90, 0),
	}}}
	jobs := &jobCreatorStub{}
	objects := testObjects(t)
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects))
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
	server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects))
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
