package share

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// 代价如实记录：单文件体积 ≈ 解压后字节 × 1.33（base64），而 zip 交付的是**压缩后**
// 的体积。两者之比完全取决于资源的可压缩性：静音夹具上 zip 60 KB、单文件 2.07 MB；
// 5 分钟 / 30 段样本 zip 1.1 MB、单文件 21.9 MB（20 倍）。真实语音的 PCM 压缩率低，
// 差距会小得多，但"单文件更大"这一点不会变。所以两种形态都要出，而不是让单文件取代目录包。
//
// 内联不是"把路径换成 data URI"这么简单：同一个资源在清单里会被引用很多次
// （混音轨模式下 30 段音频都指向同一条 mix），逐处替换会把同一份数据写 30 遍。
// 清单脚本因此改走「短 token + 一份资源表 + 同脚本内还原」，详见步骤 2 的注释。

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

// singleEntry 是内联前的一份条目字节。
type singleEntry struct {
	name string
	data []byte
}

// singleSink 收齐条目再做内联。
//
// 单文件形态**必须**整段持有全部条目：内联就是 base64 进正文，没有「边读边写」的余地。
// 所以这里没有省内存的空间，只有一条容易写错的拷贝规则（见 writeEntry）。
type singleSink struct{ entries []singleEntry }

func (s *singleSink) writeEntry(name string, data []byte) error {
	// 必须拷贝：buildInto 里同一份字节可能被复用（页图同时用于挖空与写入），
	// 而内联阶段会就地改写文本条目。
	s.entries = append(s.entries, singleEntry{name: name, data: append([]byte(nil), data...)})
	return nil
}

// writeStream 读全：data URI 要先知道长度才能编码，这里没有第二条路。
func (s *singleSink) writeStream(name string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("share: read entry %q: %w", name, err)
	}
	s.entries = append(s.entries, singleEntry{name: name, data: data})
	return nil
}

