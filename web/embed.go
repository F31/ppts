// Package web 内嵌前端构建产物（web/dist），支撑单二进制分发：
// api 在 PPTS_WEB_ROOT 未设置时直接服务内嵌的静态资源与 SPA 兜底。
package web

import (
	"embed"
	"io/fs"
)

// dist 目录由 `npm run build` 生成；仓库内仅保留 .gitkeep 占位，
// 保证未构建前端时 Go 编译仍然可用（此时内嵌的只有占位文件）。
//
//go:embed all:dist
var dist embed.FS

// DistFS 返回 vite 构建产物的根（web/dist 目录内容）。
func DistFS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: embedded dist missing: " + err.Error())
	}
	return sub
}
