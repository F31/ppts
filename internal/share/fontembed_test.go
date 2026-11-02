package share

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/share/assets"
)

// minimalFont 造一个只含 cmap(format 12) 的最小 SFNT，充当「内嵌字体」。
// 打包侧的覆盖率校验只读 cmap，不需要 glyf/CFF/head 这些表。
func minimalFont(cps ...rune) []byte {
	groups := make([][3]uint32, 0, len(cps))
	for i, cp := range cps {
		groups = append(groups, [3]uint32{uint32(cp), uint32(cp), uint32(i + 1)})
	}
	f12 := make([]byte, 16+len(groups)*12)
	binary.BigEndian.PutUint16(f12[0:], 12)
	binary.BigEndian.PutUint32(f12[4:], uint32(len(f12)))
	binary.BigEndian.PutUint32(f12[12:], uint32(len(groups)))
	for i, g := range groups {
		at := 16 + i*12
		binary.BigEndian.PutUint32(f12[at:], g[0])
		binary.BigEndian.PutUint32(f12[at+4:], g[1])
		binary.BigEndian.PutUint32(f12[at+8:], g[2])
	}

	cmap := make([]byte, 12)
	binary.BigEndian.PutUint16(cmap[2:], 1)
	binary.BigEndian.PutUint16(cmap[4:], 3)
	binary.BigEndian.PutUint16(cmap[6:], 10)
	binary.BigEndian.PutUint32(cmap[8:], 12)
	cmap = append(cmap, f12...)

	buf := make([]byte, 12+16+len(cmap))
	binary.BigEndian.PutUint32(buf[0:], 0x00010000)
	binary.BigEndian.PutUint16(buf[4:], 1)
	copy(buf[12:16], "cmap")
	binary.BigEndian.PutUint32(buf[20:], 28)
	binary.BigEndian.PutUint32(buf[24:], uint32(len(cmap)))
	copy(buf[28:], cmap)
	return buf
}

// fontCoveringBundle 造一份「恰好覆盖该包全部文本」的字体。
//
// 用包的标题与字幕 + 真实 app.js 来算字集，而不是写死一串字：写死的话
// 播放器文案一改，测试就会因为「字体缺字」而红——那时红的是测试数据，
// 不是产品缺陷，会训练人忽略这条用例。
func fontCoveringBundle(t *testing.T, title string, subtitles []string) FontAsset {
	t.Helper()
	charset := CollectFontCharset(title, subtitles, mustAppJS(t))
	return FontAsset{
		Family:  "Noto Sans CJK SC",
		Format:  "otf",
		Version: "2.004-subdeadbeef",
		Data:    minimalFont(charset.Runes...),
	}
}

func TestBundleEmbedsFont(t *testing.T) {
	input := buildInput()
	font := fontCoveringBundle(t, input.Title, subtitleTexts(input))
	input.Font = &font

	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), input, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	if _, ok := files[fontEntryPath]; !ok {
		t.Fatalf("包内没有字体文件 %s（有 %v）", fontEntryPath, keys(files))
	}
	css, ok := files[FileFontCSS]
	if !ok {
		t.Fatal("包内没有 fonts.css")
	}
	for _, want := range []string{`font-family:"PPTS Text"`, fontEntryPath, `format("opentype")`} {
		if !strings.Contains(string(css), want) {
			t.Errorf("fonts.css 缺 %q：%s", want, css)
		}
	}
	if len(manifest.Fonts) != 1 || manifest.Fonts[0] != "PPTS Text" {
		t.Fatalf("manifest.Fonts = %v, want [PPTS Text]", manifest.Fonts)
	}
	if len(manifest.FontMissing) != 0 {
		t.Fatalf("fontMissing = %v, want 空", manifest.FontMissing)
	}

	index := string(files[FileIndexHTML])
	if !strings.Contains(index, `href="fonts.css"`) {
		t.Fatal("index.html 没有引用 fonts.css")
	}
	// fonts.css 必须在 app.css 之前：字体声明先于使用它的样式表。
	if a, b := strings.Index(index, "fonts.css"), strings.Index(index, "app.css"); a < 0 || b < 0 || a > b {
		t.Fatalf("fonts.css 未排在 app.css 之前：%d vs %d", a, b)
	}
}

// CSP 里如果漏了 font-src，内嵌字体会被浏览器直接拦掉——页面不报错、只是回退到
// 系统字体。这条用例锁住那一行：功能做了却被自己的安全策略封掉，是最难发现的一类。
func TestIndexCSPAllowsFonts(t *testing.T) {
	if !strings.Contains(indexCSP, "font-src 'self' file:") {
		t.Fatalf("CSP 没有放行字体：%s", indexCSP)
	}
}

func TestBundleWithoutFontReferencesNoFontCSS(t *testing.T) {
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), buildInput(), &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if _, ok := files[FileFontCSS]; ok {
		t.Fatal("没有内嵌字体却写了 fonts.css")
	}
	if _, ok := files[fontEntryPath]; ok {
		t.Fatal("没有内嵌字体却写了字体文件")
	}
	if len(manifest.Fonts) != 0 {
		t.Fatalf("manifest.Fonts = %v, want 空", manifest.Fonts)
	}
	if strings.Contains(string(files[FileIndexHTML]), "fonts.css") {
		t.Fatal("index.html 引用了不存在的 fonts.css")
	}
}

// 覆盖不全时：打包照常成功（生僻字走系统回退是既定设计），但必须把缺的字
// 写进清单。如果这里静默通过，「字体子集覆盖率 100%」就成了一句没有证据的声明。
func TestBundleRecordsFontMissingCharacters(t *testing.T) {
	input := buildInput()
	full := fontCoveringBundle(t, input.Title, subtitleTexts(input))
	charset := CollectFontCharset(input.Title, subtitleTexts(input), mustAppJS(t))

	// 去掉第一个字，制造一个必然的缺口。
	dropped := charset.Runes[0]
	shrunk := make([]rune, 0, len(charset.Runes)-1)
	for _, r := range charset.Runes {
		if r != dropped {
			shrunk = append(shrunk, r)
		}
	}
	full.Data = minimalFont(shrunk...)
	input.Font = &full

	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), input, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(manifest.FontMissing) != 1 || manifest.FontMissing[0] != string(dropped) {
		t.Fatalf("fontMissing = %v, want [%q]", manifest.FontMissing, dropped)
	}
	if len(manifest.Fonts) != 1 {
		t.Fatalf("manifest.Fonts = %v（缺字不该导致字体不被记录）", manifest.Fonts)
	}
}

// 字体字节不是字体时必须在打包期报错，而不是写进包让用户面对「有字体还是方框」。
func TestBundleRejectsUnparsableFont(t *testing.T) {
	input := buildInput()
	input.Font = &FontAsset{Format: "otf", Data: []byte("not a font at all")}
	var buf bytes.Buffer
	if _, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), input, &buf); err == nil {
		t.Fatal("坏字体却打包成功了")
	}
}

func TestFontCSSRejectsUnknownFormat(t *testing.T) {
	if _, err := renderFontCSS(FontAsset{Format: "woff3", Data: []byte("x")}); err == nil {
		t.Fatal("未知格式却生成了 CSS")
	}
}

func mustAppJS(t *testing.T) []byte {
	t.Helper()
	js, err := assets.AppJS()
	if err != nil {
		t.Fatalf("AppJS: %v", err)
	}
	return js
}
