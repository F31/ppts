//go:build pg

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/F31/go-pptx"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/upload"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	e2eTenant = "00000000-0000-0000-0000-0000000000e2"
	e2eUser   = "e2e-user"
)

func e2eAuth[T any](msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set(tenantHeader, e2eTenant)
	req.Header().Set(userHeader, e2eUser)
	return req
}

func setupE2E(t *testing.T) (*pgxpool.Pool, *pipeline.PGStore, objectstore.ObjectStore) {
	t.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx,
		"TRUNCATE uploads, jobs, job_steps, source_revisions, projects, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO tenants(id,name) VALUES ($1,$2)", e2eTenant, "e2e"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	jobs, err := pipeline.NewPGStore(ctx, dsn)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	t.Cleanup(jobs.Close)
	return pool, jobs, objectstore.NewLocal(t.TempDir(), []byte("e2e-secret"))
}

// startE2EWorker 运行真实 worker 循环，分发 G1 的 parse/script_draft/narration/export 任务。
func startE2EWorker(t *testing.T, pool *pgxpool.Pool, jobs *pipeline.PGStore, objects objectstore.ObjectStore) {
	t.Helper()
	np := narration.NewPGStore(pool)
	parseHandler := app.NewParseHandler(objects, project.NewGoPPTXReader(project.Limits{}))
	draftHandler := app.NewScriptDraftHandler(np, objects)
	narrationHandler := app.NewNarrationHandler(np, jobs, objects, tts.NewFakeProvider())
	exportHandler := app.NewExportHandler(artifact.NewPGStore(pool), jobs, objects, nil)
	dispatch := func(ctx context.Context, job *pipeline.Job) error {
		switch job.Kind {
		case pipeline.KindParse:
			return parseHandler.Handle(ctx, job)
		case pipeline.KindScriptDraft:
			return draftHandler.Handle(ctx, job)
		case pipeline.KindNarration:
			return narrationHandler.Handle(ctx, job)
		case pipeline.KindExport:
			return exportHandler.Handle(ctx, job)
		default:
			return fmt.Errorf("e2e worker: unsupported kind %q", job.Kind)
		}
	}
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker := pipeline.NewWorker(jobs, "e2e-worker", e2eTenant, dispatch, pipeline.WorkerOptions{Poll: 20 * time.Millisecond})
	go func() { _ = worker.Run(runCtx) }()
}

