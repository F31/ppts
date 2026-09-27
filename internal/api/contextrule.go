package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/contextrule"
)

// ContextRuleHandler 提供上下文替换规则 HTTP CRUD 端点（M5，V3.0 §3.3）。
// 只操作租户行（tenant 隔离）；平台默认行由迁移写入，不对租户暴露编辑。
type ContextRuleHandler struct {
	store contextrule.Store
}

func NewContextRuleHandler(store contextrule.Store) *ContextRuleHandler {
	return &ContextRuleHandler{store: store}
}

func (h *ContextRuleHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /api/context-rules", auth(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/context-rules", auth(http.HandlerFunc(h.create)))
	mux.Handle("PUT /api/context-rules/{id}", auth(http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/context-rules/{id}", auth(http.HandlerFunc(h.delete)))
}

func (h *ContextRuleHandler) list(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	rules, err := h.store.ListByTenant(r.Context(), principal.TenantID)
	if err != nil {
		http.Error(w, `{"code":"internal","message":"failed to list context rules"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (h *ContextRuleHandler) create(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	var req struct {
		Pattern     string `json:"pattern"`
		Replacement string `json:"replacement"`
		Priority    int    `json:"priority"`
		Enabled     bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if msg := validateContextRule(req.Pattern, req.Replacement); msg != "" {
		http.Error(w, `{"code":"invalid","message":"`+msg+`"}`, http.StatusBadRequest)
		return
	}
	rec := &contextrule.Record{
		ID:          uuid.New().String(),
		TenantID:    principal.TenantID,
		Pattern:     req.Pattern,
		Replacement: req.Replacement,
		Priority:    req.Priority,
		Enabled:     req.Enabled,
	}
	if err := h.store.Create(r.Context(), rec); err != nil {
		http.Error(w, `{"code":"internal","message":"failed to create context rule"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule": rec})
}

func (h *ContextRuleHandler) update(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"code":"invalid","message":"missing id"}`, http.StatusBadRequest)
		return
	}
	var req struct {
		Pattern     string `json:"pattern"`
		Replacement string `json:"replacement"`
		Priority    int    `json:"priority"`
		Enabled     bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if msg := validateContextRule(req.Pattern, req.Replacement); msg != "" {
		http.Error(w, `{"code":"invalid","message":"`+msg+`"}`, http.StatusBadRequest)
		return
	}
	rec := &contextrule.Record{
		ID:          id,
		TenantID:    principal.TenantID,
		Pattern:     req.Pattern,
		Replacement: req.Replacement,
		Priority:    req.Priority,
		Enabled:     req.Enabled,
	}
	if err := h.store.Update(r.Context(), rec); err != nil {
		if errors.Is(err, contextrule.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"context rule not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to update context rule"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": rec})
}

func (h *ContextRuleHandler) delete(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"code":"invalid","message":"missing id"}`, http.StatusBadRequest)
		return
	}
	if err := h.store.Delete(r.Context(), principal.TenantID, id); err != nil {
		if errors.Is(err, contextrule.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"context rule not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to delete context rule"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateContextRule 校验规则合法性：pattern 必须是合法 Go regexp（R1：非法正则不可静默入库）。
// 返回错误消息；空串表示合法。
func validateContextRule(pattern, replacement string) string {
	if pattern == "" {
		return "pattern is required"
	}
	if replacement == "" {
		return "replacement is required"
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return "pattern is not a valid regular expression"
	}
	return ""
}
