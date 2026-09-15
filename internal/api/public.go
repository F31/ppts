package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/public"
	"github.com/F31/ppts/internal/tenant"
)

// registerPublicRoutes 挂载公开区 HTTP 端点（V1.6 C-1）。
//   - 匿名只读：GET /public/works（列表）、GET /public/works/{id}（详情，附封面签名 URL）
//   - 受保护写（auth 中间件）：
//     POST /public/works          用户发布自己项目（pending）
//     POST /public/featured       管理员发布官方精选（approved）
//     PUT  /public/works/{id}/review  管理员审核
//     DELETE /public/works/{id}   管理员删除
//
// 与 SPA catch-all（GET /{path...}）共存：精确 /public/works* 路由优先于通配。
func registerPublicRoutes(mux *http.ServeMux, store public.Store, objects objectstore.ObjectStore, members membership.Reader, jobs JobStore, auth func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /public/works", func(w http.ResponseWriter, r *http.Request) {
		publicListWorks(w, r, store)
	})
	mux.HandleFunc("GET /public/works/{id}", func(w http.ResponseWriter, r *http.Request) {
		publicGetWork(w, r, store, objects)
	})
	mux.HandleFunc("GET /public/works/{id}/manifest", func(w http.ResponseWriter, r *http.Request) {
		publicGetManifest(w, r, store, jobs, objects)
	})
	mux.Handle("POST /public/works", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicPublish(w, r, store)
	})))
	mux.Handle("POST /public/featured", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicFeature(w, r, store, members)
	})))
	mux.Handle("PUT /public/works/{id}/review", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicReview(w, r, store, members)
	})))
	mux.Handle("DELETE /public/works/{id}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicDelete(w, r, store, members)
	})))
}

