package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// editorGetSlideNotes 读取单页演讲者备注（任意已认证成员可见）。
// 用户手写备注优先；缺失时回退解析文档中的原始备注，保证 PPT 自带备注可见。
func editorGetSlideNotes(w http.ResponseWriter, r *http.Request, notesStore project.SlideNotesStore, objects objectstore.ObjectStore) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and sid required")))
		return
	}
	revStr := r.URL.Query().Get("revision_no")
	if revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("revision_no required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	m, err := notesStore.Get(r.Context(), principal.TenantID, projectID, revNo)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	notes := ""
	stored, hasStored := "", false
	if m != nil {
		stored, hasStored = m[slideID]
	}
	switch {
	case hasStored:
		notes = stored // 用户显式覆盖（可能为空串 = 已清空）
	case objects != nil:
		notes = docSlideNotes(r.Context(), objects, principal.TenantID, projectID, revNo, slideID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"slide_id": slideID, "notes": notes})
}

// docSlideNotes 从解析产物中读取指定页的原始备注；读取失败或无该页时返回空串。
func docSlideNotes(ctx context.Context, objects objectstore.ObjectStore, tenantID, projectID string, revisionNo int, slideID string) string {
	docKey := objectstore.DocumentKey(tenantID, projectID, srcRevString(revisionNo))
	rc, _, err := objects.Get(ctx, docKey)
	if err != nil {
		return ""
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	var doc struct {
		Pages []struct {
			SlideID   string `json:"slideId"`
			NotesText string `json:"notesText"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	for _, pg := range doc.Pages {
		if pg.SlideID == slideID {
			return strings.TrimSpace(pg.NotesText)
		}
	}
	return ""
}

// editorSetSlideNotes 保存单页演讲者备注（EDITOR+）。
func editorSetSlideNotes(w http.ResponseWriter, r *http.Request, notesStore project.SlideNotesStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and sid required")))
		return
	}
	revStr := r.URL.Query().Get("revision_no")
	if revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("revision_no required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	var body struct {
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := notesStore.Set(r.Context(), principal.TenantID, projectID, revNo, slideID, body.Notes); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
