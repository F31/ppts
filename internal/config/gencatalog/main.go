// 把登记表写成安装模板和全量清单。
//
//	go generate ./internal/config/
package main

import (
	"os"
	"path/filepath"

	"github.com/F31/ppts/internal/config"
)

func main() {
	root := filepath.Join("..", "..")
	write(filepath.Join(root, "packaging", "config.env.example"), config.InstallExample())
	write(filepath.Join(root, "packaging", "config.md"), config.CatalogMarkdown())
}

func write(path, body string) {
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		panic(err)
	}
}