func publicListWorks(w http.ResponseWriter, r *http.Request, store public.Store) {
	kind := r.URL.Query().Get("kind")
	if kind != "" && !public.Kind(kind).Valid() {
		http.Error(w, "invalid kind", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, next, err := store.ListApproved(r.Context(), public.Kind(kind), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// publicWorkResponse 在 Publication 基础上附加匿名可读的封面签名 URL。
type publicWorkResponse struct {
	*public.Publication
	CoverURL string `json:"cover_url,omitempty"`
}

func publicGetWork(w http.ResponseWriter, r *http.Request, store public.Store, objects objectstore.ObjectStore) {
	id := r.PathValue("id")
	pub, err := store.GetApproved(r.Context(), id)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp := publicWorkResponse{Publication: pub}
	if pub.CoverObjectKey != "" {
		if key, perr := objectstore.Parse(pub.CoverObjectKey); perr == nil {
			if raw, serr := objects.SignedURL(r.Context(), key, objectstore.OpRead, time.Hour); serr == nil {
				if parser, ok := objects.(signedURLParser); ok {
					raw = rewriteLocalSignedURL(parser, raw)
				}
				resp.CoverURL = raw
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// publicManifestResource / publicManifest 是与前端 Player（types.PlaybackManifest）同构的匿名清单 JSON。
// 枚举以 .String() 名称字符串输出（与 Connect protojson 一致），确保 Player 的 resource.type 字符串比较成立。
type publicManifestResource struct {
	Type        string `json:"type"`
	Key         string `json:"key"`
	SignedUrl   string `json:"signedUrl"`
	ContentType string `json:"contentType,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	SlideId     string `json:"slideId,omitempty"`
	SegmentId   string `json:"segmentId,omitempty"`
}

type publicManifest struct {
	ProjectId     string                   `json:"projectId"`
	TimelineKey   string                   `json:"timelineKey"`
	TimelineJson  string                   `json:"timelineJson"`
	Resources     []publicManifestResource `json:"resources"`
	ExpiresAtUnix int64                    `json:"expiresAtUnix"`
}

// publicGetManifest 为已批准公开作品生成匿名可播放的讲解清单（B3 音频播放接入）。
// 仅放行 approved 作品；复用既有 timeline 打包与签名逻辑，音频/字幕/页面 PNG 均签发短期匿名可读 URL。
// 作品若无成功配音任务（narration 未就绪），返回 404，前端优雅降级为「暂未生成语音讲解」。
func publicGetManifest(w http.ResponseWriter, r *http.Request, store public.Store, jobs JobStore, objects objectstore.ObjectStore) {
	id := r.PathValue("id")
	pub, err := store.GetApproved(r.Context(), id)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	job, err := jobs.LatestSucceededJob(ctx, pub.TenantID, pub.ProjectID, string(pipeline.KindNarration))
	if errors.Is(err, pipeline.ErrNoSucceededJob) {
		http.Error(w, "narration not ready", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	timelineKey, err := jobs.StepResultRef(tenant.WithContext(ctx, pub.TenantID), job.ID, "timeline")
	if err != nil || timelineKey == "" {
		http.Error(w, "narration not ready", http.StatusNotFound)
		return
	}
	// 页面 PNG 为可选项：获取失败则优雅降级为音频+字幕。
	pagePngKeys, _ := resolvePagePngKeys(ctx, jobs, objects, pub.TenantID, pub.ProjectID, timelineKey)
	bundle, _, err := loadBundle(ctx, pub.TenantID, timelineKey)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	parser, _ := objects.(signedURLParser)
	ttl := time.Hour
	resources, err := signManifestResources(ctx, objects, parser, pub.TenantID, timelineKey, bundle, pagePngKeys, ttl)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]publicManifestResource, 0, len(resources))
	for _, res := range resources {
		out = append(out, publicManifestResource{
			Type:        res.Type.String(),
			Key:         res.Key,
			SignedUrl:   res.SignedUrl,
			ContentType: res.ContentType,
			SizeBytes:   res.SizeBytes,
			ContentHash: res.ContentHash,
			SlideId:     res.SlideId,
			SegmentId:   res.SegmentId,
		})
	}
	timelineJSON, err := json.Marshal(bundle.Timeline)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, publicManifest{
		ProjectId:     pub.ProjectID,
		TimelineKey:   timelineKey,
		TimelineJson:  string(timelineJSON),
		Resources:     out,
		ExpiresAtUnix: time.Now().Add(ttl).Unix(),
	})
}

func publicPublish(w http.ResponseWriter, r *http.Request, store public.Store) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		ProjectID      string `json:"project_id"`
		Title          string `json:"title"`
		Summary        string `json:"summary"`
		CoverObjectKey string `json:"cover_object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" || body.Title == "" {
		http.Error(w, "project_id and title are required", http.StatusBadRequest)
		return
	}
	if body.CoverObjectKey != "" {
		key, err := objectstore.Parse(body.CoverObjectKey)
		if err != nil {
			http.Error(w, "invalid cover_object_key", http.StatusBadRequest)
			return
		}
		if err := key.EnsureTenant(principal.TenantID); err != nil {
			http.Error(w, "cover tenant mismatch", http.StatusForbidden)
			return
		}
	}
	in := public.NewPublication{
		ProjectID:      body.ProjectID,
		Kind:           public.KindUser,
		Title:          body.Title,
		Summary:        body.Summary,
		CoverObjectKey: body.CoverObjectKey,
		CreatedBy:      principal.UserID,
	}
	pub, err := store.Publish(r.Context(), principal.TenantID, in)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, pub)
}

func publicFeature(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	var body struct {
		ProjectID      string `json:"project_id"`
		Title          string `json:"title"`
		Summary        string `json:"summary"`
		CoverObjectKey string `json:"cover_object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" || body.Title == "" {
		http.Error(w, "project_id and title are required", http.StatusBadRequest)
		return
	}
	if body.CoverObjectKey != "" {
		key, err := objectstore.Parse(body.CoverObjectKey)
		if err != nil {
			http.Error(w, "invalid cover_object_key", http.StatusBadRequest)
			return
		}
		if err := key.EnsureTenant(principal.TenantID); err != nil {
			http.Error(w, "cover tenant mismatch", http.StatusForbidden)
			return
		}
	}
	in := public.NewPublication{
		ProjectID:      body.ProjectID,
		Kind:           public.KindFeatured,
		Title:          body.Title,
		Summary:        body.Summary,
		CoverObjectKey: body.CoverObjectKey,
		CreatedBy:      principal.UserID,
	}
	pub, err := store.Feature(r.Context(), principal.TenantID, in)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, pub)
}

func publicReview(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var body struct {
		Approve bool `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	pub, err := store.Review(r.Context(), principal.TenantID, id, body.Approve, principal.UserID)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pub)
}

func publicDelete(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := store.Delete(r.Context(), principal.TenantID, id)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireAdmin 校验当前身份在租户内达到 admin 角色；members 为 nil 时放行（开发/私有化）。
func requireAdmin(ctx context.Context, members membership.Reader, w http.ResponseWriter) (Principal, bool) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return Principal{}, false
	}
	if members != nil {
		role, err := members.GetRole(ctx, principal.TenantID, principal.UserID)
		if err != nil || roleRank(role) < roleRank(membership.RoleAdmin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return Principal{}, false
		}
	}
	return principal, true
}

func publicListMine(w http.ResponseWriter, r *http.Request, store public.Store) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	status := public.Status(r.URL.Query().Get("status"))
	items, err := store.ListMine(r.Context(), principal.TenantID, principal.UserID, status)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func publicReviewQueue(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	kind := public.Kind(r.URL.Query().Get("kind"))
	items, err := store.ListPending(r.Context(), principal.TenantID, kind)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
