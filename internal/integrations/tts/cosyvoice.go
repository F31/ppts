package tts

// CosyVoice 本地适配（Fun-CosyVoice3 的 FastAPI runtime）。
//
// 为什么不复用 OpenAI 兼容通道：CosyVoice 的 runtime 只提供
// /inference_sft、/inference_zero_shot、/inference_instruct* 这几个表单接口，
// 返回的是**裸 PCM16**（响应头 x-audio-encoding=pcm_s16le），没有 /v1/audio/speech。
// 2026-11-07 实测还有两条硬约束：
//   - SFT 路径在 CV3 权重上直接 KeyError: 'embedding'（CV3 没有 SFT 音色向量），
//     所以只能用 zero_shot；
//   - zero_shot 的 prompt_text 必须含 `<|endofprompt|>`，否则模型侧断言失败。
// 这两点都不是"配置写错"，而是模型契约，必须在适配层处理，不能丢给用户猜。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

const (
	// cosyVoiceChunkRunes 是单次请求的字符上限。
	//
	// 服务端是单飞闸门（RequestGate），默认 60s 生成期限；超时后它会把整个
	// 请求判为 "generation deadline exceeded"。2026-11-07 实测 77 字耗时 12.0s
	// （产出 18.4s 音频，约 1.5 倍实时），吞吐是瓶颈而非固定开销，所以分块
	// 大小基本不影响总时长，只影响接缝数量：每块都是一次独立推理，块边界处
	// 韵律会重新起头。取 80 字（约 12.5s，对 60s 期限有 5 倍余量）把接缝减半。
	cosyVoiceChunkRunes = 80
	// cosyVoiceMaxInputRunes 是对外声明的单段上限，必须远大于分块大小。
	//
	// Capabilities.MaxInputChars 的语义是"一次合成调用能接受多长的文本"，
	// 由适配层自己分块消化，不是单次 HTTP 请求的长度。若把它写成分块值，
	// 一页讲稿只要超过 400 字就会被 app 层直接判死（segment ... exceeds
	// voice input limit），适配层的分块能力反而用不上。
	cosyVoiceMaxInputRunes = 10000
	// cosyVoicePromptEnd 是 CV3 要求出现在 prompt_text 里的结束标记。
	cosyVoicePromptEnd = "<|endofprompt|>"
	cosyVoiceTimeout   = 120 * time.Second
	// cosyVoiceChunkAttempts / cosyVoiceRetryBackoff 控制单块重试。
	//
	// 本地服务会回收空闲 keep-alive 连接，客户端复用到一个已被对端关闭的 socket 时
	// 会得到 "write: connection reset by peer"。这类失败与文本内容无关，重发同一块
	// 通常立刻成功；但它是 RetryableError，若不在块级别消化掉，就会让上层把整份
	// 二十二页的配音任务从头重跑——实测一次全量任务因此反复重试、始终跑不完。
	cosyVoiceChunkAttempts = 3
	cosyVoiceRetryBackoff  = 2 * time.Second
	// cosyVoiceMinSplitRunes 是"拆短重试"的下界：再短就只剩几个字，
	// 拆下去只会把请求数放大而不再改变失败概率。
	cosyVoiceMinSplitRunes = 12
)

// CosyVoiceConfig 是本地 CosyVoice 运行时配置。
//
// PromptWAV/PromptText 是 zero_shot 的"音色来源"：一段参考音频 + 它对应的文字。
// 它们是部署资产（不是用户每次请求的输入），由进程环境提供。
type CosyVoiceConfig struct {
	BaseURL    string
	PromptWAV  []byte
	PromptText string
	SampleRate int
	HTTPClient *http.Client
}

// CosyVoiceProvider 把零样本克隆包装成 TTSProvider。
type CosyVoiceProvider struct {
	cfg    CosyVoiceConfig
	client *http.Client
	// mu 串行化请求：服务端一次只允许一个生成任务（并发直接 409），
	// 在客户端排队比让上层任务随机失败更可解释。
	mu sync.Mutex
	// retryBackoff 是块级重试的初始退避（指数增长）；测试可缩短以免拖慢用例。
	retryBackoff time.Duration
}

