// Command design-gallery 生成视觉基线预览页（单文件 HTML，零外链）。
//
// 用法：
//
//	go run ./cmd/design-gallery                    # 输出到 dist/design-gallery.html
//	go run ./cmd/design-gallery -out /tmp/x.html
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/F31/ppts/internal/design"
)

func main() {
	out := flag.String("out", filepath.Join("dist", "design-gallery.html"), "输出文件路径")
	flag.Parse()

	if err := design.WriteGallery(*out); err != nil {
		fmt.Fprintf(os.Stderr, "生成失败: %v\n", err)
		os.Exit(1)
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		abs = *out
	}
	fmt.Printf("已生成 %s\n", abs)
}
