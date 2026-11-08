package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

type projectVoiceSettingsPayload struct {
	Model       string `json:"model"`
	Voice       string `json:"voice"`
	RatePercent int    `json:"ratePercent"`
}

// editorGetVoiceSettings 读取项目语音属性；未保存过时返回缺省（rate=100）。
func editorGetVoiceSettings(w http.ResponseWriter, r *http.Request, voiceStore app.VoiceSettingsStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if voiceStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "voice settings store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	settings, err := voiceStore.Get(r.Context(), principal.TenantID, projectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, projectVoiceSettingsPayload{Model: settings.Model, Voice: settings.Voice, RatePercent: settings.RatePercent})
}

// editorSaveVoiceSettings 保存项目语音属性（语音模型 / 音色 / 语速）。
func editorSaveVoiceSettings(w http.ResponseWriter, r *http.Request, voiceStore app.VoiceSettingsStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if voiceStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "voice settings store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "permission_denied", "message": "requires editor role"})
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	var body projectVoiceSettingsPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "invalid JSON body"})
		return
	}
	settings := app.VoiceSettings{
		Model: strings.TrimSpace(body.Model), Voice: strings.TrimSpace(body.Voice), RatePercent: body.RatePercent,
	}
	if settings.RatePercent == 0 {
		settings.RatePercent = 100
	}
	if settings.RatePercent < 50 || settings.RatePercent > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "rate_percent must be between 50 and 200"})
		return
	}
	if err := voiceStore.Save(r.Context(), principal.TenantID, projectID, settings); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, projectVoiceSettingsPayload{Model: settings.Model, Voice: settings.Voice, RatePercent: settings.RatePercent})
}

type voiceModelItem struct {
	Name      string   `json:"name"`
	Model     string   `json:"model"`
	Voices    []string `json:"voices"`
	IsDefault bool     `json:"isDefault"`
}

// editorVoiceModels 列出本租户已启用的 TTS 网关（语音模型）及其配置的音色。
// 音色来自网关配置的 voice 字段（多个逗号分隔）；无可用网关时返回空列表，前端据此降级。
func editorVoiceModels(w http.ResponseWriter, r *http.Request, gatewayStore gateway.StoreResolver, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if gatewayStore == nil {
		// 未配置模型网关（dev 单租户常见）等价于「当前没有可用语音模型」，与下方
		// len(gws)==0 的 200 空数组是同一语义。此前返回 503 feature_disabled，把
		// 「配置缺失」说成「服务不可用」，且浏览器对每个 5xx 都记红色错误，编辑器
		// 一打开就刷屏。保留 disabled 标记与原因，不静默吞掉诊断信息。
		// 边界：未挂载的功能路由仍由 featureDisabledHandler 返回 503，本处只针对
		// 「列出可选模型」这条主路径上的能力探测。
		writeJSON(w, http.StatusOK, map[string]any{
			"models":   []voiceModelItem{},
			"disabled": true,
			"message":  "model gateway disabled",
		})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	gws, err := gatewayStore.List(r.Context(), principal.TenantID, gateway.KindTTS)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(gws) == 0 {
		gws, err = gatewayStore.List(r.Context(), gateway.PlatformTenantID, gateway.KindTTS)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	items := make([]voiceModelItem, 0, len(gws))
	for _, gw := range gws {
		if gw == nil || !gw.Enabled {
			continue
		}
		voices := remoteTTSVoices(r.Context(), gatewayStore, gw.TenantID, gw)
		if len(voices) == 0 {
			voices = configuredTTSVoices(gw)
		}
		items = append(items, voiceModelItem{Name: gw.Name, Model: gw.Model, Voices: voices, IsDefault: gw.IsDefault})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": items})
}

func configuredTTSVoices(gw *gateway.Gateway) []string {
	voices := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(raw string) {
		v := strings.TrimSpace(raw)
		if v == "" || (gw.Model != "" && v == gw.Model) {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		voices = append(voices, v)
	}
	for _, raw := range strings.Split(gw.Voice, ",") {
		add(raw)
	}
	if len(voices) == 0 && strings.TrimSpace(gw.Model) != "" {
		add(tts.DefaultSiliconFlowVoice(gw.Model))
	}
	return voices
}

func remoteTTSVoices(ctx context.Context, store gateway.StoreResolver, tenantID string, gw *gateway.Gateway) []string {
	cfg, err := store.ResolveNamed(ctx, tenantID, gw.Name, gateway.KindTTS)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.BaseURL) == "" {
		return nil
	}
	// 本地语音模型一般不校验 API Key；此前这里一律跳过空 key 网关，
	// 结果本地 9 个音色一个都列不出来，只能拿配置里的兜底音色。
	// 远程网关仍必须带 key，避免把「没配凭据」当成可匿名访问。
	if strings.TrimSpace(cfg.APIKey) == "" && !tts.IsLoopbackBaseURL(cfg.BaseURL) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, path := range []string{"/v1/audio/voices", "/v1/voices"} {
		voices := fetchTTSVoices(ctx, cfg, path)
		if len(voices) > 0 {
			return voices
		}
	}
	return nil
}

func fetchTTSVoices(ctx context.Context, cfg *gateway.Config, path string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.BaseURL, "/")+path, nil)
	if err != nil {
		return nil
	}
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	return parseVoiceList(body, cfg.Model)
}

func parseVoiceList(body []byte, model string) []string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	var add func(any)
	add = func(v any) {
		switch x := v.(type) {
		case string:
			id := strings.TrimSpace(x)
			if id == "" || id == model {
				return
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		case map[string]any:
			for _, key := range []string{"id", "voice", "voice_id", "name"} {
				if raw, ok := x[key]; ok {
					add(raw)
					return
				}
			}
		case []any:
			for _, item := range x {
				add(item)
			}
		}
	}
	switch root := payload.(type) {
	case []any:
		add(root)
	case map[string]any:
		for _, key := range []string{"voices", "data", "items", "result"} {
			if raw, ok := root[key]; ok {
				add(raw)
			}
		}
	}
	return out
}
