// Package migrations 内嵌全部 SQL 迁移脚本，供 ppts migrate 子命令
// 在二进制部署形态下离线执行（不再依赖源码目录）。
//
// 两套方言：
//   - FS：PostgreSQL（migrations/*.sql），含 RLS/角色/SECURITY DEFINER 函数；
//   - SQLite()：SQLite 单租户精简 schema（migrations/sqlite/*.sql），无 RLS/角色/函数。
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var FS embed.FS

//go:embed sqlite/*.sql
var sqliteFS embed.FS

// SQLite 返回以 sqlite/ 为根的 SQLite 迁移文件系统（供 sqlite 迁移器 glob *.sql）。
func SQLite() (fs.FS, error) {
	return fs.Sub(sqliteFS, "sqlite")
}
