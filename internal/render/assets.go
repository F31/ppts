package render

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// stageAssets 把计划里引用的底图从只读资产根复制进本 job 的工作目录。
//
// 为什么需要这一步：任务起跑前会 `RemoveAll` 工作目录（ADR-11 的重试语义要求
// 上一轮半成品必须消失），所以提交上来的计划里那些相对路径在起跑时一定不存在。
// 而合成器只能从 `publicDir`（= 工作目录）取图——Remotion 的 `staticFile()`
// 不接受工作目录之外的路径。两者相加的结果是：没有资产根时，任何带 `baseImage`
// 的计划都渲染不了（实测报 `open /work/<id>/p000.png: no such file`）。
//
// 选择「复制」而不是「直接读资产根」有三条理由：
//  1. `staticFile()` 的根就是工作目录，跨目录读不到；
//  2. 产物目录必须自包含：JobTTL 到期回收时只删一个目录，不该留下指向别处的引用；
//  3. `planHash` 在提交时就定了，复制不改变计划字节，也就不会让同一份计划算出两个哈希。
//
// 只复制计划真正引用的文件（按相对路径去重），不整目录搬运：一份 30 页计划可能
// 只用到资产根里很小一部分，而工作目录常常是 tmpfs。
func (s *Service) stageAssets(plan *Timeline, dir string) error {
	root := s.cfg.AssetDir
	if root == "" {
		return nil
	}
	seen := make(map[string]struct{}, len(plan.Pages))
	for _, page := range plan.Pages {
		rel := page.BaseImage
		if rel == "" {
			continue
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		if err := copyAsset(root, dir, rel); err != nil {
			return fmt.Errorf("准备底图失败: page %d baseImage %q: %w", page.Index, rel, err)
		}
	}
	return nil
}

// copyAsset 把 root/rel 复制成 dir/rel，两侧都要过路径校验。
//
// 两侧都校验不是多余的：root 由部署方给、dir 由服务自己分配，但 rel 来自请求体，
// 是唯一由外部控制的一段。任何一侧被 `..` 或绝对路径穿透，都等于把服务变成
// 「任意文件读取 + 写入工作目录」的工具。
func copyAsset(root, dir, rel string) error {
	src, err := workPath(root, rel)
	if err != nil {
		return fmt.Errorf("资产路径非法: %w", err)
	}
	dst, err := workPath(dir, rel)
	if err != nil {
		return fmt.Errorf("目标路径非法: %w", err)
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("资产不存在: %w", err)
	}
	// 只接受普通文件：符号链接可能指向资产根之外（例如 /etc/shadow），
	// 而目录会让下面的 Copy 读出一个空文件，把问题推迟到渲染阶段。
	if !info.Mode().IsRegular() {
		return fmt.Errorf("资产不是普通文件（mode=%s）", info.Mode().Type())
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("创建资产目录: %w", err)
	}
	return copyFile(src, dst, info.Mode().Perm())
}

// copyFile 复制文件内容。显式 Close 两端：Windows 上「defer Close + 紧接着 stat」
// 会因为句柄未释放而报错（同 writePNGFile 的注释）。
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开资产: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm&0o644)
	if err != nil {
		in.Close()
		return fmt.Errorf("创建副本: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Close()
	closeErr := in.Close()
	if copyErr != nil {
		return fmt.Errorf("复制资产: %w", copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("关闭副本: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭资产: %w", closeErr)
	}
	return nil
}
