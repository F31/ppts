package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/project"
)

// 焦点建议端点的 HTTP 契约。
//
// 最重要的是「只读」：这条端点不写库，采纳走既有的讲稿保存通道。
// 用两个可执行的判据守住——写方法根本没有注册（405），以及 GET 之后
// 讲稿分段没有被偷偷改写（Focus 仍为 nil）。

func focusSuggestionsURL() string {
	return "/projects/" + compareProject + "/slides/" + compareSlide + "/focus-suggestions"
}

type focusSuggestionsResponse struct {
	SlideID     string `json:"slideId"`
	RevisionNo  int    `json:"revisionNo"`
	Band        string `json:"band"`
	Note        string `json:"note"`
	Suggestions []struct {
		SegmentID  string `json:"segmentId"`
		Reason     string `json:"reason"`
		Candidates []struct {
			ShapeID string `json:"shapeId"`
			BarID   string `json:"barId"`
			Source  string `json:"source"`
		} `json:"candidates"`
	} `json:"suggestions"`
}

func suggestionTestObjects(t *testing.T) *compareObjectStore {
	t.Helper()
	const emu = 914400
	return &compareObjectStore{objects: map[string][]byte{
		compareDocKey("src-02"): compareDocumentJSON(t, &project.Page{
			SlideID: compareSlide,
			Shapes: []*project.Shape{
				{ID: "shape-1", Kind: "title", Text: "第三季度回顾",
					Bounds: &project.Box{X: emu, Y: emu, Width: 8 * emu, Height: emu}},
				{ID: "shape-2", Kind: "body", Text: "营收同比增长 23%",
					Bounds: &project.Box{X: emu, Y: 3 * emu, Width: 8 * emu, Height: 2 * emu}},
			},
		}),
	}}
}

func TestFocusSuggestionsHTTPContract(t *testing.T) {
	store := &fakeScriptStore{revision: compareRevision()}
	ts := newCompareServer(t, suggestionTestObjects(t), store, nil, 2)

	resp, err := http.Get(ts.URL + focusSuggestionsURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got focusSuggestionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SlideID != compareSlide || got.RevisionNo != 2 {
		t.Fatalf("头部字段 = %+v", got)
	}
	if got.Band == "" {
		t.Fatal("band 不能为空：界面要据此说明建议的可信度")
	}
	if len(got.Suggestions) != len(compareRevision().Segments) {
		t.Fatalf("每段都应有一条记录（含无建议的原因），实得 %d", len(got.Suggestions))
	}
	for i, sug := range got.Suggestions {
		if sug.SegmentID == "" || sug.Reason == "" {
			t.Fatalf("第 %d 条缺少 segmentId 或 reason：%+v", i, sug)
		}
		for _, c := range sug.Candidates {
			if c.ShapeID == "" {
				t.Fatalf("第 %d 条候选没有 shapeId：%+v", i, sug)
			}
			if c.BarID != "" {
				t.Fatalf("第 %d 条候选带 barId：规则绑定器只认整形状，不能硬凑柱子", i)
			}
			if c.Source == "" {
				t.Fatalf("第 %d 条候选没标来源：%+v", i, sug)
			}
		}
	}
}

// 只读判据一：写方法没有注册。Go 1.22 的 ServeMux 对已注册路径的错误方法返回 405。
func TestFocusSuggestionsHasNoWriteRoute(t *testing.T) {
	ts := newCompareServer(t, suggestionTestObjects(t), &fakeScriptStore{revision: compareRevision()}, nil, 2)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, ts.URL+focusSuggestionsURL(), strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应 405（没有写端点），实得 %d", method, resp.StatusCode)
		}
	}
}

// 只读判据二：GET 之后讲稿分段原封不动（没有把建议写回 Focus）。
func TestFocusSuggestionsDoesNotMutateScript(t *testing.T) {
	rev := compareRevision()
	ts := newCompareServer(t, suggestionTestObjects(t), &fakeScriptStore{revision: rev}, nil, 2)
	resp, err := http.Get(ts.URL + focusSuggestionsURL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, seg := range rev.Segments {
		if seg.Focus != nil {
			t.Fatalf("GET 之后 %s 的 Focus 被改写成 %+v：建议端点必须只读", seg.SegmentID, *seg.Focus)
		}
	}
}

func TestFocusSuggestionsWithoutScriptReturnsScriptNotFound(t *testing.T) {
	ts := newCompareServer(t, suggestionTestObjects(t), &fakeScriptStore{}, nil, 2)
	resp, err := http.Get(ts.URL + focusSuggestionsURL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "script_not_found" {
		t.Fatalf("code = %v", body["code"])
	}
}

func TestFocusSuggestionsWithoutDocumentReturnsNoParsedDocument(t *testing.T) {
	ts := newCompareServer(t, &compareObjectStore{objects: map[string][]byte{}},
		&fakeScriptStore{revision: compareRevision()}, nil, 2)
	resp, err := http.Get(ts.URL + focusSuggestionsURL())
	if err != nil {
		t.Fatal(err)
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
