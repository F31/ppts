package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// registerRevisionRoutes 挂载源版本历史只读端点（buf/protoc 不可用，不新增 Connect RPC）：
//   - GET    /projects/{pid}/revisions                 返回 current_revision 与未软删版本列表（倒序）。
//   - PATCH  /projects/{pid}/revisions/{revisionNo}    更新 PPT 显示名。
//   - DELETE /projects/{pid}/revisions/{revisionNo}    软删指定版本（禁止删除当前生效版本）。
//   - GET    /projects/{pid}/slides/{sid}/notes        读取单页备注。
//   - PATCH  /projects/{pid}/slides/{sid}/notes        保存单页备注。
func registerRevisionRoutes(mux *http.ServeMux, projects project.ProjectStore, members membership.Reader, notesStore project.SlideNotesStore, objects objectstore.ObjectStore, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/revisions", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListRevisions(w, r, projects, members, recorder)
	})))
	mux.Handle("PATCH /projects/{pid}/revisions/{revisionNo}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorUpdateRevisionDisplayName(w, r, projects, members, recorder)
	})))
	mux.Handle("GET /projects/{pid}/revisions/{revisionNo}/download", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDownloadRevision(w, r, projects, members, recorder, objects)
	})))
	mux.Handle("DELETE /projects/{pid}/revisions/{revisionNo}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDeleteRevision(w, r, projects, members, recorder)
	})))
	// 已归档项目列表与恢复（原生 HTTP，绕过 proto）：GET /projects/archived、POST /projects/{pid}/restore。
	mux.Handle("GET /projects/archived", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListArchivedProjects(w, r, projects, members)
	})))
	mux.Handle("POST /projects/{pid}/restore", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRestoreProject(w, r, projects, members, recorder)
	})))
	if notesStore != nil {
		mux.Handle("GET /projects/{pid}/slides/{sid}/notes", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			editorGetSlideNotes(w, r, notesStore, objects)
		})))
		mux.Handle("PATCH /projects/{pid}/slides/{sid}/notes", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			editorSetSlideNotes(w, r, notesStore, projects, members, recorder)
		})))
	}
}

// editorListRevisions 返回项目源版本历史。current_revision 来自 projects 行（即"当前生效版本"）；
// 版本列表排除 source_deleted_at 非空的软删版本（保留不可变版本行用于追溯，但不可预览）。
func editorListRevisions(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	userID, _ := projectAccessUser(r.Context(), members)
	proj, err := projects.GetProject(r.Context(), principal.TenantID, userID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		return
	}
	revs, err := projects.ListSourceRevisions(r.Context(), principal.TenantID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(revs))
	for _, rv := range revs {
		out = append(out, map[string]any{
			"revision_no":    rv.RevisionNo,
			"created_at":     rv.CreatedAt.Format(time.RFC3339),
			"page_count":     rv.PageCount,
			"parser_version": rv.ParserVersion,
			"object_key":     rv.ObjectKey,
			"display_name":   rv.DisplayName,
			"is_current":     rv.RevisionNo == proj.CurrentRevision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"current_revision": proj.CurrentRevision, "revisions": out})
}

// editorUpdateRevisionDisplayName 更新 PPT 展示名（EDITOR+）。
func editorUpdateRevisionDisplayName(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
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
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := projects.UpdateSourceRevisionDisplayName(r.Context(), principal.TenantID, projectID, revNo, body.DisplayName); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// editorDownloadRevision 下载指定源版本的原始 PPTX（项目可访问即可下载）。
func editorDownloadRevision(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, objects objectstore.ObjectStore) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	projectID := r.PathValue("pid")
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	rv, err := projects.GetSourceRevision(r.Context(), principal.TenantID, projectID, revNo)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		return
	}
	key, err := objectstore.Parse(rv.ObjectKey)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	rc, meta, err := objects.Get(r.Context(), key)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		return
	}
	defer rc.Close()
	name := strings.TrimSpace(rv.DisplayName)
	if name == "" {
		name = "presentation.pptx"
	}
	name = strings.ReplaceAll(name, "\"", "'")
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	if meta.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	_, _ = io.Copy(w, rc)
}

// editorDeleteRevision 软删指定源版本（禁止删除当前生效版本）。
func editorDeleteRevision(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
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
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	if err := projects.DeleteSourceRevision(r.Context(), principal.TenantID, projectID, revNo); err != nil {
		switch {
		case errors.Is(err, project.ErrProjectNotFound):
			writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		case errors.Is(err, project.ErrDeleteCurrentRevision), errors.Is(err, project.ErrDeleteLastRevision):
			writeConnectError(w, connect.NewError(connect.CodeFailedPrecondition, err))
		default:
			writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		}
		return
	}
	recorder.Record(r.Context(), audit.Event{
		TenantID:     principal.TenantID,
		ActorUser:    principal.UserID,
		Action:       "project.source_revision.delete",
		ResourceType: "project",
		ResourceID:   projectID,
		Metadata:     map[string]any{"revision_no": revNo},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// editorListArchivedProjects 返回已归档项目列表（供恢复入口）。
// 原生 HTTP：GET /projects/archived → {"projects":[{id,tenantId,owner,title,currentRevision,archived,createdAtUnix}]}。
func editorListArchivedProjects(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleViewer); err != nil {
		writeConnectError(w, err)
		return
	}
	list, _, err := projects.ListArchivedProjects(r.Context(), principal.TenantID, principal.UserID, "", 100)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		m := map[string]any{
			"id":              p.ID,
			"tenantId":        p.TenantID,
			"owner":           p.OwnerUser,
			"title":           p.Title,
			"currentRevision": p.CurrentRevision,
			"archived":        p.Archived,
			"createdAtUnix":   p.CreatedAt.Unix(),
			"archivedBy":      p.ArchivedBy,
			"archivedByName":  p.ArchivedByName,
			"archivedByEmail": p.ArchivedByEmail,
		}
		if !p.ArchivedAt.IsZero() {
			m["archivedAtUnix"] = p.ArchivedAt.Unix()
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// editorRestoreProject 恢复已归档项目。POST /projects/{pid}/restore（需 ADMIN，与归档同级）。
func editorRestoreProject(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleAdmin); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	if projectID == "" {
		http.Error(w, "pid required", http.StatusBadRequest)
		return
	}
	p, err := projects.UnarchiveProject(r.Context(), principal.TenantID, "", projectID)
	if err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if recorder != nil {
		recorder.Record(r.Context(), audit.Event{
			TenantID: principal.TenantID, ActorUser: principal.UserID,
			Action: "project.restore", ResourceType: "project", ResourceID: projectID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              p.ID,
		"tenantId":        p.TenantID,
		"owner":           p.OwnerUser,
		"title":           p.Title,
		"currentRevision": p.CurrentRevision,
		"archived":        p.Archived,
		"createdAtUnix":   p.CreatedAt.Unix(),
	})
}
