package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DeveloperDarkhan/loki-producer/internal/config"
	"github.com/DeveloperDarkhan/loki-producer/internal/server"
)

var (
	configFile = flag.String("config.file", "./config/config.yaml", "Path to config file")
)

func main() {
	flag.Parse()

	cfg, _, err := config.LoadFromFile(*configFile)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	srv, err := server.New(*configFile, cfg)
	if err != nil {
		log.Fatalf("failed to init server: %v", err)
	}
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("server exited: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("shutdown signal: %s", sig)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Stop(ctx)
}
