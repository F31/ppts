package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
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

// IsLoopbackBaseURL 判断网关是否指向本机回环的 HTTP 服务。
//
// 本地语音模型（例如 WSL 里的 Qwen3-TTS）通常不校验 API Key。只给 http + loopback
// 开「免 key」这个口子：远程供应商忘了填 key 必须继续报错，不能被静默当成匿名请求，
// 否则任务会带着 401 失败，而配置页还显示「已配置」。
func IsLoopbackBaseURL(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
		timeout := 60 * time.Second
		// 本机模型单次 100 字约 40s，留一倍余量；远程供应商仍保持 60s 快失败。
		if IsLoopbackBaseURL(cfg.BaseURL) {
			timeout = 120 * time.Second
		}
		cfg.HTTPClient = &http.Client{Timeout: timeout}
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
		// siliconflow 的 /v1/audio/speech 负载只接受 model/input/voice/format/speed/sample_rate，
		// 不消费 SpeechControl.Pauses（停顿需在文本里插入 <break> 或静音片，非结构化接口）。
		SupportsPauses: false,
	}, nil
}

// localTTSChunkRunes 是本机回环语音模型单次合成的文本上限（按 rune 计）。
//
// 实测 Qwen3-TTS 1.7B CustomVoice：28 字 13s、110 字 42s、200 字 102s、
// 809 字 >240s 且会把推理服务拖到连短句都不响应（只能重启 unit 恢复）。
// 切到 100 字以内后单块稳定落在客户端 HTTP 预算内；再把多段 WAV 拼回一段，
// 避免「超时 → 任务重试 → 服务端旧请求仍在算 → 请求堆积打挂服务」的循环。
// 远程供应商保持单次请求，不改其行为。
const localTTSChunkRunes = 100

