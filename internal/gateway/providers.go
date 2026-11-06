package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/tts"
)

// NewTTSProvider 从运行时配置构建 OpenAI 兼容 TTS 供应商。
func NewTTSProvider(cfg *Config) tts.TTSProvider {
	return tts.NewSiliconFlowProvider(tts.SiliconFlowConfig{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		Voice:      cfg.Voice,
		SampleRate: cfg.SampleRate,
	})
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
	return err
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
