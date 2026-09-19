package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/project"
)

// registerTagFolderRoutes 挂载标签 + 分组体系原生 HTTP 端点（buf/protoc 不可用，不新增 Connect RPC；#94）。
// 读取（GET）任意已认证成员可见；写入（POST/PUT/DELETE）要求 editor 及以上（project.organize 能力）。
//   - GET    /tags                            列出租户标签
//   - POST   /tags                            新建标签 {name,color?}
//   - PUT    /tags/{tagId}                    重命名/改色 {name,color?}
//   - DELETE /tags/{tagId}                    删除标签（级联清 project_tags）
//   - GET    /folders                         列出租户分组
//   - POST   /folders                         新建分组 {name}
//   - PUT    /folders/{folderId}              重命名 {name}
//   - DELETE /folders/{folderId}              删除分组（项目回落未分类）
//   - GET    /projects/organization           返回 [{projectId,folderId,tagIds}] 供前端合并列表
//   - PUT    /projects/{pid}/tags/{tagId}     给项目打标签
//   - DELETE /projects/{pid}/tags/{tagId}     移除项目标签
//   - PUT    /projects/{pid}/folder           移动项目到分组 {folderId?}（空=未分类）
func registerTagFolderRoutes(mux *http.ServeMux, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /tags", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listTags(w, r, projects)
	})))
	mux.Handle("POST /tags", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutateTag(w, r, projects, members, "create", "")
	})))
	mux.Handle("PUT /tags/{tagId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutateTag(w, r, projects, members, "rename", r.PathValue("tagId"))
	})))
	mux.Handle("DELETE /tags/{tagId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteTag(w, r, projects, members, r.PathValue("tagId"))
	})))

	mux.Handle("GET /folders", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listFolders(w, r, projects)
	})))
	mux.Handle("POST /folders", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutateFolder(w, r, projects, members, "create", "")
	})))
	mux.Handle("PUT /folders/{folderId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutateFolder(w, r, projects, members, "rename", r.PathValue("folderId"))
	})))
	mux.Handle("DELETE /folders/{folderId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteFolder(w, r, projects, members, r.PathValue("folderId"))
	})))

	mux.Handle("GET /projects/organization", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listProjectOrganization(w, r, projects, members)
	})))

	mux.Handle("PUT /projects/{pid}/tags/{tagId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachTag(w, r, projects, members, recorder, r.PathValue("pid"), r.PathValue("tagId"))
	})))
	mux.Handle("DELETE /projects/{pid}/tags/{tagId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detachTag(w, r, projects, members, recorder, r.PathValue("pid"), r.PathValue("tagId"))
	})))
	mux.Handle("PUT /projects/{pid}/folder", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		moveProject(w, r, projects, members, recorder, r.PathValue("pid"))
	})))
}

// orgError 将领域错误映射为带 HTTP 码的 connect 错误。
func orgError(err error) error {
	switch {
	case errors.Is(err, project.ErrTagNotFound),
		errors.Is(err, project.ErrFolderNotFound),
		errors.Is(err, project.ErrProjectNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, project.ErrTagNameExists),
		errors.Is(err, project.ErrFolderNameExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, project.ErrFolderNotEmpty):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func listTags(w http.ResponseWriter, r *http.Request, projects project.ProjectStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	tags, err := projects.ListTags(r.Context(), principal.TenantID)
	if err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	out := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		out = append(out, map[string]any{
			"id":         t.ID,
			"name":       t.Name,
			"color":      t.Color,
			"created_at": t.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": out})
}

func mutateTag(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, op, tagID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	var tag *project.Tag
	switch op {
	case "create":
		tag, err = projects.CreateTag(r.Context(), principal.TenantID, body.Name, body.Color)
	case "rename":
		tag, err = projects.RenameTag(r.Context(), principal.TenantID, tagID, body.Name, body.Color)
	}
	if err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         tag.ID,
		"name":       tag.Name,
		"color":      tag.Color,
		"created_at": tag.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

func deleteTag(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, tagID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if err := projects.DeleteTag(r.Context(), principal.TenantID, tagID); err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func listFolders(w http.ResponseWriter, r *http.Request, projects project.ProjectStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	folders, err := projects.ListFolders(r.Context(), principal.TenantID)
	if err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	out := make([]map[string]any, 0, len(folders))
	for _, f := range folders {
		out = append(out, map[string]any{
			"id":         f.ID,
			"name":       f.Name,
			"created_by": f.CreatedBy,
			"created_at": f.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

func mutateFolder(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, op, folderID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	var f *project.Folder
	switch op {
	case "create":
		f, err = projects.CreateFolder(r.Context(), principal.TenantID, body.Name, principal.UserID)
	case "rename":
		f, err = projects.RenameFolder(r.Context(), principal.TenantID, folderID, body.Name)
	}
	if err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         f.ID,
		"name":       f.Name,
		"created_by": f.CreatedBy,
		"created_at": f.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

func deleteFolder(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, folderID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if err := projects.DeleteFolder(r.Context(), principal.TenantID, folderID); err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func listProjectOrganization(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	userID, _ := projectAccessUser(r.Context(), members)
	rows, err := projects.ListProjectOrganization(r.Context(), principal.TenantID, userID)
	if err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		tagIDs := row.TagIDs
		if tagIDs == nil {
			tagIDs = []string{}
		}
		out = append(out, map[string]any{
			"projectId": row.ProjectID,
			"folderId":  row.FolderID,
			"tagIds":    tagIDs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": out})
}

func attachTag(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, projectID, tagID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := projects.AttachTag(r.Context(), principal.TenantID, projectID, tagID); err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func detachTag(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, projectID, tagID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := projects.DetachTag(r.Context(), principal.TenantID, projectID, tagID); err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func moveProject(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, projectID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	var body struct {
		FolderID string `json:"folderId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := projects.MoveProject(r.Context(), principal.TenantID, projectID, body.FolderID); err != nil {
		writeConnectError(w, orgError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