func NewCosyVoiceProvider(cfg CosyVoiceConfig) *CosyVoiceProvider {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cosyVoiceTimeout}
	}
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 24000
	}
	return &CosyVoiceProvider{cfg: cfg, client: client, retryBackoff: cosyVoiceRetryBackoff}
}

func (p *CosyVoiceProvider) Capabilities(_ context.Context, voiceID string) (VoiceCapabilities, error) {
	return VoiceCapabilities{
		VoiceID:       voiceID,
		Languages:     []string{"zh-CN", "en-US"},
		MaxInputChars: cosyVoiceMaxInputRunes,
		// 服务端闸门是单飞，而且会吞掉真实异常，不能声称支持并发。
		MaxConcurrentSegments: 0,
		TimestampType:         "",
		Region:                "local",
		ModelID:               "Fun-CosyVoice3-0.5B",
	}, nil
}

func (p *CosyVoiceProvider) Synthesize(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return SynthesisResult{}, errors.New("tts: cosyvoice empty text")
	}
	if len(p.cfg.PromptWAV) == 0 {
		return SynthesisResult{}, errors.New("tts: cosyvoice prompt wav not configured (set PPTS_COSYVOICE_PROMPT_WAV to a readable wav file)")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var pcm []byte
	for _, chunk := range splitTTSChunks(text, cosyVoiceChunkRunes) {
		part, err := p.synthesizeChunk(ctx, chunk)
		if err != nil {
			return SynthesisResult{}, err
		}
		pcm = append(pcm, part...)
	}
	wav, err := pcm16ToWAV(p.cfg.SampleRate, pcm)
	if err != nil {
		return SynthesisResult{}, err
	}
	durationMS, err := wavDurationMSFromRIFF(wav)
	if err != nil {
		return SynthesisResult{}, fmt.Errorf("tts: decode cosyvoice pcm: %w", err)
	}
	alignment := buildEstimatedVADAlignment(text, wav, durationMS)
	return SynthesisResult{
		Audio:           wav,
		Format:          "audio/wav",
		RealDurationMS:  durationMS,
		Alignment:       &alignment,
		BillingUnit:     "char",
		BillingQuantity: int64(len([]rune(text))),
	}, nil
}

// synthesizeChunk 在单块上消化瞬时失败，避免把"连接被回收"升级成整份任务重跑。
//
// 只重试与内容无关的失败（连接层错误、409 单飞冲突、429、5xx）；4xx 里其余状态码
// 说明请求本身有问题，重发同样的字节只会重复失败，直接上抛。
//
// 重试用尽后还会**对半拆开再试**：本地服务偶尔会在长块上把整个生成判死
// （服务端日志表现为 GenerationError: model generation failed，客户端看到
// 半途 EOF / 空 body），同样的文本拆短后往往一次就过。拆到
// cosyVoiceMinSplitRunes 仍失败才真正上抛——这样一段长讲稿不会因为某几个字
// 的偶发失败把整份 deck 拖回起点。
func (p *CosyVoiceProvider) synthesizeChunk(ctx context.Context, text string) ([]byte, error) {
	part, err := p.synthesizeChunkOnce(ctx, text)
	if err == nil {
		return part, nil
	}
	if !cosyVoiceBisectable(err) {
		return nil, err
	}
	runes := []rune(text)
	if len(runes) < cosyVoiceMinSplitRunes {
		return nil, err
	}
	mid := len(runes) / 2
	head, errHead := p.synthesizeChunk(ctx, string(runes[:mid]))
	if errHead != nil {
		return nil, errHead
	}
	tail, errTail := p.synthesizeChunk(ctx, string(runes[mid:]))
	if errTail != nil {
		return nil, errTail
	}
	return append(head, tail...), nil
}

func (p *CosyVoiceProvider) synthesizeChunkOnce(ctx context.Context, text string) ([]byte, error) {
	backoff := p.retryBackoff
	if backoff <= 0 {
		backoff = cosyVoiceRetryBackoff
	}
	var lastErr error
	for attempt := 1; attempt <= cosyVoiceChunkAttempts; attempt++ {
		part, err := p.synthesizeOnce(ctx, text)
		if err == nil {
			return part, nil
		}
		lastErr = err
		if !cosyVoiceRetryable(err) || attempt == cosyVoiceChunkAttempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, lastErr
}

// cosyVoiceBisectable 判断"拆短重试"是否可能有效。
//
// 鉴权/参数类 4xx 拆得再细也一样失败，只会把请求数翻倍；连接层失败、服务端
// 生成失败、空响应对长度敏感，拆短有意义。
func cosyVoiceBisectable(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusConflict ||
			httpErr.Status == http.StatusTooManyRequests ||
			httpErr.Status >= 500
	}
	// 非 HTTP 错误：连接层失败，或"服务端 200 但没声音"的生成失败。
	var retryable *RetryableError
	return errors.As(err, &retryable) || isCosyVoiceEmptyAudio(err)
}

