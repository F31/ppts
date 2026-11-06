package tts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestSplitTTSChunks(t *testing.T) {
	short := "本地语音模型短句。"
	if got := splitTTSChunks(short, localTTSChunkRunes); len(got) != 1 || got[0] != short {
		t.Fatalf("短文本不应分块: %v", got)
	}

	// 带句读的长文本：优先在标点处切，且不能丢字符。
	long := strings.Repeat("这一页介绍本地语音模型的接入方式，讲稿会被切成若干段分别合成。", 12)
	chunks := splitTTSChunks(long, localTTSChunkRunes)
	if len(chunks) < 3 {
		t.Fatalf("长文本应切成多块，实际 %d 块", len(chunks))
	}
	var joined strings.Builder
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > localTTSChunkRunes {
			t.Fatalf("第 %d 块 %d 字，超过上限 %d", i, n, localTTSChunkRunes)
		}
		joined.WriteString(c)
	}
	if joined.String() != long {
		t.Fatal("分块拼接后与原文不一致（丢了字符）")
	}

	// 完全没有标点的长文本也必须硬切，不能整段发出去。
	plain := strings.Repeat("啊", 350)
	chunks = splitTTSChunks(plain, localTTSChunkRunes)
	if len(chunks) != 4 {
		t.Fatalf("无标点文本应按长度硬切为 4 块，实际 %d", len(chunks))
	}
	if strings.Join(chunks, "") != plain {
		t.Fatal("无标点文本分块丢了字符")
	}
}

func TestConcatWAV16(t *testing.T) {
	out, err := concatWAV16([][]byte{smallWAV(t), smallWAV(t)})
	if err != nil {
		t.Fatalf("concatWAV16: %v", err)
	}
	samples, sr, ch, ok := wavPCM16(out)
	if !ok {
		t.Fatal("拼接结果不是可解析的 PCM16 WAV")
	}
	if sr != 16000 || ch != 1 || len(samples) != 32000 {
		t.Fatalf("拼接结果 = %d samples / %dHz / %dch，期望 32000 / 16000 / 1", len(samples), sr, ch)
	}
	if _, err := concatWAV16(nil); err == nil {
		t.Fatal("空片段必须报错")
	}
}

func TestSiliconFlowLocalChunksLongText(t *testing.T) {
	var mu sync.Mutex
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		inputs = append(inputs, body.Input)
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(smallWAV(t))
	}))
	defer srv.Close()

	// 本机回环 + 空 key：既能免鉴权，也会触发分块。
	text := strings.Repeat("本地语音模型长讲稿分块测试，避免单次请求把推理服务拖死。", 9)
	p := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: srv.URL, Model: "qwen3-tts-1.7b-customvoice", Voice: "vivian"})
	res, err := p.Synthesize(context.Background(), SynthesisRequest{Text: text})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), inputs...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("长文本应分多次请求，实际 %d 次", len(got))
	}
	for i, in := range got {
		if n := utf8.RuneCountInString(in); n > localTTSChunkRunes {
			t.Fatalf("第 %d 次请求 %d 字，仍会触发长文本超时", i, n)
		}
	}
	if strings.Join(got, "") != text {
		t.Fatal("多次请求的文本拼起来与原文不一致")
	}
	if res.RealDurationMS != int64(3000) {
		t.Fatalf("拼接后时长 = %dms，期望 3 段 × 1s = 3000ms", res.RealDurationMS)
	}
	if res.BillingQuantity != int64(utf8.RuneCountInString(text)) {
		t.Fatalf("计费字符数 = %d，应按完整原文计", res.BillingQuantity)
	}
}

func TestSiliconFlowRemoteLongTextNotChunked(t *testing.T) {
	calls := 0
	var sent string
	p := NewSiliconFlowProvider(SiliconFlowConfig{
		BaseURL: "https://api.siliconflow.cn", APIKey: "sk-remote", Model: "m",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			var body struct {
				Input string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent = body.Input
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"audio/wav"}},
				Body:       http.NoBody,
			}, nil
		})},
	})
	// 远程供应商保持单次请求；这里用空音频让流程在读取字节后失败，
	// 只验证「请求次数 = 1」与「input 是全文」，不需要完整合成结果。
	text := strings.Repeat("远程供应商不应被分块。", 40)
	_, _ = p.Synthesize(context.Background(), SynthesisRequest{Text: text})
	if calls != 1 {
		t.Fatalf("远程长文本发起了 %d 次请求，应保持 1 次", calls)
	}
	if sent != text {
		t.Fatal("远程请求没有发送完整原文")
	}
}
