package media

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"strings"
	"testing"
)

// webpArgs 是纯函数：即使机器上没有 ffmpeg，也能断言参数形态。
// 这些点每一个都对应一次真实故障（见各条注释），靠"跑一次编码"是断不出来的。
func TestWebPArgs(t *testing.T) {
	args := strings.Join(webpArgs(1600, 900, "/tmp/page.webp"), " ")
	for _, want := range []struct{ name, needle string }{
		{"从 stdin 读原始像素", "-f rawvideo"},
		{"输入像素格式必须与 rgb24 对齐", "-pix_fmt rgb24"},
		{"尺寸必须传给解码器", "-s 1600x900"},
		{"编码器必须是 libwebp", "-c:v libwebp"},
		{"质量档", "-quality 75"},
		{"压缩力度", "-compression_level 6"},
		{"同输入同字节", "-fflags +bitexact"},
		{"输出格式必须显式指定", "-f webp"},
	} {
		if !strings.Contains(args, want.needle) {
			t.Errorf("%s：参数里缺 %q（实际：%s）", want.name, want.needle, args)
		}
	}
	// 输入走 stdin，输出必须落文件：WebP 的 RIFF 长度要回填，管道做不到。
	if !strings.Contains(args, "-i -") {
		t.Errorf("输入应当来自 stdin（-i -），实际：%s", args)
	}
	if !strings.HasSuffix(args, "/tmp/page.webp") {
		t.Errorf("输出必须是文件路径（管道会让 RIFF 长度写成 0），实际：%s", args)
	}
	if strings.Contains(args, "-nostdin") == false {
		t.Error("必须带 -nostdin：容器里没有 tty，ffmpeg 会等输入")
	}
}

// rgb24 必须按行处理 stride：缩放后的 NRGBA 通常是紧凑的，但 toNRGBA 走
// draw.Draw 时可能带 padding；把 padding 当像素喂进去，画面会整体错位。
func TestRGB24HandlesStride(t *testing.T) {
	const w, h = 3, 2
	stride := w*4 + 8 // 人为加 padding
	pix := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := y*stride + x*4
			pix[p+0] = byte(x + 1)
			pix[p+1] = byte(y + 1)
			pix[p+2] = 200
			pix[p+3] = 255
		}
	}
	img := &image.NRGBA{Pix: pix, Stride: stride, Rect: image.Rect(0, 0, w, h)}
	out := rgb24(img)
	if len(out) != w*h*3 {
		t.Fatalf("rgb24 长度 = %d，期望 %d（不能把 padding 也算进去）", len(out), w*h*3)
	}
	// 第一个像素 = RGB(1,1,200)，最后一行的第一个像素 = RGB(1,2,200)
	if out[0] != 1 || out[1] != 1 || out[2] != 200 {
		t.Fatalf("首像素 = %v，期望 [1 1 200]", out[:3])
	}
	last := (h - 1) * w * 3
	if out[last] != 1 || out[last+1] != 2 || out[last+2] != 200 {
		t.Fatalf("末行首像素 = %v，期望 [1 2 200]", out[last:last+3])
	}
	// 每 3 字节里都不该出现 alpha 的 255
	for i := 2; i < len(out); i += 3 {
		if out[i] != 200 {
			t.Fatalf("第 %d 个通道是 %d，说明通道错位", i, out[i])
		}
	}
}

func TestIsWebP(t *testing.T) {
	yes := append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 4)...)
	if !isWebP(yes) {
		t.Fatal("合法 RIFF/WEBP 头被判否")
	}
	for name, data := range map[string][]byte{
		"jpeg":      {0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0},
		"空":         {},
		"短":         []byte("RIFF"),
		"riff非webp": append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 4)...),
	} {
		if isWebP(data) {
			t.Errorf("%s 被判成了 webp", name)
		}
	}
}

// checkWebP 必须拒掉「头合法但 RIFF 长度是 0」的文件——这正是本次实测里
// 一半页图在浏览器里裂开的原因（ffmpeg 写管道时无法回填长度）。
// 只查头是不够的：头永远是对的，坏在长度字段。
func TestCheckWebPRejectsUnfilledRiffSize(t *testing.T) {
	body := make([]byte, 64)
	broken := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), body...) // 长度 0
	if err := checkWebP(broken); err == nil {
		t.Fatal("RIFF 长度为 0 时应报错，否则坏图会直接写进包")
	}
	good := append([]byte{}, broken...)
	binary.LittleEndian.PutUint32(good[4:8], uint32(len(good)-8))
	if err := checkWebP(good); err != nil {
		t.Fatalf("合法回填被判坏: %v", err)
	}
	if err := checkWebP([]byte{0xFF, 0xD8, 0xFF}); err == nil {
		t.Fatal("非 webp 应报错")
	}
}

// 集成用例：需要 ffmpeg 带 libwebp。
// 验证三件事——输出是真 WebP（RIFF/WEBP）、比真值像素小、两次编码逐字节相同
// （bitexact；包 diff 与体积基线都依赖这条）。
func TestWebPEncodeReal(t *testing.T) {
	enc, err := NewWebPPageEncoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	img := pageLikeImage(640, 360)
	first, ext, err := enc.EncodePage(context.Background(), img)
	if err != nil {
		t.Skipf("ffmpeg lacks libwebp: %v", err)
	}
	if ext != ".webp" {
		t.Fatalf("扩展名 = %q，期望 .webp", ext)
	}
	if !isWebP(first) {
		t.Fatalf("输出不是 WebP：% x", first[:min(len(first), 16)])
	}
	// 原始像素 = 640*360*3 = 691200 字节。真实页图上 WebP 约是 1/10，
	// 这里的合成图更规整，判 1/3 已经足够区分"真压缩"与"原样吐回"。
	if len(first) >= 640*360*3/3 {
		t.Fatalf("没有压缩：%d 字节", len(first))
	}
	second, _, err := enc.EncodePage(context.Background(), img)
	if err != nil {
		t.Fatalf("第二次编码：%v", err)
	}
	if !bytesEqual(first, second) {
		t.Fatal("两次编码字节不同：bitexact 没生效，体积基线与包 diff 都会失效")
	}
}

// pageLikeImage 造一张接近页面的图：浅色底 + 深色横条（模拟文字行）。
// 纯渐变图对 WebP 过于友好，判"有没有压缩"会失去区分力。
func pageLikeImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	bg := color.NRGBA{R: 250, G: 249, B: 246, A: 255}
	fg := color.NRGBA{R: 28, G: 34, B: 74, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := bg
			if y%24 < 6 && x > w/10 && x < w*9/10 {
				c = fg
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
