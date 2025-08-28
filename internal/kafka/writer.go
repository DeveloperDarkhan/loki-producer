package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/scram"
)

type Writer struct {
	w *kafka.Writer
}

type WriterConfig struct {
	Brokers      []string
	Topic        string
	RequiredAcks int           // 0 none, 1 one, -1/all => all
	Balancer     string        // least_bytes|round_robin|hash|sticky (legacy)
	WriteTimeout time.Duration // used for dialer timeout (connect) – actual write timeout handled by caller context

	// Security options
	SASLEnabled           bool
	SASLMechanism         string // scram-sha-512|scram-sha-256
	SASLUsername          string
	SASLPassword          string // also read from env KAFKA_SASL_PASSWORD if empty
	TLSEnabled            bool
	TLSInsecureSkipVerify bool
	TLSCAFile             string
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

	// Dialer with optional TLS/SASL
	dialer := &kafka.Dialer{
		Timeout:   cfg.WriteTimeout,
		DualStack: true,
	}

	// TLS
	if cfg.TLSEnabled {
		tlsCfg := &tls.Config{InsecureSkipVerify: cfg.TLSInsecureSkipVerify}
		if strings.TrimSpace(cfg.TLSCAFile) != "" {
			caPEM, err := os.ReadFile(cfg.TLSCAFile)
			if err != nil {
				return nil, fmt.Errorf("read ca file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caPEM) {
				return nil, errors.New("failed to append CA certs")
			}
			tlsCfg.RootCAs = pool
		}
		dialer.TLS = tlsCfg
	}

	// SASL SCRAM
	if cfg.SASLEnabled {
		user := strings.TrimSpace(cfg.SASLUsername)
		pass := cfg.SASLPassword
		if pass == "" {
			pass = os.Getenv("KAFKA_SASL_PASSWORD")
		}
		if user == "" || pass == "" {
			return nil, errors.New("SASL enabled but username/password not provided")
		}
		mechName := strings.ToLower(strings.TrimSpace(cfg.SASLMechanism))
		var mech sasl.Mechanism
		switch mechName {
		case "scram-sha-512":
			m, err := scram.Mechanism(scram.SHA512, user, pass)
			if err != nil {
				return nil, fmt.Errorf("scram512 mech: %w", err)
			}
			mech = m
		case "scram-sha-256":
			m, err := scram.Mechanism(scram.SHA256, user, pass)
			if err != nil {
				return nil, fmt.Errorf("scram256 mech: %w", err)
			}
			mech = m
		default:
			return nil, fmt.Errorf("unsupported SASL mechanism: %s", cfg.SASLMechanism)
		}
		dialer.SASLMechanism = mech
	}

	// Wrap dialer to match Transport.Dial signature (net.Conn)
	tr := &kafka.Transport{
		TLS:  dialer.TLS,
		SASL: dialer.SASLMechanism,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			c, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
	}

	w := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        cfg.Topic,
		Balancer:     balancer,
		RequiredAcks: reqAcks,
		Async:        false,
		Transport:    tr,
	}

	// Attach dialer with timeout if provided (>0)
	// (Dial timeout customization skipped due to kafka-go version differences; rely on context timeouts)

	// Optional debug logging (env KAFKA_DEBUG=1)
	if os.Getenv("KAFKA_DEBUG") != "" {
		w.Logger = log.New(os.Stdout, "kafka.writer ", log.LstdFlags|log.Lmicroseconds)
		w.ErrorLogger = log.New(os.Stderr, "kafka.writer.err ", log.LstdFlags|log.Lmicroseconds)
	}

	return &Writer{w: w}, nil
}

func (w *Writer) Write(ctx context.Context, msg kafka.Message) error {
	return w.w.WriteMessages(ctx, msg)
}

func (w *Writer) Close() error {
	return w.w.Close()
}
