package tts

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func wavDurationMS(data []byte) int64 {
	if len(data) < 44 || string(data[:4]) != "RIFF" {
		return 0
	}
	byteRate := binary.LittleEndian.Uint32(data[28:32])
	dataSize := binary.LittleEndian.Uint32(data[40:44])
	return int64(dataSize) * 1000 / int64(byteRate)
}

func TestFakeProviderSynthesize(t *testing.T) {
	f := NewFakeProvider()
	ctx := context.Background()
	text := "本页介绍 PCIe 5.0 协议"
	res, err := f.Synthesize(ctx, SynthesisRequest{
		LogicalOpID: "op-1", VoiceID: "v", Text: text, Language: "zh-CN",
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if res.Format != "audio/wav" || len(res.Audio) <= 44 {
		t.Fatalf("audio: format=%s len=%d", res.Format, len(res.Audio))
	}
	chars := len([]rune(text))
	wantMS := int64(float64(chars)/f.CharsPerSecond*1000) + 400
	got := wavDurationMS(res.Audio)
	if got != res.RealDurationMS {
		t.Fatalf("wav duration %d != reported %d", got, res.RealDurationMS)
	}
	if res.RealDurationMS < wantMS-100 || res.RealDurationMS > wantMS+100 {
		t.Fatalf("duration: reported=%d want≈%d", res.RealDurationMS, wantMS)
	}
	if res.Alignment == nil || res.Alignment.Method != AlignEstimate || len(res.Alignment.Tokens) != chars {
		t.Fatalf("alignment: %+v", res.Alignment)
	}
	if res.BillingQuantity != int64(chars) {
		t.Fatalf("billing: %d", res.BillingQuantity)
	}
}

func TestFakeProviderCapabilitiesAndFailure(t *testing.T) {
	f := NewFakeProvider()
	ctx := context.Background()
	caps, err := f.Capabilities(ctx, "v")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps.Languages) != 1 || caps.Languages[0] != "zh-CN" || caps.TimestampType != TimestampPerChar {
		t.Fatalf("caps: %+v", caps)
	}

	f.AlwaysFail = true
	_, err = f.Synthesize(ctx, SynthesisRequest{Text: "x", VoiceID: "v"})
	var retry *RetryableError
	if !errors.As(err, &retry) {
		t.Fatalf("injected failure should be retryable, got %v", err)
	}
}

func TestAlignmentMonotonicInsideAudio(t *testing.T) {
	f := NewFakeProvider()
	res, _ := f.Synthesize(context.Background(), SynthesisRequest{Text: "一二三四五六七八九十", VoiceID: "v"})
	tokens := res.Alignment.Tokens
	for i := 1; i < len(tokens); i++ {
		if tokens[i].StartUS < tokens[i-1].EndUS {
			t.Fatalf("non-monotonic tokens at %d", i)
		}
	}
	// 全部落在音频时长内。
	if int64(len(tokens)) > 0 {
		last := tokens[len(tokens)-1]
		if last.EndUS > res.RealDurationMS*1000 {
			t.Fatalf("token beyond audio duration")
		}
	}
}
