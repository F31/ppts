package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

type stubReader struct{ doc *project.Document }

func (s stubReader) Inspect(context.Context, io.ReaderAt, int64) (*project.Document, error) {
	return s.doc, nil
}

type stubRenderer struct {
	res *render.RenderResult
	err error
}

func (s stubRenderer) Render(context.Context, io.ReaderAt, int64, render.RenderOptions) (*render.RenderResult, error) {
	return s.res, s.err
}

func renderParseJob(t *testing.T, sourceKey string) *pipeline.Job {
	t.Helper()
	snap, err := json.Marshal(ParseSnapshot{
		SourceRevisionID: "rev-1", ProjectID: "project-1", ObjectKey: sourceKey,
		RevisionNo: 1, ParserVersion: ParserVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pipeline.Job{
		ID: "job-parse", TenantID: "tenant-1", ProjectID: "project-1",
		Kind: pipeline.KindParse, InputSnapshot: string(snap),
	}
}

func TestParseHandlerStoresRenderedPages(t *testing.T) {
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	sourceKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "deck", Ext: "pptx",
	}
	if err := objects.Put(ctx, sourceKey, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{ContentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation"}); err != nil {
		t.Fatal(err)
	}
	doc := &project.Document{Pages: []*project.Page{
		{Index: 0, SlideID: "slide-1"},
		{Index: 1, SlideID: "slide-2"},
	}}
	renderer := stubRenderer{res: &render.RenderResult{
		Pages: []render.PageImage{
			{Index: 0, Width: 10, Height: 10, PNG: []byte("png-0")},
			{Index: 1, Width: 10, Height: 10, PNG: []byte("png-1")},
		},
		Report: render.RenderReport{Renderer: "stub", PageCount: 2},
	}}
	steps := &stepRecorder{}
	handler := NewParseHandler(objects, stubReader{doc: doc}).WithRenderer(renderer).WithSteps(steps)

	if err := handler.Handle(ctx, renderParseJob(t, sourceKey.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	for _, id := range []string{"page-0001", "page-0002"} {
		key := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "src-01", AssetType: "render", AssetID: id, Ext: "png"}
		r, _, err := objects.Get(ctx, key)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		r.Close()
	}
	step, ok := steps.latest["pages:v1:deck"]
	if !ok || step.State != pipeline.StepSuccess || step.ResultRef == "" {
		t.Fatalf("render step = %+v", step)
	}
	manifestKey, err := objectstore.Parse(step.ResultRef)
	if err != nil {
		t.Fatalf("manifest key: %v", err)
	}
	r, _, err := objects.Get(ctx, manifestKey)
	if err != nil {
		t.Fatalf("manifest get: %v", err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	var manifest PageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}
	if len(manifest.Pages) != 2 || manifest.Pages[1].SlideID != "slide-2" || manifest.Pages[1].Index != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
}

// hiddenPage 构造隐藏页（p:sldId@show="0"），放映与 PDF 导出都会跳过。
func hiddenPage(index int, slideID string) *project.Page {
	hidden := true
	return &project.Page{Index: index, SlideID: slideID, Hidden: &hidden}
}

func TestParseHandlerAlignsRenderedPagesToVisibleSlides(t *testing.T) {
	// 隐藏页不进 PDF：渲染结果只有 2 页，必须映射到可见页 slide-1/slide-3。
	// 若按解析索引对齐，会把第二张图挂到 slide-2（隐藏页）上，隐藏页之后整体错位。
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	sourceKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "deck", Ext: "pptx",
	}
	if err := objects.Put(ctx, sourceKey, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	doc := &project.Document{Pages: []*project.Page{
		{Index: 0, SlideID: "slide-1"},
		hiddenPage(1, "slide-2"),
		{Index: 2, SlideID: "slide-3"},
	}}
	renderer := stubRenderer{res: &render.RenderResult{
		Pages: []render.PageImage{
			{Index: 0, PNG: []byte("png-0")},
			{Index: 1, PNG: []byte("png-1")},
		},
		Report: render.RenderReport{Renderer: "stub", PageCount: 2},
	}}
	steps := &stepRecorder{}
	handler := NewParseHandler(objects, stubReader{doc: doc}).WithRenderer(renderer).WithSteps(steps)

	if err := handler.Handle(ctx, renderParseJob(t, sourceKey.String())); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	step := steps.latest["pages:v1:deck"]
	if step.State != pipeline.StepSuccess || step.ResultRef == "" {
		t.Fatalf("render step = %+v", step)
	}
	manifestKey, err := objectstore.Parse(step.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := objects.Get(ctx, manifestKey)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	var manifest PageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Pages) != 2 {
		t.Fatalf("manifest pages = %+v", manifest.Pages)
	}
	if manifest.Pages[0].SlideID != "slide-1" || manifest.Pages[0].Index != 0 {
		t.Fatalf("page[0] = %+v，应为 slide-1", manifest.Pages[0])
	}
	if manifest.Pages[1].SlideID != "slide-3" || manifest.Pages[1].Index != 2 {
		t.Fatalf("page[1] = %+v，应跳过隐藏页映射到 slide-3", manifest.Pages[1])
	}
}

func TestParseHandlerPageCountMismatchMarksStepFailed(t *testing.T) {
	// 渲染页数与可见页数不一致（渲染器漏页）：宁缺毋错，不落清单并标记步骤失败。
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	sourceKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "deck", Ext: "pptx",
	}
	if err := objects.Put(ctx, sourceKey, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	doc := &project.Document{Pages: []*project.Page{
		{Index: 0, SlideID: "slide-1"},
		{Index: 1, SlideID: "slide-2"},
	}}
	renderer := stubRenderer{res: &render.RenderResult{
		Pages:  []render.PageImage{{Index: 0, PNG: []byte("png-0")}},
		Report: render.RenderReport{Renderer: "stub", PageCount: 1},
	}}
	steps := &stepRecorder{}
	handler := NewParseHandler(objects, stubReader{doc: doc}).WithRenderer(renderer).WithSteps(steps)

	if err := handler.Handle(ctx, renderParseJob(t, sourceKey.String())); err != nil {
		t.Fatalf("parse should succeed without page manifest: %v", err)
	}
	step := steps.latest["pages:v1:deck"]
	if step.State != pipeline.StepFailed || step.ResultRef != "" {
		t.Fatalf("render step = %+v，页数失配时应失败且不落清单", step)
	}
}

// TestParseHandlerRendererMissingLeavesTrace 锁定「依赖缺失不能表现为'什么都没发生'」：
// 未注入渲染器时，解析仍成功（不阻塞主链路），但必须落一份 renderer="unavailable" 的清单，
// 使 /slides/render 能把「环境没装渲染器」与「尚未渲染 / 渲染失败」区分开。
// 此前这里什么都不写，前端只能看到一片占位缩略图，无从判断原因。
func TestParseHandlerRendererMissingLeavesTrace(t *testing.T) {
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	sourceKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "deck", Ext: "pptx",
	}
	if err := objects.Put(ctx, sourceKey, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	steps := &stepRecorder{}
	// 注意：不调用 WithRenderer —— 这正是 LibreOffice/poppler 未安装时的生产形态。
	handler := NewParseHandler(objects, stubReader{doc: &project.Document{Pages: []*project.Page{{Index: 0, SlideID: "slide-1"}}}}).
		WithSteps(steps)

	if err := handler.Handle(ctx, renderParseJob(t, sourceKey.String())); err != nil {
		t.Fatalf("解析本身应成功（缺少页面图不应阻塞主链路）: %v", err)
	}
	step, ok := steps.latest["pages:v1:deck"]
	if !ok {
		t.Fatal("缺少 pages 步骤记录：渲染器缺失这件事没有留痕")
	}
	// 必须留 ref：/slides/render 只有拿到 ref 才读得到 renderer 字段。
	if step.ResultRef == "" {
		t.Fatal("步骤未写 ResultRef：接口拿不到清单，用户侧将完全无信号")
	}
	// 但状态必须是失败：没有页面图就是没有页面图，不能把降级说成成功。
	if step.State != pipeline.StepFailed {
		t.Fatalf("步骤状态 = %v，渲染器不可用时应为 failed（诚实表达未产出页面图）", step.State)
	}
	manifestKey, err := objectstore.Parse(step.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := objects.Get(ctx, manifestKey)
	if err != nil {
		t.Fatalf("manifest 应可读取: %v", err)
	}
	defer r.Close()
	data, _ := io.ReadAll(r)
	var manifest PageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Renderer != RendererUnavailable {
		t.Fatalf("manifest.Renderer = %q，应为 %q", manifest.Renderer, RendererUnavailable)
	}
	if len(manifest.Pages) != 0 {
		t.Fatalf("pages = %+v，渲染器不可用时应为空", manifest.Pages)
	}
}

func TestParseHandlerRenderFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	sourceKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "deck", Ext: "pptx",
	}
	if err := objects.Put(ctx, sourceKey, bytes.NewReader([]byte("pptx")), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	steps := &stepRecorder{}
	handler := NewParseHandler(objects, stubReader{doc: &project.Document{Pages: []*project.Page{{Index: 0, SlideID: "slide-1"}}}}).
		WithRenderer(stubRenderer{err: errors.New("no renderer")}).WithSteps(steps)

	if err := handler.Handle(ctx, renderParseJob(t, sourceKey.String())); err != nil {
		t.Fatalf("parse should succeed without page images: %v", err)
	}
	step, ok := steps.latest["pages:v1:deck"]
	if !ok || step.State != pipeline.StepFailed {
		t.Fatalf("render step = %+v", step)
	}
	docKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "src-01", AssetType: "document", AssetID: "extracted", Ext: "json"}
	r, _, err := objects.Get(ctx, docKey)
	if err != nil {
		t.Fatalf("extracted document should exist: %v", err)
	}
	r.Close()
}
