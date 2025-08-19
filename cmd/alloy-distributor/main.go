package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Updated to local module path
	"github.com/DeveloperDarkhan/loki-producer/internal/buildinfo"
	"github.com/DeveloperDarkhan/loki-producer/internal/config"
	"github.com/DeveloperDarkhan/loki-producer/internal/server"
)

var (
	configFile    = flag.String("config.file", "/config/config.yaml", "Path to config file (mounted via ConfigMap)")
	listBalancers = flag.Bool("list-balancers", false, "Print supported Kafka balancers and exit")
)

func main() {
	flag.Parse()

	if *listBalancers {
		for _, b := range config.SupportedBalancers() {
			println(b)
		}
		return
	}

	cfg, rawBytes, err := config.LoadFromFile(*configFile)
	if err != nil {
		log.Fatalf(`{"level":"fatal","msg":"failed to load config","error":%q,"path":%q}`, err.Error(), *configFile)
	}

	srv, err := server.New(*configFile, cfg)
	if err != nil {
		log.Fatalf(`{"level":"fatal","msg":"failed to init server","error":%q}`, err.Error())
	}

	// Log startup (structured) including raw config hash
	buildinfo.LogStartup(*cfg, rawBytes)

	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf(`{"level":"fatal","msg":"server exited with error","error":%q}`, err.Error())
		}
	}()

	// Handle signals: SIGINT/SIGTERM graceful; SIGHUP reload
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		sig := <-sigCh
		switch sig {
		case syscall.SIGHUP:
			log.Println(`{"level":"info","msg":"received SIGHUP, reloading config"}`)
			if err := srv.Reload(); err != nil {
				log.Printf(`{"level":"error","msg":"reload failed","error":%q}`, err.Error())
			} else {
				log.Println(`{"level":"info","msg":"reload completed"}`)
			}
		case syscall.SIGINT, syscall.SIGTERM:
			log.Printf(`{"level":"info","msg":"shutdown signal","signal":%q}`, sig.String())
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if err := srv.Stop(ctx); err != nil {
				log.Printf(`{"level":"warn","msg":"graceful stop error","error":%q}`, err.Error())
			}
			log.Println(`{"level":"info","msg":"exiting"}`)
			return
		}
	}
}
