package tts

import (
	"context"
	"fmt"
)

// FakeProvider 是开发/测试用假供应商：无外部调用，产出静音 WAV，
// 时长按字符数估算（V4.0 §6.4 仅用于生成前估算与流水线联调，非正式时长来源）。
// 它不是正式 TTS 主链路（V4.0 §8.2）；接入真实供应商时替换。
type FakeProvider struct {
	// CharsPerSecond 估算语速（默认 4）。
	CharsPerSecond float64
	// SampleRate 输出采样率（默认 16000）。
	SampleRate int
	// AlwaysFail 若为 true，Synthesize 恒失败（注入供应商故障测试）。
	AlwaysFail bool
}

// NewFakeProvider 创建假供应商。
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{CharsPerSecond: 4, SampleRate: 16000}
}

func (f *FakeProvider) Capabilities(_ context.Context, voiceID string) (VoiceCapabilities, error) {
	return VoiceCapabilities{
		VoiceID: voiceID, Languages: []string{"zh-CN"},
		MaxInputChars: 10000, TimestampType: TimestampPerChar,
		Region: "dev-fake", ModelID: "fake-wav", SupportsPauses: true,
	}, nil
}

// Synthesize 生成指定时长的静音 WAV，并构造字符级估算对齐。
func (f *FakeProvider) Synthesize(_ context.Context, req SynthesisRequest) (SynthesisResult, error) {
	if f.AlwaysFail {
		return SynthesisResult{}, &RetryableError{Err: fmt.Errorf("fake provider injected failure")}
	}
	cps := f.CharsPerSecond
	if cps <= 0 {
		cps = 4
	}
	rate := f.SampleRate
	if rate <= 0 {
		rate = 16000
	}
	// 时长 = 字符数/语速 秒 + 固定首尾静音 200ms。
	chars := len([]rune(req.Text))
	durationMS := int64(float64(chars)/cps*1000) + 400
	n := int(float64(rate) * float64(durationMS) / 1000.0)

	// 构造最小 WAV。
	audio := make([]byte, 44+n*2)
	h := audio[:44]
	copy(h[0:4], "RIFF")
	putLe32(h[4:], uint32(44-8+n*2))
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	putLe32(h[16:], 16) // fmt size
	putLe16(h[20:], 1)  // PCM
	putLe16(h[22:], 1)  // mono
	putLe32(h[24:], uint32(rate))
	putLe32(h[28:], uint32(rate*2)) // byte rate
	putLe16(h[32:], 2)              // block align
	putLe16(h[34:], 16)             // bits
	copy(h[36:40], "data")
	putLe32(h[40:], uint32(n*2))
	for i := 0; i < n; i++ {
		audio[44+i*2], audio[44+i*2+1] = 0, 0 // 静音
	}

	// 字符级估算对齐：在"去掉首尾静音"的净语音区间内匀速分布，保证不越界。
	const leadUS = int64(200000) // 200ms 首静音
	netUS := int64(durationMS)*1000 - leadUS
	usPerChar := netUS / int64(max(chars, 1))
	var tokens []TokenOffset
	for i, r := range []rune(req.Text) {
		start := leadUS + int64(i)*usPerChar
		tokens = append(tokens, TokenOffset{StartUS: start, EndUS: start + usPerChar, Char: string(r)})
	}

	return SynthesisResult{
		Audio: audio, Format: "audio/wav",
		RealDurationMS: durationMS,
		Alignment:      &Alignment{Text: req.Text, Tokens: tokens, Method: AlignEstimate},
		BillingUnit:    "char", BillingQuantity: int64(chars),
	}, nil
}

func putLe16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
func putLe32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
