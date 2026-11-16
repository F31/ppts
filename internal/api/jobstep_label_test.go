package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
)

func TestParseStepTarget(t *testing.T) {
	slide, seg := parseStepTarget("page", "page:v1:slide-285")
	if slide != "slide-285" || seg != "" {
		t.Fatalf("page key = %q %q", slide, seg)
	}
	slide, seg = parseStepTarget("tts_segment", "tts_segment:slide-285:seg-30:abc")
	if slide != "slide-285" || seg != "seg-30" {
		t.Fatalf("tts key = %q %q", slide, seg)
	}
	slide, seg = parseStepTarget("timeline", "timeline:asset")
	if slide != "" || seg != "" {
		t.Fatalf("timeline key should not invent a page: %q %q", slide, seg)
	}
}

func TestSubjectsPreferDocumentPageAndCountSegments(t *testing.T) {
	steps := []pipeline.JobStep{
		{StepType: "tts_segment", StepKey: "tts_segment:slide-b:seg-1:h"},
		{StepType: "tts_segment", StepKey: "tts_segment:slide-a:seg-1:h"},
		{StepType: "tts_segment", StepKey: "tts_segment:slide-b:seg-2:h"},
		{StepType: "timeline", StepKey: "timeline:asset"},
		{StepType: "page", StepKey: "page:v1:slide-a"},
	}
	labels := map[string]jobSlideLabel{
		"slide-b": {Page: 30, Title: "结论", Summary: "账号余额不足时停在这一页"},
	}
	order := map[string]int{"slide-a": 1, "slide-b": 2}
	got := subjectsForSteps(steps, labels, order)
	if got[0].Page != 30 || got[0].Title != "结论" || got[0].Segment != 1 {
		t.Fatalf("first segment = %+v", got[0])
	}
	if got[1].Page != 1 || got[1].Title != "" || got[1].Segment != 1 {
		t.Fatalf("slide-a without a document label should use snapshot order: %+v", got[1])
	}
	if got[2].Page != 30 || got[2].Segment != 2 || got[2].Summary == "" {
		t.Fatalf("second segment of slide-b = %+v", got[2])
	}
	if got[3].Page != 0 || got[3].Segment != 0 {
		t.Fatalf("timeline = %+v", got[3])
	}
	if got[4].Page != 1 || got[4].Segment != 0 {
		t.Fatalf("script page step should not get a segment number: %+v", got[4])
	}
}

func TestLabelsFromDocumentUsesDeckOrderAndNotes(t *testing.T) {
	raw := []byte(`{"pages":[
		{"slideId":"slide-a","name":"封面","shapes":[{"text":"封面正文"}]},
		{"slideId":"","name":"空"},
		{"slideId":"slide-b","name":"  结论  ","notesText":"这页说明余额不足"}
	]}`)
	got := labelsFromDocument(raw)
	if got["slide-a"].Page != 1 || got["slide-a"].Title != "封面" || got["slide-a"].Summary != "封面正文" {
		t.Fatalf("slide-a = %+v", got["slide-a"])
	}
	if got["slide-b"].Page != 3 || got["slide-b"].Title != "结论" || !strings.Contains(got["slide-b"].Summary, "余额不足") {
		t.Fatalf("slide-b = %+v", got["slide-b"])
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty slide id must be dropped")
	}
}

type memDoc struct {
	data []byte
	err  error
}

func (m memDoc) Get(context.Context, objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	if m.err != nil {
		return nil, objectstore.ObjectMeta{}, m.err
	}
	return io.NopCloser(bytes.NewReader(m.data)), objectstore.ObjectMeta{}, nil
}

func TestJobDocumentLabelsSwallowsReadFailures(t *testing.T) {
	broken := jobDocumentLabels{objects: memDoc{err: errors.New("missing")}}
	if broken.SlideLabels(context.Background(), "t", "u", "p", 1) != nil {
		t.Fatal("read failure must not surface as labels")
	}
	huge := jobDocumentLabels{objects: memDoc{data: bytes.Repeat([]byte("x"), maxJobDocumentBytes+1)}}
	if huge.SlideLabels(context.Background(), "t", "u", "p", 1) != nil {
		t.Fatal("oversized document must be skipped")
	}
	ok := jobDocumentLabels{objects: memDoc{data: []byte(`{"pages":[{"slideId":"slide-9","name":"结论","notesText":"概述"}]}`)}}
	got := ok.SlideLabels(context.Background(), "t", "u", "p", 2)
	if got["slide-9"].Page != 1 || got["slide-9"].Title != "结论" || got["slide-9"].Summary != "概述" {
		t.Fatalf("labels = %+v", got["slide-9"])
	}
}

func TestPublicJobDetailLabelsStepsFromDocument(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-label", Kind: pipeline.KindNarration, ProjectID: "project-1",
		InputSnapshot: `{"revisionNo":1,"slides":[{"slideId":"slide-a","scriptRevision":2},{"slideId":"slide-b","scriptRevision":2}]}`,
	}
	store := &jobDetailInspectorStub{
		jobDetailStub: jobDetailStub{job: job},
		steps: []pipeline.JobStep{
			{StepType: "tts_segment", StepKey: "tts_segment:slide-b:seg-1:h", State: pipeline.StepFailed},
			{StepType: "timeline", StepKey: "timeline:asset", State: pipeline.StepSuccess, ResultRef: "obj"},
		},
	}
	slides := staticSlides{labels: map[string]jobSlideLabel{
		"slide-b": {Page: 30, Title: "结论", Summary: "余额不足"},
	}}
	mux := http.NewServeMux()
	registerJobDetailRoutes(mux, store, slides, identityAuth)
	req := httptest.NewRequest(http.MethodGet, "/jobs/job-label/detail", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-1"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeDetail(t, rec)
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %+v", got.Steps)
	}
	if got.Steps[0].Page != 30 || got.Steps[0].Title != "结论" || got.Steps[0].Summary != "余额不足" || got.Steps[0].Segment != 1 {
		t.Fatalf("failed step = %+v", got.Steps[0])
	}
	if got.Steps[1].Page != 0 || got.Steps[1].Title != "" {
		t.Fatalf("timeline must stay unlabeled: %+v", got.Steps[1])
	}
	if len(got.SlideLabels) != 2 || got.SlideLabels[1].Page != 30 || got.SlideLabels[1].Title != "结论" {
		t.Fatalf("slideLabels = %+v", got.SlideLabels)
	}
	if got.SlideLabels[0].SlideID != "slide-a" || got.SlideLabels[0].Page != 1 {
		t.Fatalf("unlabeled slide should still carry snapshot page: %+v", got.SlideLabels[0])
	}
}

type staticSlides struct {
	labels map[string]jobSlideLabel
}

func (s staticSlides) SlideLabels(context.Context, string, string, string, int) map[string]jobSlideLabel {
	return s.labels
}
