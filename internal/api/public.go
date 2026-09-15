package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/public"
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
func registerPublicRoutes(mux *http.ServeMux, store public.Store, objects objectstore.ObjectStore, members membership.Reader, auth func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /public/works", func(w http.ResponseWriter, r *http.Request) {
		publicListWorks(w, r, store)
	})
	mux.HandleFunc("GET /public/works/{id}", func(w http.ResponseWriter, r *http.Request) {
		publicGetWork(w, r, store, objects)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
