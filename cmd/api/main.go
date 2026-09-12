package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/api"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
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
	if dsn == "" {
		return errors.New("PPTS_DATABASE_URL is required")
	}
	objectRoot := os.Getenv("PPTS_OBJECT_ROOT")
	objectSecret := os.Getenv("PPTS_OBJECT_SECRET")
	if objectRoot == "" || objectSecret == "" {
		return errors.New("PPTS_OBJECT_ROOT and PPTS_OBJECT_SECRET are required")
	}
	addr := os.Getenv("PPTS_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
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

	server := &http.Server{
		Addr:              addr,
		Handler:           api.NewHandler(project.NewPGProjectStore(pool), narration.NewPGStore(pool), jobs, artifact.NewPGStore(pool), objectstore.NewLocal(objectRoot, []byte(objectSecret))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("api listening on %s", addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
