package share

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
)

func noscriptFixture() ([]SlideEntry, []media.SubtitleCue) {
	slides := []SlideEntry{
		{SlideID: "slide-0", Image: "slides/001.jpg"},
		{SlideID: "slide-1", Image: "slides/002.png"},
	}
	subtitles := []media.SubtitleCue{
		{SlideID: "slide-0", Text: "第一页讲稿：季度营收同比 18%。"},
		{SlideID: "slide-0", Text: "第二句补充说明。"},
		{SlideID: "slide-1", Text: "第二页讲稿：成本结构变化。"},
	}
	return slides, subtitles
}

// 禁 JS 时没有人能补内容，所以 noscript 必须自带「逐页静态图 + 讲稿」。
// 只写一句"请启用 JavaScript"在 V17 的「可读」判据下是不合格的。
func TestIndexHTMLNoscriptCarriesPagesAndScript(t *testing.T) {
	slides, subtitles := noscriptFixture()
	data, err := renderIndexHTML("季度复盘", slides, subtitles)
	if err != nil {
		t.Fatalf("renderIndexHTML: %v", err)
	}
	html := string(data)

	if got := strings.Count(html, `<img class="ppts-noscript-image"`); got != len(slides) {
		t.Fatalf("noscript images = %d, want %d", got, len(slides))
	}
	// 页图名必须与清单一致，不能假设扩展名：有动画的页是挖空后的 .png 底板。
	for _, slide := range slides {
		if !strings.Contains(html, `src="`+slide.Image+`"`) {
			t.Fatalf("noscript missing image %q", slide.Image)
		}
	}
	// 同页多条字幕要合并到同一 figcaption，而不是只留第一条。
	if !strings.Contains(html, "第一页讲稿：季度营收同比 18%。 第二句补充说明。") {
		t.Fatalf("noscript lost merged subtitle for slide-0")
	}
	if !strings.Contains(html, "第二页讲稿：成本结构变化。") {
		t.Fatalf("noscript lost subtitle for slide-1")
	}
}

// 懒加载在禁 JS 时仍然生效：未进入视口的图不会请求，naturalWidth 为 0。
// 门禁「每张图都真的加载了」的判据会因此假红，所以模板里不能出现 loading="lazy"。
func TestIndexHTMLEagerLoadsNoscriptImages(t *testing.T) {
	slides, subtitles := noscriptFixture()
	data, err := renderIndexHTML("t", slides, subtitles)
	if err != nil {
		t.Fatalf("renderIndexHTML: %v", err)
	}
	if strings.Contains(string(data), `loading="lazy"`) {
		t.Fatal("noscript images must not be lazy: they would never load when JS is off")
	}
}

func TestIndexHTMLFallsBackWhenSlideHasNoScript(t *testing.T) {
	slides := []SlideEntry{{SlideID: "slide-0", Image: "slides/001.jpg"}}
	data, err := renderIndexHTML("t", slides, nil)
	if err != nil {
		t.Fatalf("renderIndexHTML: %v", err)
	}
	if !strings.Contains(string(data), "（本页无讲稿）") {
		t.Fatal("slide without subtitles should still render a placeholder line")
	}
}

// 标题是 PPT 里的不可信输入：必须被转义，不能因为进了 noscript 就漏掉。
func TestIndexHTMLEscapesTitleInsideNoscript(t *testing.T) {
	data, err := renderIndexHTML(`季度 <script>alert(1)</script>`, nil, nil)
	if err != nil {
		t.Fatalf("renderIndexHTML: %v", err)
	}
	html := string(data)
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("noscript title was not escaped")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("expected escaped title, got %q", html[:200])
	}
}
