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

	"github.com/your-org/simple-distributor/internal/server"
)

var (
	httpAddr     = flag.String("http.listen", ":3100", "Listen address")
	maxBodyBytes = flag.Int64("max.body.bytes", 50<<20, "Max request body size (compressed)")
	quiet        = flag.Bool("quiet", false, "Do not log each push (only metrics/errors)")
)

func main() {
	flag.Parse()

	s := server.New(server.Config{
		Addr:         *httpAddr,
		MaxBodyBytes: *maxBodyBytes,
		Quiet:        *quiet,
	})

	go func() {
		log.Printf("listening on %s", *httpAddr)
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutdown signal received")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}