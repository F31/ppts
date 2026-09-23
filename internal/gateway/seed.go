package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/F31/ppts/internal/tenant"
)

// CipherFromEnv 加载 AES-GCM 密钥：优先 PPTS_GATEWAY_AES_KEY_BASE64，
// 回退 PPTS_BYOS_AES_KEY_BASE64。未配置时返回错误（网关功能禁用）。
func CipherFromEnv() (CredentialCipher, error) {
	raw := os.Getenv("PPTS_GATEWAY_AES_KEY_BASE64")
	if raw == "" {
		raw = os.Getenv("PPTS_BYOS_AES_KEY_BASE64")
	}
	if raw == "" {
		return nil, errors.New("gateway: no AES key configured (PPTS_GATEWAY_AES_KEY_BASE64 or PPTS_BYOS_AES_KEY_BASE64)")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	return tenant.NewAESGCMCipher(key)
}

// Env 是从进程环境构造的网关种子配置。
type Env struct {
	TTSProvider string
	TTSBaseURL  string
	TTSAPIKey   string
	TTSModel    string
	TTSVoice    string
	LLMProvider string
	LLMBaseURL  string
	LLMAPIKey   string
	LLMModel    string
	LLMVision   string
}

// EnvFromEnv 读取 PPTS_TTS_* / PPTS_LLM_* 环境变量。
func EnvFromEnv() Env {
	return Env{
		TTSProvider: strings.TrimSpace(os.Getenv("PPTS_TTS_PROVIDER")),
		TTSBaseURL:  os.Getenv("PPTS_TTS_BASE_URL"),
		TTSAPIKey:   os.Getenv("PPTS_TTS_API_KEY"),
		TTSModel:    os.Getenv("PPTS_TTS_MODEL"),
		TTSVoice:    os.Getenv("PPTS_TTS_VOICE"),
		LLMProvider: strings.TrimSpace(os.Getenv("PPTS_LLM_PROVIDER")),
		LLMBaseURL:  os.Getenv("PPTS_LLM_BASE_URL"),
		LLMAPIKey:   os.Getenv("PPTS_LLM_API_KEY"),
		LLMModel:    os.Getenv("PPTS_LLM_MODEL"),
		LLMVision:   os.Getenv("PPTS_LLM_VISION_MODEL"),
	}
}

// SeedFromEnv 把环境变量配置幂等写入平台默认行：
// 仅当平台租户在该 kind 下尚无任何网关时写入（名为 "env"，is_default=true）。
// 已存在 DB 配置时以 DB 为准，env 不再覆盖。错误由调用方记录（不阻断启动）。
func SeedFromEnv(ctx context.Context, store Store, env Env) error {
	if env.TTSProvider == "siliconflow" && env.TTSAPIKey != "" {
		if err := seedKind(ctx, store, KindTTS, env.TTSAPIKey, &Gateway{
			TenantID: PlatformTenantID, Name: "env", Kind: KindTTS,
			Provider: "openai_compatible", BaseURL: defaultIfEmpty(env.TTSBaseURL, "https://api.siliconflow.cn"),
			Model: defaultIfEmpty(env.TTSModel, "FunAudioLLM/CosyVoice2-0.5B"),
			Voice: env.TTSVoice, IsDefault: true, Enabled: true,
		}); err != nil {
			return err
		}
	}
	if env.LLMProvider == "siliconflow" && env.LLMAPIKey != "" {
		if err := seedKind(ctx, store, KindLLM, env.LLMAPIKey, &Gateway{
			TenantID: PlatformTenantID, Name: "env", Kind: KindLLM,
			Provider: "openai_compatible", BaseURL: defaultIfEmpty(env.LLMBaseURL, "https://api.siliconflow.cn"),
			Model: defaultIfEmpty(env.LLMModel, "Qwen/Qwen2.5-7B-Instruct"),
			// 视觉模型**不填默认值**：留空即表示不启用"视觉锚点"。需要时显式设置 PPTS_LLM_VISION_MODEL。
			VisionModel: strings.TrimSpace(env.LLMVision),
			IsDefault:   true, Enabled: true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func seedKind(ctx context.Context, store Store, kind Kind, apiKey string, gw *Gateway) error {
	existing, err := store.List(ctx, PlatformTenantID, kind)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil // DB 已有配置，以 DB 为准。
	}
	return store.Create(ctx, gw, apiKey)
}

func defaultIfEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimRight(strings.TrimSpace(v), "/")
}
