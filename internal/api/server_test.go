package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func (*fakeProjectStore) CreateSourceRevision(context.Context, string, project.NewSourceRevision) (*project.SourceRevision, error) {
	return nil, errors.New("not used")
}

func (*fakeProjectStore) GetSourceRevision(context.Context, string, string, int) (*project.SourceRevision, error) {
	return nil, errors.New("not used")
}

type jobCreatorStub struct {
	job            *pipeline.Job
	err            error
	tenantID       string
	projectID      string
	idempotencyKey string
	inputSnapshot  string
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(projects, &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, store, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, store, jobs, &fakeArtifactStore{}, testObjects(t)))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, store, jobs, &fakeArtifactStore{}, testObjects(t)))
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

func TestCreateExportPersistsFixedSnapshotAndRejectsCrossTenantKeys(t *testing.T) {
	store := &fakeScriptStore{revision: newTestRevision()}
	jobs := &jobCreatorStub{}
	objects := testObjects(t)
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, store, jobs, &fakeArtifactStore{}, objects))
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, &fakeScriptStore{}, &jobCreatorStub{}, artifacts, objects))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewExportServiceClient(http.DefaultClient, server.URL)

	resp, err := client.CreateDownload(context.Background(), authRequest(&pptsv1.CreateDownloadRequest{ArtifactId: "artifact-1", TtlSeconds: 60}))
	if err != nil {
		t.Fatalf("CreateDownload: %v", err)
	}
	if resp.Msg.GetSignedUrl() == "" || resp.Msg.GetExpiresAtUnix() <= time.Now().Unix() {
		t.Fatalf("download = %+v", resp.Msg)
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
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, objects))
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
		counts[res.GetType()]++
	}
	if counts[pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_TIMELINE] != 1 || counts[pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_PAGE_PNG] != 1 || counts[pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_AUDIO] != 1 {
		t.Fatalf("counts = %v", counts)
	}

	_, err = client.GetManifest(context.Background(), authRequest(&pptsv1.GetPlaybackManifestRequest{
		ProjectId: "project-1", TimelineKey: timelineKey.String(), TtlSeconds: 60,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("page count code=%v err=%v", connect.CodeOf(err), err)
	}
}

func putAPIObject(t *testing.T, objects objectstore.ObjectStore, key objectstore.ObjectKey, data []byte, contentType string) {
	t.Helper()
	if err := objects.Put(context.Background(), key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: contentType, ContentHash: "hash", Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
}
