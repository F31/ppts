package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/membership"
)

// GatewayHandler 提供模型网关管理 HTTP CRUD 端点（admin only，G3 可视化配置）。
type GatewayHandler struct {
	store   gateway.StoreResolver
	members membership.Reader
	audit   audit.Store
}

// NewGatewayHandler 创建网关管理 handler。
func NewGatewayHandler(store gateway.StoreResolver, members membership.Reader, auditStore audit.Store) *GatewayHandler {
	return &GatewayHandler{store: store, members: members, audit: auditStore}
}

func (h *GatewayHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /api/model-gateways", auth(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/model-gateways", auth(http.HandlerFunc(h.create)))
	mux.Handle("PUT /api/model-gateways/{name}", auth(http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/model-gateways/{name}", auth(http.HandlerFunc(h.delete)))
	mux.Handle("POST /api/model-gateways/{name}/set-default", auth(http.HandlerFunc(h.setDefault)))
	mux.Handle("POST /api/model-gateways/{name}/test", auth(http.HandlerFunc(h.test)))
}

type gatewayRequest struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	BaseURL     string `json:"baseUrl"`
	APIKey      string `json:"apiKey"`
	Model       string `json:"model"`
	VisionModel string `json:"visionModel"`
	Voice       string `json:"voice"`
	SampleRate  int    `json:"sampleRate"`
	IsDefault   *bool  `json:"isDefault,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Version     int    `json:"version"`
}

func (h *GatewayHandler) requireAdmin(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, `{"code":"unauthenticated","message":"missing principal"}`, http.StatusUnauthorized)
		return p, false
	}
	if h.members != nil {
		role, err := h.members.GetRole(r.Context(), p.TenantID, p.UserID)
		if errors.Is(err, membership.ErrNotFound) {
			http.Error(w, `{"code":"forbidden","message":"no role granted in tenant"}`, http.StatusForbidden)
			return Principal{}, false
		}
		if err != nil {
			http.Error(w, `{"code":"internal","message":"failed to load role"}`, http.StatusInternalServerError)
			return Principal{}, false
		}
		if roleRank(role) < roleRank(membership.RoleAdmin) {
			http.Error(w, `{"code":"forbidden","message":"admin role required"}`, http.StatusForbidden)
			return Principal{}, false
		}
	}
	return p, true
}

func (h *GatewayHandler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	kind := gateway.Kind(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind == gateway.KindTTS || kind == gateway.KindLLM {
		gws, err := h.store.List(r.Context(), p.TenantID, kind)
		if err != nil {
			http.Error(w, `{"code":"internal","message":"failed to list gateways"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"gateways": gws})
		return
	}
	// 无 kind 过滤：返回两类全部。
	var out []*gateway.Gateway
	for _, k := range []gateway.Kind{gateway.KindTTS, gateway.KindLLM} {
		gws, err := h.store.List(r.Context(), p.TenantID, k)
		if err != nil {
			http.Error(w, `{"code":"internal","message":"failed to list gateways"}`, http.StatusInternalServerError)
			return
		}
		out = append(out, gws...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"gateways": out})
}

