package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
)

// focusSuggestionCandidate 是一条可选的焦点建议（shapeId 必然非空，barId 恒为空）。
//
// barId 恒为空不是遗漏：规则绑定器只认「整块形状」，切开的图表在候选清单里
// 只有一根根柱子，没有整块图表。把 shape id 硬凑成某根柱子等于让机器随便挑数，
// 所以这类目标不会进建议（app.SuggestFocus 的闸门）。
type focusSuggestionCandidate struct {
	ShapeID string `json:"shapeId"`
	BarID   string `json:"barId,omitempty"`
	Source  string `json:"source"`
}

type focusSuggestionItem struct {
	SegmentID  string                     `json:"segmentId"`
	Candidates []focusSuggestionCandidate `json:"candidates"`
	Reason     string                     `json:"reason"`
}

// focusSuggestionsPayload 是 GET /projects/{pid}/slides/{sid}/focus-suggestions 的应答。
type focusSuggestionsPayload struct {
	SlideID     string                `json:"slideId"`
	RevisionNo  int                   `json:"revisionNo"`
	Band        string                `json:"band"`
	Note        string                `json:"note,omitempty"`
	Suggestions []focusSuggestionItem `json:"suggestions"`
}

// editorFocusSuggestions 给这一页每段讲稿一组**只读**焦点建议。
//
// 与 focus-targets 一样是只读端点，而且更进一步：连「采纳」端点都不提供。
// 用户点选/一键采纳后写的是讲稿段落的 focus 字段，走**既有**的 ScriptService.Update
// 自动保存通道——那样草案、防抖、乐观并发、三态语义都只有一份实现。
// 多一条写路径就会出现「先改文字再点建议，服务端收到新建议 + 旧文本」这类分叉。
//
// 讲稿从库里读（不是让请求带上来）：建议必须基于「这一刻服务端认可的讲稿」，
// 前端草稿可能还没落库；而解析页取同一个源版本，避免 id 指到另一版形状上。
func editorFocusSuggestions(w http.ResponseWriter, r *http.Request, objects objectstore.ObjectStore, scripts narration.Store, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	proj, ok := requireProject(w, r, projects, members, recorder)
	if !ok {
		return
	}
	slideID := strings.TrimSpace(r.PathValue("sid"))
	if slideID == "" {
		http.Error(w, "slide_id required", http.StatusBadRequest)
		return
	}
	// 版本号取法与 focus-targets / 对照面板一致：请求头 > 查询参数 > 项目当前版本。
	// 三处必须同源，否则「编辑器里选得到」与「成片里指得到」会指向不同版本的形状。
	revNo := requestSourceRevision(r.Header)
	if revNo <= 0 {
		revNo, _ = strconv.Atoi(r.URL.Query().Get("revision_no"))
	}
	if revNo <= 0 {
		revNo = int(proj.CurrentRevision)
	}
	if revNo <= 0 {
		revNo = 1
	}

	rev, err := scripts.Get(r.Context(), principal.TenantID, proj.ID, revNo, slideID, requestLanguage(r.Header))
	if errors.Is(err, narration.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"code": "script_not_found", "message": "该页还没有讲稿，先写稿再来谈焦点",
		})
		return
	}
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}

	pg, err := loadComparePage(r.Context(), objects, principal.TenantID, proj.ID, revNo, slideID)
	switch {
	case errors.Is(err, errProjectHasNoDocument):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_parsed_document", "message": "该项目还没有解析完成的版本"})
		return
	case errors.Is(err, errSlideNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "slide_not_found", "message": "源版本中找不到该页"})
		return
	case err != nil:
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}

	got := app.SuggestFocus(pg, rev.Segments)
	items := make([]focusSuggestionItem, 0, len(got.Suggestions))
	for _, s := range got.Suggestions {
		item := focusSuggestionItem{
			SegmentID:  s.SegmentID,
			Candidates: make([]focusSuggestionCandidate, 0, len(s.Candidates)),
			Reason:     s.Reason,
		}
		for _, c := range s.Candidates {
			item.Candidates = append(item.Candidates, focusSuggestionCandidate{
				ShapeID: c.Focus.ShapeID, BarID: c.Focus.BarID, Source: c.Source,
			})
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, focusSuggestionsPayload{
		SlideID: slideID, RevisionNo: revNo, Band: got.Band,
		Note: got.Note, Suggestions: items,
	})
}
