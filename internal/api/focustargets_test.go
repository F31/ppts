package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/project"
)

// 焦点候选端点的 HTTP 契约：编辑器靠它列选项，字段少一个就选不出来，
// 而"没有文档"与"这页不在文档里"必须给出不同的码——界面要能分别提示。

func focusTargetsURL() string {
	return "/projects/" + compareProject + "/slides/" + compareSlide + "/focus-targets"
}

type focusTargetsResponse struct {
	SlideID     string `json:"slideId"`
	RevisionNo  int    `json:"revisionNo"`
	Orientation string `json:"orientation"`
	Note        string `json:"note"`
	Targets     []struct {
		ShapeID  string   `json:"shapeId"`
		BarID    string   `json:"barId"`
		Kind     string   `json:"kind"`
		Label    string   `json:"label"`
		Category string   `json:"category"`
		Series   string   `json:"series"`
		Value    *float64 `json:"value"`
		Box      [4]int64 `json:"box"`
	} `json:"targets"`
}

func TestFocusTargetsHTTPContract(t *testing.T) {
	objects := &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{
			SlideID: compareSlide,
			Shapes: []*project.Shape{
				{ID: "shape-7", Kind: "chart", ChartType: "bar", ChartOrientation: "vertical",
					ChartBars: []project.ChartBar{
						{ID: "p1-s2-b0", Category: "Q1", Series: "系列 A", Value: 174,
							Box: [4]int{100, 200, 60, 370}, LabelDir: "up", LabelPad: 42},
					}},
				{ID: "shape-3", Kind: "text", Text: "一季度回顾",
					Bounds: &project.Box{X: 10, Y: 20, Width: 300, Height: 60}},
			},
		}),
	}}
	ts := newCompareServer(t, objects, nil, nil, 2)

	resp, err := http.Get(ts.URL + focusTargetsURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got focusTargetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SlideID != compareSlide || got.RevisionNo != 2 || got.Orientation != "vertical" {
		t.Fatalf("头部字段 = %+v", got)
	}
	if len(got.Targets) != 2 {
		t.Fatalf("want 2 targets, got %+v", got.Targets)
	}
	bar := got.Targets[0]
	if bar.BarID != "p1-s2-b0" || bar.Kind != "bar" || bar.Label != "Q1 · 系列 A = 174" {
		t.Fatalf("柱子 = %+v", bar)
	}
	if bar.Value == nil || *bar.Value != 174 {
		t.Fatalf("值应为 174，got %v", bar.Value)
	}
	// 框必须换算回 EMU（页面图是 1920 画布像素）：前端将来画布点选要用，
	// 两端坐标系不一致会让"可选但点不中"。
	if bar.Box[2] <= 0 || bar.Box[2] == 60 {
		t.Fatalf("框应为 EMU 而不是画布像素，got %v", bar.Box)
	}
	if got.Targets[1].Kind != "text" || got.Targets[1].BarID != "" || got.Targets[1].Value != nil {
		t.Fatalf("文字形状 = %+v", got.Targets[1])
	}
}

func TestFocusTargetsMissingDocument(t *testing.T) {
	ts := newCompareServer(t, &compareObjectStore{objects: map[string][]byte{}}, nil, nil, 2)
	resp, err := http.Get(ts.URL + focusTargetsURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "no_parsed_document" {
		t.Fatalf("code = %v", body["code"])
	}
}

func TestFocusTargetsSlideNotFound(t *testing.T) {
	objects := &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{SlideID: "other-slide"}),
	}}
	ts := newCompareServer(t, objects, nil, nil, 2)
	resp, err := http.Get(ts.URL + focusTargetsURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "slide_not_found" {
		t.Fatalf("code = %v", body["code"])
	}
}

// 没有对象存储时说明"这个部署没开这项能力"，而不是 500 或空清单：
// 空清单会让前端显示一个"这页没有可指的东西"的假结论。
func TestFocusTargetsWithoutObjectStore(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: compareProject, TenantID: compareTenant, OwnerUser: db.LocalUserID,
		Title: "无存储", CurrentRevision: 1,
	}}}
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: compareTenant, UserID: db.LocalUserID},
		})
	}
	mux.Handle("GET /projects/{pid}/slides/{sid}/focus-targets", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorFocusTargets(w, r, nil, projects, nil, nil)
	})))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + focusTargetsURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "feature_disabled" {
		t.Fatalf("code = %v", body["code"])
	}
}
