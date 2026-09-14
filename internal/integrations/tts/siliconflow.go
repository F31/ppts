package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SiliconFlowConfig 是 SiliconFlow（https://api.siliconflow.cn）OpenAI 兼容语音合成配置。
type SiliconFlowConfig struct {
	BaseURL    string // 缺省 https://api.siliconflow.cn
	APIKey     string // 必填（环境变量 PPTS_TTS_API_KEY，不入库）
	Model      string // 缺省 FunAudioLLM/CosyVoice2-0.5B
	Voice      string // 缺省读取请求 voice；此处为兜底
	SampleRate int
	HTTPClient *http.Client
}

// SiliconFlowProvider 以 OpenAI 兼容 /v1/audio/speech 实现 TTSProvider。
// 供应商不返回对齐时间戳，适配器解码 WAV 得到真实时长并按字符匀速构造"估算"对齐
// （AlignEstimate），满足 validateSynthesisResult 的边界校验。
type SiliconFlowProvider struct {
	cfg SiliconFlowConfig
}

// NewSiliconFlowProvider 创建供应商并填充缺省值。
func NewSiliconFlowProvider(cfg SiliconFlowConfig) *SiliconFlowProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.siliconflow.cn"
	}
	if cfg.Model == "" {
		cfg.Model = "FunAudioLLM/CosyVoice2-0.5B"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &SiliconFlowProvider{cfg: cfg}
}

func (p *SiliconFlowProvider) Capabilities(_ context.Context, voiceID string) (VoiceCapabilities, error) {
	if strings.TrimSpace(voiceID) == "" {
		voiceID = p.cfg.Voice
	}
	return VoiceCapabilities{
		VoiceID:       voiceID,
		Languages:     []string{"zh-CN", "en-US"},
		MaxInputChars: 5000,
		Streaming:     false,
		Region:        "cn-siliconflow",
		ModelID:       p.cfg.Model,
	}, nil
}

func (p *SiliconFlowProvider) Synthesize(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return SynthesisResult{}, errors.New("tts: siliconflow api key not configured")
	}
	voice := req.VoiceID
	if strings.TrimSpace(voice) == "" {
		voice = p.cfg.Voice
	}
	payload := map[string]any{
		"model":           p.cfg.Model,
		"input":           req.Text,
		"voice":           voice,
		"response_format": "wav",
	}
	if req.SpeechControl.RatePercent > 0 && req.SpeechControl.RatePercent != 100 {
		payload["speed"] = float64(req.SpeechControl.RatePercent) / 100.0
	}
	if p.cfg.SampleRate > 0 {
		payload["sample_rate"] = p.cfg.SampleRate
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return SynthesisResult{}, err
	}
	endpoint := strings.TrimSuffix(p.cfg.BaseURL, "/") + "/v1/audio/speech"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return SynthesisResult{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return SynthesisResult{}, &RetryableError{Err: err}
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return SynthesisResult{}, &RetryableError{Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return SynthesisResult{}, siliconFlowError(resp.StatusCode, audio)
	}
	if len(audio) == 0 {
		return SynthesisResult{}, &RetryableError{Err: errors.New("tts: siliconflow returned empty audio")}
	}
	durationMS, err := wavDurationMSFromRIFF(audio)
	if err != nil {
		return SynthesisResult{}, fmt.Errorf("tts: decode siliconflow wav: %w", err)
	}
	// 供应商可能写占位大 data 长度；规范化为标准 PCM16 WAV，保证下游 media 可解码。
	audio, err = normalizeWAV16(audio)
	if err != nil {
		return SynthesisResult{}, fmt.Errorf("tts: normalize siliconflow wav: %w", err)
	}
	alignment := buildEstimatedAlignment(req.Text, durationMS)
	chars := len([]rune(req.Text))
	return SynthesisResult{
		Audio:           audio,
		Format:          "audio/wav",
		RealDurationMS:  durationMS,
		Alignment:       &alignment,
		BillingUnit:     "char",
		BillingQuantity: int64(chars),
	}, nil
}

// siliconFlowError 把非 2xx 响应映射为错误；429/5xx 为可重试。
func siliconFlowError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 400 {
		msg = msg[:400]
	}
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	base := fmt.Errorf("tts: siliconflow http %d: %s", status, msg)
	if status == http.StatusTooManyRequests || status >= 500 {
		return &RetryableError{Err: base}
	}
	return base
}

