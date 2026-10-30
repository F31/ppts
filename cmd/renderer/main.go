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
		assetDir   = flag.String("assets-dir", "", "底图只读根目录（计划里的 baseImage 相对它解析；空 = 不启用）")
		backend    = flag.String("backend", "stub", "backend：stub | remotion")
		version    = flag.String("version", "dev", "服务版本（参与 planHash；remotion 后端建议写成 remotion@x|chrome@y）")
		fonts      = flag.String("fonts", "", "逗号分隔的可用字体族，格式 family@version")
		concurrent = flag.Int("concurrency", 2, "同时渲染任务数")
		maxQueued  = flag.Int("max-queued", 8, "排队任务数上限（超出返回 429 + Retry-After）")
		jobTTL     = flag.Duration("job-ttl", 30*time.Minute, "终态任务与产物的保留时长")
		sweepEvery = flag.Duration("retention-sweep", time.Minute, "过期任务回收的最小间隔")
		maxPlan    = flag.Int64("max-plan-bytes", render.MaxPlanBytes, "请求体上限（字节）")
		// 默认清扫：单副本部署下重启就该把上一轮的孤儿目录删掉（tasks 3.14）。
		keepOrphans = flag.Bool("keep-orphans", false, "保留上次进程留在 workdir 的 job 目录（多副本共享 workdir 时必须打开）")
		scriptPath  = flag.String("renderer-script", "", "renderer/render.mjs 路径（默认取 PPTS_RENDERER_SCRIPT）")
		chromePath  = flag.String("chrome", "", "chrome-headless-shell 路径（默认取 PPTS_CHROME）")
		renderTO    = flag.Duration("render-timeout", 15*time.Minute, "单次渲染的保底超时（按片长自动放宽，上限 6h）")
		selftest    = flag.Bool("selftest", false, "跑一遍后端自检后退出：staging → 渲染 2 页 → 校验产物")
		// 故障注入：让指定页在第一次渲染时失败，用来验收「单页失败只降级该页」（tasks 3.7）。
		// 真实渲染里没有可靠的造法——页面内容合法就不会失败，而这条路径回归时
		// 用户看到的只是「这页没动画」，不会报错。
		selftestFaultPage = flag.Int("selftest-degrade-page", -1, "自检时让第 N 页渲染失败，验证降级成静态画面（0 基；-1 = 不注入）")
		// 自检产物默认随成功删除。要拿成片做帧比对（例如验证降级页确实是静态画面）时打开它，
		// 路径会打印在输出里——否则只能靠失败来保留现场。
		selftestKeep = flag.Bool("selftest-keep", false, "自检成功后保留产物目录（用于取片核对）")
		// 片段缓存（M5）：目录共享即多副本共享命中。空 = 关闭（每页都渲）。
		segCacheDir = flag.String("segment-cache", "", "片段缓存目录（空 = 关闭缓存）")
		segCacheMax = flag.Int64("segment-cache-max-bytes", 4<<30, "片段缓存容量上限（字节；0 = 不限）")
		// 页级并发：总渲染位 = 页并发 × 帧并发，内存按乘积算（ADR-16）。
		pageConcurrency = flag.Int("page-concurrency", 1, "同时渲染的页数（1 = 串行）")
	)
	flag.Parse()

	if *workDir == "" {
		return errors.New("-workdir 必填：产物必须落在明确的目录里")
	}
	var impl render.Backend
	switch *backend {
	case "stub":
		impl = &render.StubBackend{}
	case "remotion":
		impl = &render.RemotionBackend{
			Script:          *scriptPath,
			Chrome:          *chromePath,
			Concurrency:     *concurrent,
			PageConcurrency: *pageConcurrency,
			Timeout:         *renderTO,
			Logf: func(format string, args ...any) {
				log.Printf(format, args...)
			},
		}
	default:
		return fmt.Errorf("未知 backend %q（可选 stub | remotion）", *backend)
	}

	// 自检在服务启动之前跑完并退出：镜像构建/部署流水线拿退出码当门禁，
	// 而不是「容器起来了」就当渲染可用。
	if *selftest {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return runSelfTest(ctx, impl, *workDir, *selftestFaultPage, *selftestKeep)
	}

	// 片段缓存是可选组件：配错目录不该让服务起不来（缓存没有能力让渲染失败），
	// 但必须吼一声——「以为开了缓存其实没开」是性能问题里最难发现的一种。
	var segCache render.SegmentCache
	if *segCacheDir != "" {
		c, cerr := render.NewDirSegmentCache(*segCacheDir, *segCacheMax)
		if cerr != nil {
			log.Printf("segment cache disabled: %v", cerr)
		} else {
			segCache = c
			log.Printf("segment cache: %s (max %d bytes)", c.Root(), *segCacheMax)
		}
	}

	svc, err := render.NewService(render.ServiceConfig{
		Backend:        impl,
		WorkDir:        *workDir,
		AssetDir:       *assetDir,
		Version:        *version,
		Fonts:          parseFonts(*fonts),
		MaxConcurrent:  *concurrent,
		MaxQueued:      *maxQueued,
		JobTTL:         *jobTTL,
		RetentionSweep: *sweepEvery,
		MaxPlanBytes:   *maxPlan,
		KeepOrphans:    *keepOrphans,
		SegmentCache:   segCache,
		Logf: func(format string, args ...any) {
			log.Printf(format, args...)
		},
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