func e2eDeck(t *testing.T) []byte {
	t.Helper()
	p, err := pptx.New()
	if err != nil {
		t.Fatalf("pptx.New: %v", err)
	}
	defer p.Close()
	layouts, _ := p.Layouts()
	slide, err := p.AddSlide(layouts[0])
	if err != nil {
		t.Fatalf("AddSlide: %v", err)
	}
	if _, err := slide.AddTextBox(pptx.TextBoxSpec{
		X: 914400, Y: 914400, Width: 6000000, Height: 914400, Text: "端到端验收 PCIe 5.0",
	}); err != nil {
		t.Fatalf("AddTextBox: %v", err)
	}
	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

// TestE2ERealChainOverHTTP 是 G1 真实链路端到端验收：
// CreateProject → CreateUpload → PUT → CompleteUpload → 解析 → GetSlides →
// GenerateDraft → GetScript → CreateGeneration → GetNarration → GetManifest →
// CreateExport(SRT) → GetArtifact → CreateDownload → 下载正文。
// 走真实 PG + 本地对象存储 + 真实 worker，仅 TTS 用 fake（正式供应商受凭据阻塞）。
func TestE2ERealChainOverHTTP(t *testing.T) {
	ctx := context.Background()
	pool, jobs, objects := setupE2E(t)
	startE2EWorker(t, pool, jobs, objects)

	server := httptest.NewServer(NewHandler(
		project.NewPGProjectStore(pool), upload.NewPGUploadStore(pool),
		narration.NewPGStore(pool), jobs, artifact.NewPGStore(pool), objects,
	))
	t.Cleanup(server.Close)
	hc := http.DefaultClient

	projectClient := pptsv1connect.NewProjectServiceClient(hc, server.URL)
	uploadClient := pptsv1connect.NewUploadServiceClient(hc, server.URL)
	scriptClient := pptsv1connect.NewScriptServiceClient(hc, server.URL)
	narrationClient := pptsv1connect.NewNarrationServiceClient(hc, server.URL)
	playbackClient := pptsv1connect.NewPlaybackServiceClient(hc, server.URL)
	exportClient := pptsv1connect.NewExportServiceClient(hc, server.URL)

	// 1) 建项目。
	created, err := projectClient.Create(ctx, e2eAuth(&pptsv1.CreateProjectRequest{Title: "E2E 验收"}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	projectID := created.Msg.GetProject().GetId()
	if projectID == "" {
		t.Fatalf("empty project id: %+v", created.Msg)
	}

	// 2) 授权直传：CreateUpload → PUT 正文 → CompleteUpload。
	deck := e2eDeck(t)
	sum := sha256.Sum256(deck)
	hash := hex.EncodeToString(sum[:])
	ses, err := uploadClient.CreateUpload(ctx, e2eAuth(&pptsv1.CreateUploadRequest{
		ProjectId: projectID, Filename: "deck.pptx", SizeBytes: int64(len(deck)),
	}))
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if len(ses.Msg.GetSignedUploadUrls()) != 1 {
		t.Fatalf("upload urls = %+v", ses.Msg)
	}
	putReq, err := http.NewRequest(http.MethodPut, server.URL+ses.Msg.GetSignedUploadUrls()[0], bytes.NewReader(deck))
	if err != nil {
		t.Fatal(err)
	}
	putReq.Header.Set("Content-Type", "application/octet-stream")
	putResp, err := hc.Do(putReq)
	if err != nil {
		t.Fatalf("PUT object: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d", putResp.StatusCode)
	}
	completed, err := uploadClient.CompleteUpload(ctx, e2eAuth(&pptsv1.CompleteUploadRequest{
		UploadId: ses.Msg.GetUploadId(), ExpectedHash: hash, SizeBytes: int64(len(deck)),
	}))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if completed.Msg.GetSourceRevisionId() == "" || completed.Msg.GetJobId() == "" {
		t.Fatalf("complete = %+v", completed.Msg)
	}

	// 3) 解析完成 → GetSlides 出现页面。
	slides := waitSlides(t, ctx, projectClient, projectID)
	if len(slides) == 0 || slides[0].GetSlideId() == "" {
		t.Fatalf("slides = %+v", slides)
	}
	slideID := slides[0].GetSlideId()

	// 4) 原文讲稿生成 → GetScript 出现草稿。
	draftReq := e2eAuth(&pptsv1.GenerateDraftRequest{
		ProjectId: projectID, SlideIds: []string{slideID}, Mode: pptsv1.ScriptMode_SCRIPT_MODE_ORIGINAL,
	})
	draftReq.Header().Set("Idempotency-Key", "e2e-draft")
	if _, err := scriptClient.GenerateDraft(ctx, draftReq); err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	script := waitScript(t, ctx, scriptClient, projectID, slideID)
	if len(script.GetSegments()) == 0 || script.GetSegments()[0].GetDisplayText() == "" {
		t.Fatalf("script = %+v", script)
	}

	// 5) 配音生成 → GetNarration ready。
	genReq := e2eAuth(&pptsv1.CreateGenerationRequest{
		ProjectId: projectID, SlideIds: []string{slideID}, VoiceId: "fake-voice-1",
	})
	genReq.Header().Set("Idempotency-Key", "e2e-narr")
	if _, err := narrationClient.CreateGeneration(ctx, genReq); err != nil {
		t.Fatalf("CreateGeneration: %v", err)
	}
	narr := waitNarration(t, ctx, playbackClient, projectID)
	if narr.GetTimelineKey() == "" {
		t.Fatalf("narration = %+v", narr)
	}

	// 6) 播放 manifest：无页面图时仍含 timeline + 音频 + SRT/VTT。
	manifest, err := playbackClient.GetManifest(ctx, e2eAuth(&pptsv1.GetPlaybackManifestRequest{
		ProjectId: projectID, TimelineKey: narr.GetTimelineKey(), TtlSeconds: 600,
	}))
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if manifest.Msg.GetTimelineJson() == "" {
		t.Fatalf("empty timeline json")
	}
	kinds := map[pptsv1.PlaybackResourceType]int{}
	for _, r := range manifest.Msg.GetResources() {
		if !strings.HasPrefix(r.GetSignedUrl(), "/ppts/object/") {
			t.Fatalf("resource url not rewritten: %+v", r)
		}
		kinds[r.GetType()]++
	}
	for _, want := range []pptsv1.PlaybackResourceType{
		pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_TIMELINE,
		pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_AUDIO,
		pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_SUBTITLE_SRT,
		pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_SUBTITLE_VTT,
	} {
		if kinds[want] == 0 {
			t.Fatalf("manifest missing resource %v: %+v", want, manifest.Msg.GetResources())
		}
	}
	// 浏览器实际拉取音频资源：验证本地签名链接可用。
	for _, r := range manifest.Msg.GetResources() {
		if r.GetType() != pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_AUDIO {
			continue
		}
		aresp, err := hc.Get(server.URL + r.GetSignedUrl())
		if err != nil {
			t.Fatalf("fetch audio resource: %v", err)
		}
		abytes, _ := io.ReadAll(aresp.Body)
		aresp.Body.Close()
		if aresp.StatusCode != http.StatusOK || len(abytes) == 0 {
			t.Fatalf("audio resource status=%d len=%d", aresp.StatusCode, len(abytes))
		}
		break
	}

	// 7) 导出 SRT → GetArtifact → CreateDownload → 实际下载正文。
	expReq := e2eAuth(&pptsv1.CreateExportRequest{
		ProjectId: projectID, Format: pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT, TimelineKey: narr.GetTimelineKey(),
	})
	expReq.Header().Set("Idempotency-Key", "e2e-export-srt")
	exp, err := exportClient.CreateExport(ctx, expReq)
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	artifactID := waitArtifact(t, ctx, jobs, exp.Msg.GetJobId())
	got, err := exportClient.GetArtifact(ctx, e2eAuth(&pptsv1.GetArtifactRequest{ArtifactId: artifactID}))
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.Msg.GetFormat() != pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT || got.Msg.GetSizeBytes() == 0 {
		t.Fatalf("artifact = %+v", got.Msg)
	}
	dl, err := exportClient.CreateDownload(ctx, e2eAuth(&pptsv1.CreateDownloadRequest{ArtifactId: artifactID, TtlSeconds: 300}))
	if err != nil {
		t.Fatalf("CreateDownload: %v", err)
	}
	resp, err := hc.Get(server.URL + dl.Msg.GetSignedUrl())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) == 0 || !bytes.Contains(body, []byte("-->")) {
		t.Fatalf("download body status=%d body=%q", resp.StatusCode, string(body))
	}
}

func waitSlides(t *testing.T, ctx context.Context, client pptsv1connect.ProjectServiceClient, projectID string) []*pptsv1.SlideSummary {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.GetSlides(ctx, e2eAuth(&pptsv1.GetSlidesRequest{ProjectId: projectID}))
		if err == nil && len(resp.Msg.GetSlides()) > 0 {
			return resp.Msg.GetSlides()
		}
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("GetSlides: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for parsed slides on project %s", projectID)
	return nil
}

func waitScript(t *testing.T, ctx context.Context, client pptsv1connect.ScriptServiceClient, projectID, slideID string) *pptsv1.ScriptRevision {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(ctx, e2eAuth(&pptsv1.GetScriptRequest{ProjectId: projectID, SlideId: slideID}))
		if err == nil && len(resp.Msg.GetSegments()) > 0 {
			return resp.Msg
		}
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("GetScript: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for script on slide %s", slideID)
	return nil
}

func waitNarration(t *testing.T, ctx context.Context, client pptsv1connect.PlaybackServiceClient, projectID string) *pptsv1.GetNarrationResponse {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.GetNarration(ctx, e2eAuth(&pptsv1.GetNarrationRequest{ProjectId: projectID}))
		if err == nil && resp.Msg.GetReady() {
			return resp.Msg
		}
		if err != nil {
			t.Fatalf("GetNarration: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for narration on project %s", projectID)
	return nil
}

func waitArtifact(t *testing.T, ctx context.Context, jobs *pipeline.PGStore, jobID string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		job, err := jobs.Get(ctx, jobID, e2eTenant)
		if err != nil {
			t.Fatalf("job get: %v", err)
		}
		if job.State == pipeline.StateSucceeded {
			ref, err := jobs.StepResultRef(ctx, jobID, "export")
			if err != nil {
				t.Fatalf("StepResultRef: %v", err)
			}
			if ref == "" {
				t.Fatalf("export succeeded without artifact ref")
			}
			return ref
		}
		if job.State == pipeline.StateFailed {
			t.Fatalf("export job failed: %+v", job)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for export job %s", jobID)
	return ""
}
