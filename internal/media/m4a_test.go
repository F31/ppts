package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// toneWAV 生成 sampleRate 单声道 PCM16 正弦波，时长由 seconds 决定。
//
// 用正弦而不是静音：静音在 AAC 里会被压到接近 0 字节，编码器哪怕完全失效
// （例如把参数拼错、输出的是空壳文件）也可能"看起来变小了"。
func toneWAV(seconds float64, sampleRate int) []byte {
	frames := int(seconds * float64(sampleRate))
	dataSize := frames * 2
	buf := make([]byte, 44+dataSize)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:], 1) // mono
	binary.LittleEndian.PutUint32(buf[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataSize))
	for i := 0; i < frames; i++ {
		v := int16(math.Round(12000 * math.Sin(2*math.Pi*440*float64(i)/float64(sampleRate))))
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(v))
	}
	return buf
}

// 参数表是纯函数，可以在没有 ffmpeg 的机器上断言：
// bitexact 丢了就等于丢了"同输入同产物"，而这一点靠跑一次编码是看不出来的
// ——单次运行永远自洽，只有两次比对才暴露。
func TestM4AArgsKeepsDeterminismAndMono(t *testing.T) {
	args := strings.Join(m4aArgs("in.wav", "out.m4a", 0), " ")
	for _, want := range []string{
		"-fflags +bitexact",
		"-map_metadata -1",
		"-c:a aac",
		"-b:a 64k", // 0 → 取默认值 AACBitrateKbps
		"-ac 1",
		"-movflags +faststart",
		"-nostdin",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q: %s", want, args)
		}
	}
	// 显式码率要能覆盖默认值（将来调 96k 时不希望有人只改常量、忘了这里）。
	if got := strings.Join(m4aArgs("i", "o", 96), " "); !strings.Contains(got, "-b:a 96k") {
		t.Fatalf("explicit bitrate not honoured: %s", got)
	}
}

func TestEncodeM4ARejectsEmptyInput(t *testing.T) {
	enc := &MP4Encoder{ffmpeg: "ffmpeg"}
	if _, err := enc.EncodeM4A(context.Background(), nil); err == nil {
		t.Fatal("empty wav must be rejected before spawning a process")
	}
}

// nil 接收者必须给出 ErrFFmpegUnavailable 而不是 panic：打包器会把 nil 编码器
// 传下来（本该在注入处拦住），这是最后一道防线。
func TestEncodeM4ANilEncoder(t *testing.T) {
	var enc *MP4Encoder
	if _, err := enc.EncodeM4A(context.Background(), []byte("x")); !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("err = %v, want ErrFFmpegUnavailable", err)
	}
}

// 集成用例：需要本机 ffmpeg。它验证四件事——
//  1. 输出是真 m4a（ftyp box），不是空壳；
//  2. 体积显著小于 WAV（这是整条优化的唯一目的）；
//  3. 时长与源一致（时长漂移会让画面与声音错位，而错位只在长片上才看得出）；
//  4. 两次编码逐字节相同（bitexact 的承诺，体积基线与包 diff 都依赖它）。
func TestEncodeM4AProducesSmallerDeterministicFile(t *testing.T) {
	enc, err := NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	const seconds = 2.0
	wav := toneWAV(seconds, 24000)

	first, err := enc.EncodeM4A(context.Background(), wav)
	if err != nil {
		t.Fatalf("EncodeM4A: %v", err)
	}
	if len(first) < 8 || string(first[4:8]) != "ftyp" {
		t.Fatalf("output is not an mp4 container: % x", first[:min(len(first), 16)])
	}
	if len(first) >= len(wav)/3 {
		t.Fatalf("transcode did not shrink enough: wav=%d m4a=%d", len(wav), len(first))
	}

	// 时长以 ffprobe 为准：容器头里的 duration 与实际解码时长可能不一致，
	// 而播放器用的是后者。ffprobe 对 mp4 需要可 seek 的输入，落临时文件。
	probePath := filepath.Join(t.TempDir(), "probe.m4a")
	if err := os.WriteFile(probePath, first, 0o600); err != nil {
		t.Fatalf("write probe file: %v", err)
	}
	out, err := exec.Command(enc.ffprobe, "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", probePath).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	got, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if perr != nil {
		t.Fatalf("parse ffprobe duration %q: %v", out, perr)
	}
	// 容差 100ms：AAC 的编码器延迟（约 1024 samples ≈ 43ms）会体现在容器时长上，
	// 但超过 100ms 就说明时间轴对不上，播放到后半段会明显错位。
	if math.Abs(got-seconds) > 0.1 {
		t.Fatalf("duration = %.3fs, want %.1fs (±0.1)", got, seconds)
	}

	second, err := enc.EncodeM4A(context.Background(), wav)
	if err != nil {
		t.Fatalf("second EncodeM4A: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two encodes of the same wav differ; bitexact is not in effect")
	}
}
