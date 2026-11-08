package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// WebPQuality 是页图有损 WebP 的质量档。
//
// 75 来自 ADR-034 的真实素材实验：14 页商务模板上体积是 JPEG q82 的 41.4%，
// 而 SSIM（0.9952 vs 0.9924）与高差像素占比（2.28% vs 2.53%）反而更优。
// 再往上一档（q80）体积 46.9%，收益只剩 5 个百分点、单页多 40–60 KB；
// 往下一档（q70）只再省 1.5 个百分点，文字边缘开始变软。
const WebPQuality = 75

// WebPPageEncoder 用本机 ffmpeg 的 libwebp 把页图压成有损 WebP（ADR-034）。
//
// 为什么走外部进程而不是纯 Go：标准库不能编 WebP；唯一可用的纯 Go 实现
// （nativewebp）只做 VP8L lossless，实测同一批页图反而比 JPEG 大 14.2%
// （缩放后的抗锯齿文字边缘是 lossless 的天敌）。cgo 绑定 libwebp 会破坏
// CI 的 4×crossbuild，所以沿用 ADR-032 已经验证过的"外部编码器 + 失败降级"。
type WebPPageEncoder struct {
	ffmpeg string
}

// NewWebPPageEncoder 探测本机 ffmpeg。找不到时返回 ErrFFmpegUnavailable，
// 调用方应当把 nil 编码器交给打包器（页图退回 JPEG），而不是让导出失败。
func NewWebPPageEncoder() (*WebPPageEncoder, error) {
	fm, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("%w: ffmpeg=%v", ErrFFmpegUnavailable, err)
	}
	return &WebPPageEncoder{ffmpeg: fm}, nil
}

// EncodePage 把缩放好的像素压成 WebP，返回字节与固定扩展名 .webp。
//
// 像素走 rbga→rgb24：页图是不透明的（透明内容在层图路径里，那条路走 PNG），
// 去掉 alpha 通道既少一次平面编码，也让 ffmpeg 的输入行宽与 NRGBA 的步长解耦。
func (e *WebPPageEncoder) EncodePage(ctx context.Context, img *image.NRGBA) ([]byte, string, error) {
	if e == nil || e.ffmpeg == "" {
		return nil, "", ErrFFmpegUnavailable
	}
	if img == nil || img.Rect.Dx() <= 0 || img.Rect.Dy() <= 0 {
		return nil, "", errors.New("media: empty page image")
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	raw := rgb24(img)

	dir, err := os.MkdirTemp("", "ppts-webp-*")
	if err != nil {
		return nil, "", fmt.Errorf("media: create webp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	dst := filepath.Join(dir, "page.webp")

	// 输出落临时文件，不走 stdout：WebP 是 RIFF 容器，RIFF 头里的总长度
	// **必须回填**，而管道不可 seek。ffmpeg 对小输出（约 32KB 以内）能在内部
	// 缓冲后回填，超过就写 0——头仍然合法（RIFF/WEBP），解码器却一律拒绝
	// （`could not create decoder object`）。本仓库的页图正好有一半超过这个阈值，
	// 于是"一半页面打不开"，而单张编码测试是绿的。这与 ADR-032 的 m4a faststart
	// 是同一个约束：muxer 要回填，就别给它管道。
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, e.ffmpeg, webpArgs(w, h, dst)...)
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("media: webp encode failed: %w\n%s", err, truncateOutput(errBuf.Bytes()))
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		return nil, "", fmt.Errorf("media: read encoded webp: %w", err)
	}
	if len(data) == 0 {
		return nil, "", errors.New("media: webp encode produced no bytes")
	}
	// 容器完整性：头对了但 RIFF 长度是 0 的文件，浏览器和 PIL 都打不开。
	// 只校验头是不够的——这个坑就是本次实测里"一半页图裂开"的原因。
	if err := checkWebP(data); err != nil {
		return nil, "", err
	}
	return data, ".webp", nil
}

// webpArgs 构造编码参数。纯函数，便于在**没有 ffmpeg 的机器上**断言参数形态
// （例如"必须从 stdin 读 rawvideo""必须 bitexact"这类，跑一次编码是断言不出来的）。
func webpArgs(width, height int, dst string) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		// 输入是内存里的原始像素，不落盘：页图本来就有一份 PNG 在对象存储里，
		// 再写一个临时文件只是把磁盘 IO 换成了"路径不存在"这类新故障点。
		"-f", "rawvideo",
		"-pix_fmt", "rgb24",
		"-s", strconv.Itoa(width) + "x" + strconv.Itoa(height),
		"-i", "-",
		"-c:v", "libwebp",
		"-quality", strconv.Itoa(WebPQuality),
		// 默认 effort 是 4；6 在真实页图上快 0.02s 的差别，体积差 1–2%。
		"-compression_level", "6",
		// 同 AAC：同输入必须得到同字节，否则任何"包变了没有"的比对都失效。
		"-fflags", "+bitexact",
		"-flags:v", "+bitexact",
		"-f", "webp",
		dst,
	}
}

// rgb24 把 NRGBA 摊成 3 字节/像素的紧凑缓冲（丢掉 alpha）。
func rgb24(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	out := make([]byte, 0, w*h*3)
	if img.Stride == w*4 && img.Rect.Min == (image.Point{}) {
		for i := 0; i < len(img.Pix); i += 4 {
			out = append(out, img.Pix[i], img.Pix[i+1], img.Pix[i+2])
		}
		return out
	}
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			p := row[x*4:]
			out = append(out, p[0], p[1], p[2])
		}
	}
	return out
}

// isWebP 校验 RIFF/WEBP 容器头。
func isWebP(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// checkWebP 校验容器头**与** RIFF 声明的总长度。
//
// 只查头会放过一类静默损坏：ffmpeg 写不可 seek 的输出时把长度字段写 0，
// 头仍然合法，解码器却拒绝整个文件。声明的长度必须等于 len(data)-8
// （RIFF 的长度字段不含前 8 字节）。
func checkWebP(data []byte) error {
	if !isWebP(data) {
		return fmt.Errorf("media: webp encode produced non-webp output (%d bytes)", len(data))
	}
	declared := int(binary.LittleEndian.Uint32(data[4:8]))
	if declared != len(data)-8 {
		return fmt.Errorf("media: webp header declares %d bytes, file has %d（容器长度未回填）",
			declared, len(data)-8)
	}
	return nil
}
