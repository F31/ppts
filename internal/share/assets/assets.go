// Package assets 内嵌分享成品播放器的构建产物。
//
// app.js / app.css 由 `npm run build:share`（web/vite.share.config.ts）生成并**提交入库**，
// 目的是让 `go build` 不依赖 Node 工具链。CI 会重跑构建并 `git diff --exit-code`，
// 生成物与源码漂移即失败（与 catalog 生成物的纪律一致）。
package assets

import (
	"embed"
	"io/fs"
)

//go:embed app.js app.css
var files embed.FS

// FS 返回播放器资源根（含 app.js、app.css）。
func FS() fs.FS {
	return files
}

// AppJS 返回播放器脚本字节。
func AppJS() ([]byte, error) {
	return files.ReadFile("app.js")
}

// AppCSS 返回播放器样式字节。
func AppCSS() ([]byte, error) {
	return files.ReadFile("app.css")
}
