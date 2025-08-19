package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/your-org/simple-kafka-distributor/internal/config"
	"github.com/your-org/simple-kafka-distributor/internal/server"
)

func main() {
	cfg := config.Load()

	// Флаг для переопределения порта (не обязателен, но оставим)
	portFlag := flag.String("port", cfg.Port, "Listen port")
	flag.Parse()
	cfg.Port = *portFlag

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("init server error: %v", err)
	}

	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("server stopped with error: %v", err)
		}
	}()

	// Graceful shutdown
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	log.Println("[INFO] shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		log.Printf("[WARN] graceful stop error: %v", err)
	}
	log.Println("[INFO] bye")
}