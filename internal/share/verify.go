package share

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	maxEntryBytes = 64 << 20
	maxTotalBytes = 512 << 20
)

// VerifyBundle 解包并校验成品包结构，返回清单与条目内容。
//
// 打包器在 Build 末尾已经用同一套 Manifest.Validate 自检；这里是给测试、CI 与
// 外部工具（例如对已下载的 zip 做抽检）复用的入口，保证"服务端认为合格"与
// "验收脚本认为合格"是同一个定义。
func VerifyBundle(data []byte) (*Manifest, map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("share: open zip: %w", err)
	}
	files := make(map[string][]byte, len(zr.File))
	var total int64
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if err := checkBundlePath(entry.Name); err != nil {
			return nil, nil, err
		}
		if entry.UncompressedSize64 > maxEntryBytes {
			return nil, nil, fmt.Errorf("share: entry %q is too large (%d bytes)", entry.Name, entry.UncompressedSize64)
		}
		if total += int64(entry.UncompressedSize64); total > maxTotalBytes {
			return nil, nil, fmt.Errorf("share: bundle exceeds %d bytes", maxTotalBytes)
		}
		if _, exists := files[entry.Name]; exists {
			return nil, nil, fmt.Errorf("share: duplicate entry %q", entry.Name)
		}
		if _, exists := files[entry.Name]; exists {
			return nil, nil, fmt.Errorf("share: duplicate entry %q", entry.Name)
		}
		rc, openErr := entry.Open()
		if openErr != nil {
			return nil, nil, fmt.Errorf("share: read entry %q: %w", entry.Name, openErr)
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, maxEntryBytes))
		rc.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("share: read entry %q: %w", entry.Name, readErr)
		}
		files[entry.Name] = content
	}
	return verifyEntries(files)
}

// VerifyDir 校验目录形态的成品包（隐私模式的主要产物形态）。
//
// 与 VerifyBundle 走同一个 verifyEntries：目录里多一个 manifest 之外的文件不会被发现
// （包本来就可能被用户加说明文件），但清单引用的每个条目、结构约束与外链扫描完全一致。
func VerifyDir(dir string) (*Manifest, map[string][]byte, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("share: resolve bundle directory: %w", err)
	}
	files := make(map[string][]byte)
	var total int64
	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		name := filepath.ToSlash(rel)
		if pathErr := checkBundlePath(name); pathErr != nil {
			return pathErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Size() > maxEntryBytes {
			return fmt.Errorf("share: entry %q is too large (%d bytes)", name, info.Size())
		}
		if total += info.Size(); total > maxTotalBytes {
			return fmt.Errorf("share: bundle exceeds %d bytes", maxTotalBytes)
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return fmt.Errorf("share: read entry %q: %w", name, readErr)
		}
		files[name] = data
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	return verifyEntries(files)
}

// verifyEntries 是 zip 与目录共用的结构门禁 + 清单解析。
func verifyEntries(files map[string][]byte) (*Manifest, map[string][]byte, error) {
	raw, ok := files[FileManifestJSON]
	if !ok {
		return nil, nil, fmt.Errorf("share: bundle is missing %s", FileManifestJSON)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, nil, fmt.Errorf("share: parse %s: %w", FileManifestJSON, err)
	}
	names := make(map[string]struct{}, len(files))
	for name := range files {
		names[name] = struct{}{}
	}
	if err := manifest.Validate(names); err != nil {
		return nil, nil, err
	}
	return &manifest, files, nil
}

// CountExternalRefs 统计包内文本文件的外部引用数量（spec: MUST 为 0）。
//
// 规则在 ScanExternalRefs 里，这里只做汇总：打包时自检与事后校验**必须**同一套判据，
// 否则会出现"打包认为没事、校验报外链"这种互相矛盾的报告，让人开始怀疑门禁本身。
func CountExternalRefs(files map[string][]byte) int {
	count := 0
	for name, content := range files {
		count += len(ScanExternalRefs(name, content))
	}
	return count
}

// ExternalRefSamples 返回外链条目与片段，供校验输出与失败信息使用。
func ExternalRefSamples(files map[string][]byte) map[string][]string {
	out := make(map[string][]string)
	for name, content := range files {
		if refs := ScanExternalRefs(name, content); len(refs) > 0 {
			out[name] = refs
		}
	}
	return out
}
