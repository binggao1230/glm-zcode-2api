// Command server runs the Z Code → OpenAI-compatible gateway.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"glm-zcode-2api/internal/config"
	"glm-zcode-2api/internal/server"
)

func main() {
	configPath := flag.String("config", "", "path to the gateway config JSON")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatalf("event=startup_failed error=%q", err.Error())
	}

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, logger).Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	failed := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			failed <- err
		}
	}()

	logger.Printf("event=startup listen=%s service=%s models=%v provider=%s",
		cfg.Listen, server.Service, cfg.ModelIDs(), cfg.Upstream.ProviderID)

	select {
	case err := <-failed:
		logger.Fatalf("event=server_failed error=%q", err.Error())
	case <-stop:
		logger.Println("event=shutdown")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}
}
