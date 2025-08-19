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
	RequiredAcks int
	Balancer     string
}

func NewWriter(cfg WriterConfig) (*Writer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("no kafka brokers")
	}
	var balancer kafka.Balancer
	switch cfg.Balancer {
	case "round_robin", "roundrobin":
		balancer = &kafka.RoundRobin{}
	case "sticky", "":
		balancer = &kafka.Sticky{}
	case "hash":
		balancer = &kafka.Hash{}
	default:
		return nil, fmt.Errorf("unknown balancer: %s", cfg.Balancer)
	}

	w := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     balancer,
		RequiredAcks: cfg.RequiredAcks,
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