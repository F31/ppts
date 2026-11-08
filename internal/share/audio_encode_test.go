package share

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
)

// fakeEncoder 是可控的编码器桩：用它验证「转码成功/失败/反而更大」三条分支，
// 不需要机器上真有 ffmpeg。真实编码路径由 m4a_test.go 的集成用例覆盖。
type fakeEncoder struct {
	out []byte
	err error
	// seen 记录被喂进来的字节数：转码失败的降级要保留原 WAV，而不是丢掉音频。
	seen int
}

func (f *fakeEncoder) EncodeM4A(_ context.Context, wav []byte) ([]byte, error) {
	f.seen = len(wav)
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

// audioMixOf 跑一次打包，返回清单与包内条目字节。
func audioMixOf(t *testing.T, enc M4AEncoder) (*Manifest, map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	builder := NewBundleBuilder(baseReader(t), nil).SetAudioEncoder(enc)
	if _, err := builder.Build(context.Background(), buildInput(), &buf); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// VerifyBundle 复用生产的那套结构校验：清单引用的条目必须真的在包里。
	manifest, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	return manifest, files
}

// 转码成功时，包里只能有 m4a，不能两条音轨都留——否则体积按「两份音频」算，
// 而这正好是这次优化要消除的那一项。
func TestBuildUsesM4AMixWhenEncoderSucceeds(t *testing.T) {
	enc := &fakeEncoder{out: []byte("fake-m4a-payload")}
	manifest, files := audioMixOf(t, enc)

	if manifest.AudioMix != FileAudioMixM4A {
		t.Fatalf("audioMix = %q, want %q", manifest.AudioMix, FileAudioMixM4A)
	}
	if len(files[FileAudioMixM4A]) == 0 {
		t.Fatal("bundle is missing audio/mix.m4a")
	}
	if _, ok := files[FileAudioMix]; ok {
		t.Fatal("bundle must not carry the wav mix once it has been transcoded")
	}
	// 逐段音频的 src 必须指向同一条混音轨：单时钟播放是这套包的语义基础。
	for i, entry := range manifest.Audio {
		if entry.Src != FileAudioMixM4A {
			t.Fatalf("audio %d src = %q, want %q", i, entry.Src, FileAudioMixM4A)
		}
	}
	if enc.seen == 0 {
		t.Fatal("encoder received no bytes; the mix was never assembled")
	}
}

// 禁 JS 时的提示必须写**实际**文件名。写死 mix.wav 的话，转码后的包会让用户
// 照着提示去目录里找一个不存在的文件。
func TestIndexHTMLPointsAtActualAudioEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		enc  M4AEncoder
		want string
	}{
		{name: "转码成功", enc: &fakeEncoder{out: []byte("x")}, want: FileAudioMixM4A},
		{name: "退回 wav", enc: &fakeEncoder{err: errors.New("boom")}, want: FileAudioMix},
		{name: "未注入编码器", enc: nil, want: FileAudioMix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, files := audioMixOf(t, tc.enc)
			if manifest.AudioMix != tc.want {
				t.Fatalf("audioMix = %q, want %q", manifest.AudioMix, tc.want)
			}
			page := string(files[FileIndexHTML])
			if !strings.Contains(page, "音频为同目录") {
				t.Fatal("noscript note should mention audio when a mix exists")
			}
			if !strings.Contains(page, tc.want) {
				t.Fatalf("noscript note must name %q", tc.want)
			}
			// 另一条必须不出现：两个都写等于把选择权丢给用户去猜。
			other := FileAudioMix
			if tc.want == FileAudioMix {
				other = FileAudioMixM4A
			}
			if strings.Contains(page, other) {
				t.Fatalf("noscript note must not mention %q", other)
			}
		})
	}
}

// 没有混音轨（逐段模式）时不该提音频：让用户去找一个不存在的文件比不说更糟。
func TestIndexHTMLOmitsAudioNoteWithoutMix(t *testing.T) {
	in := buildInput()
	// 时间轴里没有段落 → 没有 clips → 也没有混音轨。
	in.Timeline = &media.Timeline{SchemaVersion: media.TimelineSchemaVersion, DurationUS: 250_000,
		Slides: []media.SlideCue{{SlideID: "slide-1", StartUS: 0, EndUS: 250_000}}}
	var buf bytes.Buffer
	if _, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), in, &buf); err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if page := string(files[FileIndexHTML]); strings.Contains(page, "音频为同目录") {
		t.Fatal("noscript note mentions audio although the bundle has no mix track")
	}
}

// 转码失败不能连音频一起丢：包里退回 WAV，导出仍然成功。
// 这条是「更小的包」与「能导出」之间的排序——后者优先。
func TestBuildFallsBackToWavWhenEncoderFails(t *testing.T) {
	manifest, files := audioMixOf(t, &fakeEncoder{err: errors.New("ffmpeg exploded")})
	if manifest.AudioMix != FileAudioMix {
		t.Fatalf("audioMix = %q, want %q", manifest.AudioMix, FileAudioMix)
	}
	if len(files[FileAudioMix]) == 0 {
		t.Fatal("wav mix is missing after a failed transcode")
	}
	// 清单里的每段都要指向这条可播放的音频，否则播放器找不到 src。
	for i, entry := range manifest.Audio {
		if entry.Src != FileAudioMix {
			t.Fatalf("audio %d src = %q, want %q", i, entry.Src, FileAudioMix)
		}
	}
}

// 编码结果反而更大时必须保留 WAV：目标一直是"更小"，不是为了在清单里写 aac。
// 极短或近乎静音的混音轨上，AAC 的头开销确实会反超 WAV。
func TestBuildKeepsWavWhenTranscodeIsNotSmaller(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  []byte
	}{
		{name: "更大", out: bytes.Repeat([]byte{0}, 1<<20)},
		{name: "空输出", out: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, files := audioMixOf(t, &fakeEncoder{out: tc.out})
			if manifest.AudioMix != FileAudioMix {
				t.Fatalf("audioMix = %q, want %q", manifest.AudioMix, FileAudioMix)
			}
			if _, ok := files[FileAudioMixM4A]; ok {
				t.Fatal("oversized or empty transcode output must not be shipped")
			}
		})
	}
}

// 没注入编码器时行为与转码落地前完全一致：这条老路径是隐私模式的保底。
func TestBuildWithoutEncoderKeepsWav(t *testing.T) {
	manifest, files := audioMixOf(t, nil)
	if manifest.AudioMix != FileAudioMix {
		t.Fatalf("audioMix = %q, want %q", manifest.AudioMix, FileAudioMix)
	}
	if _, ok := files[FileAudioMixM4A]; ok {
		t.Fatal("m4a entry must not appear without an encoder")
	}
}

// typed-nil：把 (*media.MP4Encoder)(nil) 装进接口后接口并不为 nil，打包器会去
// 调用它。降级逻辑必须在这种情况仍然交出可播放的 WAV，而不是写出空条目
// （空条目会让清单校验失败，把"少装一个二进制"升级成"导出整个失败"）。
func TestBuildSurvivesTypedNilEncoder(t *testing.T) {
	var nilEnc *media.MP4Encoder
	manifest, files := audioMixOf(t, nilEnc)
	if manifest.AudioMix != FileAudioMix {
		t.Fatalf("audioMix = %q, want %q", manifest.AudioMix, FileAudioMix)
	}
	if len(files[FileAudioMix]) == 0 {
		t.Fatal("wav mix is empty")
	}
}
