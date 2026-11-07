package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/tts"
)

// ProviderCosyVoice 是本地 CosyVoice runtime 的 provider 标识。
//
// 它的接口形态与 OpenAI 兼容通道不同（表单接口 + 裸 PCM16 + 零样本参考音频），
// 不能只靠 base_url 猜：同一个 50000 端口换一个镜像就可能变成别的协议。
const ProviderCosyVoice = "cosyvoice"

// NewTTSProvider 从运行时配置构建 TTS 供应商。
func NewTTSProvider(cfg *Config) tts.TTSProvider {
	if strings.EqualFold(strings.TrimSpace(cfg.Provider), ProviderCosyVoice) {
		return tts.NewCosyVoiceProvider(tts.CosyVoiceConfig{
			BaseURL:    cfg.BaseURL,
			PromptWAV:  cosyVoicePromptWAV(),
			PromptText: os.Getenv("PPTS_COSYVOICE_PROMPT_TEXT"),
			SampleRate: cfg.SampleRate,
		})
	}
	return tts.NewSiliconFlowProvider(tts.SiliconFlowConfig{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		Voice:      cfg.Voice,
		SampleRate: cfg.SampleRate,
	})
}

// cosyVoicePromptWAV 读取零样本参考音频（部署资产，按路径缓存一次）。
//
// 读不到返回 nil，让 Synthesize 在真正需要时给出「未配置参考音频」的具体错误——
// 构造 provider 时静默返回一个坏实例，只会在配音任务里变成难查的失败。
var cosyVoicePromptCache struct {
	mu   sync.Mutex
	path string
	data []byte
}

func cosyVoicePromptWAV() []byte {
	path := strings.TrimSpace(os.Getenv("PPTS_COSYVOICE_PROMPT_WAV"))
	if path == "" {
		return nil
	}
	cosyVoicePromptCache.mu.Lock()
	defer cosyVoicePromptCache.mu.Unlock()
	if cosyVoicePromptCache.path == path && cosyVoicePromptCache.data != nil {
		return cosyVoicePromptCache.data
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cosyVoicePromptCache.path, cosyVoicePromptCache.data = path, data
	return data
}

// NewLLMProvider 从运行时配置构建 OpenAI 兼容 LLM 供应商（文本改写 + 视觉锚点）。
// 视觉能力由 cfg.VisionModel 决定：非空即用于多模态调用；为空表示该配置不启用视觉。
func NewLLMProvider(cfg *Config) (*llm.SiliconFlowProvider, error) {
	return llm.NewSiliconFlowProvider(llm.SiliconFlowConfig{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model, VisionModel: cfg.VisionModel,
	})
}

// ProviderCache 按配置哈希缓存 provider 实例（配置变化 → 新实例；旧实例随上限淘汰）。
type ProviderCache struct {
	mu  sync.Mutex
	tts map[string]tts.TTSProvider
	llm map[string]*llm.SiliconFlowProvider
}

const providerCacheLimit = 32

// NewProviderCache 创建缓存。
func NewProviderCache() *ProviderCache {
	return &ProviderCache{tts: map[string]tts.TTSProvider{}, llm: map[string]*llm.SiliconFlowProvider{}}
}

func cfgKey(cfg *Config) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%d",
		cfg.Kind, cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.VisionModel, cfg.Voice, cfg.SampleRate, cfg.Version)
}

// TTS 返回（缓存的）TTS 供应商实例。
func (c *ProviderCache) TTS(cfg *Config) tts.TTSProvider {
	key := cfgKey(cfg)
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.tts[key]; ok {
		return p
	}
	if len(c.tts) >= providerCacheLimit {
		c.tts = map[string]tts.TTSProvider{}
	}
	p := NewTTSProvider(cfg)
	c.tts[key] = p
	return p
}

// LLM 返回（缓存的）LLM 供应商实例。
func (c *ProviderCache) LLM(cfg *Config) (*llm.SiliconFlowProvider, error) {
	key := cfgKey(cfg)
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.llm[key]; ok {
		return p, nil
	}
	p, err := NewLLMProvider(cfg)
	if err != nil {
		return nil, err
	}
	if len(c.llm) >= providerCacheLimit {
		c.llm = map[string]*llm.SiliconFlowProvider{}
	}
	c.llm[key] = p
	return p, nil
}

