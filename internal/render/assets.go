package render

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// stageAssetsFor 把**选定页**引用的底图从只读资产根复制进本 job 的工作目录。
// pages 为 nil 表示全部页。
//
// 为什么需要这一步（原有理由）：任务起跑前会 `RemoveAll` 工作目录（ADR-11 的重试语义
// 要求上一轮半成品必须消失），所以提交上来的计划里那些相对路径在起跑时一定不存在。
// 而合成器只能从 `publicDir`（= 工作目录）取图——Remotion 的 `staticFile()`
// 不接受工作目录之外的路径。
//
// 为什么需要「选定页」（M5）：只有未命中缓存的页才会被渲染，给命中页复制底图
// 纯属搬运——10 页计划里改 1 页时，这等于白搬 9 张 1080p PNG 进 tmpfs。
// 调用方必须把「转场依赖的前一页」也算进集合（见 withTransitionPrevs），
// 少传一页的表现是转场那几帧空白，而不是报错。
func (s *Service) stageAssetsFor(plan *Timeline, dir string, pages []int) error {
	root := s.cfg.AssetDir
	if root == "" {
		return nil
	}
	selected := make(map[int]struct{}, len(pages))
	if pages == nil {
		for i := range plan.Pages {
			selected[i] = struct{}{}
		}
	} else {
		for _, i := range pages {
			selected[i] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(selected))
	for i, page := range plan.Pages {
		if _, ok := selected[i]; !ok {
			continue
		}
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

// stageAssets 复制全部页的底图，保留给只关心「整片」的调用方与既有用例。
func (s *Service) stageAssets(plan *Timeline, dir string) error {
	return s.stageAssetsFor(plan, dir, nil)
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
	// Lstat 而不是 Stat：Stat 会跟随符号链接，一个指向 /etc/passwd 的链接会被
	// 当成普通文件复制进产物目录——路径字符串合法，但读到的东西在资产根之外。
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("资产不存在: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("资产是符号链接（可能指向资产根之外），拒绝")
	}
	// 只接受普通文件：目录会让下面的 Copy 读出一个空文件，
	// 把问题推迟到渲染阶段才以「画面空白」的形式暴露。
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
