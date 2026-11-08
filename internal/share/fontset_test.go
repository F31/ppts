package share

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/share/assets"
)

func TestCollectFontCharsetDedupesAndSorts(t *testing.T) {
	got := CollectFontCharset("复盘", []string{"看看数据", "复盘"}, nil)
	// 「复盘」在标题与字幕里各出现一次，只能算一个码位。
	if n := countRune(got.Runes, '复'); n != 1 {
		t.Fatalf("『复』出现 %d 次，want 1", n)
	}
	for i := 1; i < len(got.Runes); i++ {
		if got.Runes[i-1] >= got.Runes[i] {
			t.Fatalf("码位未升序去重：%U >= %U", got.Runes[i-1], got.Runes[i])
		}
	}
	if got.Sources["title"] == 0 || got.Sources["subtitles"] == 0 {
		t.Fatalf("sources 未记录来源：%v", got.Sources)
	}
}

func TestCollectFontCharsetDropsControls(t *testing.T) {
	got := CollectFontCharset("", []string{"a\nb\tc\rd\x00e"}, nil)
	for _, r := range got.Runes {
		switch r {
		case '\n', '\r', '\t', 0:
			t.Fatalf("控制字符 %U 不应进字集", r)
		}
	}
	// 空格必须留下：它不出轮廓，但决定词距与断行（见 charset.txt 的注释）。
	if countRune(got.Runes, ' ') != 1 {
		t.Fatalf("空格丢失：%U", got.Runes)
	}
	// 全角空格同理。
	got2 := CollectFontCharset("　", nil, nil)
	if countRune(got2.Runes, 0x3000) != 1 {
		t.Fatalf("U+3000 丢失：%U", got2.Runes)
	}
}

// 播放器脚本里的 UI 文案必须自动进字集。
//
// 这条用例的价值不在于「今天收全了」，而在于**以后不会漏**：app.js 新增一句
// 「网络不稳定，已暂停」时，若字集靠人工列表维护，字体就会缺这几个字——
// 而缺字在浏览器里的表现是方框，不是报错，没人会立刻发现。
func TestPlayerScriptTextInCharset(t *testing.T) {
	js, err := assets.AppJS()
	if err != nil {
		t.Fatalf("AppJS: %v", err)
	}
	got := CollectFontCharset("", nil, js)
	set := make(map[rune]struct{}, len(got.Runes))
	for _, r := range got.Runes {
		set[r] = struct{}{}
	}
	// 逐个核对脚本里实际出现的每个非 ASCII 字符都在集合里。
	for _, r := range string(js) {
		if r < 0x80 {
			continue
		}
		if _, ok := set[r]; !ok {
			t.Fatalf("app.js 里的字符 %q(%U) 没进字集", r, r)
		}
	}
}

// 模板里写死的提示文案必须被 bundleFixedText 覆盖。
//
// 直接拿渲染产物比对，而不是比对模板源码：模板里的中文会经过 html/template 转义，
// 比对源码只能证明「这些字在文件里」，比对产物才能证明「这些字会显示出来」。
func TestTemplateTextCoveredByCharset(t *testing.T) {
	title := "季度复盘"
	slides := []SlideEntry{
		{SlideID: "s1", Image: "slides/001.png"},
		{SlideID: "s2", Image: "slides/002.png"}, // 无讲稿 → 触发「（本页无讲稿）」
	}
	subtitles := []media.SubtitleCue{{SlideID: "s1", Text: "第一页讲数据"}}
	html, err := renderIndexHTML(title, slides, subtitles, false, FileAudioMix)
	if err != nil {
		t.Fatalf("renderIndexHTML: %v", err)
	}
	js, err := assets.AppJS()
	if err != nil {
		t.Fatalf("AppJS: %v", err)
	}
	charset := CollectFontCharset(title, []string{"第一页讲数据"}, js)
	set := make(map[rune]struct{}, len(charset.Runes))
	for _, r := range charset.Runes {
		set[r] = struct{}{}
	}
	// 只查非 ASCII：ASCII 由字体必然收录，且 CSP/标签里全是 ASCII，逐字断言噪声太大。
	for _, r := range string(html) {
		if r < 0x80 {
			continue
		}
		if _, ok := set[r]; !ok {
			t.Fatalf("index.html 里的字符 %q(%U) 没进字集；"+
				"模板改了文案就要同步 bundleFixedText", r, r)
		}
	}
}

// 反过来查「多收」：bundleFixedText 里的每个非 ASCII 字符都必须真的会显示。
//
// 多收不像漏收那样出方框，它的代价是字集虚大——子集被迫带上永远用不到的字形，
// 而且它会掩盖漏收：字集里本来就有这个字，扫 app.js 时漏了也看不出来。
// 第一版常量凭空列了「加载失败 / 退出全屏 / 静音」，播放器里根本没有这些文案。
func TestBundleFixedTextHasNoPhantomCharacters(t *testing.T) {
	slides := []SlideEntry{{SlideID: "s1", Image: "a.png"}}
	// 刻意留一个无讲稿的页：模板里的「（本页无讲稿）」只有那个分支才会渲染出来。
	// 混音轨名按实际值传入：禁 JS 提示里的「同目录 …」是条件分支，传空会让那段
	// 文案整段消失，于是字集里那几个字被误判成虚增。
	html, err := renderIndexHTML("季度复盘", slides, nil, false, FileAudioMix)
	if err != nil {
		t.Fatalf("renderIndexHTML: %v", err)
	}
	js, err := assets.AppJS()
	if err != nil {
		t.Fatalf("AppJS: %v", err)
	}
	visible := string(html) + string(js)
	for _, r := range bundleFixedText {
		if r < 0x80 {
			continue
		}
		if !strings.ContainsRune(visible, r) {
			t.Errorf("固定文案里的 %q(%U) 在模板与播放器里都不出现——这是虚增的字集", r, r)
		}
	}
}

func TestCodepointsMatchesRunes(t *testing.T) {
	c := CollectFontCharset("AB", nil, nil)
	cp := c.Codepoints()
	if len(cp) != len(c.Runes) {
		t.Fatalf("len(cp)=%d, len(runes)=%d", len(cp), len(c.Runes))
	}
	for i := range cp {
		if uint32(c.Runes[i]) != cp[i] {
			t.Fatalf("cp[%d]=%U, want %U", i, cp[i], c.Runes[i])
		}
	}
	if c.String() != string(c.Runes) {
		t.Fatalf("String() 与 Runes 不一致")
	}
}

func countRune(rs []rune, want rune) int {
	n := 0
	for _, r := range rs {
		if r == want {
			n++
		}
	}
	return n
}