// Probe 对网关做最小真实探活：TTS 合成短文本 / LLM 单 token chat。
// 返回耗时与失败原因（不泄露 key）。
func Probe(ctx context.Context, cfg *Config) (latencyMS int64, err error) {
	started := time.Now()
	var probeErr error
	switch cfg.Kind {
	case KindTTS:
		probeErr = probeTTS(ctx, cfg)
	case KindLLM:
		probeErr = probeLLM(ctx, cfg)
	default:
		probeErr = fmt.Errorf("gateway: unknown kind %q", cfg.Kind)
	}
	latency := time.Since(started).Milliseconds()
	if probeErr != nil {
		return latency, probeErr
	}
	return latency, nil
}

func probeTTS(ctx context.Context, cfg *Config) error {
	provider := NewTTSProvider(cfg)
	_, err := provider.Synthesize(ctx, tts.SynthesisRequest{
		LogicalOpID: "gateway-probe:llm-tts", VoiceID: cfg.Voice, Text: "网关连通性测试。", Language: "zh-CN",
	})
	if err != nil {
		return classifyTTSProbeError(cfg, err)
	}
	return nil
}

// classifyTTSProbeError 把语音探活失败归成可行动类别。
//
// 真实故障（2026-11-07）：本机 Qwen3-TTS 服务被移除后，`local_qwen_tts` 这一行
// 仍是默认网关，探活表现为 30s 的 "dial tcp 127.0.0.1:8000: i/o timeout"。
// 旧实现把这段 Go 原文直接交给管理面：用户看不出「服务没起」和「配置写错」的
// 区别，也没有任何分类码可供前端本地化。LLM 侧早已分类，TTS 侧却漏了。
func classifyTTSProbeError(cfg *Config, err error) error {
	endpoint := strings.TrimSuffix(cfg.BaseURL, "/") + "/v1/audio/speech"
	// transport 层失败（端口不通、DNS 解析失败、超时）没有 HTTP 状态码，
	// 这是本地语音服务最常见的故障形态，必须单独成一类。
	if isTransportError(err) {
		return &probeError{ProbeCodeUnreachable,
			fmt.Sprintf("gateway: tts endpoint %s is unreachable (connection refused or timed out): %v", endpoint, err)}
	}
	var httpErr *tts.HTTPError
	if errors.As(err, &httpErr) {
		lower := strings.ToLower(httpErr.Message)
		quota := strings.Contains(lower, "quota") || strings.Contains(lower, "balance") ||
			strings.Contains(lower, "insufficient") || strings.Contains(lower, "credit")
		switch {
		case httpErr.Status == http.StatusPaymentRequired ||
			(quota && (httpErr.Status == http.StatusForbidden || httpErr.Status == http.StatusTooManyRequests)):
			return &probeError{ProbeCodeQuota, httpErr.Error()}
		case httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden:
			return &probeError{ProbeCodeCredentials, httpErr.Error()}
		case httpErr.Status == http.StatusTooManyRequests:
			return &probeError{ProbeCodeRateLimited, httpErr.Error()}
		case httpErr.Status == http.StatusNotFound:
			return &probeError{ProbeCodeModel, httpErr.Error()}
		default:
			return &probeError{ProbeCodeUpstream, httpErr.Error()}
		}
	}
	// 无法归类的失败仍要给码：否则前端只能显示原始字符串，而"没有分类"在
	// 界面上和"分类失败"无法区分。
	return &probeError{ProbeCodeUpstream, err.Error()}
}

// isTransportError 判断错误是否发生在拿到 HTTP 响应之前。
//
// url.Error 覆盖连接被拒、DNS、TLS 握手与客户端超时；net.Error 覆盖更底层的
// 网络错误。context 超时/取消同样属于"对端没回话"，按不可达处理。
func isTransportError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// ProbeCode 把探活失败归成可行动的类别。管理面据此给本地化提示，而不是把
// 上游的 30s 静默超时原样丢给用户——真实故障里"凭据有效、模型存在、账户额度耗尽"
// 只会表现为 chat 挂起，光看超时根本查不到该去充值。
type ProbeCode string

const (
	ProbeCodeNone        ProbeCode = ""
	ProbeCodeQuota       ProbeCode = "quota_exhausted"
	ProbeCodeCredentials ProbeCode = "credentials_rejected"
	ProbeCodeModel       ProbeCode = "model_not_in_catalog"
	ProbeCodeRateLimited ProbeCode = "rate_limited"
	ProbeCodeNoResponse  ProbeCode = "no_response"
	ProbeCodeUpstream    ProbeCode = "upstream_error"
	// ProbeCodeUnreachable 表示连接阶段就失败：端口不通、DNS 解析失败、连接超时。
	// 与 no_response 的区别是「有没有拿到过 HTTP 响应」——前者要去看服务是否在跑，
	// 后者是服务活着但对具体请求不吭声。
	ProbeCodeUnreachable ProbeCode = "service_unreachable"
)

