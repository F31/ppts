//go:build pg

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

type fakePolisher struct {
	outputs     []string
	visual      []llm.VisualAnchor
	calls       int
	visualCalls int
	sources     []string
}

func (p *fakePolisher) Rewrite(_ context.Context, req llm.RewriteRequest) (llm.RewriteResult, error) {
	p.calls++
	p.sources = append(p.sources, req.SourceText)
	if p.calls <= len(p.outputs) {
		return llm.RewriteResult{Text: p.outputs[p.calls-1]}, nil
	}
	return llm.RewriteResult{Text: req.SourceText}, nil
}

func (p *fakePolisher) ExtractVisual(context.Context, llm.VisualExtractRequest) ([]llm.VisualAnchor, error) {
	p.visualCalls++
	return p.visual, nil
}

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
	_, parseJob := uploadDeck(t, env, 1, deckBytes(t))
	runParseWorker(t, env, parseJob.ID)

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
	if len(rev.Segments[0].SourceRefs) < 1 || rev.Segments[0].SourceRefs[0] != slideID {
		t.Fatalf("source refs = %v", rev.Segments[0].SourceRefs)
	}
	if len(rev.Segments[0].SourceAnchors) == 0 || rev.Segments[0].SourceAnchors[0].SlideID != slideID {
		t.Fatalf("source anchors = %+v", rev.Segments[0].SourceAnchors)
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

func TestScriptDraftOriginalUsesNotesOnly(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	docKey := objectstore.ObjectKey{TenantID: appTenant, ProjectID: appProject, Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	doc := `{"schemaVersion":"1.0","pages":[{"index":0,"slideId":"slide-1","notesText":"备注原文","shapes":[{"id":"shape-1","kind":"text","text":"版面文字不应被原文朗读采用"}]},{"index":1,"slideId":"slide-2","shapes":[{"text":"无备注页版面文字"}]}]}`
	if err := env.objects.Put(ctx, docKey, bytes.NewReader([]byte(doc)), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	snap := ScriptDraftSnapshot{ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "original"}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-original-notes", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	store := narration.NewPGStore(env.pool)
	handler := NewScriptDraftHandler(store, env.objects)
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	rev, err := store.Get(ctx, appTenant, appProject, "slide-1", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if got := rev.Segments[0].DisplayText; got != "备注原文" {
		t.Fatalf("original text = %q", got)
	}
	if _, err := store.Get(ctx, appTenant, appProject, "slide-2", "zh-CN"); err != narration.ErrNotFound {
		t.Fatalf("slide-2 should have no original draft without notes, got err=%v", err)
	}
}

func TestScriptDraftOverwritePolishUsesCurrentScript(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	docKey := objectstore.ObjectKey{TenantID: appTenant, ProjectID: appProject, Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	doc := `{"schemaVersion":"1.0","pages":[{"index":0,"slideId":"slide-1","shapes":[{"id":"shape-1","kind":"text","text":"版面原文"}]}]}`
	if err := env.objects.Put(ctx, docKey, bytes.NewReader([]byte(doc)), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	store := narration.NewPGStore(env.pool)
	rev, err := store.EnsureExists(ctx, appTenant, appProject, "slide-1", "zh-CN", narration.ModePolish)
	if err != nil {
		t.Fatal(err)
	}
	current := "用户当前讲稿，应该作为润色输入。"
	if _, err := store.Update(ctx, appTenant, appProject, "slide-1", "zh-CN", rev.Revision, []*narration.Segment{{SegmentID: "seg-01", DisplayText: current, SpokenText: current, Status: narration.StatusDraft}}); err != nil {
		t.Fatal(err)
	}
	snap := ScriptDraftSnapshot{ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "polish", SlideIDs: []string{"slide-1"}, Overwrite: true}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-polish-current", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	polisher := &fakePolisher{outputs: []string{"润色后的当前讲稿。"}}
	handler := NewScriptDraftHandler(store, env.objects).WithPolisher(polisher)
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(polisher.sources) != 1 || polisher.sources[0] != current {
		t.Fatalf("polisher source = %#v", polisher.sources)
	}
}

func TestScriptDraftPolishUsesLLMWhenEntitiesPreserved(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	docKey := objectstore.ObjectKey{TenantID: appTenant, ProjectID: appProject, Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	doc := `{"schemaVersion":"1.0","pages":[{"index":0,"slideId":"slide-1","shapes":[{"id":"shape-1","kind":"text","text":"吞吐提升 23.5%，延迟 12ms。"}]}]}`
	if err := env.objects.Put(ctx, docKey, bytes.NewReader([]byte(doc)), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	snap := ScriptDraftSnapshot{ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "polish"}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-polish", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	polisher := &fakePolisher{outputs: []string{"这页的重点是吞吐提升 23.5%，同时延迟控制在 12ms。"}}
	handler := NewScriptDraftHandler(narration.NewPGStore(env.pool), env.objects).WithPolisher(polisher)
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	rev, err := narration.NewPGStore(env.pool).Get(ctx, appTenant, appProject, "slide-1", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if rev.Mode != narration.ModePolish || len(rev.Segments) != 1 || rev.Segments[0].DisplayText != polisher.outputs[0] {
		t.Fatalf("revision = %+v", rev)
	}
	if len(rev.Segments[0].SourceAnchors) != 1 || rev.Segments[0].SourceAnchors[0].ShapeID != "shape-1" || rev.Segments[0].SourceAnchors[0].Confidence != 1 {
		t.Fatalf("anchors = %+v", rev.Segments[0].SourceAnchors)
	}
	if polisher.calls != 1 {
		t.Fatalf("polisher calls = %d", polisher.calls)
	}
}

func TestScriptDraftPolishFallsBackWhenEntityGuardStillFails(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	docKey := objectstore.ObjectKey{TenantID: appTenant, ProjectID: appProject, Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	source := "吞吐提升 23.5%，延迟 12ms。"
	doc := `{"schemaVersion":"1.0","pages":[{"index":0,"slideId":"slide-1","shapes":[{"text":"` + source + `"}]}]}`
	if err := env.objects.Put(ctx, docKey, bytes.NewReader([]byte(doc)), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	snap := ScriptDraftSnapshot{ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "polish"}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-polish-guard", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	polisher := &fakePolisher{outputs: []string{"吞吐提升 30%，延迟 10ms。", "吞吐提升 30%，延迟 10ms。"}}
	handler := NewScriptDraftHandler(narration.NewPGStore(env.pool), env.objects).WithPolisher(polisher)
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	rev, err := narration.NewPGStore(env.pool).Get(ctx, appTenant, appProject, "slide-1", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if rev.Segments[0].DisplayText != source || rev.Segments[0].SpokenText != source {
		t.Fatalf("expected fallback source, got %+v", rev.Segments[0])
	}
	if polisher.calls != 2 {
		t.Fatalf("expected guard retry, calls=%d", polisher.calls)
	}
}

func TestScriptDraftAIGeneratedUsesLLMWithEntityGuard(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	docKey := objectstore.ObjectKey{TenantID: appTenant, ProjectID: appProject, Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	source := "端到端验收 PCIe 5.0。"
	doc := `{"schemaVersion":"1.0","pages":[{"index":0,"slideId":"slide-1","shapes":[{"text":"` + source + `"}]}]}`
	if err := env.objects.Put(ctx, docKey, bytes.NewReader([]byte(doc)), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	snap := ScriptDraftSnapshot{ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "ai_generated"}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-ai", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	text := "这一页可以这样讲：我们正在验证端到端链路对 PCIe 5.0 场景的支撑。"
	polisher := &fakePolisher{outputs: []string{text}}
	handler := NewScriptDraftHandler(narration.NewPGStore(env.pool), env.objects).WithPolisher(polisher)
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	rev, err := narration.NewPGStore(env.pool).Get(ctx, appTenant, appProject, "slide-1", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if rev.Mode != narration.ModeAIGenerated || len(rev.Segments) != 1 || rev.Segments[0].DisplayText != text {
		t.Fatalf("revision = %+v", rev)
	}
}

func TestScriptDraftAddsVisualAnchorsWhenPagePNGExists(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	docKey := objectstore.ObjectKey{TenantID: appTenant, ProjectID: appProject, Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	doc := `{"schemaVersion":"1.0","pages":[{"index":0,"slideId":"slide-1","shapes":[{"id":"shape-1","kind":"text","text":"可见数字 42%。"}]}]}`
	if err := env.objects.Put(ctx, docKey, bytes.NewReader([]byte(doc)), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	pageKey := objectstore.ObjectKey{TenantID: appTenant, ProjectID: appProject, Revision: "src-01", AssetType: "render", AssetID: "page-0001", Ext: "png"}
	if err := env.objects.Put(ctx, pageKey, bytes.NewReader([]byte("png")), objectstore.ObjectMeta{ContentType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	snap := ScriptDraftSnapshot{ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "polish"}
	snapBytes, _ := json.Marshal(snap)
	job, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "draft-visual", string(snapBytes), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	polisher := &fakePolisher{outputs: []string{"可见数字 42%，适合重点讲解。"}, visual: []llm.VisualAnchor{{Kind: "text", Raw: "图中可见 42%", Confidence: 0.8}}}
	handler := NewScriptDraftHandler(narration.NewPGStore(env.pool), env.objects).WithPolisher(polisher).WithVisualExtractor(polisher)
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	rev, err := narration.NewPGStore(env.pool).Get(ctx, appTenant, appProject, "slide-1", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if polisher.visualCalls != 1 {
		t.Fatalf("visual calls = %d", polisher.visualCalls)
	}
	anchors := rev.Segments[0].SourceAnchors
	if len(anchors) != 2 || anchors[1].Kind != "visual_text" || anchors[1].Raw != "图中可见 42%" || anchors[1].Confidence != 0.8 {
		t.Fatalf("anchors = %+v", anchors)
	}
}
