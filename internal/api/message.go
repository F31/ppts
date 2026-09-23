package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/mail"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/messaging"
)

// MessageHandler 提供消息服务配置（发件箱/短信网关）的 HTTP 端点（admin）。
type MessageHandler struct {
	store       messaging.Store
	members     membership.Reader
	audit       audit.Store
	operatorIDs map[string]bool
}

// NewMessageHandler 创建消息服务 handler。
func NewMessageHandler(store messaging.Store, members membership.Reader, auditStore audit.Store, operatorIDs map[string]bool) *MessageHandler {
	return &MessageHandler{store: store, members: members, audit: auditStore, operatorIDs: operatorIDs}
}

// Register 挂载消息服务路由。store 为 nil（未配置 AES 密钥）时统一返回 503 feature_disabled。
func (h *MessageHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	if h.store == nil {
		disabled := auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"code":    "feature_disabled",
				"message": "message service disabled: set PPTS_GATEWAY_AES_KEY_BASE64 (32-byte base64 AES-256 key) to enable",
			})
		}))
		mux.Handle("GET /api/message-channels", disabled)
		mux.Handle("PUT /api/message-channels/email", disabled)
		mux.Handle("POST /api/message-channels/email/test", disabled)
		mux.Handle("PUT /api/message-channels/sms", disabled)
		return
	}
	mux.Handle("GET /api/message-channels", auth(http.HandlerFunc(h.get)))
	mux.Handle("PUT /api/message-channels/email", auth(http.HandlerFunc(h.saveEmail)))
	mux.Handle("POST /api/message-channels/email/test", auth(http.HandlerFunc(h.testEmail)))
	mux.Handle("PUT /api/message-channels/sms", auth(http.HandlerFunc(h.saveSMS)))
}

func (h *MessageHandler) requireAdmin(w http.ResponseWriter, r *http.Request) (Principal, bool) {
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

func (h *MessageHandler) isOperator(userID string) bool { return h.operatorIDs[userID] }

func (h *MessageHandler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	operator := h.isOperator(p.UserID)
	resp := map[string]any{"is_operator": operator}
	if cfg, err := h.store.GetEmail(r.Context(), p.TenantID); err == nil {
		resp["email"] = cfg
	} else if !errors.Is(err, messaging.ErrNotFound) {
		http.Error(w, `{"code":"internal","message":"failed to load email config"}`, http.StatusInternalServerError)
		return
	}
	if operator {
		if cfg, err := h.store.GetEmail(r.Context(), messaging.PlatformTenantID); err == nil {
			resp["platform_email"] = cfg
		}
	}
	if cfg, err := h.store.GetSMS(r.Context(), p.TenantID); err == nil {
		resp["sms"] = cfg
	} else if !errors.Is(err, messaging.ErrNotFound) {
		http.Error(w, `{"code":"internal","message":"failed to load sms config"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type emailRequest struct {
	Enabled         *bool  `json:"enabled"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	FromAddress     string `json:"from_address"`
	FromName        string `json:"from_name"`
	TLSMode         string `json:"tls_mode"`
	Password        string `json:"password"`
	PlatformDefault bool   `json:"platform_default"`
}

func (h *MessageHandler) saveEmail(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req emailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	target := p.TenantID
	if req.PlatformDefault {
		if !h.isOperator(p.UserID) {
			http.Error(w, `{"code":"forbidden","message":"operator privilege required to set platform default"}`, http.StatusForbidden)
			return
		}
		target = messaging.PlatformTenantID
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	in := messaging.EmailInput{
		Enabled: enabled, Host: req.Host, Port: req.Port, Username: req.Username,
		FromAddress: req.FromAddress, FromName: req.FromName, TLSMode: req.TLSMode, Password: req.Password,
	}
	if err := h.store.SaveEmail(r.Context(), target, in); err != nil {
		if isValidationError(err) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid", "message": err.Error()})
			return
		}
		http.Error(w, `{"code":"internal","message":"failed to save email config"}`, http.StatusInternalServerError)
		return
	}
	_ = h.record(r, p, "message.email.save", map[string]any{"platform_default": req.PlatformDefault})
	cfg, _ := h.store.GetEmail(r.Context(), target)
	writeJSON(w, http.StatusOK, map[string]any{"email": cfg})
}

type emailTestRequest struct {
	To              string `json:"to"`
	PlatformDefault bool   `json:"platform_default"`
}

func (h *MessageHandler) testEmail(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req emailTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" || !strings.Contains(to, "@") {
		http.Error(w, `{"code":"invalid","message":"a valid recipient email is required"}`, http.StatusBadRequest)
		return
	}
	scope := p.TenantID
	if req.PlatformDefault {
		if !h.isOperator(p.UserID) {
			http.Error(w, `{"code":"forbidden","message":"operator privilege required"}`, http.StatusForbidden)
			return
		}
		scope = messaging.PlatformTenantID
	}
	rt, err := h.store.ResolveEmail(r.Context(), scope)
	if errors.Is(err, messaging.ErrNotFound) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "not_configured", "message": "邮件服务尚未配置或未启用"})
		return
	}
	if err != nil {
		http.Error(w, `{"code":"internal","message":"failed to resolve email config"}`, http.StatusInternalServerError)
		return
	}
	sender, err := messaging.BuildSender(rt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_config", "message": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	msg := mail.Message{
		To:      to,
		Subject: "PPTS 邮件服务测试",
		Text:    "这是一封来自 PPTS 的测试邮件。收到此邮件说明发件箱服务配置正确。",
	}
	if err := sender.Send(ctx, msg); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = h.record(r, p, "message.email.test", map[string]any{"platform_default": req.PlatformDefault})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type smsRequest struct {
	Enabled         *bool  `json:"enabled"`
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"`
	SignName        string `json:"sign_name"`
	TemplateCode    string `json:"template_code"`
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
}

func (h *MessageHandler) saveSMS(w http.ResponseWriter, r *http.Request) {
	p, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req smsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"invalid","message":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	in := messaging.SMSInput{
		Enabled: enabled, Provider: req.Provider, Endpoint: req.Endpoint, SignName: req.SignName,
		TemplateCode: req.TemplateCode, AccessKeyID: req.AccessKeyID, AccessKeySecret: req.AccessKeySecret,
	}
	if err := h.store.SaveSMS(r.Context(), p.TenantID, in); err != nil {
		http.Error(w, `{"code":"internal","message":"failed to save sms config"}`, http.StatusInternalServerError)
		return
	}
	_ = h.record(r, p, "message.sms.save", nil)
	cfg, _ := h.store.GetSMS(r.Context(), p.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{"sms": cfg})
}

func (h *MessageHandler) record(r *http.Request, p Principal, action string, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	return h.audit.Record(r.Context(), audit.Event{
		TenantID: p.TenantID, ActorUser: p.UserID, Action: action,
		ResourceType: "message_channel", ResourceID: "email", Metadata: meta,
	})
}

// isValidationError 粗略判定配置校验错误（messaging.ValidateEmail 返回纯文本错误）。
func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"required", "invalid", "must be", "not a valid"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
