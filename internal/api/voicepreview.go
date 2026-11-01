package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/project"
)

// 音色试听（V4.0 §5「提供先试听本页按钮，不要求等全篇生成」的最小实现）。
//
// 边界刻意收窄：**只回答"这个音色 + 这个语速听起来什么样"**，不写对象存储、
// 不建任务、不进时间轴。返回的是一次性音频字节（前端用 Blob URL 播），
// 因此没有需要清理的中间产物，也不会在成片里多出"试听残留"的音频资产。
//
// 与真实生成保持同一条链路：供应商解析走与 worker 相同的
// gateway.Resolve(tenant, tts) 缺省网关——试听必须代表成片实际会用的模型，
// 而不是界面上被选中但生成时不生效的那一行配置。
const (
	voicePreviewMaxRunes  = 120
	voicePreviewBudget    = 20 * time.Second
	voicePreviewDefaultZH = "这是一段试听示例，用来确认音色和语速是否符合预期。"
	voicePreviewDefaultEN = "This is a short sample so you can check the voice and speaking rate."
)

type voicePreviewBody struct {
	Voice string `json:"voice"`
	// Model 是用户在"语音模型"下拉里选的模型；为空表示用项目/租户默认。
	// 试听与生成必须走同一条解析，否则试听听到的音色不代表生成结果。
	Model       string `json:"model"`
	RatePercent int    `json:"ratePercent"`
	Text        string `json:"text"`
	Language    string `json:"language"`
}

type voicePreviewDeps struct {
	gateways gateway.StoreResolver
	// provider 非空时直接使用（测试 / 离线部署），不再解析网关。
	provider tts.TTSProvider
	// buildProvider 在解析到网关配置后构建供应商；nil 用 gateway.NewTTSProvider。
	buildProvider func(*gateway.Config) tts.TTSProvider
	projects      project.ProjectStore
	members       membership.Reader
	recorder      audit.Recorder
}

// registerVoicePreviewRoutes 挂载 POST /projects/{pid}/voice-preview。
func registerVoicePreviewRoutes(mux *http.ServeMux, deps voicePreviewDeps, auth func(http.Handler) http.Handler) {
	if deps.buildProvider == nil {
		deps.buildProvider = gateway.NewTTSProvider
	}
	mux.Handle("POST /projects/{pid}/voice-preview", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		voicePreview(w, r, deps)
	})))
}