func (p *SiliconFlowProvider) Synthesize(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	if strings.TrimSpace(p.cfg.APIKey) == "" && !IsLoopbackBaseURL(p.cfg.BaseURL) {
		return SynthesisResult{}, errors.New("tts: siliconflow api key not configured")
	}
	voice := req.VoiceID
	if strings.TrimSpace(voice) == "" {
		voice = p.cfg.Voice
	}
	if strings.TrimSpace(voice) == "" {
		voice = DefaultSiliconFlowVoice(p.cfg.Model)
	}
	chunks := []string{req.Text}
	if IsLoopbackBaseURL(p.cfg.BaseURL) {
		chunks = splitTTSChunks(req.Text, localTTSChunkRunes)
	}

	var audio []byte
	var err error
	if len(chunks) == 1 {
		audio, err = p.synthesizeOnce(ctx, chunks[0], voice, req.SpeechControl.RatePercent)
	} else {
		parts := make([][]byte, 0, len(chunks))
		for _, chunk := range chunks {
			part, partErr := p.synthesizeOnce(ctx, chunk, voice, req.SpeechControl.RatePercent)
			if partErr != nil {
				return SynthesisResult{}, partErr
			}
			parts = append(parts, part)
		}
		audio, err = concatWAV16(parts)
		if err != nil {
			return SynthesisResult{}, fmt.Errorf("tts: concat local chunks: %w", err)
		}
	}
	if err != nil {
		return SynthesisResult{}, err
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
	// 阶段一：先在真实音频上做静音锚点估算（estimated_vad）；VAD 无效时该方法内部
	// 自动回退到整体匀速估算（estimated）。两者都满足 validateSynthesisResult 的边界校验。
	alignment := buildEstimatedVADAlignment(req.Text, audio, durationMS)
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

// synthesizeOnce 发一次 /v1/audio/speech，返回原始（未规范化）WAV 字节。
func (p *SiliconFlowProvider) synthesizeOnce(ctx context.Context, text, voice string, ratePercent int) ([]byte, error) {
	payload := map[string]any{
		"model":           p.cfg.Model,
		"input":           text,
		"voice":           voice,
		"response_format": "wav",
	}
	if ratePercent > 0 && ratePercent != 100 {
		payload["speed"] = float64(ratePercent) / 100.0
	}
	if p.cfg.SampleRate > 0 {
		payload["sample_rate"] = p.cfg.SampleRate
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSuffix(p.cfg.BaseURL, "/") + "/v1/audio/speech"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if key := strings.TrimSpace(p.cfg.APIKey); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, &RetryableError{Err: err}
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &RetryableError{Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, siliconFlowError(resp.StatusCode, audio, voice)
	}
	if len(audio) == 0 {
		return nil, &RetryableError{Err: errors.New("tts: siliconflow returned empty audio")}
	}
	return audio, nil
}

// splitTTSChunks 按句读把长文本切成不超过 limit 个 rune 的片段。
// 优先在后半段找最近的标点，实在没有才硬切；空白会被 TrimSpace 去掉
// （空白本来也不发音），其余字符一个不丢。
func splitTTSChunks(text string, limit int) []string {
	runes := []rune(text)
	if limit < 1 || len(runes) <= limit {
		return []string{text}
	}
	out := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if isTTSBoundary(runes[i-1]) {
				cut = i
				break
			}
		}
		chunk := strings.TrimSpace(string(runes[:cut]))
		if chunk != "" {
			out = append(out, chunk)
		}
		runes = runes[cut:]
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		out = append(out, tail)
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func isTTSBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '，', '、', '\n', '.', '!', '?', ';', ',':
		return true
	}
	return false
}

// concatWAV16 把同一采样率/声道的 PCM16 WAV 顺序拼成一段标准 WAV。
// 只拼 data 区，不引入额外静音；下游 normalizeWAV16 会再规范化一次。
func concatWAV16(parts [][]byte) ([]byte, error) {
	if len(parts) == 0 {
		return nil, errors.New("no wav parts")
	}
	var all []int16
	var sampleRate, channels int
	for i, part := range parts {
		samples, sr, ch, ok := wavPCM16(part)
		if !ok {
			return nil, fmt.Errorf("part %d is not PCM16 WAV", i)
		}
		if i == 0 {
			sampleRate, channels = sr, ch
		} else if sr != sampleRate || ch != channels {
			return nil, fmt.Errorf("part %d format mismatch: %dHz/%dch, expected %dHz/%dch", i, sr, ch, sampleRate, channels)
		}
		all = append(all, samples...)
	}
	pcmLen := len(all) * 2
	out := make([]byte, 44+pcmLen)
	copy(out[0:4], "RIFF")
	putU32(out[4:8], uint32(36+pcmLen))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putU32(out[16:20], 16)
	putU16(out[20:22], 1) // PCM
	putU16(out[22:24], uint16(channels))
	putU32(out[24:28], uint32(sampleRate))
	putU32(out[28:32], uint32(sampleRate*channels*2))
	putU16(out[32:34], uint16(channels*2))
	putU16(out[34:36], 16)
	copy(out[36:40], "data")
	putU32(out[40:44], uint32(pcmLen))
	for i, s := range all {
		putU16(out[44+i*2:44+i*2+2], uint16(s))
	}
	return out, nil
}

// siliconFlowError 把非 2xx 响应映射为错误；429/5xx 为可重试。
func siliconFlowError(status int, body []byte, voice string) error {
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
	base := fmt.Errorf("tts: siliconflow http %d: %s%s", status, msg, voiceFormatHint(status, body, voice))
	if status == http.StatusTooManyRequests || status >= 500 {
		return &RetryableError{Err: base}
	}
	return base
}

// voiceFormatHint 只在供应商明确拒绝音色时，补上"我们实际发了什么"和正确格式。
//
// SiliconFlow 对写错的音色只回 {"code":20047,"message":"Invalid voice"}，
// 用户看到这句话无从判断：是音色名不存在，还是分隔符写成了 "."（预置音色要求
// "<model>:<voice>"）。补一句原文不会泄露任何东西，却能让人一眼看出是格式问题。
func voiceFormatHint(status int, body []byte, voice string) string {
	if status != http.StatusBadRequest {
		return ""
	}
	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.Code != 20047 && !strings.Contains(strings.ToLower(parsed.Message), "invalid voice") {
		return ""
	}
	return fmt.Sprintf(" (sent voice=%q; SiliconFlow preset voices require \"<model>:<voice>\", e.g. FunAudioLLM/CosyVoice2-0.5B:alex)", voice)
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
	// 四舍五入到整毫秒，避免截断引入最长近 1ms 的负误差（该时长同时用于
	// 时间轴声明与额度结算）。
	return int64(math.Round(float64(dataSize) / float64(byteRate) * 1000)), nil
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