func (h *GatewayHandler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req gatewayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	gw, err := buildGateway(p.TenantID, &req)
	if err != nil {
		http.Error(w, `{"code":"invalid","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	wasDefault := req.IsDefault != nil && *req.IsDefault
	gw.IsDefault = false
	if err := h.store.Create(r.Context(), gw, req.APIKey); err != nil {
		http.Error(w, `{"code":"internal","message":"failed to create gateway"}`, http.StatusInternalServerError)
		return
	}
	if wasDefault {
		if err := h.store.SetDefault(r.Context(), p.TenantID, gw.Name, gw.Kind); err != nil && !errors.Is(err, gateway.ErrNotFound) {
			http.Error(w, `{"code":"internal","message":"failed to set default"}`, http.StatusInternalServerError)
			return
		}
	}
	_ = h.record(r, p, "gateway.create", gw.Name, map[string]any{"kind": string(gw.Kind)})
	created, _ := h.store.Get(r.Context(), p.TenantID, gw.Name, gw.Kind)
	writeJSON(w, http.StatusCreated, map[string]any{"gateway": created})
}

func (h *GatewayHandler) update(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var req gatewayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Version <= 0 {
		http.Error(w, `{"code":"invalid","message":"version is required for updates"}`, http.StatusBadRequest)
		return
	}
	kind, err := parseKind(req.Kind)
	if err != nil {
		http.Error(w, `{"code":"invalid","message":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	// 以当前值合并请求字段：未提供的字段保持不变。
	cur, err := h.store.Get(r.Context(), p.TenantID, name, kind)
	if err != nil {
		if errors.Is(err, gateway.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"gateway not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to load gateway"}`, http.StatusInternalServerError)
		return
	}
	gw := &gateway.Gateway{
		TenantID: cur.TenantID, Name: cur.Name, Kind: cur.Kind,
		Provider:    valueOr(req.Provider, cur.Provider),
		BaseURL:     valueOr(req.BaseURL, cur.BaseURL),
		Model:       valueOr(req.Model, cur.Model),
		VisionModel: valueOr(req.VisionModel, cur.VisionModel),
		Voice:       valueOr(req.Voice, cur.Voice),
		SampleRate:  cur.SampleRate, IsDefault: cur.IsDefault, Enabled: cur.Enabled,
		Version: req.Version,
	}
	if req.SampleRate > 0 {
		gw.SampleRate = req.SampleRate
	}
	if req.IsDefault != nil {
		gw.IsDefault = *req.IsDefault
	}
	if req.Enabled != nil {
		gw.Enabled = *req.Enabled
	}
	if err := h.store.Update(r.Context(), gw, req.APIKey); err != nil {
		if errors.Is(err, gateway.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"gateway not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to update gateway"}`, http.StatusInternalServerError)
		return
	}
	_ = h.record(r, p, "gateway.update", name, map[string]any{"kind": string(gw.Kind), "version": gw.Version})
	updated, _ := h.store.Get(r.Context(), p.TenantID, name, gw.Kind)
	writeJSON(w, http.StatusOK, map[string]any{"gateway": updated})
}

func valueOr(req, cur string) string {
	if strings.TrimSpace(req) != "" {
		return strings.TrimSpace(req)
	}
	return cur
}

func (h *GatewayHandler) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	kind, _ := parseKind(r.URL.Query().Get("kind"))
	if kind == "" {
		http.Error(w, `{"code":"invalid","message":"kind query param is required (tts|llm)"}`, http.StatusBadRequest)
		return
	}
	if err := h.store.Delete(r.Context(), p.TenantID, name, kind); err != nil {
		if errors.Is(err, gateway.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"gateway not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to delete gateway"}`, http.StatusInternalServerError)
		return
	}
	_ = h.record(r, p, "gateway.delete", name, map[string]any{"kind": string(kind)})
	w.WriteHeader(http.StatusNoContent)
}

func (h *GatewayHandler) setDefault(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	kind, _ := parseKind(r.URL.Query().Get("kind"))
	if kind == "" {
		http.Error(w, `{"code":"invalid","message":"kind query param is required (tts|llm)"}`, http.StatusBadRequest)
		return
	}
	if err := h.store.SetDefault(r.Context(), p.TenantID, name, kind); err != nil {
		if errors.Is(err, gateway.ErrNotFound) {
			http.Error(w, `{"code":"not_found","message":"gateway not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to set default"}`, http.StatusInternalServerError)
		return
	}
	_ = h.record(r, p, "gateway.set_default", name, map[string]any{"kind": string(kind)})
	w.WriteHeader(http.StatusNoContent)
}

func (h *GatewayHandler) test(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	kind, _ := parseKind(r.URL.Query().Get("kind"))
	if kind == "" {
		http.Error(w, `{"code":"invalid","message":"kind query param is required (tts|llm)"}`, http.StatusBadRequest)
		return
	}
	cfg, err := h.store.ResolveNamed(r.Context(), p.TenantID, name, kind)
	if err != nil {
		http.Error(w, `{"code":"not_found","message":"gateway not found"}`, http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	latency, err := gateway.Probe(ctx, cfg)
	resp := map[string]any{"ok": err == nil, "latencyMs": latency}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *GatewayHandler) record(r *http.Request, p Principal, action, resourceID string, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	return h.audit.Record(r.Context(), audit.Event{
		TenantID: p.TenantID, ActorUser: p.UserID, Action: action,
		ResourceType: "model_gateway", ResourceID: resourceID, Metadata: meta,
	})
}

func buildGateway(tenantID string, req *gatewayRequest) (*gateway.Gateway, error) {
	kind, err := parseKind(req.Kind)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		return nil, errors.New("baseUrl is required")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, errors.New("model is required")
	}
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = "openai_compatible"
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return &gateway.Gateway{
		TenantID: tenantID, Name: name, Kind: kind, Provider: provider,
		BaseURL: strings.TrimRight(baseURL, "/"), Model: model,
		VisionModel: strings.TrimSpace(req.VisionModel), Voice: strings.TrimSpace(req.Voice),
		SampleRate: req.SampleRate, Enabled: enabled,
		Version: req.Version,
	}, nil
}

func parseKind(s string) (gateway.Kind, error) {
	switch strings.TrimSpace(s) {
	case "tts":
		return gateway.KindTTS, nil
	case "llm":
		return gateway.KindLLM, nil
	case "":
		return "", errors.New("kind is required (tts|llm)")
	default:
		return "", errors.New("kind must be tts or llm")
	}
}