// wavDurationMSFromRIFF 解析 PCM WAV 得到时长（毫秒）。
// 部分供应商（SiliconFlow）会把 RIFF/data 长度写成占位大值，实际数据到 EOF 为止；
// 解析时若声明大小超过剩余字节，按剩余字节截断，与 ffprobe/标准播放器行为一致。
func wavDurationMSFromRIFF(data []byte) (int64, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, errors.New("invalid wav header")
	}
	var sampleRate uint32
	var byteRate uint32
	var dataSize int
	i := 12
	for i+8 <= len(data) {
		chunkID := string(data[i : i+4])
		size := int(le32(data[i+4 : i+8]))
		switch chunkID {
		case "fmt ":
			if size >= 16 {
				sampleRate = le32(data[i+12 : i+16])
				byteRate = le32(data[i+16 : i+20])
			}
		case "data":
			remaining := len(data) - (i + 8)
			if size > remaining || size < 0 {
				size = remaining
			}
			dataSize = size
			i += 8 + dataSize
			continue
		}
		step := 8 + size
		if step <= 0 {
			break
		}
		i += step
	}
	if byteRate == 0 || sampleRate == 0 || dataSize <= 0 {
		return 0, errors.New("wav missing fmt/data chunks")
	}
	return int64(float64(dataSize) / float64(byteRate) * 1000), nil
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func le16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}

// normalizeWAV16 把 RIFF/WAVE 重新编码为标准 PCM16 WAV（修正占位大 data 长度）。
// 返回的标准 WAV 与 media.writeWAVHeader 布局一致，保证 media.parsePCM16WAV 可解码。
func normalizeWAV16(data []byte) ([]byte, error) {
	var sampleRate uint32
	var channels uint16
	var blockAlign uint16
	var bits uint16
	var pcm []byte
	i := 12
	foundData := false
	for i+8 <= len(data) {
		chunkID := string(data[i : i+4])
		size := int(le32(data[i+4 : i+8]))
		start := i + 8
		switch chunkID {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("wav fmt chunk too small")
			}
			sampleRate = le32(data[start+4 : start+8])
			channels = le16(data[start+2 : start+4])
			blockAlign = le16(data[start+12 : start+14])
			bits = le16(data[start+14 : start+16])
		case "data":
			end := start + size
			if end > len(data) || size < 0 {
				end = len(data)
			}
			pcm = data[start:end]
			foundData = true
		}
		step := 8 + size
		if step <= 0 {
			break
		}
		i += step
	}
	if !foundData || sampleRate == 0 || channels == 0 || bits != 16 || blockAlign != channels*2 {
		return nil, errors.New("wav missing or malformed fmt/data")
	}
	byteRate := sampleRate * uint32(blockAlign)
	out := make([]byte, 44+len(pcm))
	copy(out[0:4], "RIFF")
	putU32(out[4:8], uint32(36+len(pcm)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putU32(out[16:20], 16)
	putU16(out[20:22], 1) // PCM
	putU16(out[22:24], channels)
	putU32(out[24:28], sampleRate)
	putU32(out[28:32], byteRate)
	putU16(out[32:34], blockAlign)
	putU16(out[34:36], 16)
	copy(out[36:40], "data")
	putU32(out[40:44], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out, nil
}

func putU16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
func putU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// buildEstimatedAlignment 把文本字符在净语音区间（去掉首尾 200ms 静音）内匀速分布，
// 构造估算对齐（AlignEstimate）。用于不提供时间戳的供应商。
func buildEstimatedAlignment(text string, durationMS int64) Alignment {
	chars := []rune(text)
	const leadUS = int64(200000)
	netUS := durationMS*1000 - leadUS
	if netUS < 0 {
		netUS = 0
	}
	usPerChar := netUS / int64(max(len(chars), 1))
	tokens := make([]TokenOffset, 0, len(chars))
	for i, r := range chars {
		start := leadUS + int64(i)*usPerChar
		tokens = append(tokens, TokenOffset{StartUS: start, EndUS: start + usPerChar, Char: string(r)})
	}
	return Alignment{Text: text, Tokens: tokens, Method: AlignEstimate}
}
