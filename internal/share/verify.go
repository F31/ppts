package share

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
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
	names := make(map[string]struct{}, len(zr.File))
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
		rc, openErr := entry.Open()
		if openErr != nil {
			return nil, nil, fmt.Errorf("share: read entry %q: %w", entry.Name, openErr)
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, maxEntryBytes))
		rc.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("share: read entry %q: %w", entry.Name, readErr)
		}
		if _, exists := names[entry.Name]; exists {
			return nil, nil, fmt.Errorf("share: duplicate entry %q", entry.Name)
		}
		names[entry.Name] = struct{}{}
		files[entry.Name] = content
	}
	raw, ok := files[FileManifestJSON]
	if !ok {
		return nil, nil, fmt.Errorf("share: bundle is missing %s", FileManifestJSON)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, nil, fmt.Errorf("share: parse %s: %w", FileManifestJSON, err)
	}
	if err := manifest.Validate(names); err != nil {
		return nil, nil, err
	}
	return &manifest, files, nil
}

// externalRefPattern 匹配 src/href/url() 指向远程资源的引用。
// 只关心"会发网络请求"的形态：协议相对 // 与 http(s)://；data: 是内联，不算外链。
var externalRefPattern = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']?\s*(?:https?:)?//|url\(\s*["']?\s*(?:https?:)?//`)

// CountExternalRefs 统计包内文本文件的外部引用数量（spec: MUST 为 0）。
func CountExternalRefs(files map[string][]byte) int {
	count := 0
	for name, content := range files {
		switch {
		case strings.HasSuffix(name, ".html"), strings.HasSuffix(name, ".css"), strings.HasSuffix(name, ".js"):
		default:
			continue
		}
		count += len(externalRefPattern.FindAllIndex(content, -1))
	}
	return count
}
