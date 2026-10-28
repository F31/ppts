package design

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGalleryIsSelfContained(t *testing.T) {
	data, err := BuildGallery()
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	html := string(data)
	forbidden := map[string]string{
		"http://":       "外部链接（file:// 下会被拦）",
		"https://":      "外部链接",
		"<link":         "外部样式表",
		`type="module"`: "ES module（file:// 下被 CORS 拦）",
		"@import":       "外部字体导入",
		"fetch(":        "网络请求",
	}
	for token, why := range forbidden {
		if strings.Contains(html, token) {
			t.Errorf("预览页含 %q（%s）", token, why)
		}
	}
	for _, must := range []string{"<!doctype html>", "<style>", "cubic-bezier(", "成片视觉基线"} {
		if !strings.Contains(html, must) {
			t.Errorf("预览页缺少 %q", must)
		}
	}
}

func TestGalleryCoversAllArchetypesAndEasings(t *testing.T) {
	data, err := BuildGallery()
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	html := string(data)
	list, err := Archetypes()
	if err != nil {
		t.Fatalf("原型解析失败: %v", err)
	}
	for _, a := range list {
		if !strings.Contains(html, a.ID) {
			t.Errorf("预览页缺少版式 %s", a.ID)
		}
	}
	for _, e := range DefaultMotion().Easings {
		if !strings.Contains(html, e.Name) {
			t.Errorf("预览页缺少缓动 %s", e.Name)
		}
	}
	// 度量表必须列出全部规则编号。
	for _, code := range []string{RuleFontSize, RuleContrast, RuleElementCount, RuleWhitespace, RuleDensity, RuleBeat} {
		if !strings.Contains(html, code) {
			t.Errorf("预览页缺少度量规则 %s", code)
		}
	}
}

func TestWriteGallery(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "gallery.html")
	if err := WriteGallery(p); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("文件不存在: %v", err)
	}
	if info.Size() < 4096 {
		t.Errorf("文件只有 %d 字节，明显不完整", info.Size())
	}
}
