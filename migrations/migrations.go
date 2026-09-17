// Package migrations 内嵌全部 SQL 迁移脚本，供 ppts-api migrate 子命令
// 在二进制部署形态下离线执行（不再依赖源码目录）。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
