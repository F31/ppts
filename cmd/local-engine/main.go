package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/F31/ppts/internal/sidecar"
)

// local-engine 是桌面 Go sidecar（V4.0 §11.5）。由 Tauri Rust 进程作为子进程启动，
// 通过标准输入/输出上的有界 NDJSON 协议通信；不接受网络连接。
// 当前实现握手/能力/心跳；G4 起注册解析、渲染、导出等业务方法。
func main() {
	log.SetFlags(0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := sidecar.NewEngine().Serve(ctx, os.Stdin, os.Stdout); err != nil {
		log.Printf("local-engine: %v", err)
		os.Exit(1)
	}
}
