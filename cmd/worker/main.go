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

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
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

	objects := objectstore.NewLocal(objectRoot, nil)
	parseHandler := app.NewParseHandler(objects, project.NewGoPPTXReader(project.Limits{}))
	narrationHandler := app.NewNarrationHandler(narration.NewPGStore(pool), jobs, objects, tts.NewFakeProvider())
	mp4Encoder, err := media.NewMP4Encoder()
	if err != nil {
		log.Printf("worker: mp4 encoder unavailable: %v", err)
	}
	exportHandler := app.NewExportHandler(artifact.NewPGStore(pool), jobs, objects, mp4Encoder)
	dispatch := func(ctx context.Context, job *pipeline.Job) error {
		switch job.Kind {
		case pipeline.KindParse:
			return parseHandler.Handle(ctx, job)
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
	log.Printf("worker %s started for tenant %s", owner, tenantID)
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
