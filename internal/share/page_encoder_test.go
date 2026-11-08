package share

import (
	"bytes"
	"context"
	"errors"
	"image"
	"strings"
	"testing"
)

// fakePageEncoder 返回预置产物，用来验证「择优」与「降级」两条分支——
// 两条分支都不需要真的 ffmpeg，判据也不该依赖它装没装。
type fakePageEncoder struct {
	data []byte
	ext  string
	err  error
	// calls 记录被调次数：断「一次都没调」比断「结果对」能抓到更早的回归。
	calls int
}

func (f *fakePageEncoder) EncodePage(_ context.Context, img *image.NRGBA) ([]byte, string, error) {
	f.calls++
	if f.err != nil {
		return nil, "", f.err
	}
	if f.data == nil {
		return nil, f.ext, nil
	}
	return f.data, f.ext, nil
}

func buildWithPageEncoder(t *testing.T, enc PageImageEncoder) ([]byte, *Manifest) {
	t.Helper()
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(baseReader(t), nil).SetPageImageEncoder(enc).
		Build(context.Background(), buildInput(), &buf)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	return buf.Bytes(), manifest
}

func slideImages(manifest *Manifest) []string {
	out := make([]string, 0, len(manifest.Slides))
	for _, s := range manifest.Slides {
		out = append(out, s.Image)
	}
	return out
}

// 注入的编码器确实更小时，包内页图与清单必须同时切到它——只改清单不改文件是
// 「清单说 .webp，包里是 .jpg」；只改文件不改清单是解压后 404。
func TestPageImageEncoderTakenWhenSmaller(t *testing.T) {
	// 一页真实页面图编码后是 100–400KB，这里给一个必然小于它的假产物。
	fake := &fakePageEncoder{data: bytes.Repeat([]byte{0x42}, 512), ext: ".webp"}
	zipBytes, manifest := buildWithPageEncoder(t, fake)
	if fake.calls == 0 {
		t.Fatal("注入的编码器一次都没被调用")
	}
	images := slideImages(manifest)
	for _, name := range images {
		if !strings.HasSuffix(name, ".webp") {
			t.Fatalf("清单页图 = %q，期望 .webp", name)
		}
	}
	_, files, err := VerifyBundle(zipBytes)
	if err != nil {
		t.Fatalf("解压: %v", err)
	}
	for _, name := range images {
		if len(files[name]) != 512 {
			t.Fatalf("包内 %s = %d 字节，期望注入产物的 512 字节", name, len(files[name]))
		}
	}
}

// 反而更大时必须留 JPEG：目标始终是「包更小」，不是在产物里写 webp。
// 小图（纯色页、占位页）就会撞上这个分支。
func TestPageImageEncoderNotTakenWhenBigger(t *testing.T) {
	fake := &fakePageEncoder{data: bytes.Repeat([]byte{0x42}, 4<<20), ext: ".webp"}
	zipBytes, manifest := buildWithPageEncoder(t, fake)
	if fake.calls == 0 {
		t.Fatal("应当先编一次再比大小，而不是直接跳过")
	}
	for _, name := range slideImages(manifest) {
		if !strings.HasSuffix(name, ".jpg") {
			t.Fatalf("清单页图 = %q，期望退回 .jpg", name)
		}
	}
	_, files, err := VerifyBundle(zipBytes)
	if err != nil {
		t.Fatalf("解压: %v", err)
	}
	for name, data := range files {
		if strings.HasPrefix(name, "slides/") && len(data) == 4<<20 {
			t.Fatalf("包内 %s 用了更大的产物（%d 字节）", name, len(data))
		}
	}
}

// 编码失败/空输出不能拖垮整包：页图退回 JPEG，导出照常完成。
// 这条与音频的 keep("encode_error") 同构——「更小的包」不该换掉「能导出」。
func TestPageImageEncoderFailureFallsBackToJPEG(t *testing.T) {
	for name, fake := range map[string]*fakePageEncoder{
		"报错":    {err: errors.New("ffmpeg: not found")},
		"空输出":   {data: []byte{}, ext: ".webp"},
		"扩展名为空": {data: bytes.Repeat([]byte{0x42}, 512), ext: ""},
	} {
		t.Run(name, func(t *testing.T) {
			zipBytes, manifest := buildWithPageEncoder(t, fake)
			for _, img := range slideImages(manifest) {
				if !strings.HasSuffix(img, ".jpg") {
					t.Fatalf("清单页图 = %q，期望退回 .jpg", img)
				}
			}
			_, files, err := VerifyBundle(zipBytes)
			if err != nil {
				t.Fatalf("解压: %v", err)
			}
			for _, img := range slideImages(manifest) {
				if len(files[img]) == 0 {
					t.Fatalf("包内缺 %s", img)
				}
			}
		})
	}
}

// 不注入编码器时产物必须与 M6-1 完全一致：清单仍写 .jpg，页图仍是 JPEG。
// 这是「开关关闭的包逐字节等于旧版」这条硬约束在页图上的落点。
func TestPageImageEncoderAbsentKeepsJPEG(t *testing.T) {
	zipBytes, manifest := buildWithPageEncoder(t, nil)
	for _, img := range slideImages(manifest) {
		if !strings.HasSuffix(img, ".jpg") {
			t.Fatalf("清单页图 = %q，期望 .jpg", img)
		}
	}
	_, files, err := VerifyBundle(zipBytes)
	if err != nil {
		t.Fatalf("解压: %v", err)
	}
	for _, img := range slideImages(manifest) {
		data := files[img]
		if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
			t.Fatalf("%s 不是 JPEG（前两字节 %x）", img, data[:2])
		}
	}
}
