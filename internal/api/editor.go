package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/tenant"
)

// registerEditorRoutes 挂载核心创作辅助的 HTTP 端点（B2 M2 真实渲染缩略图）。
// GET /projects/{pid}/slides/render 返回每页渲染 PNG 的短期签名可读 URL，按 slideId 对齐，
// 供编辑器缩略图列表与 PPT 预览使用。受 auth 中间件保护（仅项目所属租户成员可访问）。
func registerEditorRoutes(mux *http.ServeMux, jobs JobStore, objects objectstore.ObjectStore, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/slides/render", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSlideRender(w, r, jobs, objects)
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
