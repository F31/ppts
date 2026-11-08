package share

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
)

// 混音轨的内存占用（P3-4）。
//
// 混音轨是成品包里**唯一**会随时长线性增长、且可能到上百 MB 的条目：
// 5 分钟 24kHz 单声道 PCM16 就是 14.4 MB，30 分钟是 86 MB。它原来在
// `bytes.Buffer` 里拼完再整段交给 zip，于是「拼一份、写一份」同时驻留两份，
// 全量导出的峰值内存与成片时长成正比——而这条路是导出最常见的形态。
//
// 判据不看时间，只看**这次打包一共分配了多少字节**：整段驻留的实现至少要为
// 混音轨分配一份与它等长的内存（bytes.Buffer 扩容还要再翻一倍），
// 流式实现只付一个 copy 缓冲区。夹具让**多条时间轴段共用同一份音频**，
// 于是「读进来的素材」远小于「拼出来的混音轨」——分配量因此只可能来自混音轨本身，
// 不会被素材读取的成本淹掉（这也是为什么不用「每页一段各自不同音频」的夹具）。
//
// 阈值取「混音轨本身的字节数」而不是某个魔数：判据要能自己解释——
// 一条 N 字节的混音轨，打包它时总分配量必须**小于 N**；整段驻留的实现至少要为它
// 分配一份 N，扩容还要再翻一倍（实测 32.0 MB vs 14.4 MB 的混音轨）。

// mixPagePNG 造一张小页图：本用例只量音频，页图编解码的分配要小到不影响结论。
func mixPagePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// mixFixture 造「一页、segments 段 10 秒音频」的输入，所有段复用同一份音频。
//
// 段数而不是页数：页数会把页图编解码的分配一起算进来（30 页 JPEG 约 21 MB），
// 那条曲线与混音轨无关，却足以把这里的结论淹没。一段一个 slide 也合法，
// 多段顺序播放本来就是同一页里讲很久的正常形态。
func mixFixture(segments int) (BundleInput, ResourceReader) {
	const sampleRate = 24000
	const segUS = 10 * 1_000_000
	clip := pcm16WAV(sampleRate * 10)
	reader := &mapReader{
		pages:   map[string][]byte{"pages/1.png": mixPagePNG()},
		objects: map[string][]byte{"audio/clip.wav": clip, "subs/cues.vtt": []byte("WEBVTT\n\n")},
	}
	timeline := &media.Timeline{
		SchemaVersion: media.TimelineSchemaVersion, DurationUS: int64(segments) * segUS,
		Slides: []media.SlideCue{{SlideID: "slide-1", StartUS: 0, EndUS: int64(segments) * segUS}},
	}
	for i := 0; i < segments; i++ {
		start := int64(i) * segUS
		timeline.Slides[0].Segments = append(timeline.Slides[0].Segments, media.SegmentCue{
			SegmentID: fmt.Sprintf("seg-%d", i+1), AudioKey: "audio/clip.wav",
			StartUS: start, EndUS: start + segUS,
		})
	}
	in := BundleInput{
		Timeline:    timeline,
		PagePNGKeys: []string{"pages/1.png"},
		VTTKey:      "subs/cues.vtt",
		Width:       320,
		Height:      180,
		FPS:         30,
		Title:       "混音轨内存夹具",
	}
	return in, reader
}

// TestBuildDoesNotBufferWholeMix 断言混音轨不再整段驻留内存。
func TestBuildDoesNotBufferWholeMix(t *testing.T) {
	const segments = 30 // 30 × 10s = 5 分钟 → 14.4 MB 混音轨，素材只有 480 KB
	in, reader := mixFixture(segments)
	mixBytes := int64(segments) * 10 * 24000 * 2

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	if _, err := NewBundleBuilder(reader, nil).Build(context.Background(), in, io.Discard); err != nil {
		t.Fatalf("Build: %v", err)
	}

	runtime.ReadMemStats(&after)
	allocated := uint64(after.TotalAlloc - before.TotalAlloc) //nolint:gosec // 差值只会变小，不会溢出
	t.Logf("混音轨 %d 字节，本次打包共分配 %d 字节", mixBytes, allocated)
	if allocated >= uint64(mixBytes) { //nolint:gosec // mixBytes 由常量算出，恒为正
		t.Fatalf("Build allocated %d bytes for a %d-byte mix: the mix is still buffered whole",
			allocated, mixBytes)
	}
}

// BenchmarkBuildMixMemory 用同一份夹具给出可直接对拍的分配量。
//
// 输出写 io.Discard：zip 的压缩产物本就不在这个用例的考察范围，混进来只会让
// 「改前/改后」的差值被输出缓冲稀释。
func BenchmarkBuildMixMemory(b *testing.B) {
	in, reader := mixFixture(30) // 5 分钟，与线上典型成片同量级
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewBundleBuilder(reader, nil).Build(context.Background(), in, io.Discard); err != nil {
			b.Fatalf("Build: %v", err)
		}
	}
}

// mixTempDirs 数出临时目录里还剩几个混音轨目录（本包只用 ppts-mix- 这一个前缀）。
func mixTempDirs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ppts-mix-") {
			n++
		}
	}
	return n
}

// TestBuildCleansUpMixTempDir 断言混音轨的临时目录在成功与降级两条路上都被删掉。
//
// 这条不是洁癖：临时文件泄漏在这个仓库已经出过一次（导出临时 zip 留在 %TEMP%，
// 71 个、每个几十 MB）。混音轨目录里躺的是一份与成片等长的 WAV，漏一次就是几十 MB，
// 而用户只会看到「C 盘越来越小」。
func TestBuildCleansUpMixTempDir(t *testing.T) {
	before := mixTempDirs(t)

	// 成功路径：混音轨拼成。
	in, reader := mixFixture(2)
	if _, err := NewBundleBuilder(reader, nil).Build(context.Background(), in, io.Discard); err != nil {
		t.Fatalf("Build (mix): %v", err)
	}
	if got := mixTempDirs(t); got != before {
		t.Fatalf("temp dirs after a successful build = %d, want %d", got, before)
	}

	// 降级路径：素材不是 PCM16，拼不成混音轨，退回逐段。
	in, reader = mixFixture(2)
	reader.(*mapReader).objects["audio/clip.wav"] = []byte("not a wav at all")
	if _, err := NewBundleBuilder(reader, nil).Build(context.Background(), in, io.Discard); err != nil {
		t.Fatalf("Build (per-segment fallback): %v", err)
	}
	if got := mixTempDirs(t); got != before {
		t.Fatalf("temp dirs after a fallback build = %d, want %d", got, before)
	}
}

// TestBundleOutRejectsStreamedTextEntry 断言流式通道只用于二进制条目。
//
// 文本条目要先扫外链（V14），扫描必须先有完整字节；一旦有人为了省内存把
// 字幕/清单也走流式，自包含门禁就被悄悄绕过了，而现场表现是「断网白屏」。
func TestBundleOutRejectsStreamedTextEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cues.vtt")
	if err := os.WriteFile(path, []byte("WEBVTT\n\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sink := &singleSink{}
	out := &bundleOut{sink: sink, files: map[string]struct{}{}}
	err := out.file(FileSubtitlesVTT, path)
	if err == nil {
		t.Fatal("streaming a text entry must fail: external-reference scanning would be skipped")
	}
	if len(sink.entries) != 0 {
		t.Fatalf("rejected entry still reached the sink: %d entries", len(sink.entries))
	}
}
