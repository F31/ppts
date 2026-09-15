// Package tts 定义语音合成供应商端口（V4.0 §8.1）与适配实现。
//
// 业务只依赖 TTSProvider 接口；供应商能力显式声明，不统一假定支持
// （如流式、时间戳类型、SSML 子集、地区与模型）。异步供应商在适配器内部
// 实现提交/查询/取消，并持久化远端任务 ID。
package tts

import (
	"context"
	"strings"
	"time"
)

// DefaultSiliconFlowVoice 返回给定模型对应的系统预置音色。
// SiliconFlow 的系统预置音色需带模型前缀（如 FunAudioLLM/CosyVoice2-0.5B:alex）。
func DefaultSiliconFlowVoice(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "FunAudioLLM/CosyVoice2-0.5B"
	}
	return model + ":alex"
}

// TTSProvider 语音合成供应商端口（V4.0 §8.1）。
type TTSProvider interface {
	// Capabilities 返回指定音色的能力清单；生成前校验，能力不支持时提示而非静默丢弃参数。
	Capabilities(ctx context.Context, voiceID string) (VoiceCapabilities, error)
	// Synthesize 合成指定分段，返回真实时长与对齐；错误含可重试信息。
	Synthesize(ctx context.Context, req SynthesisRequest) (SynthesisResult, error)
}

// VoiceCapabilities 是音色能力（V4.0 §8.1）。
type VoiceCapabilities struct {
	VoiceID          string
	Languages        []string // BCP47，如 zh-CN
	MaxInputChars    int
	SSMLSubset       []string // 支持的 SSML 标签
	Pronunciation    bool     // 支持读音控制
	TimestampType    TimestampType
	Streaming        bool
	Region           string
	ModelID          string
	MaxBatchSegments int // 供应商同请求可合成的分段上限
}

// TimestampType 对齐时间戳类型（V4.0 §8.4 适配器统一到内部时间基准）。
type TimestampType string

const (
	TimestampBoundary TimestampType = "boundary_events" // Azure 字/句边界事件
	TimestampPerChar  TimestampType = "per_character"   // 字符级窗口（ElevenLabs）
)

// SynthesisRequest 单次合成请求（V4.0 §8.1）。
type SynthesisRequest struct {
	LogicalOpID string // 逻辑操作 ID（幂等追踪与账本）
	VoiceID     string
	Text        string // 供应商输入（provider_payload）
	Language    string // BCP47
	// SpeechControl 结构化语音控制（语速百分比/停顿/强调）；供应商不支持的子集须显式拒绝。
	SpeechControl SpeechControl
	ExecReq       int // 预留：强制资源集
	SampleRate    int // 输出采样率（0=供应商默认）
}

// SpeechControl 结构化语音控制（禁止把任意 SSML 直接交给用户拼接，V4.0 §6.3）。
type SpeechControl struct {
	RatePercent int        `json:"ratePercent"`      // 100=标准
	Pauses      []Pause    `json:"pauses,omitempty"` // 停顿节点（毫秒）
	Emphasis    []Emphasis `json:"emphasis,omitempty"`
}

// Pause 结构化停顿。
type Pause struct {
	AfterChars int `json:"afterChars"`
	DurationMS int `json:"durationMs"`
}

// Emphasis 强调标注。
type Emphasis struct {
	StartChar int `json:"startChar"`
	EndChar   int `json:"endChar"`
}

// Alignment 是对齐映射（V4.0 §7.1 Alignment）。
type Alignment struct {
	Text   string          `json:"text"`
	Tokens []TokenOffset   `json:"tokens"`
	Method AlignmentMethod `json:"method"`
}

// TokenOffset 单字符/词的偏移（内部时间基准：微秒）。
type TokenOffset struct {
	StartUS int64  `json:"startUs"`
	EndUS   int64  `json:"endUs"`
	Char    string `json:"char"`
}

// AlignmentMethod 对齐来源可信度（V4.0 §8.4）。
type AlignmentMethod string

const (
	AlignProvider AlignmentMethod = "provider_timestamps" // 供应商原生时间戳
	AlignForced   AlignmentMethod = "forced_alignment"    // 经验证的强制对齐
	AlignEstimate AlignmentMethod = "estimated"           // 明确标记为估算
)

// SynthesisResult 合成结果（V4.0 §8.1）。
type SynthesisResult struct {
	Audio             []byte
	Format            string // audio/wav | audio/mpeg
	RealDurationMS    int64  // 解码后真实时长
	Alignment         *Alignment
	ProviderRequestID string
	BillingUnit       string // 供应商计费单位（字符/token/秒）
	BillingQuantity   int64
	Warnings          []string
}

// RetryableError 供应商可重试错误（超时/429/临时5xx），携带退避提示。
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }

func (e *RetryableError) Unwrap() error { return e.Err }
