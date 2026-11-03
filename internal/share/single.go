package share

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// 单文件形态（ADR-029 ⑤）。
//
// 定位：与目录包**同源**的另一种交付视图，不是第二套打包逻辑。
// 条目集合、降级语义、字体覆盖校验、自包含校验全部复用 `buildInto`——
// 这里只做一件事：把同一批条目内联进 index.html。
//
// 为什么不做成独立实现：如果单文件自己拼 HTML，两份产物迟早会分叉，
// 而分叉的现场在客户手里（"zip 能播，单文件黑屏"），我们看不到。
//
// 代价如实记录：单文件体积 = 解压后字节 × 1.33（base64），而 zip 交付的是**压缩后**
// 的体积。两者之比完全取决于音频的可压缩性：静音夹具上 zip 60 KB、单文件 2.07 MB，
// 相差 36 倍；真实语音的 PCM 压缩率低，差距会小得多，但"单文件更大"这一点不会变。
// 所以两种形态都要出，而不是让单文件取代目录包。

// singleFileCSP 是单文件形态的内容安全策略。
//
// 与目录包最大的差别是必须允许内联脚本与内联样式——单文件里没有"外部文件"可引用。
// 放宽的两项都只作用于**本文件内**：`default-src 'none'` 仍然封掉一切网络来源，
// img/media/font 只允许 `data:`，所以"断网也能播"与目录包同义。
//
// 内联脚本在目录包里是被刻意禁止的（ADR-1：门禁探针不能往页面插脚本）。
// 单文件不跑浏览器门禁探针，这条约束在这里没有对应物；但这也意味着
// **单文件产物不参与 V13/V18 的逐帧比对**，它的验收靠"目录包同一份组装"+ 结构门禁。
const singleFileCSP = "default-src 'none'; img-src data:; media-src data:; font-src data:; " +
	"style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"

var (
	// indexCSPAttrRe 只匹配 index.html 里的 CSP meta（编译器常量，注入面为零）。
	indexCSPAttrRe = regexp.MustCompile(`(?i)<meta\s+http-equiv="Content-Security-Policy"[^>]*>`)
	// linkCSSRe / scriptJSRe 匹配模板里那几种固定写法。
	linkCSSRe  = regexp.MustCompile(`(?i)<link\s+rel="stylesheet"\s+href="([^"]+)"\s*/?>`)
	scriptJSRe = regexp.MustCompile(`(?i)<script\s+src="([^"]+)"\s*>\s*</script>`)
)

