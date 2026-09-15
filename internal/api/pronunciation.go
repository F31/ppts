package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/pronunciation"
)

// PronunciationHandler 提供发音词典 HTTP CRUD 端点。
type PronunciationHandler struct {
	store pronunciation.Store
}

func NewPronunciationHandler(store pronunciation.Store) *PronunciationHandler {
	return &PronunciationHandler{store: store}
}

func (h *PronunciationHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /api/pronunciation", auth(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/pronunciation", auth(http.HandlerFunc(h.create)))
	mux.Handle("PUT /api/pronunciation/{id}", auth(http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/pronunciation/{id}", auth(http.HandlerFunc(h.delete)))
}

func (h *PronunciationHandler) list(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	tenantID := principal.TenantID
	dicts, err := h.store.ListByTenant(r.Context(), tenantID)
	if err != nil {
		http.Error(w, `{"code":"internal","message":"failed to list dictionaries"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dictionaries": dicts})
}

func (h *PronunciationHandler) create(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	tenantID := principal.TenantID
	var req struct {
		Name  string              `json:"name"`
		Rules pronunciation.Rules `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, `{"code":"invalid","message":"name is required"}`, http.StatusBadRequest)
		return
	}
	dict := &pronunciation.Dictionary{
		ID:       uuid.New().String(),
		TenantID: tenantID,
		Name:     req.Name,
		Rules:    req.Rules,
	}
	if err := h.store.Create(r.Context(), dict); err != nil {
		http.Error(w, `{"code":"internal","message":"failed to create dictionary"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"dictionary": dict})
}

func (h *PronunciationHandler) update(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	tenantID := principal.TenantID
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"code":"invalid","message":"missing id"}`, http.StatusBadRequest)
		return
	}
	var req struct {
		Name  string              `json:"name"`
		Rules pronunciation.Rules `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	dict := &pronunciation.Dictionary{
		ID:       id,
		TenantID: tenantID,
		Name:     req.Name,
		Rules:    req.Rules,
	}
	if err := h.store.Update(r.Context(), dict); err != nil {
		if errors.Is(err, pronunciation.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"dictionary not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to update dictionary"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dictionary": dict})
}

func (h *PronunciationHandler) delete(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return
	}
	tenantID := principal.TenantID
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"code":"invalid","message":"missing id"}`, http.StatusBadRequest)
		return
	}
	if err := h.store.Delete(r.Context(), tenantID, id); err != nil {
		if errors.Is(err, pronunciation.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"dictionary not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to delete dictionary"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
