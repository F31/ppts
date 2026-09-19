package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

// registerEditorRoutes 挂载核心创作辅助的 HTTP 端点（B2 M2 真实渲染缩略图；B2 M3 ⑥ 无备注页来源）。
// GET /projects/{pid}/slides/render 返回每页渲染 PNG 的短期签名可读 URL，按 slideId 对齐，供编辑器缩略图与 PPT 预览。
// PUT /projects/{pid}/slides/{sid}/source + GET /projects/{pid}/slides/sources 管理"无备注页讲稿来源"选择。
// 均受 auth 中间件保护（仅项目所属租户成员可访问）。
func registerEditorRoutes(mux *http.ServeMux, jobs JobStore, objects objectstore.ObjectStore, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/slides/render", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSlideRender(w, r, jobs, objects, projects, members, recorder)
	})))
	// M3 ⑥：无备注页讲稿来源选择。
	mux.Handle("PUT /projects/{pid}/slides/{sid}/source", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSetSlideSource(w, r, srcStore, projects, members, recorder)
	})))
	mux.Handle("GET /projects/{pid}/slides/sources", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListSlideSources(w, r, srcStore, projects, members, recorder)
	})))
}

type editorSlideRenderItem struct {
	SlideID string `json:"slideId"`
	URL     string `json:"url"`
}

