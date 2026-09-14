//go:build pg

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/F31/go-pptx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/upload"
)

const (
	appTenant  = "00000000-0000-0000-0000-00000000000a"
	appProject = "00000000-0000-0000-0000-0000000000aa"
)

type appEnv struct {
	pool     *pgxpool.Pool
	projects *project.PGProjectStore
	jobs     *pipeline.PGStore
	objects  *objectstore.LocalFS
	uploads  *upload.PGUploadStore
	root     string
}

func setupApp(t *testing.T) *appEnv {
	t.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE uploads, jobs, job_steps, narration_segments, narration_scripts, source_revisions, projects, tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", []any{appTenant, "app-test"}},
	} {
		if _, err := pool.Exec(context.Background(), q.sql, q.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := tenant.Run(context.Background(), pool, appTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,$3,$3) ON CONFLICT DO NOTHING",
			appProject, appTenant, "tester")
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	root := t.TempDir()
	jobs, err := pipeline.NewPGStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("jobs store: %v", err)
	}
	t.Cleanup(jobs.Close)
	return &appEnv{
		pool: pool, projects: project.NewPGProjectStore(pool),
		jobs: jobs, objects: objectstore.NewLocal(root, []byte("test-secret")),
		uploads: upload.NewPGUploadStore(pool), root: root,
	}
}

func deckBytes(t *testing.T) []byte {
	t.Helper()
	p, err := pptx.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()
	layouts, _ := p.Layouts()
	slide, err := p.AddSlide(layouts[0])
	if err != nil {
		t.Fatalf("AddSlide: %v", err)
	}
	if _, err := slide.AddTextBox(pptx.TextBoxSpec{
		X: 914400, Y: 914400, Width: 6000000, Height: 914400, Text: "入库测试 PCIe 5.0",
	}); err != nil {
		t.Fatalf("AddTextBox: %v", err)
	}
	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

// uploadDeck 走真实上传链路（CreateUpload → Put → CompleteUpload）入库，
// 返回源版本与解析任务。
func uploadDeck(t *testing.T, env *appEnv, revisionNo int, data []byte) (*project.SourceRevision, *pipeline.Job) {
	t.Helper()
	ctx := context.Background()
	svc := NewUploadService(env.uploads, env.projects, env.jobs, env.objects)
	res, err := svc.CreateUpload(ctx, appTenant, UploadRequest{
		ProjectID: appProject, Filename: "deck.pptx", SizeBytes: int64(len(data)),
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	key, err := objectstore.Parse(res.ObjectKey)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if err := env.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sum := sha256.Sum256(data)
	_, jobID, err := svc.CompleteUpload(ctx, appTenant, res.Session.ID, hex.EncodeToString(sum[:]), int64(len(data)))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	rev, err := env.projects.GetSourceRevision(ctx, appTenant, appProject, revisionNo)
	if err != nil {
		t.Fatalf("GetSourceRevision: %v", err)
	}
	job, err := env.jobs.Get(ctx, jobID, appTenant)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	return rev, job
}

func TestIngestCreatesRevisionAndJob(t *testing.T) {
	env := setupApp(t)
	data := deckBytes(t)

	rev, job := uploadDeck(t, env, 1, data)
	if rev.RevisionNo != 1 || job.Kind != pipeline.KindParse {
		t.Fatalf("result: %+v %+v", rev, job)
	}
	if job.State != pipeline.StateQueued {
		t.Fatalf("job not queued: %+v", job)
	}
	// 再次入库同源数据：新会话创建新版本，解析任务可独立发现。
	rev2, job2 := uploadDeck(t, env, 2, data)
	if rev2.RevisionNo != 2 {
		t.Fatalf("re-upload did not bump revision: rev=%d", rev2.RevisionNo)
	}
	if job2.ID == job.ID {
		t.Fatalf("re-upload reused parse job id: %s", job2.ID)
	}
}

func TestIngestParseVertical(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	_, parseJob := uploadDeck(t, env, 1, deckBytes(t))

	parse := NewParseHandler(env.objects, project.NewGoPPTXReader(project.Limits{}))
	worker := pipeline.NewWorker(env.jobs, "app-wk-1", appTenant, parse.Handle, pipeline.WorkerOptions{Poll: 20 * time.Millisecond})
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = worker.Run(runCtx)

	done, err := env.jobs.Get(ctx, parseJob.ID, appTenant)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	if done.State != pipeline.StateSucceeded {
		t.Fatalf("parse job: %+v", done)
	}

	// 解析产物可读且含页面。
	outKey := objectstore.ObjectKey{
		TenantID: appTenant, ProjectID: appProject,
		Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := env.objects.Get(ctx, outKey)
	if err != nil {
		t.Fatalf("Get extracted: %v", err)
	}
	defer rc.Close()
	var outDoc struct {
		ParserVersion string            `json:"parserVersion"`
		Pages         []json.RawMessage `json:"pages"`
		Features      *struct {
			PageCount int `json:"pageCount"`
		} `json:"features"`
	}
	if err := json.NewDecoder(rc).Decode(&outDoc); err != nil {
		t.Fatalf("decode extracted: %v", err)
	}
	if outDoc.ParserVersion == "" || len(outDoc.Pages) != 1 || outDoc.Features == nil || outDoc.Features.PageCount != 1 {
		t.Fatalf("extracted doc unexpected: %+v", map[string]any{
			"parser": outDoc.ParserVersion, "pages": len(outDoc.Pages), "features": outDoc.Features,
		})
	}
}