func voicePreview(w http.ResponseWriter, r *http.Request, deps voicePreviewDeps) {
	if deps.gateways == nil && deps.provider == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "model gateway disabled"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// 试听会真实计费：只读成员不该用别人的额度做音色实验。
	if err := requireRole(r.Context(), deps.members, membership.RoleEditor); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "permission_denied", "message": "requires editor role"})
		return
	}
	projectID, ok := requireProjectAccess(w, r, deps.projects, deps.members, deps.recorder)
	if !ok {
		return
	}

	var body voicePreviewBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		observability.VoicePreview("invalid")
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "invalid JSON body"})
		return
	}
	voice := strings.TrimSpace(body.Voice)
	if voice == "" {
		observability.VoicePreview("invalid")
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "voice is required"})
		return
	}
	rate := body.RatePercent
	if rate == 0 {
		rate = 100
	}
	if rate < 50 || rate > 200 {
		observability.VoicePreview("invalid")
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "ratePercent must be between 50 and 200"})
		return
	}
	language := strings.TrimSpace(body.Language)
	if language == "" {
		language = requestLanguage(r.Header)
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		text = voicePreviewDefaultText(language)
	}
	// 超长一律拒绝，不静默截断：用户听到的会变短，却看不出"是被砍过"的。
	if n := utf8.RuneCountInString(text); n > voicePreviewMaxRunes {
		observability.VoicePreview("invalid")
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":    "text_too_long",
			"message": "preview text is limited to " + strconv.Itoa(voicePreviewMaxRunes) + " characters, got " + strconv.Itoa(n),
		})
		return
	}

	provider, model, status, code, msg := voicePreviewProvider(r.Context(), principal.TenantID, body.Model, deps)
	if provider == nil {
		observability.VoicePreview(code)
		writeJSON(w, status, map[string]any{"code": code, "message": msg})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), voicePreviewBudget)
	defer cancel()

	if caps, err := provider.Capabilities(ctx, voice); err != nil {
		observability.VoicePreview("tts_failed")
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "tts_failed", "message": sanitizeProviderError(err)})
		return
	} else if caps.MaxInputChars > 0 && utf8.RuneCountInString(text) > caps.MaxInputChars {
		observability.VoicePreview("invalid")
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":    "text_too_long",
			"message": "voice accepts at most " + strconv.Itoa(caps.MaxInputChars) + " characters",
		})
		return
	}

	started := time.Now()
	result, err := provider.Synthesize(ctx, tts.SynthesisRequest{
		LogicalOpID:   "voice-preview:" + projectID + ":" + voice,
		VoiceID:       voice,
		Text:          text,
		Language:      language,
		SpeechControl: tts.SpeechControl{RatePercent: rate},
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			observability.VoicePreview("timeout")
			writeJSON(w, http.StatusGatewayTimeout, map[string]any{
				"code":    "tts_timeout",
				"message": "voice preview did not finish within " + voicePreviewBudget.String(),
			})
			return
		}
		observability.VoicePreview("tts_failed")
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "tts_failed", "message": sanitizeProviderError(err)})
		return
	}
	if len(result.Audio) == 0 {
		// 200 + 空体在浏览器里就是"按了没反应"，必须当成失败报出来。
		observability.VoicePreview("tts_failed")
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "tts_failed", "message": "tts returned empty audio"})
		return
	}

	observability.VoicePreview("ok")
	w.Header().Set("Content-Type", detectAudioFormat(result.Format))
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Audio)))
	// 试听内容随文本/音色/语速变化，且含真实计费调用：不让中间层缓存也不进浏览器历史缓存。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-PPTS-Audio-Duration-Ms", strconv.FormatInt(result.RealDurationMS, 10))
	w.Header().Set("X-PPTS-Preview-Model", headerSafe(model))
	w.Header().Set("X-PPTS-Preview-Latency-Ms", strconv.FormatInt(time.Since(started).Milliseconds(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Audio)
}

// voicePreviewProvider 解析本次试听使用的供应商。
// 返回 (nil, 空模型, 状态码, 错误码, 提示) 表示无法试听。
//
// 与生成共用 gateway.ResolveModel：点名的模型不存在时直接拒绝，而不是用默认模型
// 合成一段"听起来也没错"的样音——那段样音会让用户以为选项生效了。
func voicePreviewProvider(ctx context.Context, tenantID, model string, deps voicePreviewDeps) (tts.TTSProvider, string, int, string, string) {
	if deps.provider != nil {
		return deps.provider, "", http.StatusOK, "", ""
	}
	cfg, err := gateway.ResolveModel(ctx, deps.gateways, tenantID, gateway.KindTTS, model)
	if errors.Is(err, gateway.ErrModelNotConfigured) {
		return nil, "", http.StatusBadRequest, "model_not_configured", err.Error()
	}
	if errors.Is(err, gateway.ErrNotFound) {
		return nil, "", http.StatusServiceUnavailable, "tts_not_configured", "no TTS model gateway configured for this tenant"
	}
	if err != nil {
		return nil, "", http.StatusInternalServerError, "internal", "resolve tts gateway failed"
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		// 网关存在但没配密钥：这是配置缺项，不是供应商故障。
		return nil, "", http.StatusServiceUnavailable, "tts_not_configured", "TTS gateway has no API key"
	}
	return deps.buildProvider(cfg), cfg.Model, http.StatusOK, "", ""
}

func voicePreviewDefaultText(language string) string {
	if strings.HasPrefix(strings.ToLower(language), "en") {
		return voicePreviewDefaultEN
	}
	return voicePreviewDefaultZH
}

func detectAudioFormat(format string) string {
	format = strings.TrimSpace(format)
	if format == "" {
		return "audio/wav"
	}
	if strings.Contains(format, "/") {
		return format
	}
	return "audio/" + format
}

// sanitizeProviderError 只把供应商错误压成一行短文本：上游响应体可能很长，
// 且绝不把配置（含密钥）回显给浏览器。
func sanitizeProviderError(err error) string {
	msg := strings.TrimSpace(strings.ReplaceAll(err.Error(), "\n", " "))
	// 按**字**截断而不是按字节：按字节切会把多字节字符砍成半个，
	// 再经 JSON 编码就变成 U+FFFD，用户看到的错误提示会带乱码。
	if runes := []rune(msg); len(runes) > 200 {
		msg = string(runes[:200])
	}
	if msg == "" {
		msg = "tts synthesis failed"
	}
	return msg
}

// headerSafe 保证写进 HTTP 头的值只有可见 ASCII（允许 ':' 与 '-'，供 voiceID 使用）；
// 中文或控制字符会被 net/http 拒绝，直接导致试听变成 500。
func headerSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
