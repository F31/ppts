package share

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestScanExternalRefsClassifiesTargets(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"相对页图", `<img src="slides/001.jpg">`, nil},
		{"相对样式", `<link rel="stylesheet" href="app.css">`, nil},
		{"https", `<script src="https://cdn.example.com/x.js"></script>`, []string{"https://cdn.example.com/x.js"}},
		{"http", `<img src="http://cdn/x.png">`, []string{"http://cdn/x.png"}},
		// 协议相对 URL 写起来像相对路径，是最容易被漏的一种外链。
		{"协议相对", `<img src="//cdn.example.com/x.png">`, []string{"//cdn.example.com/x.png"}},
		{"file 绝对", `<img src="file:///tmp/x.png">`, []string{"file:///tmp/x.png"}},
		{"根绝对", `<img src="/static/x.png">`, []string{"/static/x.png"}},
		{"css url", `body{background:url(https://cdn/bg.png)}`, []string{"https://cdn/bg.png"}},
		{"css url 引号", `body{background:url("//cdn/bg.png")}`, []string{"//cdn/bg.png"}},
		{"import", `@import "https://fonts.example.com/x.css";`, []string{"https://fonts.example.com/x.css"}},
		// data: 是内联，不发请求；片段引用连请求都没有。
		{"data uri", `body{background:url(data:image/png;base64,AA)}`, nil},
		{"片段", `<a href="#top">回到顶部</a>`, nil},
		// 字符串字面量不是引用：把 JS 里的 URL 当外链会造成大面积误报。
		{"js 字符串", `var u = "https://example.com";`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanExternalRefs("index.html", []byte(tc.body))
			if len(got) != len(tc.want) {
				t.Fatalf("refs = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("refs[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// 二进制条目不进正则：随机字节里凑出 `src=` 的概率不为零，误报比漏报更难查。
func TestScanExternalRefsSkipsBinary(t *testing.T) {
	if refs := ScanExternalRefs("slides/001.png", []byte(`src="https://x/y"`)); len(refs) != 0 {
		t.Fatalf("binary entry scanned: %v", refs)
	}
}

// 同一片段出现多次只报一次：报错信息要能一眼看完，不是用来统计重复的。
func TestScanExternalRefsDeduplicates(t *testing.T) {
	body := `<script src="https://cdn/x.js"></script><script src="https://cdn/x.js"></script>`
	if refs := ScanExternalRefs("index.html", []byte(body)); len(refs) != 1 {
		t.Fatalf("refs = %v, want 1", refs)
	}
}

// 打包时就该失败：坏包一旦产出，后面所有门禁都只是在描述它有多坏。
func TestBuildRejectsExternalReference(t *testing.T) {
	reader := baseReader(t)
	reader.objects["tenants/t1/projects/p1/subs/cues.vtt"] = []byte(
		"WEBVTT\n\n00:00:00.000 --> 00:00:01.000\n" + `<img src="https://cdn/x.png">` + "\n")

	var buf bytes.Buffer
	_, err := NewBundleBuilder(reader, nil).Build(context.Background(), buildInput(), &buf)
	if err == nil {
		t.Fatal("Build succeeded with an external reference in the bundle")
	}
	if !strings.Contains(err.Error(), "https://cdn/x.png") {
		t.Fatalf("error should name the offending ref, got %v", err)
	}
	if buf.Len() != 0 && !strings.Contains(err.Error(), "external") {
		t.Fatalf("unexpected error: %v", err)
	}
}
