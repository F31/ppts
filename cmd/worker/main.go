package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/retention"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dsn := os.Getenv("PPTS_DATABASE_URL")
	tenantID := os.Getenv("PPTS_TENANT_ID")
	objectRoot := os.Getenv("PPTS_OBJECT_ROOT")
	if dsn == "" || tenantID == "" || objectRoot == "" {
		return errors.New("PPTS_DATABASE_URL, PPTS_TENANT_ID, and PPTS_OBJECT_ROOT are required")
	}
	if os.Getenv("PPTS_TTS_PROVIDER") != "fake" {
		return errors.New("development worker requires explicit PPTS_TTS_PROVIDER=fake")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	jobs, err := pipeline.NewPGStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer jobs.Close()

	policyStore := tenant.NewPGStore(pool)
	objects, err := objectstore.NewRegistry("local", map[string]objectstore.ObjectStore{
		"local": objectstore.NewLocal(objectRoot, nil),
	}, policyStore)
	if err != nil {
		return err
	}
	parseHandler := app.NewParseHandler(objects, project.NewGoPPTXReader(project.Limits{}))
	scriptDraftHandler := app.NewScriptDraftHandler(narration.NewPGStore(pool), objects)
	narrationHandler := app.NewNarrationHandler(narration.NewPGStore(pool), jobs, objects, tts.NewFakeProvider()).WithUsage(usage.NewPGStore(pool))
	mp4Encoder, err := media.NewMP4Encoder()
	if err != nil {
		log.Printf("worker: mp4 encoder unavailable: %v", err)
	}
	exportHandler := app.NewExportHandler(artifact.NewPGStore(pool), jobs, objects, mp4Encoder)
	dispatch := func(ctx context.Context, job *pipeline.Job) error {
		switch job.Kind {
		case pipeline.KindParse:
			return parseHandler.Handle(ctx, job)
		case pipeline.KindScriptDraft:
			return scriptDraftHandler.Handle(ctx, job)
		case pipeline.KindNarration:
			return narrationHandler.Handle(ctx, job)
		case pipeline.KindExport:
			return exportHandler.Handle(ctx, job)
		default:
			return fmt.Errorf("worker: unsupported job kind %q", job.Kind)
		}
	}
	hostname, _ := os.Hostname()
	owner := hostname + "-" + strconv.Itoa(os.Getpid())
	worker := pipeline.NewWorker(jobs, owner, tenantID, dispatch, pipeline.WorkerOptions{})

	// 保留与清理后台循环（G3-7）：孤儿上传清理 + 源文件到期/按需删除。
	sweeper := retention.NewSweeper(retention.NewPGStore(pool), objects, durationEnv("PPTS_UPLOAD_ABANDON_TTL", 24*time.Hour), log.Default())
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	go runSweeper(sweepCtx, sweeper, durationEnv("PPTS_RETENTION_INTERVAL", time.Hour))

	log.Printf("worker %s started for tenant %s", owner, tenantID)
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// runSweeper 启动即执行一次，随后按 interval 周期清理。
func runSweeper(ctx context.Context, s *retention.Sweeper, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	_ = s.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Sweep(ctx)
		}
	}
}

// durationEnv 读取时长环境变量，非法或缺失时返回默认值。
func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