// BuildSingle 打包并把全部条目内联成一个 HTML 文档。
//
// 返回的 manifest 与目录包完全一致（同一次 buildInto 产出）：调用方用它落日志、
// 记条目数，不需要为单文件另算一份。
func (b *BundleBuilder) BuildSingle(ctx context.Context, in BundleInput) ([]byte, *Manifest, error) {
	// 单文件形态的首页提示要改口（不存在"同目录文件"）；条目集合与清单不变，
	// 所以仍然只调一次 buildInto（见 BundleInput.singleFile）。
	in.singleFile = true
	sink := &singleSink{}
	manifest, _, err := b.buildInto(ctx, in, sink)
	if err != nil {
		return nil, nil, err
	}
	entries := sink.entries

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

	// 2) 文本条目里出现的资源路径 → 内联引用。
	//
	// 两种条目走两条路，原因是**同一个资源会被引用很多次**：混音轨模式下
	// `audio[].src` 的每一段都指向同一条 mix（30 段就是 30 次），`slides[i].image`
	// 与 `slides[i].shots[0].image` 还是同一个文件。若在清单脚本上直接
	// ReplaceAll，每次引用都会展开成一份完整 data URI——实测 5 分钟 / 30 段：
	// 单文件 618 MB（zip 是 1.1 MB），而"每份资源只有一份数据"只要 22 MB。
	// 小样本（2 段）看不出差别，所以这处一直绿着。
	//
	// 所以清单脚本（manifest.js）改走「短 token + 一份资源表 + 同脚本内还原」：
	// 引用点只留 `@@ppts-res-3@@`，资源表在脚本尾部写一次，随后深遍历清单把
	// token 换回 data URI。还原必须与赋值在**同一个 <script>** 里——app.js 是
	// 后加载的脚本，它读到的必须已经是还原后的清单。
	//
	// 其它文本条目（含 index.html 的 <noscript>）仍写完整 data URI：禁 JS 时没有
	// 任何脚本能还原 token，那里必须是可直接加载的引用。它们各自是不同的文件，
	// 不构成重复。
	manifestBody := ""
	manifestIdx, hasManifest := byName[FileManifestJS]
	if hasManifest {
		manifestBody = string(entries[manifestIdx].data)
	}
	tokenOrder := make([]string, 0, len(names))
	tokenURI := make(map[string]string, len(names))
	for i, name := range names {
		if !strings.Contains(manifestBody, name) {
			continue
		}
		token := fmt.Sprintf(singleTokenFmt, i)
		tokenURI[token] = uris[name]
		tokenOrder = append(tokenOrder, token)
		manifestBody = strings.ReplaceAll(manifestBody, name, token)
	}
	if hasManifest {
		entries[manifestIdx].data = []byte(manifestBody)
	}

	for i := range entries {
		if !isTextEntry(entries[i].name) || entries[i].name == FileManifestJS {
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
		body := string(entries[i].data)
		if name == FileManifestJS && len(tokenOrder) > 0 {
			// 资源表 + 还原必须紧跟清单赋值，且不能出现在 app.js 里——
			// app.js 是后加载的独立脚本，看不到这里的局部变量 T。
			body += singleRestoreJS(tokenOrder, tokenURI)
		}
		// 脚本里可能出现 `</script>` 字面量（字符串常量），内联时必须断开，
		// 否则浏览器在那里提前结束脚本块，后面的代码变成页面文本。
		body = strings.ReplaceAll(body, "</script", `<\/script`)
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
	//
	// 只管**二进制**条目：文本条目名（subtitles.vtt / manifest.json / timeline.json）
	// 在单文件里仍然以机读字段的形式留在清单里（`subtitles.src`），播放器不加载它们
	// （用的是 `subtitles.inline` 内联的那份 cues），所以那不是坏链。反过来把它们也
	// 抹掉会破坏"清单与目录包逐字节一致"这条等价关系。
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

// singleTokenFmt 是清单脚本里的资源占位符。
//
// 选 `@@ppts-res-N@@` 而不是裸序号：token 会以 JSON 字符串的形式留在清单里，
// 必须是"一眼看得出是打包器塞的"、又不会与真实路径或文案撞车的形状。清单里的
// 路径都带目录前缀与扩展名，`@@` 包起来的写法不可能被命中。
const singleTokenFmt = "@@ppts-res-%d@@"

// singleRestoreJS 生成「资源表 + 就地还原」的脚本尾巴，追加在 manifest.js 之后。
//
// 三条约束：
//  1. 必须与清单赋值在**同一个 <script>** 内：app.js 是后加载的独立脚本，
//     读的是 window.__PPT_MANIFEST__，看不到这里的局部变量。放到 app.js 里会
//     晚一步——播放器第一帧就把 token 当成资源路径去加载。
//  2. 还原是**深遍历**而不是按字段名逐个处理：清单里出现资源路径的字段已有
//     slides[].image / shots[].image / layers[].image / audio[].src / audioMix
//     五处，以后加字段时不该要求同步改这里（漏改的现场是"那一层不显示"）。
//  3. 资源表用完即弃（T 是 IIFE 局部变量，不挂 window）：13 MB 的数据若再被
//     一个全局名引用住，等于白省一份内存。
func singleRestoreJS(order []string, tokenURI map[string]string) string {
	var b strings.Builder
	b.WriteString("(function(){var T={")
	for i, token := range order {
		if i > 0 {
			b.WriteString(",")
		}
		// JSON 编码保证引号/反斜杠安全：值来自 data URI（base64），键是我们自己的
		// token，两者本来都安全，但这里不赌"将来也安全"。
		key, _ := json.Marshal(token)
		val, _ := json.Marshal(tokenURI[token])
		b.Write(key)
		b.WriteString(":")
		b.Write(val)
	}
	b.WriteString("};\n")
	b.WriteString("var M=window.__PPT_MANIFEST__;if(!M)return;\n")
	b.WriteString("var fix=function(s){for(var k in T){if(s.indexOf(k)>=0)s=s.split(k).join(T[k]);}return s;};\n")
	b.WriteString("var walk=function(v){if(typeof v==='string')return fix(v);\n")
	b.WriteString("if(v&&typeof v==='object'){for(var p in v){if(Object.prototype.hasOwnProperty.call(v,p))v[p]=walk(v[p]);}}\n")
	b.WriteString("return v;};\nwalk(M);})();\n")
	return b.String()
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
