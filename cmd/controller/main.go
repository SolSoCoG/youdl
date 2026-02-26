package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"youdl/internal/common"
	"youdl/internal/controller"
)

func main() {
	cfg, err := common.LoadControllerConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	srv, err := controller.NewServer(cfg)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv.StartCleanup()

	go func() {
		log.Printf("controller listening on %s", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutCtx)
}
