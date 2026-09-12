//go:build pg

package app

import (
	"bytes"
	"context"
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
		"TRUNCATE uploads, jobs, job_steps, source_revisions, projects, tenants RESTART IDENTITY CASCADE"); err != nil {
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

func TestIngestCreatesRevisionAndJob(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	svc := NewIngestService(env.projects, env.jobs, env.objects)

	res, err := svc.Ingest(ctx, appTenant, appProject, bytes.NewReader(deckBytes(t)))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.SourceRevision.RevisionNo != 1 || res.ParseJob.Kind != pipeline.KindParse {
		t.Fatalf("result: %+v", res.SourceRevision)
	}
	if res.ParseJob.State != pipeline.StateQueued {
		t.Fatalf("job not queued: %+v", res.ParseJob)
	}
	// 幂等：同源数据再次入队 → 复用同一 parse 任务（按源哈希），源版本递增。
	res2, err := svc.Ingest(ctx, appTenant, appProject, bytes.NewReader(deckBytes(t)))
	if err != nil {
		t.Fatalf("Ingest#2: %v", err)
	}
	if res2.ParseJob.ID == res.ParseJob.ID {
		if res2.SourceRevision.RevisionNo != 2 {
			t.Fatalf("same-hash parse job reused but revision not bumped: rev=%d", res2.SourceRevision.RevisionNo)
		}
	} else {
		t.Fatalf("same-hash re-ingest created a different parse job (idempotency lost): %s vs %s",
			res.ParseJob.ID, res2.ParseJob.ID)
	}
}

func TestIngestParseVertical(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	svc := NewIngestService(env.projects, env.jobs, env.objects)
	res, err := svc.Ingest(ctx, appTenant, appProject, bytes.NewReader(deckBytes(t)))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	parse := NewParseHandler(env.objects, project.NewGoPPTXReader(project.Limits{}))
	worker := pipeline.NewWorker(env.jobs, "app-wk-1", appTenant, parse.Handle, pipeline.WorkerOptions{Poll: 20 * time.Millisecond})
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = worker.Run(runCtx)

	done, err := env.jobs.Get(ctx, res.ParseJob.ID, appTenant)
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