type probeError struct {
	code ProbeCode
	msg  string
}

func (e *probeError) Error() string { return e.msg }

// ProbeCodeOf 取出探活错误的类别；非分类错误返回空串。
func ProbeCodeOf(err error) string {
	if err == nil {
		return ""
	}
	var pe *probeError
	if errors.As(err, &pe) {
		return string(pe.code)
	}
	return ""
}

// 这两个超时做成包级变量，便于回归用例在 httptest 上快速覆盖超时分支。
var llmProbeTimeout = 30 * time.Second
var llmCatalogTimeout = 5 * time.Second

func probeLLM(ctx context.Context, cfg *Config) error {
	payload, err := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
	})
	if err != nil {
		return err
	}
	chatCtx, cancel := context.WithTimeout(ctx, llmProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(chatCtx, http.MethodPost,
		strings.TrimRight(cfg.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: llmProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// 上游不响应时，用同一个凭据查 /v1/models 区分「配置错了」和「账户/服务出问题」。
		// chatCtx 已经到期，但父 ctx 仍有效，诊断请求用它自己的短超时。
		return diagnoseLLMNoResponse(ctx, cfg, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return classifyLLMStatus(resp.StatusCode, body)
	}
	return nil
}

// classifyLLMStatus 把上游的 4xx/5xx 翻成可行动类别，同时保留原始响应体做技术详情。
func classifyLLMStatus(status int, body []byte) error {
	msg := truncate(string(body), 200)
	lower := strings.ToLower(msg)
	quota := strings.Contains(lower, "quota") || strings.Contains(lower, "balance") ||
		strings.Contains(lower, "insufficient") || strings.Contains(lower, "credit")
	switch {
	case status == http.StatusPaymentRequired || (quota && (status == http.StatusForbidden || status == http.StatusTooManyRequests)):
		return &probeError{ProbeCodeQuota,
			fmt.Sprintf("gateway: upstream quota/balance exhausted (status %d): %s", status, msg)}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &probeError{ProbeCodeCredentials,
			fmt.Sprintf("gateway: upstream rejected credentials or access (status %d): %s", status, msg)}
	case status == http.StatusTooManyRequests:
		return &probeError{ProbeCodeRateLimited,
			fmt.Sprintf("gateway: upstream rate limited (status %d): %s", status, msg)}
	default:
		return &probeError{ProbeCodeUpstream,
			fmt.Sprintf("gateway: llm status %d: %s", status, msg)}
	}
}

func diagnoseLLMNoResponse(parent context.Context, cfg *Config, chatErr error) error {
	ctx, cancel := context.WithTimeout(parent, llmCatalogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(cfg.BaseURL, "/")+"/v1/models", nil)
	if err != nil {
		return &probeError{ProbeCodeNoResponse,
			fmt.Sprintf("gateway: chat/completions did not respond: %v", chatErr)}
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := (&http.Client{Timeout: llmCatalogTimeout}).Do(req)
	if err != nil {
		return &probeError{ProbeCodeNoResponse,
			fmt.Sprintf("gateway: chat/completions did not respond (%v); /v1/models also unreachable: %v", chatErr, err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &probeError{ProbeCodeCredentials,
			fmt.Sprintf("gateway: chat/completions did not respond (%v); upstream rejected credentials while listing models (status %d)", chatErr, resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return &probeError{ProbeCodeUpstream,
			fmt.Sprintf("gateway: chat/completions did not respond (%v); /v1/models returned status %d: %s", chatErr, resp.StatusCode, truncate(string(body), 200))}
	}
	ids, err := parseModelIDs(body)
	if err != nil {
		return &probeError{ProbeCodeNoResponse,
			fmt.Sprintf("gateway: chat/completions did not respond (%v); /v1/models returned unreadable payload: %v", chatErr, err)}
	}
	if !containsString(ids, cfg.Model) {
		return &probeError{ProbeCodeModel,
			fmt.Sprintf("gateway: configured model %q is not in upstream catalog (available: %s); chat/completions also did not respond: %v",
				cfg.Model, strings.Join(limitStrings(ids, 8), ", "), chatErr)}
	}
	return &probeError{ProbeCodeNoResponse,
		fmt.Sprintf("gateway: credentials and model %q are valid (/v1/models OK), but chat/completions did not respond within %s; check upstream quota/balance or service status: %v",
			cfg.Model, llmProbeTimeout, chatErr)}
}

func parseModelIDs(body []byte) ([]string, error) {
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func limitStrings(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	out := append([]string(nil), list[:n]...)
	return append(out, "…")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
