package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"youdl/internal/common"
	"youdl/internal/worker"
)

func main() {
	cfg, err := common.LoadWorkerConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	w, err := worker.New(cfg)
	if err != nil {
		log.Fatalf("worker: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("worker %s starting (controller=%s, max_jobs=%d)",
		cfg.WorkerID, cfg.ControllerURL, cfg.MaxJobs)

	if err := w.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("worker: %v", err)
	}
	log.Println("worker stopped")
}
