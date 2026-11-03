package share

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 单文件形态（ADR-029 ⑤）只做一件事：把目录包的同一批条目内联。
// 因此这一组用例守的不是"单文件能播"（那由目录包的门禁间接保证），
// 而是三条容易悄悄坏掉的等价关系：
//   1. 条目集合同源（内联不多不少）；
//   2. 没有外链、没有残留路径（断网双击的唯一前提）；
//   3. 清单与目录包逐字节一致（同一个 buildInto 产出）。

func buildBundleZip(t *testing.T) ([]byte, *Manifest) {
	t.Helper()
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), buildInput(), &buf)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	return buf.Bytes(), manifest
}

func TestBuildSingleInlinesEveryResource(t *testing.T) {
	single, manifest := mustBuildSingle(t)
	html := string(single)

	for _, want := range []string{
		"data:image/",            // 页图（打包器会按体积转 jpg，MIME 随之改变）
		"data:audio/wav;base64,", // 音频（段与混音）
		"<style>",                // app.css 内联
		"<script>",               // app.js / manifest.js / subtitles.js 内联
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("单文件缺少 %q", want)
		}
	}
	// 目录包里的路径一个都不能留：留下就是断链（单文件没有"同目录"这回事）。
	for _, name := range []string{`src="slides/`, `src="audio/`, `href="slides/`} {
		if strings.Contains(html, name) {
			t.Fatalf("单文件仍按相对路径引用资源: %q", name)
		}
	}
	// 外部文件引用必须全部消失：样式与脚本都已内联。
	if strings.Contains(html, "<script src=") || strings.Contains(html, "<link rel=\"stylesheet\"") {
		t.Fatal("单文件仍以标签引用外部脚本或样式")
	}
	if refs := ScanExternalRefs(FileIndexHTML, single); len(refs) > 0 {
		t.Fatalf("单文件存在外链: %v", refs)
	}
	if len(manifest.Slides) != 2 {
		t.Fatalf("manifest slides = %d, want 2", len(manifest.Slides))
	}
}

func TestBuildSingleRelaxesCSPButKeepsNetworkClosed(t *testing.T) {
	single, _ := mustBuildSingle(t)
	html := string(single)

	if !strings.Contains(html, "script-src 'unsafe-inline'") {
		t.Fatal("单文件必须允许内联脚本，否则整个播放器不执行")
	}
	if strings.Contains(html, "script-src 'self'") {
		t.Fatal("单文件的 CSP 不应再声明 self（文件不存在，声明白写）")
	}
	// 放宽的只是"内联"，网络来源仍然全封——这是"断网能播"的判据。
	if !strings.Contains(html, "default-src 'none'") {
		t.Fatal("单文件必须保留 default-src 'none'")
	}
	if !strings.Contains(html, "img-src data:") || !strings.Contains(html, "media-src data:") {
		t.Fatal("图片与音频必须显式允许 data:")
	}
}

func TestBuildSingleManifestMatchesZip(t *testing.T) {
	_, zipManifest := buildBundleZip(t)
	_, singleManifest := mustBuildSingle(t)
	// 同一个 buildInto：若两边的清单出现差异，说明单文件走了另一条组装路径。
	a, err := json.Marshal(zipManifest)
	if err != nil {
		t.Fatalf("marshal zip manifest: %v", err)
	}
	b, err := json.Marshal(singleManifest)
	if err != nil {
		t.Fatalf("marshal single manifest: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("单文件与目录包的清单不一致:\nzip=%s\nsingle=%s", a, b)
	}
}

func TestDataURIMimeIsExplicit(t *testing.T) {
	if _, err := dataURI("audio/mix.wav", []byte("x")); err != nil {
		t.Fatalf("wav 应可内联: %v", err)
	}
	if _, err := dataURI("slides/001.png", []byte("x")); err != nil {
		t.Fatalf("png 应可内联: %v", err)
	}
	// 未知扩展名必须报错：编成 application/octet-stream 也能"生成成功"，
	// 但浏览器会静默跳过（字体/音频尤其），现场只表现为"没声音/没字体"。
	if _, err := dataURI("assets/blob.bin", []byte("x")); err == nil {
		t.Fatal("未知扩展名应报错，而不是静默套一个默认 MIME")
	}
}

func mustBuildSingle(t *testing.T) ([]byte, *Manifest) {
	t.Helper()
	out, manifest, err := NewBundleBuilder(baseReader(t), nil).BuildSingle(context.Background(), buildInput())
	if err != nil {
		t.Fatalf("build single: %v", err)
	}
	return out, manifest
}

// 文本复制（ADR-029 ⑥）：开关写进清单，且两种形态写的是同一个值。
func TestManifestCarriesTextCopy(t *testing.T) {
	ctx := context.Background()

	in := buildInput()
	in.TextCopy = true
	var buf bytes.Buffer
	m, err := NewBundleBuilder(baseReader(t), nil).Build(ctx, in, &buf)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !m.TextCopy {
		t.Fatal("开启时清单没有 textCopy")
	}

	single, mSingle, err := NewBundleBuilder(baseReader(t), nil).BuildSingle(ctx, in)
	if err != nil {
		t.Fatalf("build single: %v", err)
	}
	if !mSingle.TextCopy || !bytes.Contains(single, []byte(`"textCopy":true`)) {
		t.Fatal("单文件形态没有带上 textCopy")
	}

	// 关闭时必须整个键都不出现：默认行为是"不允许把讲稿原文带走"，
	// 而字段存在与否也是旧包（没有这个键）与新包行为一致的前提。
	in.TextCopy = false
	var buf2 bytes.Buffer
	m2, err := NewBundleBuilder(baseReader(t), nil).Build(ctx, in, &buf2)
	if err != nil {
		t.Fatalf("build off: %v", err)
	}
	if m2.TextCopy {
		t.Fatal("默认关闭时清单不应标记 textCopy")
	}
	if bytes.Contains(buf2.Bytes(), []byte("textCopy")) {
		t.Fatal("关闭时产物里不应出现 textCopy 字样（含压缩前的清单文本）")
	}
}
