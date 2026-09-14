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
