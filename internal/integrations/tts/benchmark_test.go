package tts

import (
	"encoding/binary"
	"testing"
)

// makeAudio 按给定时长生成 VAD 可用的语音/静音交错 PCM16 mono WAV。
// 独立于测试辅助 burstWAV（后者签名要求 *testing.T），供基准在 *testing.B 下直接使用。
func makeAudio(b *testing.B, ms int) []byte {
	b.Helper()
	const rate = 16000
	total := rate * ms / 1000
	samples := make([]int16, total)
	// 300ms 首静音后，以 600ms 语音 + 200ms 停顿循环铺满，尾随 300ms 静音。
	head := rate * 300 / 1000
	tail := total - rate*300/1000
	pos := head
	voiced := true
	slot := rate * 600 / 1000
	for pos < tail {
		n := slot
		if n > tail-pos {
			n = tail - pos
		}
		if voiced {
			for k := 0; k < n; k++ {
				samples[pos+k] = 12000
			}
		}
		voiced = !voiced
		pos += n
		if !voiced {
			slot = rate * 200 / 1000
		} else {
			slot = rate * 600 / 1000
		}
	}
	dataSize := uint32(len(samples) * 2)
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], 36+dataSize)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], 1) // mono
	binary.LittleEndian.PutUint32(out[24:], rate)
	binary.LittleEndian.PutUint32(out[28:], rate*2)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:], dataSize)
	for i, s := range samples {
		out[44+i*2] = byte(s)
		out[44+i*2+1] = byte(s >> 8)
	}
	return out
}

func benchmarkRecomputeAlignment(b *testing.B, ms int) {
	audio := makeAudio(b, ms)
	duration := int64(ms)
	text := "今天天气很好，我们出去走走吧。这是用于对齐计算的基准测试文本，包含标点与多段语音。"
	b.SetBytes(int64(len(audio)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al := RecomputeAlignment(text, audio, duration)
		if al.Method == "" {
			b.Fatal("empty alignment method")
		}
	}
}

// BenchmarkRecomputeAlignment_Short 短分段（约 2s 音频，配音分段常见形态）。
func BenchmarkRecomputeAlignment_Short(b *testing.B) { benchmarkRecomputeAlignment(b, 2000) }

// BenchmarkRecomputeAlignment_Medium 中长分段（约 8s 音频）。
func BenchmarkRecomputeAlignment_Medium(b *testing.B) { benchmarkRecomputeAlignment(b, 8000) }

// BenchmarkRecomputeAlignment_Long 长分段（约 30s 音频，覆盖长句边界）。
func BenchmarkRecomputeAlignment_Long(b *testing.B) { benchmarkRecomputeAlignment(b, 30000) }
