package share

import (
	"fmt"
	"regexp"
	"strings"
)

// 自包含性（V14）：包内所有引用必须落在包里。
//
// 这条约束的唯一意义是「断网也能播」，所以判据是**引用目标**而不是「看起来像不像 URL」：
// `data:` 与片段引用 `#x` 不产生网络请求，放行；以 `/` 开头的绝对路径虽然可能仍在同一
// 主机上，但成品包可以放在任意子路径（甚至 file://），绝对路径会直接 404，也一并拒绝。
var refPattern = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']([^"']*)["']|url\(\s*["']?([^"')]*)["']?\s*\)|@import\s+["']([^"']*)["']`)

// textEntrySuffixes 是需要扫描的条目类型。二进制（图片/音频）不进正则：
// 一是没有意义，二是随机字节里凑出 `src=` 的概率不为零，误报比漏报更难查。
var textEntrySuffixes = []string{".html", ".htm", ".css", ".js", ".json", ".vtt", ".srt", ".txt"}

// ScanExternalRefs 返回条目中的外部引用片段（已去重、保持出现顺序）。
//
// 返回的是**原始片段**而不是行号：报错信息要能直接被人贴进搜索框，
// 「第 173 行第 42 列」对压缩后的 app.js 毫无帮助。
func ScanExternalRefs(name string, data []byte) []string {
	if !isTextEntry(name) {
		return nil
	}
	var found []string
	seen := make(map[string]struct{})
	for _, match := range refPattern.FindAllStringSubmatch(string(data), -1) {
		ref := ""
		for i := 1; i < len(match); i++ {
			if ref = match[i]; ref != "" {
				break
			}
		}
		if !isExternalRef(ref) {
			continue
		}
		trimmed := strings.TrimSpace(ref)
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		found = append(found, trimmed)
	}
	return found
}

// isExternalRef 判断一个引用是否会离开包。
//
// `//host/a.js` 必须排在 `/` 判断之前：协议相对 URL 会被浏览器补成当前协议 + 外部主机，
// 它是最容易被漏掉的一种外链（写起来像相对路径）。
func isExternalRef(ref string) bool {
	r := strings.TrimSpace(ref)
	if r == "" || strings.HasPrefix(r, "#") {
		return false
	}
	lower := strings.ToLower(r)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") {
		return false
	}
	for _, prefix := range []string{"http://", "https://", "//", "file:"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return strings.HasPrefix(r, "/")
}

func isTextEntry(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range textEntrySuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// checkExternalRefs 是打包时的自检入口：违规即失败，不产出坏包。
//
// 放在写 entry 的唯一出口上（emit），意味着**所有**新增条目自动被覆盖——
// 以后有人往包里加一个 `.woff2` 或新的 css，不需要记得再来注册一次扫描。
func checkExternalRefs(name string, data []byte) error {
	refs := ScanExternalRefs(name, data)
	if len(refs) == 0 {
		return nil
	}
	return fmt.Errorf("share: entry %q references external resources: %s", name, strings.Join(refs, ", "))
}