func isCosyVoiceEmptyAudio(err error) bool {
	return err != nil && strings.Contains(err.Error(), "returned empty audio")
}

func cosyVoiceRetryable(err error) bool {
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		return true
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusConflict ||
			httpErr.Status == http.StatusTooManyRequests ||
			httpErr.Status >= 500
	}
	return false
}

// synthesizeOnce 发一次 /inference_zero_shot，返回裸 PCM16LE 字节。
func (p *CosyVoiceProvider) synthesizeOnce(ctx context.Context, text string) ([]byte, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("tts_text", text)
	_ = mw.WriteField("prompt_text", p.promptText())
	_ = mw.WriteField("stream", "false")

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="prompt_wav"; filename="prompt.wav"`)
	h.Set("Content-Type", "audio/wav")
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(p.cfg.PromptWAV); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/inference_zero_shot"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	// 合成是纯粹函数：同样的文本重发得到同样的音频。标成幂等后，net/http 在
	// "复用了一条已被服务端关闭的连接"时可以直接重放，而不是把 reset 抛给上层。
	sum := sha256.Sum256([]byte(text))
	httpReq.Header.Set("Idempotency-Key", fmt.Sprintf("cosyvoice-%x", sum[:8]))

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &RetryableError{Err: err}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// 服务端闸门在生成失败时仍回 200 + 空 body，"读不到"只能靠调用方判断；
		// 这里把真实状态码留住，避免上游 409 被当成"合成成功但没声音"。
		return nil, &HTTPError{
			Status:  resp.StatusCode,
			Message: fmt.Sprintf("tts: cosyvoice http %d: %s", resp.StatusCode, truncateString(string(raw), 200)),
		}
	}
	if readErr != nil {
		return nil, &RetryableError{Err: fmt.Errorf("tts: cosyvoice read body: %w", readErr)}
	}
	if len(raw) == 0 {
		return nil, errors.New("tts: cosyvoice returned empty audio (the service reports generation failed; check its logs)")
	}
	if len(raw)%2 != 0 {
		return nil, fmt.Errorf("tts: cosyvoice returned odd-length pcm (%d bytes)", len(raw))
	}
	if sr := resp.Header.Get("X-Sample-Rate"); sr != "" {
		var got int
		if _, err := fmt.Sscanf(sr, "%d", &got); err == nil && got > 0 {
			p.cfg.SampleRate = got
		}
	}
	return raw, nil
}

// promptText 保证 prompt_text 带 CV3 要求的结束标记。
func (p *CosyVoiceProvider) promptText() string {
	text := strings.TrimSpace(p.cfg.PromptText)
	if text == "" {
		text = "希望你以后能够做的比我还好呦。"
	}
	if !strings.Contains(text, cosyVoicePromptEnd) {
		text += cosyVoicePromptEnd
	}
	return text
}

// pcm16ToWAV 把裸 PCM16LE 单声道包成标准 WAV。
func pcm16ToWAV(sampleRate int, pcm []byte) ([]byte, error) {
	if sampleRate <= 0 {
		return nil, errors.New("tts: invalid sample rate for wav")
	}
	if len(pcm) == 0 {
		return nil, errors.New("tts: empty pcm")
	}
	out := make([]byte, 44+len(pcm))
	copy(out[0:4], "RIFF")
	putU32(out[4:8], uint32(36+len(pcm)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putU32(out[16:20], 16)
	putU16(out[20:22], 1) // PCM
	putU16(out[22:24], 1) // mono
	putU32(out[24:28], uint32(sampleRate))
	putU32(out[28:32], uint32(sampleRate*2))
	putU16(out[32:34], 2)
	putU16(out[34:36], 16)
	copy(out[36:40], "data")
	putU32(out[40:44], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out, nil
}

func truncateString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
