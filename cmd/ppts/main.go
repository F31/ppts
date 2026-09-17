// ppts 单一入口（单二进制发行）：
//
//	ppts server    API + 内嵌前端控制台（缺省命令）
//	ppts worker    任务执行器（解析/讲稿/配音/导出）
//	ppts migrate   幂等应用内嵌 SQL 迁移
//	ppts version   打印版本
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/F31/ppts/internal/migrate"
	"github.com/F31/ppts/migrations"
)

// version 由发布构建经 -ldflags "-X main.version=..." 注入；源码构建为 dev。
var version = "dev"

func main() {
	log.SetFlags(0)
	// 无子命令时默认 server，保留原 ppts-api「直接跑即起服务」的行为。
	cmd := "server"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "server":
		err = runServer()
	case "worker":
		err = runWorker()
	case "migrate":
		err = runMigrate()
	case "version", "-v", "--version":
		fmt.Printf("ppts %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "ppts: unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `ppts — PPT 智能讲解平台（单二进制发行）

用法: ppts [命令]

命令:
  server    启动 API + 前端控制台（缺省命令；配置见 packaging/config.env.example）
  worker    启动任务 worker（依赖 ffmpeg、LibreOffice、poppler、中文字体）
  migrate   幂等应用数据库迁移（superuser DSN：PPTS_MIGRATE_DATABASE_URL，回退 PPTS_DATABASE_URL）
  version   打印版本
`)
}

// runMigrate 应用全部内嵌 SQL 迁移（幂等）。迁移含建角色/扩展，需要 superuser 连接：
// 优先取 PPTS_MIGRATE_DATABASE_URL，回退 PPTS_DATABASE_URL。
func runMigrate() error {
	dsn := os.Getenv("PPTS_MIGRATE_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("PPTS_DATABASE_URL")
	}
	if dsn == "" {
		return errors.New("PPTS_MIGRATE_DATABASE_URL or PPTS_DATABASE_URL is required")
	}
	applied, err := migrate.Apply(context.Background(), dsn, migrations.FS)
	if err != nil {
		return err
	}
	for _, name := range applied {
		log.Printf("migrate: applied %s", name)
	}
	log.Printf("migrate: ok, %d applied, schema up to date", len(applied))
	return nil
}
