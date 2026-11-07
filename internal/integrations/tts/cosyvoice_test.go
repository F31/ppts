package tts

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// CosyVoice 适配的回归。真实环境（2026-11-07，Fun-CosyVoice3-0.5B）：
//   - /inference_sft 在 CV3 权重上直接 KeyError: 'embedding'，只能走 zero_shot；
//   - zero_shot 的 prompt_text 不含 <|endofprompt|> 时模型侧断言失败；
//   - 生成失败时服务端仍回 200，只是 body 为空。
// 这三条都属于"配置看起来全对、但发不出声音"的形态，必须由适配层兜住。

func cvTestWAV(t *testing.T) []byte {
	t.Helper()
	// 1 秒静音 24kHz PCM16，足够包成参考音频。
	return mustWAV(t, 24000, make([]byte, 24000*2))
}

func mustWAV(t *testing.T, sampleRate int, pcm []byte) []byte {
	t.Helper()
	wav, err := pcm16ToWAV(sampleRate, pcm)
	if err != nil {
		t.Fatalf("pcm16ToWAV: %v", err)
	}
	return wav
}

func cvServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return srv
}

func TestCosyVoiceSynthesizeSendsPromptMarkerAndWrapsPCM(t *testing.T) {
	var gotPath, gotPrompt, gotText string
	var gotWavBytes int
	srv := cvServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			return
		}
		gotPrompt = r.FormValue("prompt_text")
		gotText = r.FormValue("tts_text")
		if file, _, err := r.FormFile("prompt_wav"); err == nil {
			data, _ := io.ReadAll(file)
			gotWavBytes = len(data)
		}
		w.Header().Set("X-Sample-Rate", "24000")
		w.WriteHeader(http.StatusOK)
		// 0.5s 24kHz PCM16 → 24000 字节
		_, _ = w.Write(make([]byte, 24000))
	})

	provider := NewCosyVoiceProvider(CosyVoiceConfig{
		BaseURL:    srv.URL,
		PromptWAV:  cvTestWAV(t),
		PromptText: "希望你以后能够做的比我还好呦。",
	})
	res, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: "链路自检。", VoiceID: "cosyvoice3"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if gotPath != "/inference_zero_shot" {
		t.Fatalf("path = %q, want /inference_zero_shot（CV3 不支持 SFT）", gotPath)
	}
	if !strings.Contains(gotPrompt, cosyVoicePromptEnd) {
		t.Fatalf("prompt_text 缺 %s：%q", cosyVoicePromptEnd, gotPrompt)
	}
	if gotText != "链路自检。" {
		t.Fatalf("tts_text = %q", gotText)
	}
	if gotWavBytes == 0 {
		t.Fatal("参考音频没有随请求发出，zero_shot 无法复刻音色")
	}
	if res.Format != "audio/wav" || res.RealDurationMS < 400 || res.RealDurationMS > 600 {
		t.Fatalf("结果不合预期: format=%s duration=%dms", res.Format, res.RealDurationMS)
	}
	if res.BillingUnit != "char" || res.BillingQuantity != 5 {
		t.Fatalf("计费口径 = %s/%d, want char/5", res.BillingUnit, res.BillingQuantity)
	}
	if res.Alignment == nil || len(res.Alignment.Tokens) == 0 {
		t.Fatal("缺少对齐信息")
	}
	// 返回值必须是标准 RIFF，下游 media 才能解码。
	if string(res.Audio[:4]) != "RIFF" || string(res.Audio[8:12]) != "WAVE" {
		t.Fatalf("返回的不是 WAV: % x", res.Audio[:12])
	}
}

func TestCosyVoiceEmptyBodyIsAnError(t *testing.T) {
	srv := cvServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	provider := cvFast(NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: srv.URL, PromptWAV: cvTestWAV(t)}))
	_, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: "自检"})
	if err == nil {
		t.Fatal("200 + 空 body 必须报错，不能当成功")
	}
	if !strings.Contains(err.Error(), "empty audio") {
		t.Fatalf("错误应说明服务端没产出音频: %v", err)
	}
}

func TestCosyVoiceMissingPromptIsActionable(t *testing.T) {
	provider := NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: "http://127.0.0.1:1"})
	_, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: "自检"})
	if err == nil || !strings.Contains(err.Error(), "PPTS_COSYVOICE_PROMPT_WAV") {
		t.Fatalf("缺参考音频时应给出可操作的错误: %v", err)
	}
}

func TestCosyVoiceLongTextIsChunked(t *testing.T) {
	calls := 0
	srv := cvServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseMultipartForm(4 << 20)
		if n := len([]rune(r.FormValue("tts_text"))); n > cosyVoiceChunkRunes {
			t.Errorf("单次请求 %d 字，超过上限 %d（服务端 60s 期限会超时）", n, cosyVoiceChunkRunes)
		}
		w.Header().Set("X-Sample-Rate", "24000")
		_, _ = w.Write(make([]byte, 2400))
	})
	provider := NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: srv.URL, PromptWAV: cvTestWAV(t)})
	long := strings.Repeat("这是一句用于分块验证的讲解词。", 12)
	if _, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: long}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if calls < 2 {
		t.Fatalf("长文本只发了 %d 次请求，分块未生效", calls)
	}
}

