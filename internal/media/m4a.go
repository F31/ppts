package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AACBitrateKbps 是成品包混音轨的目标码率。
//
// 64kbps 单声道来自设计 §20.6 的体积口径（5 分钟 ≈ 2.4 MB）。讲解音轨是人声，
// 64k 在 24–48kHz 源上都听不出编码痕迹；往上一档 128k 每 5 分钟多出 2.4 MB，
// 而差别只有 A/B 对比才分得出——这个产物的瓶颈是 IM 附件大小，不是听感上限。
const AACBitrateKbps = 64

// EncodeM4A 把整段 WAV 转成 AAC，封装在 m4a（本质就是只含音轨的 MP4）里。
//
// 入参出参都是字节，只为「手上已经有一份内存副本」的调用方保留（测试与门禁）；
// 成品包那条路走 EncodeM4AFile——混音轨动辄几十 MB，从文件到文件才是它的常态。
func (e *MP4Encoder) EncodeM4A(ctx context.Context, wav []byte) ([]byte, error) {
	if e == nil || e.ffmpeg == "" {
		return nil, ErrFFmpegUnavailable
	}
	if len(wav) == 0 {
		return nil, errors.New("media: empty wav input")
	}
	dir, err := os.MkdirTemp("", "ppts-m4a-*")
	if err != nil {
		return nil, fmt.Errorf("media: create transcode dir: %w", err)
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "mix.wav")
	dst := filepath.Join(dir, "mix.m4a")
	if err := os.WriteFile(src, wav, 0o600); err != nil {
		return nil, fmt.Errorf("media: write wav for transcode: %w", err)
	}
	if err := e.EncodeM4AFile(ctx, src, dst); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		return nil, fmt.Errorf("media: read encoded m4a: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("media: aac encode produced no bytes")
	}
	return data, nil
}

// EncodeM4AFile 把 srcPath 的 WAV 转码到 dstPath，返回产物大小由调用方自行 stat。
//
// 为什么落临时文件而不走管道：MP4 的 moov 需要回填才能写 `-movflags +faststart`
// （索引前置，浏览器可以边下边播）。管道上退而求其次只能写 fragmented mp4，
// 而它在部分浏览器的 `<audio>` 直连播放里不被支持——现场表现为"没声音"，
// 排查成本远高于一次临时文件。
//
// 入参是**路径**：调用方（成品包打包器）本来就把混音轨拼在临时文件里，
// 再经 []byte 中转等于把整段 WAV 读进内存又写回磁盘。
func (e *MP4Encoder) EncodeM4AFile(ctx context.Context, srcPath, dstPath string) error {
	if e == nil || e.ffmpeg == "" {
		return ErrFFmpegUnavailable
	}
	if strings.TrimSpace(srcPath) == "" || strings.TrimSpace(dstPath) == "" {
		return errors.New("media: source and destination paths are required")
	}
	cmd := exec.CommandContext(ctx, e.ffmpeg, m4aArgs(srcPath, dstPath, AACBitrateKbps)...)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return fmt.Errorf("media: aac encode failed: %w\n%s", runErr, truncateOutput(out))
	}
	info, err := os.Stat(dstPath)
	if err != nil {
		return fmt.Errorf("media: stat encoded m4a: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("media: aac encode produced no bytes")
	}
	return nil
}

// m4aArgs 构造转码参数。纯函数，便于在**没有 ffmpeg 的机器上**断言参数形态
// （例如"必须 bitexact"这条，靠跑一次编码是断言不出来的）。
func m4aArgs(src, dst string, bitrateKbps int) []string {
	if bitrateKbps <= 0 {
		bitrateKbps = AACBitrateKbps
	}
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		// bitexact：MP4 复用器默认把创建时间写进 mvhd，同一份输入两次编码得到
		// 不同字节。成品包要支持「同输入同产物」的比对（体积基线、包 diff），
		// 这条是前提；否则每次导出的哈希都不同，任何"变了没有"的判断都失效。
		//
		// `-fflags` 在 `-i` 前后各写一次：前者作用于输入解复用，后者作用于输出
		// 复用。只写一处时另一侧仍会写入非确定字段，而"只写一处也能过"的直觉
		// 正是这类问题被漏掉的原因。
		"-fflags", "+bitexact",
		"-i", src,
		"-vn",
		"-c:a", "aac",
		"-b:a", fmt.Sprintf("%dk", bitrateKbps),
		// 单声道：讲解音轨本来就是一路人声，双声道在同样码率下是平分比特，
		// 只会让两耳都更糊（设计 §20.6 的预算也是按单声道写的）。
		"-ac", "1",
		// 这两条是**输出**选项，必须排在 `-i` 之后：放前面会被当成输入选项，
		// ffmpeg 直接以 exit 234 拒绝（"cannot be applied to input url"）。
		"-map_metadata", "-1",
		"-flags:a", "+bitexact",
		"-fflags", "+bitexact",
		"-movflags", "+faststart",
		"-f", "mp4",
		dst,
	}
}

// truncateOutput 把 ffmpeg 的 stderr 截到一行量级：失败时人要能读懂，
// 但整段 stderr 可能几十行，塞进错误信息后反而把真正那行埋了。
func truncateOutput(out []byte) string {
	const limit = 600
	if len(out) <= limit {
		return string(out)
	}
	return string(out[:limit]) + "…（已截断）"
}
