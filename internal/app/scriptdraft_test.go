//go:build pg

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// runParseWorker 执行已入队的 parse 任务（复用 create deck 的 ingest）。
func runParseWorker(t *testing.T, env *appEnv, jobID string) {
	t.Helper()
	parse := NewParseHandler(env.objects, project.NewGoPPTXReader(project.Limits{}))
	worker := pipeline.NewWorker(env.jobs, "scriptdraft-parse", appTenant, parse.Handle, pipeline.WorkerOptions{Poll: 20 * time.Millisecond})
	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = worker.Run(runCtx)
	done, err := env.jobs.Get(context.Background(), jobID, appTenant)
	if err != nil {
		t.Fatalf("Get parse job: %v", err)
	}
	if done.State != pipeline.StateSucceeded {
		t.Fatalf("parse job = %+v", done)
	}
}

func parsedSlideID(t *testing.T, env *appEnv, revisionNo int) string {
	t.Helper()
	key := objectstore.ObjectKey{
		TenantID: appTenant, ProjectID: appProject,
		Revision: srcRevString(revisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := env.objects.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get parsed doc: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Pages []struct {
			SlideID string `json:"slideId"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) == 0 || doc.Pages[0].SlideID == "" {
		t.Fatalf("no parsed pages: %s", data)
	}
	return doc.Pages[0].SlideID
}

func TestScriptDraftProducesOriginalDraftFromParsedDocument(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	ingest := NewIngestService(env.projects, env.jobs, env.objects)
	res, err := ingest.Ingest(ctx, appTenant, appProject, bytes.NewReader(deckBytes(t)))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	runParseWorker(t, env, res.ParseJob.ID)

	// 入队 script_draft 任务并消费。
	snap := ScriptDraftSnapshot{
		ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "original",
	}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-e2e", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatalf("Create script_draft job: %v", err)
	}
	handler := NewScriptDraftHandler(narration.NewPGStore(env.pool), env.objects)
	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// 该页讲稿已存在且带原文分段。
	slideID := parsedSlideID(t, env, 1)
	rev, err := narration.NewPGStore(env.pool).Get(ctx, appTenant, appProject, slideID, "zh-CN")
	if err != nil {
		t.Fatalf("Get script: %v", err)
	}
	if rev.Mode != narration.ModeOriginal || rev.Status != narration.StatusDraft {
		t.Fatalf("revision = %+v", rev)
	}
	if len(rev.Segments) != 1 || rev.Segments[0].SegmentID != "seg-01" ||
		rev.Segments[0].DisplayText == "" || rev.Segments[0].SpokenText == "" {
		t.Fatalf("segments = %+v", rev.Segments)
	}
	if len(rev.Segments[0].SourceRefs) != 1 || rev.Segments[0].SourceRefs[0] != slideID {
		t.Fatalf("source refs = %v", rev.Segments[0].SourceRefs)
	}

	// 幂等：再次处理不覆盖已存在（revision 不变）。
	before := rev.Revision
	job2, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-e2e-2", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatalf("Create#2: %v", err)
	}
	if err := handler.Handle(context.Background(), job2); err != nil {
		t.Fatalf("Handle#2: %v", err)
	}
	after, err := narration.NewPGStore(env.pool).Get(ctx, appTenant, appProject, slideID, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before {
		t.Fatalf("draft overwritten: revision %d → %d", before, after.Revision)
	}
}

func TestScriptDraftSkipsEmptyPages(t *testing.T) {
	// 直接构造 document.json 无正文的解析结果。
	env := setupApp(t)
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING`, appTenant, "app-test"); err != nil {
		t.Fatal(err)
	}
	docKey := objectstore.ObjectKey{
		TenantID: appTenant, ProjectID: appProject,
		Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	doc := `{"schemaVersion":"1.0","pages":[{"index":0,"slideId":"slide-1"},{"index":1,"slideId":"slide-2"}]}`
	if err := env.objects.Put(ctx, docKey, bytes.NewReader([]byte(doc)), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	snap := ScriptDraftSnapshot{ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "original"}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-empty", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewScriptDraftHandler(narration.NewPGStore(env.pool), env.objects)
	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	store := narration.NewPGStore(env.pool)
	if _, err := store.Get(ctx, appTenant, appProject, "slide-1", "zh-CN"); err != narration.ErrNotFound {
		t.Fatalf("slide-1 should have no draft, got err=%v", err)
	}
}