// BuildSingle 打包并把全部条目内联成一个 HTML 文档。
//
// 返回的 manifest 与目录包完全一致（同一次 buildInto 产出）：调用方用它落日志、
// 记条目数，不需要为单文件另算一份。
func (b *BundleBuilder) BuildSingle(ctx context.Context, in BundleInput) ([]byte, *Manifest, error) {
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	manifest, _, err := b.buildInto(ctx, in, func(name string, data []byte) error {
		// 必须拷贝：buildInto 里同一份字节可能被复用（页图同时用于挖空与写入），
		// 而内联阶段会就地改写文本条目。
		entries = append(entries, entry{name: name, data: append([]byte(nil), data...)})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	byName := make(map[string]int, len(entries))
	for i, e := range entries {
		byName[e.name] = i
	}
	if _, ok := byName[FileIndexHTML]; !ok {
		return nil, nil, errors.New("share: single file requires index.html")
	}

	// 1) 二进制条目 → data URI。
	//
	// 顺序按路径长度倒序替换：`slides/001.png` 与 `slides/001-shot-01.png` 互不为前缀，
	// 但把长的先换掉可以避免将来命名变短时出现"换了一半"的路径。
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if isTextEntry(e.name) || e.name == FileIndexHTML {
			continue
		}
		names = append(names, e.name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	uris := make(map[string]string, len(names))
	for _, name := range names {
		i := byName[name]
		uri, uerr := dataURI(name, entries[i].data)
		if uerr != nil {
			return nil, nil, uerr
		}
		uris[name] = uri
	}

	// 2) 文本条目里出现的资源路径 → data URI。
	//
	// 清单是 JS（`manifest.js`），引用的路径以 JSON 字符串形式出现；
	// 直接在文本上替换路径是最省事也最不容易错的做法——路径是包内唯一标识，
	// 不需要解析 JSON 再回写（回写会改字段顺序、破坏"同一份组装"的可比性）。
	for i := range entries {
		if !isTextEntry(entries[i].name) {
			continue
		}
		s := string(entries[i].data)
		for _, name := range names {
			if strings.Contains(s, name) {
				s = strings.ReplaceAll(s, name, uris[name])
			}
		}
		entries[i].data = []byte(s)
	}

	// 3) index.html：内联样式/脚本，换 CSP。
	index := string(entries[byName[FileIndexHTML]].data)
	index = indexCSPAttrRe.ReplaceAllString(index,
		`<meta http-equiv="Content-Security-Policy" content="`+singleFileCSP+`">`)
	var inlineErr error
	index = linkCSSRe.ReplaceAllStringFunc(index, func(m string) string {
		name := linkCSSRe.FindStringSubmatch(m)[1]
		i, ok := byName[name]
		if !ok {
			inlineErr = fmt.Errorf("share: single file references missing stylesheet %q", name)
			return m
		}
		return "<style>\n" + string(entries[i].data) + "\n</style>"
	})
	if inlineErr != nil {
		return nil, nil, inlineErr
	}
	index = scriptJSRe.ReplaceAllStringFunc(index, func(m string) string {
		name := scriptJSRe.FindStringSubmatch(m)[1]
		i, ok := byName[name]
		if !ok {
			inlineErr = fmt.Errorf("share: single file references missing script %q", name)
			return m
		}
		// 脚本里可能出现 `</script>` 字面量（字符串常量），内联时必须断开，
		// 否则浏览器在那里提前结束脚本块，后面的代码变成页面文本。
		body := strings.ReplaceAll(string(entries[i].data), "</script", `<\/script`)
		return "<script>\n" + body + "\n</script>"
	})
	if inlineErr != nil {
		return nil, nil, inlineErr
	}

	out := []byte(index)

	// 4) 自包含复核：单文件必须一个外链都没有。
	//
	// 这一步与目录包共用 V14 的判据（`ScanExternalRefs`），但要求更严：
	// 目录包允许"引用包内文件"，单文件连包内文件都不存在了，任何非 data: 引用都是坏链。
	if refs := ScanExternalRefs(FileIndexHTML, out); len(refs) > 0 {
		return nil, nil, fmt.Errorf("share: single file still references %d external resource(s): %v",
			len(refs), refs[:min(len(refs), 3)])
	}
	// 残留检查：条目名还在正文里，说明漏了一处替换（上面那条正则只查 src/href/url，
	// 查不到写在 JS 字符串里的路径）。漏掉的后果是播放器请求一个不存在的文件。
	for _, name := range names {
		if bytes.Contains(out, []byte(name)) {
			return nil, nil, fmt.Errorf("share: single file still contains resource path %q", name)
		}
	}
	if len(out) == 0 {
		return nil, nil, errors.New("share: single file is empty")
	}
	return out, manifest, nil
}

// dataURI 把一条包内资源编成 data URI。
//
// MIME 按扩展名定死而不走 mime.TypeByExtension：后者依赖宿主机的 /etc/mime.types，
// 同一份包在不同机器上可能得到不同的 Content-Type，而字体/音频的 MIME 写错时
// 浏览器是**静默跳过**（不回退、不报错），现场只剩"没声音"。
func dataURI(name string, data []byte) (string, error) {
	mime, ok := singleFileMIME[strings.ToLower(path.Ext(name))]
	if !ok {
		return "", fmt.Errorf("share: single file does not know how to inline %q", name)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

var singleFileMIME = map[string]string{
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".wav":   "audio/wav",
	".mp3":   "audio/mpeg",
	".m4a":   "audio/mp4",
	".otf":   "font/otf",
	".ttf":   "font/ttf",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".json":  "application/json",
	".vtt":   "text/vtt",
	".srt":   "application/x-subrip",
	".js":    "text/javascript",
	".css":   "text/css",
}