// editorSlideRender 读取最近一次成功解析任务的页面清单（PageManifest），为每页渲染 PNG 签发短期匿名可读 URL。
// 解析未完成、页面清单缺失或某页渲染图不存在时，对应项跳过；整体缺失时返回空列表，前端优雅降级为序号/标题缩略图。
func editorSlideRender(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	ctx := r.Context()
	job, err := jobs.LatestSucceededJob(ctx, principal.TenantID, projectID, string(pipeline.KindParse))
	if errors.Is(err, pipeline.ErrNoSucceededJob) {
		writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ref, err := jobs.StepResultRef(tenant.WithContext(ctx, principal.TenantID), job.ID, "pages")
	if err != nil || ref == "" {
		writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
		return
	}
	manifest, err := loadPageManifest(ctx, objects, principal.TenantID, ref)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	parser, _ := objects.(signedURLParser)
	ttl := 15 * time.Minute
	out := make([]editorSlideRenderItem, 0, len(manifest.Pages))
	for _, pg := range manifest.Pages {
		if pg.SlideID == "" || pg.Key == "" {
			continue
		}
		key, perr := objectstore.Parse(pg.Key)
		if perr != nil {
			continue
		}
		if perr = key.EnsureTenant(principal.TenantID); perr != nil {
			continue
		}
		signed, serr := objects.SignedURL(ctx, key, objectstore.OpRead, ttl)
		if serr != nil {
			continue
		}
		url := signed
		if parser != nil {
			url = rewriteLocalSignedURL(parser, signed)
		}
		out = append(out, editorSlideRenderItem{SlideID: pg.SlideID, URL: url})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slides": out})
}

type editorSlideSourceItem struct {
	SlideID    string `json:"slideId"`
	Source     string `json:"source"`
	CustomText string `json:"customText"`
}

// editorSetSlideSource 持久化单页讲稿来源选择（M3 ⑥）。body: {source, customText?}。
// source ∈ layout/title/body/notes/custom；custom 时 customText 必填且非空。
func editorSetSlideSource(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if srcStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "slide script source store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		http.Error(w, "project_id and slide_id are required", http.StatusBadRequest)
		return
	}
	var body struct {
		Source     string `json:"source"`
		CustomText string `json:"customText"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	kind := app.ScriptSourceKind(body.Source)
	if !app.ValidScriptSourceKinds[kind] {
		http.Error(w, "invalid source kind", http.StatusBadRequest)
		return
	}
	if kind == app.ScriptSourceCustom && strings.TrimSpace(body.CustomText) == "" {
		http.Error(w, "custom source requires customText", http.StatusBadRequest)
		return
	}
	if err := srcStore.Set(tenant.WithContext(r.Context(), principal.TenantID), principal.TenantID, projectID, slideID, kind, body.CustomText); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, editorSlideSourceItem{SlideID: slideID, Source: body.Source, CustomText: body.CustomText})
}

// editorListSlideSources 返回项目内所有页的讲稿来源选择（M3 ⑥）。
func editorListSlideSources(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if srcStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "slide script source store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if projectID == "" {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	choices, err := srcStore.List(tenant.WithContext(r.Context(), principal.TenantID), principal.TenantID, projectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]editorSlideSourceItem, 0, len(choices))
	for _, choice := range choices {
		items = append(items, editorSlideSourceItem{SlideID: choice.SlideID, Source: string(choice.Kind), CustomText: choice.CustomText})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": items})
}

// registerRevisionRoutes 挂载源版本历史只读端点（buf/protoc 不可用，不新增 Connect RPC）：
//   - GET /projects/{pid}/revisions：返回 current_revision 与未软删版本列表（倒序），供版本抽屉展示。
func registerRevisionRoutes(mux *http.ServeMux, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/revisions", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListRevisions(w, r, projects, members, recorder)
	})))
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
			"is_current":     rv.RevisionNo == proj.CurrentRevision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"current_revision": proj.CurrentRevision, "revisions": out})
}

// editorDiffRevisions 比较两个源版本的幻灯片差异（V4.0 §7.1 版本管理增强）。
// GET /projects/{pid}/revisions/{revA}/diff/{revB}
// 返回 {added: [{slideId,name}], removed: [...], changed: [{slideId,oldName,newName,oldNotes,newNotes}]}
func editorDiffRevisions(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	revAStr := r.PathValue("revA")
	revBStr := r.PathValue("revB")
	if projectID == "" || revAStr == "" || revBStr == "" {
		http.Error(w, "project_id, revA, revB are required", http.StatusBadRequest)
		return
	}
	revA, err := strconv.Atoi(revAStr)
	if err != nil {
		http.Error(w, "invalid revA", http.StatusBadRequest)
		return
	}
	revB, err := strconv.Atoi(revBStr)
	if err != nil {
		http.Error(w, "invalid revB", http.StatusBadRequest)
		return
	}
	if revA == revB {
		writeJSON(w, http.StatusOK, map[string]any{"added": []map[string]any{}, "removed": []map[string]any{}, "changed": []map[string]any{}})
		return
	}

	docs, err := loadProjectDocuments(r.Context(), projects, objects, principal.TenantID, projectID, revA, revB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	diff := diffDocuments(docs[revA], docs[revB])
	writeJSON(w, http.StatusOK, diff)
}

// loadProjectDocuments 按 revision_no 顺序读取两个版本的 extracted document.json。
// 返回 map[revisionNo]*project.Document；缺失版本为 nil。
func loadProjectDocuments(ctx context.Context, projects project.ProjectStore, objects objectstore.ObjectStore, tenantID, projectID string, revA, revB int) (map[int]*project.Document, error) {
	out := make(map[int]*project.Document, 2)
	for _, rev := range [...]int{revA, revB} {
	_, err := projects.GetSourceRevision(ctx, tenantID, projectID, rev)
		if err != nil {
			out[rev] = nil
			continue
		}
		key := objectstore.ObjectKey{
			TenantID: tenantID, ProjectID: projectID,
			Revision: srcRevString(rev), AssetType: "document", AssetID: "extracted", Ext: "json",
		}
		rc, _, err := objects.Get(ctx, key)
		if err != nil {
			out[rev] = nil
			continue
		}
		var data []byte
		data, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			out[rev] = nil
			continue
		}
		var doc project.Document
		if err := json.Unmarshal(data, &doc); err != nil {
			out[rev] = nil
			continue
		}
		out[rev] = &doc
	}
	return out, nil
}

// diffDocuments 比较两个 Document，返回页级差异。
// oldDoc=revA, newDoc=revB：added=新增页、removed=删除页、changed=标题/备注变更页。
func diffDocuments(oldDoc, newDoc *project.Document) map[string]any {
	added := []map[string]any{}
	removed := []map[string]any{}
	changed := []map[string]any{}

	oldIndex := make(map[string]*project.Page, len(oldDoc.Pages))
	newIndex := make(map[string]*project.Page, len(newDoc.Pages))
	for _, pg := range oldDoc.Pages {
		oldIndex[pg.SlideID] = pg
	}
	for _, pg := range newDoc.Pages {
		newIndex[pg.SlideID] = pg
	}

	// removed: 在旧版有、新版无
	for id, oldPg := range oldIndex {
		if _, ok := newIndex[id]; !ok {
			removed = append(removed, map[string]any{"slideId": id, "name": oldPg.Name, "pageCount": oldDoc.Features.PageCount})
		}
	}
	// added: 在新版有、旧版无
	for id, newPg := range newIndex {
		if _, ok := oldIndex[id]; !ok {
			added = append(added, map[string]any{"slideId": id, "name": newPg.Name, "pageCount": newDoc.Features.PageCount})
		}
	}
	// changed: 同 slideId 但 title 或 notes 变化
	for id, newPg := range newIndex {
		oldPg, ok := oldIndex[id]
		if !ok {
			continue
		}
		if oldPg.Name != newPg.Name || oldPg.NotesText != newPg.NotesText {
			changed = append(changed, map[string]any{
				"slideId":    id,
				"oldName":    oldPg.Name,
				"newName":    newPg.Name,
				"oldNotes":   truncate(oldPg.NotesText, 80),
				"newNotes":   truncate(newPg.NotesText, 80),
				"pageCount":  newDoc.Features.PageCount,
			})
		}
	}
	return map[string]any{
		"added":   added,
		"removed": removed,
		"changed": changed,
	}
}


// registerDiffRevisionRoutes 挂载源版本 diff 端点。
func registerDiffRevisionRoutes(mux *http.ServeMux, projects project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/revisions/{revA}/diff/{revB}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDiffRevisions(w, r, projects, objects, members, recorder)
	})))
}
