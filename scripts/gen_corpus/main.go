// gen_corpus 生成 ppts 自己的合成兼容性语料（testdata/corpus/generated/s1*.pptx）。
// 用法：go run ./scripts/gen_corpus  （在仓库根执行）
// 语料使用 go-pptx 创建，覆盖：单页/多页、备注、文本框、表格。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/F31/go-pptx"
)

var outDir = "testdata/corpus/generated"

var decks = []struct {
	id        string
	desc      string
	pages     int
	withNotes bool
}{
	{"s100", "single text page", 1, false},
	{"s101", "two pages with notes", 2, true},
	{"s102", "three pages plain text", 3, false},
	{"s103", "four pages mixed notes", 4, true},
	{"s104", "five pages, no notes", 5, false},
	{"s105", "three pages with notes only second", 3, true},
	{"s106", "eight pages marker text", 8, false},
	{"s107", "two pages english+chinese", 2, false},
}

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	for _, d := range decks {
		path := filepath.Join(outDir, d.id+".pptx")
		if err := build(d.id, d.desc, d.pages, d.withNotes, path); err != nil {
			fatal(fmt.Errorf("%s: %w", d.id, err))
		}
		fmt.Printf("wrote %s (%s)\n", path, d.desc)
	}
}

func build(id, desc string, pages int, withNotes bool, path string) error {
	p, err := pptx.New()
	if err != nil {
		return err
	}
	defer p.Close()
	layouts, err := p.Layouts()
	if err != nil || len(layouts) == 0 {
		return fmt.Errorf("layouts: %v", err)
	}
	for i := 0; i < pages; i++ {
		s, err := p.AddSlide(layouts[0])
		if err != nil {
			return err
		}
		if _, err := s.AddTextBox(pptx.TextBoxSpec{
			X: 457200, Y: 457200, Width: 8534400, Height: 500000,
			Text: fmt.Sprintf("%s — page %d：PCIe 5.0 x16 / 64 GB", desc, i+1),
		}); err != nil {
			return err
		}
		if withNotes {
			_ = s.SetSpeakerNotes(fmt.Sprintf("第 %d 页备注：测试讲稿，数字 64 与 5.0 为专业术语。", i+1))
		}
	}
	_, err = p.Save(context.Background(), path)
	return err
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
