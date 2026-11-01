package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// parseJobForTenant 用带租户的快照构造解析任务：生产路径的快照由上传流程写入 tenantId，
// 缺了它页数回写会因 tenant_id 条件不匹配而"成功但影响 0 行"。
func parseJobForTenant(t *testing.T, sourceKey string) *pipeline.Job {
	t.Helper()
	job := renderParseJob(t, sourceKey)
	var snap ParseSnapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap.TenantID = "tenant-1"
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("snapshot marshal: %v", err)
	}
	job.InputSnapshot = string(raw)
	return job
}

// pageCountRecorder 记录回写调用。页数回写曾经在生产里"静默未接线"
// （handler 支持、worker 没注入），表现为 PPT 列表页数恒为 0 且没有任何报错；
// 这里把 handler 侧的契约钉住，接线由 scripts/e2e/import_report_live.sh 兜底。
type pageCountRecorder struct {
	calls   int
	tenant  string
	project string
	rev     int
	pages   int
	err     error
}

func (r *pageCountRecorder) UpdateSourceRevisionPageCount(_ context.Context, tenantID, projectID string, rev, pages int) error {
	r.calls++
	r.tenant, r.project, r.rev, r.pages = tenantID, projectID, rev, pages
	return r.err
}

func TestParseHandlerWritesBackPageCount(t *testing.T) {
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	sourceKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "deck", Ext: "pptx",
	}
	if err := objects.Put(ctx, sourceKey, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	doc := &project.Document{
		Pages:    []*project.Page{{Index: 0}, {Index: 1}, {Index: 2}},
		Features: &project.Features{PageCount: 3},
	}
	rec := &pageCountRecorder{}
	handler := NewParseHandler(objects, stubReader{doc: doc}).WithSteps(&stepRecorder{}).WithProjects(rec)

	if err := handler.Handle(ctx, parseJobForTenant(t, sourceKey.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("回写调用 %d 次，期望 1 次", rec.calls)
	}
	if rec.tenant != "tenant-1" || rec.project != "project-1" || rec.rev != 1 || rec.pages != 3 {
		t.Errorf("回写参数 = %s/%s rev=%d pages=%d", rec.tenant, rec.project, rec.rev, rec.pages)
	}
}

func TestParseHandlerWithoutProjectsStillParses(t *testing.T) {
	// 未注入 projects 时解析必须照常成功：页数回写是附带动作，
	// 不能因为宿主没接线就整份文件解析失败。
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	sourceKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "deck", Ext: "pptx",
	}
	if err := objects.Put(ctx, sourceKey, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	doc := &project.Document{Pages: []*project.Page{{Index: 0}}, Features: &project.Features{PageCount: 1}}
	handler := NewParseHandler(objects, stubReader{doc: doc})

	if err := handler.Handle(ctx, parseJobForTenant(t, sourceKey.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	key := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := objects.Get(ctx, key)
	if err != nil {
		t.Fatalf("解析产物未落盘: %v", err)
	}
	rc.Close()
}
