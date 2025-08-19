package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/segmentio/kafka-go"
)

type Writer struct {
	w *kafka.Writer
}

type WriterConfig struct {
	Brokers      []string
	Topic        string
	RequiredAcks int    // 0 none, 1 one, -1/all => all
	Balancer     string // least_bytes|round_robin|hash
}

func NewWriter(cfg WriterConfig) (*Writer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("no kafka brokers")
	}
	var balancer kafka.Balancer
	switch cfg.Balancer {
	case "", "least_bytes", "least-bytes", "least", "sticky":
		// Treat legacy 'sticky' as LeastBytes strategy
		balancer = &kafka.LeastBytes{}
	case "round_robin", "roundrobin":
		balancer = &kafka.RoundRobin{}
	case "hash":
		balancer = &kafka.Hash{}
	default:
		return nil, fmt.Errorf("unknown balancer: %s", cfg.Balancer)
	}

	// Map RequiredAcks int to kafka.RequiredAcks
	var reqAcks kafka.RequiredAcks
	switch cfg.RequiredAcks {
	case 0:
		reqAcks = kafka.RequireNone
	case 1:
		reqAcks = kafka.RequireOne
	default:
		reqAcks = kafka.RequireAll
	}

	w := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     balancer,
		RequiredAcks: reqAcks,
		Async:        false,
	}

	return &Writer{w: w}, nil
}

func (w *Writer) Write(ctx context.Context, msg kafka.Message) error {
	return w.w.WriteMessages(ctx, msg)
}

func (w *Writer) Close() error {
	return w.w.Close()
}
