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
	"github.com/F31/ppts/internal/project"
)

// focusTargetPayload 是一条可选焦点。
//
// 字段与 app.FocusTarget 逐一对齐，但 Value 用指针：图表里 0 是**一个真实的数**
// （"这一季是 0"），与"这个目标没有数值"必须能分开——否则前端会把每根柱子都当成有值。
type focusTargetPayload struct {
	ShapeID  string   `json:"shapeId"`
	BarID    string   `json:"barId,omitempty"`
	Kind     string   `json:"kind"` // text | picture | bar
	Label    string   `json:"label"`
	Category string   `json:"category,omitempty"`
	Series   string   `json:"series,omitempty"`
	Value    *float64 `json:"value,omitempty"`
	Box      [4]int64 `json:"box"`
}

// focusTargetsPayload 是 GET /projects/{pid}/slides/{sid}/focus-targets 的应答。
type focusTargetsPayload struct {
	SlideID     string               `json:"slideId"`
	RevisionNo  int                  `json:"revisionNo"`
	Orientation string               `json:"orientation,omitempty"`
	Targets     []focusTargetPayload `json:"targets"`
	// Note 说明清单为什么可能不全（某张图没切开）。空 = 没有要交代的。
	Note string `json:"note,omitempty"`
}

// editorFocusTargets 返回一页上「这一段可以指谁」的候选清单。
//
// 只读、不改状态：选中之后写的是讲稿段落的 focus 字段，走既有的讲稿保存通道
// （ScriptService.Update）。这里不提供"设置焦点"端点——多一条写路径就多一份
// 与乐观并发不一致的风险，而讲稿保存本来就要带上整份分段。
//
// 清单来自解析期的 document.json，不是重新解析 PPTX：用户看到的候选必须与
// 成片里真正能指的框同源，否则会出现"编辑器里选得到、导出时找不到"。
func editorFocusTargets(w http.ResponseWriter, r *http.Request, objects objectstore.ObjectStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if objects == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code": "feature_disabled", "message": "object store not configured",
		})
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
	// 版本号取法与对照面板一致：请求头优先（前端按当前查看的源版本带上），
	// 其次查询参数，最后退到项目当前版本。
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

	found := app.FocusTargetsForPage(pg)
	targets := make([]focusTargetPayload, 0, len(found.Targets))
	for _, t := range found.Targets {
		item := focusTargetPayload{
			ShapeID: t.ShapeID, BarID: t.BarID, Kind: t.Kind, Label: t.Label,
			Category: t.Category, Series: t.Series,
			Box: [4]int64{t.Box.X, t.Box.Y, t.Box.Width, t.Box.Height},
		}
		if t.HasValue {
			v := t.Value
			item.Value = &v
		}
		targets = append(targets, item)
	}

	writeJSON(w, http.StatusOK, focusTargetsPayload{
		SlideID: slideID, RevisionNo: revNo, Orientation: found.Orientation,
		Targets: targets, Note: found.Note,
	})
}