func TestCosyVoiceCapabilitiesDeclareChunkAwareLimit(t *testing.T) {
	provider := NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: "http://127.0.0.1:1"})
	caps, err := provider.Capabilities(context.Background(), "cosyvoice3")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	// 上限是"一次合成调用能吃的文本长度"，适配层内部会按 cosyVoiceChunkRunes 分块。
	// 如果这里退回到分块大小，任何超过 40 字的整页讲稿都会在 app 层被判死。
	if caps.MaxInputChars <= cosyVoiceChunkRunes {
		t.Fatalf("MaxInputChars=%d 不应等于分块大小 %d", caps.MaxInputChars, cosyVoiceChunkRunes)
	}
	if caps.MaxInputChars < 1000 {
		t.Fatalf("MaxInputChars=%d 太小，整页讲稿（实测最长 484 字）会被拒", caps.MaxInputChars)
	}
	if caps.MaxConcurrentSegments != 0 {
		t.Fatalf("服务端单飞，不能声明并发，得到 %d", caps.MaxConcurrentSegments)
	}
}

func cvFast(p *CosyVoiceProvider) *CosyVoiceProvider {
	// 块级重试的退避在真实环境是秒级；用例里缩到毫秒，避免 409/5xx 用例等 6 秒。
	p.retryBackoff = time.Millisecond
	return p
}

func TestCosyVoiceRetriesTransientChunkFailure(t *testing.T) {
	// 首块被服务端直接掐断（模拟 keep-alive 连接被回收），第二次请求应重发成功。
	var calls int
	srv := cvServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close() // 不给任何响应，客户端读到 EOF
				return
			}
		}
		w.Header().Set("X-Sample-Rate", "24000")
		_, _ = w.Write(make([]byte, 2400))
	})
	provider := cvFast(NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: srv.URL, PromptWAV: cvTestWAV(t)}))
	res, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: "短句"})
	if err != nil {
		t.Fatalf("单块瞬时失败应被块级重试消化，得到 %v", err)
	}
	if calls != 2 {
		t.Fatalf("期望 2 次请求（1 次失败 + 1 次重试），实际 %d 次", calls)
	}
	if len(res.Audio) == 0 || res.RealDurationMS <= 0 {
		t.Fatalf("重试成功后仍应产出音频: %d bytes, %d ms", len(res.Audio), res.RealDurationMS)
	}
}

func TestCosyVoiceDoesNotRetryPermanent4xx(t *testing.T) {
	// 400 说明请求本身有问题，重发同样的字节只会重复失败，必须立刻上抛。
	var calls int
	srv := cvServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("tts_text is required"))
	})
	provider := cvFast(NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: srv.URL, PromptWAV: cvTestWAV(t)}))
	_, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: "短句"})
	if err == nil {
		t.Fatal("400 应上抛错误")
	}
	var httpErr *HTTPError
	if !asHTTPError(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("应保留上游 400 状态码，得到 %v", err)
	}
	if calls != 1 {
		t.Fatalf("不可重试的 4xx 只应请求 1 次，实际 %d 次", calls)
	}
}

func TestCosyVoiceBisectsPoisonChunk(t *testing.T) {
	// 服务端对"太长"的输入整段失败（真实表现：200 空 body / 半途 EOF），
	// 拆短后能正常出声。适配层必须自己拆，不能把整份任务拖回起点。
	var longFails, shortOK int
	srv := cvServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(4 << 20)
		text := r.FormValue("tts_text")
		if len([]rune(text)) > 30 {
			longFails++
			w.WriteHeader(http.StatusOK) // 生成失败也回 200，body 为空
			return
		}
		shortOK++
		w.Header().Set("X-Sample-Rate", "24000")
		_, _ = w.Write(make([]byte, 2400))
	})
	provider := cvFast(NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: srv.URL, PromptWAV: cvTestWAV(t)}))
	text := strings.Repeat("这是一句用于验证拆短重试的讲解词。", 8) // 144 字 → 两块 80/64
	res, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: text})
	if err != nil {
		t.Fatalf("拆短后应能完成合成，得到 %v", err)
	}
	if len(res.Audio) == 0 {
		t.Fatal("应产出音频")
	}
	if longFails == 0 || shortOK == 0 {
		t.Fatalf("应同时出现长块失败与短块成功，得到 long=%d short=%d", longFails, shortOK)
	}
}

func TestCosyVoiceHTTPErrorKeepsStatus(t *testing.T) {
	srv := cvServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("TTS busy; request was not queued"))
	})
	provider := cvFast(NewCosyVoiceProvider(CosyVoiceConfig{BaseURL: srv.URL, PromptWAV: cvTestWAV(t)}))
	_, err := provider.Synthesize(context.Background(), SynthesisRequest{Text: "自检"})
	if err == nil {
		t.Fatal("409 必须报错")
	}
	var httpErr *HTTPError
	if !asHTTPError(err, &httpErr) || httpErr.Status != http.StatusConflict {
		t.Fatalf("应保留 409 状态码供上层分类: %v", err)
	}
}

func asHTTPError(err error, target **HTTPError) bool {
	for err != nil {
		if e, ok := err.(*HTTPError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestPCM16ToWAVHeader(t *testing.T) {
	pcm := make([]byte, 480)
	wav, err := pcm16ToWAV(24000, pcm)
	if err != nil {
		t.Fatalf("pcm16ToWAV: %v", err)
	}
	if len(wav) != 44+len(pcm) {
		t.Fatalf("len = %d", len(wav))
	}
	if binary.LittleEndian.Uint32(wav[24:28]) != 24000 || binary.LittleEndian.Uint16(wav[22:24]) != 1 {
		t.Fatalf("采样率/声道写错: % x", wav[20:28])
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(pcm)) {
		t.Fatalf("data size = %d", got)
	}
}
