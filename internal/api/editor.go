package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/tenant"
)

// registerEditorRoutes 挂载核心创作辅助的 HTTP 端点（B2 M2 真实渲染缩略图；B2 M3 ⑥ 无备注页来源）。
// GET /projects/{pid}/slides/render 返回每页渲染 PNG 的短期签名可读 URL，按 slideId 对齐，供编辑器缩略图与 PPT 预览。
// PUT /projects/{pid}/slides/{sid}/source + GET /projects/{pid}/slides/sources 管理"无备注页讲稿来源"选择。
// 均受 auth 中间件保护（仅项目所属租户成员可访问）。
func registerEditorRoutes(mux *http.ServeMux, jobs JobStore, objects objectstore.ObjectStore, srcStore app.ScriptSourceStore, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/slides/render", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSlideRender(w, r, jobs, objects)
	})))
	// M3 ⑥：无备注页讲稿来源选择。
	mux.Handle("PUT /projects/{pid}/slides/{sid}/source", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSetSlideSource(w, r, srcStore)
	})))
	mux.Handle("GET /projects/{pid}/slides/sources", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListSlideSources(w, r, srcStore)
	})))
}

type editorSlideRenderItem struct {
	SlideID string `json:"slideId"`
	URL     string `json:"url"`
}

// editorSlideRender 读取最近一次成功解析任务的页面清单（PageManifest），为每页渲染 PNG 签发短期匿名可读 URL。
// 解析未完成、页面清单缺失或某页渲染图不存在时，对应项跳过；整体缺失时返回空列表，前端优雅降级为序号/标题缩略图。
func editorSlideRender(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
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
func editorSetSlideSource(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore) {
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
func editorListSlideSources(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore) {
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
