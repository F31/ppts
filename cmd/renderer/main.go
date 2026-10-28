// Command renderer 是独立的动画渲染服务（L4）。
//
// 它与 API/worker 分离部署（设计方案 §8.1）：Chromium/Remotion 的内存峰值与崩溃域
// 都不该混进业务进程。本进程只做三件事：校验计划、按 planHash 去重、调 backend 出片段。
//
// 当前内置 backend 是 stub（写占位片段），真实光栅 backend 需要容器镜像与 Node 运行时，
// 通过同样的 Backend 接口接入，本文件不需要改。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/render"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "renderer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr       = flag.String("addr", ":8090", "监听地址")
		workDir    = flag.String("workdir", "", "产物工作目录（必填）")
		backend    = flag.String("backend", "stub", "backend：stub")
		version    = flag.String("version", "dev", "服务版本（参与 planHash）")
		fonts      = flag.String("fonts", "", "逗号分隔的可用字体族，格式 family@version")
		concurrent = flag.Int("concurrency", 2, "同时渲染任务数")
		maxPlan    = flag.Int64("max-plan-bytes", render.MaxPlanBytes, "请求体上限（字节）")
	)
	flag.Parse()

	if *workDir == "" {
		return errors.New("-workdir 必填：产物必须落在明确的目录里")
	}
	var impl render.Backend
	switch *backend {
	case "stub":
		impl = &render.StubBackend{}
	default:
		return fmt.Errorf("未知 backend %q（当前只内置 stub；真实光栅 backend 随镜像接入）", *backend)
	}

	svc, err := render.NewService(render.ServiceConfig{
		Backend:       impl,
		WorkDir:       *workDir,
		Version:       *version,
		Fonts:         parseFonts(*fonts),
		MaxConcurrent: *concurrent,
		MaxPlanBytes:  *maxPlan,
	})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: svc.Handler(),
		// 提交本身很快（只做校验与入队），渲染在后台进行，因此读超时可以短。
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("renderer %s listening on %s (backend=%s, workdir=%s)", *version, *addr, impl.Name(), *workDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Print("renderer: 收到退出信号，等待进行中的请求结束")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// parseFonts 解析 family@version 列表。字体版本参与 planHash：
// 字体换了，同一份计划的像素就变了，必须重渲。
func parseFonts(s string) []render.Font {
	if s == "" {
		return nil
	}
	var out []render.Font
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			token := s[start:i]
			start = i + 1
			if token == "" {
				continue
			}
			family, version := token, ""
			for j := 0; j < len(token); j++ {
				if token[j] == '@' {
					family, version = token[:j], token[j+1:]
					break
				}
			}
			out = append(out, render.Font{Family: family, Version: version})
		}
	}
	return out
}
