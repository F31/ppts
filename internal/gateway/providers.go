package gateway

import (
	"bytes"
	"context"
	"encoding/json"
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
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway: llm status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
