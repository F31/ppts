package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

// 本文件只放零状态的小工具：不含 handler 字段、不触对象存储、不做 IO 决策，
// 因此可以脱离 NarrationHandler 单独阅读与单测。带 h 的方法留在 narration.go。

// ttsAdapterVersion 是合成配方的版本号，参与 configHash（见 synthesisHash）。
//
// 只要"同样的文本会得到不同波形"就必须升版：分块大小、prompt、拼接方式、
// 适配器内部的语速/重采样处理都算配方。不升版的后果不是"音频旧一点"，而是
// 同一 configHash 键下的音频对象与 manifest（含 durationMS）来自不同配方——
// 时间轴按 manifest 记时长，导出按真实文件校验，最终表现为
// "audio clip ... duration differs from timeline"（2026-11-07 实测：分块 40→80
// 后，10 页缓存命中、12 页新合成，混用导致整份成片导出被拒）。
//
// v2：CosyVoice 适配器单次请求分块 40→80 字。
const ttsAdapterVersion = "v2"

func synthesisHash(snapshot NarrationSnapshot, capabilities tts.VoiceCapabilities, text string, sc tts.SpeechControl) (string, error) {
	input := struct {
		AdapterVersion string            `json:"adapterVersion"`
		Text           string            `json:"text"`
		Language       string            `json:"language"`
		VoiceID        string            `json:"voiceId"`
		ModelID        string            `json:"modelId"`
		Region         string            `json:"region"`
		SampleRate     int               `json:"sampleRate"`
		SpeechControl  tts.SpeechControl `json:"speechControl"`
	}{ttsAdapterVersion, text, snapshot.Language, snapshot.VoiceID, capabilities.ModelID, capabilities.Region, snapshot.SampleRate, sc}
	b, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return hashBytes(b), nil
}

func validateSynthesisResult(text string, result tts.SynthesisResult) error {
	if len(result.Audio) == 0 || result.RealDurationMS <= 0 {
		return errors.New("narration job: provider returned empty audio or duration")
	}
	if result.Alignment == nil || result.Alignment.Text != text {
		return errors.New("narration job: provider returned missing or mismatched alignment")
	}
	limitUS := result.RealDurationMS * 1000
	var previousEnd int64
	for _, token := range result.Alignment.Tokens {
		if token.StartUS < previousEnd || token.EndUS <= token.StartUS || token.EndUS > limitUS {
			return errors.New("narration job: provider returned invalid alignment offsets")
		}
		previousEnd = token.EndUS
	}
	return nil
}

func audioExtension(format string) (string, error) {
	switch format {
	case "audio/wav":
		return "wav", nil
	case "audio/mpeg":
		return "mp3", nil
	default:
		return "", fmt.Errorf("narration job: unsupported audio format %q", format)
	}
}

func classifyTTSError(err error) error {
	var retryable *tts.RetryableError
	if !errors.As(err, &retryable) {
		return err
	}
	retry := &pipeline.RetryError{Err: err}
	if retryable.RetryAfter > 0 {
		retry.At = time.Now().Add(retryable.RetryAfter)
	}
	return retry
}

func ttsErrorFlags(err error) (retryable bool, throttled bool) {
	if err == nil {
		return false, false
	}
	var retry *tts.RetryableError
	retryable = errors.As(err, &retry)
	var status interface{ HTTPStatus() int }
	if errors.As(err, &status) && status.HTTPStatus() == http.StatusTooManyRequests {
		throttled = true
	}
	return retryable, throttled
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashReader 流式计算 reader 内容的 SHA-256 与字节数，供导出等大文件路径在不上浮内存的前提下
// 得到内容寻址键与元数据尺寸。
func hashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// scopedSegmentID 生成时间轴使用的全局分段键：分段 id 只需页内唯一，
// 但时间轴/字幕/播放清单要求全局唯一，因此统一以「页:段」作为键。
func scopedSegmentID(slideID, segmentID string) string {
	return slideID + ":" + segmentID
}

// timelineFocus 把讲稿的焦点目标搬进时间轴。空目标（ShapeID 为空）= 不指。
//
// 这一跳是刻意的拷贝而不是共享类型：时间轴是播放与导出的时钟模型，它只需要
// "指哪"，不需要讲稿包的存储语义（见 media.SegmentFocus 的注释）。
func timelineFocus(in *narration.FocusTarget) *media.SegmentFocus {
	if !in.Valid() {
		return nil
	}
	return &media.SegmentFocus{ShapeID: in.ShapeID, BarID: in.BarID}
}
