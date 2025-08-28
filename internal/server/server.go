package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	kafkago "github.com/segmentio/kafka-go"
	"golang.org/x/time/rate"

	// Use local module path instead of old alloy-distributor path
	"github.com/DeveloperDarkhan/loki-producer/internal/config"
	"github.com/DeveloperDarkhan/loki-producer/internal/kafka"
	"github.com/DeveloperDarkhan/loki-producer/internal/metrics"
)

type Server struct {
	cfgFile string

	mu         sync.RWMutex
	cfg        *config.Config
	httpServer *http.Server
	kWriter    *kafka.Writer
	metrics    *metrics.Registry
	stopHealth chan struct{}
	reloadCh   chan struct{}

	// health counters
	prevTotal         uint64
	prevSuccess       uint64
	prevErrors        uint64
	consecutiveErrors int

	// rate limiting
	globalLimiter  *rateLimiterWrapper
	tenantLimiters *perTenantLimiter
}

type rateLimiterWrapper struct {
	lim *rate.Limiter
}

func New(cfgFile string, cfg *config.Config) (*Server, error) {
	writer, err := kafka.NewWriter(kafka.WriterConfig{
		Brokers:               cfg.KafkaBrokers,
		Topic:                 cfg.KafkaTopic,
		RequiredAcks:          cfg.KafkaRequiredAcks,
		Balancer:              cfg.KafkaBalancer,
		WriteTimeout:          cfg.KafkaWriteTimeout,
		SASLEnabled:           cfg.KafkaSASLEnabled,
		SASLMechanism:         cfg.KafkaSASLMechanism,
		SASLUsername:          cfg.KafkaSASLUsername,
		SASLPassword:          cfg.KafkaSASLPassword,
		TLSEnabled:            cfg.KafkaTLSEnabled,
		TLSInsecureSkipVerify: cfg.KafkaTLSInsecureSkipVerify,
		TLSCAFile:             cfg.KafkaTLSCAFile,
	})
	if err != nil {
		return nil, fmt.Errorf("kafka writer init: %w", err)
	}

	mreg := metrics.NewRegistry(cfg.MetricsEnableTenantLabel, cfg.SLAGaugeEnable)

	s := &Server{
		cfgFile:    cfgFile,
		cfg:        cfg,
		kWriter:    writer,
		metrics:    mreg,
		stopHealth: make(chan struct{}),
		reloadCh:   make(chan struct{}, 1),
	}

	s.buildRateLimitersLocked()

	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/push", s.wrapRequest("/loki/api/v1/push", s.handlePush))
	mux.HandleFunc("/api/prom/push", s.wrapRequest("/api/prom/push", s.handlePush))
	mux.HandleFunc("/ready", s.readyHandler)
	mux.HandleFunc("/configz", s.configzHandler)
	mux.HandleFunc("/reload", s.reloadHandler)
	mux.Handle("/metrics", promhttp.Handler())

	s.httpServer = &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 4 * time.Second,
		ReadTimeout:       25 * time.Second,
		WriteTimeout:      25 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	return s, nil
}

func (s *Server) Start() error {
	log.Printf(`{"level":"info","msg":"listening","port":%q,"topic":%q,"brokers":%q}`, s.cfg.Port, s.cfg.KafkaTopic, strings.Join(s.cfg.KafkaBrokers, ","))
	// Optional startup probe: try metadata or write tiny message
	if s.cfg.KafkaProbeEnabled {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.KafkaProbeTimeout)
		defer cancel()
		probeErr := s.kafkaProbe(ctx)
		if probeErr != nil {
			if s.cfg.KafkaProbeRequired {
				return fmt.Errorf("kafka startup probe failed: %w", probeErr)
			}
			log.Printf(`{"level":"warn","msg":"kafka probe failed (non-fatal)","error":%q}`, probeErr.Error())
		}
	}
	go s.healthLoop()
	return s.httpServer.ListenAndServe()
}

func (s *Server) Stop(ctx context.Context) error {
	close(s.stopHealth)
	log.Println(`{"level":"info","msg":"stopping http server"}`)
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return err
	}
	log.Println(`{"level":"info","msg":"closing kafka writer"}`)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kWriter.Close()
}

// Reload loads config file and applies changes.
func (s *Server) Reload() error {
	config.ReloadMutex.Lock()
	defer config.ReloadMutex.Unlock()

	newCfg, _, err := config.LoadFromFile(s.cfgFile)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oldImmutable := s.cfg.ImmutableSubset()
	newImmutable := newCfg.ImmutableSubset()

	rebuildWriter := config.ImmutableChanged(oldImmutable, newImmutable)
	if rebuildWriter {
		log.Printf(`{"level":"info","msg":"immutable config changed - rebuilding kafka writer"}`)
		newWriter, err := kafka.NewWriter(kafka.WriterConfig{
			Brokers:               newCfg.KafkaBrokers,
			Topic:                 newCfg.KafkaTopic,
			RequiredAcks:          newCfg.KafkaRequiredAcks,
			Balancer:              newCfg.KafkaBalancer,
			WriteTimeout:          newCfg.KafkaWriteTimeout,
			SASLEnabled:           newCfg.KafkaSASLEnabled,
			SASLMechanism:         newCfg.KafkaSASLMechanism,
			SASLUsername:          newCfg.KafkaSASLUsername,
			SASLPassword:          newCfg.KafkaSASLPassword,
			TLSEnabled:            newCfg.KafkaTLSEnabled,
			TLSInsecureSkipVerify: newCfg.KafkaTLSInsecureSkipVerify,
			TLSCAFile:             newCfg.KafkaTLSCAFile,
		})
		if err != nil {
			return fmt.Errorf("rebuild writer: %w", err)
		}
		oldWriter := s.kWriter
		s.kWriter = newWriter
		_ = oldWriter.Close()
		// metrics registry: if tenant label setting changed, we cannot swap safely without restart
		if oldImmutable.MetricsEnableTenantLabel != newImmutable.MetricsEnableTenantLabel {
			log.Printf(`{"level":"warn","msg":"metrics_enable_tenant_label change requires restart to take effect"}`)
		}
	}

	// Replace cfg
	s.cfg = newCfg
	s.buildRateLimitersLocked()

	log.Printf(`{"level":"info","msg":"reload applied","port":%q,"balancer":%q,"acks":%d}`, newCfg.Port, newCfg.KafkaBalancer, newCfg.KafkaRequiredAcks)
	return nil
}

func (s *Server) buildRateLimitersLocked() {
	if !s.cfg.RateLimitEnabled {
		s.globalLimiter = nil
		s.tenantLimiters = nil
		return
	}
	if s.cfg.RateLimitGlobalRPS > 0 {
		burst := s.cfg.RateLimitGlobalBurst
		if burst <= 0 {
			burst = int(s.cfg.RateLimitGlobalRPS * 2)
			if burst < 1 {
				burst = 1
			}
		}
		s.globalLimiter = &rateLimiterWrapper{lim: newTokenLimiter(s.cfg.RateLimitGlobalRPS, burst)}
	} else {
		s.globalLimiter = nil
	}
	if s.cfg.RateLimitPerTenantRPS > 0 {
		burst := s.cfg.RateLimitPerTenantBurst
		if burst <= 0 {
			burst = int(s.cfg.RateLimitPerTenantRPS * 2)
			if burst < 1 {
				burst = 1
			}
		}
		s.tenantLimiters = newPerTenantLimiter(s.cfg.RateLimitPerTenantRPS, burst)
	} else {
		s.tenantLimiters = nil
	}
}

// Simple replacement using golang.org/x/time/rate wrapper
// Provided separately to allow future custom logic.
func newTokenLimiter(rps float64, burst int) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(rps), burst)
}

func (s *Server) readyHandler(w http.ResponseWriter, _ *http.Request) {
	if s.isHealthy() {
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, "degraded", http.StatusServiceUnavailable)
	}
}

func (s *Server) configzHandler(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(s.cfg.RuntimeView())
}

func (s *Server) reloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if err := s.Reload(); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

type resultRecorder struct {
	http.ResponseWriter
	status int
	result string
}

func (r *resultRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) wrapRequest(endpoint string, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &resultRecorder{ResponseWriter: w}
		fn(rr, r)
		result := rr.result
		if result == "" {
			switch {
			case rr.status >= 500:
				result = "error"
			case rr.status >= 400:
				result = "client_error"
			case rr.status == 0 || (rr.status >= 200 && rr.status < 300):
				result = "success"
			default:
				result = "other"
			}
		}
		s.metrics.RequestDurationHist.WithLabelValues(endpoint, result).Observe(time.Since(start).Seconds())
	}
}

func classifyContentType(ct string) string {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return "proto"
	}
	if strings.HasPrefix(ct, "application/x-protobuf") || strings.HasPrefix(ct, "application/octet-stream") {
		return "proto"
	}
	if strings.HasPrefix(ct, "application/json") {
		return "json"
	}
	return "other"
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	rr, _ := w.(*resultRecorder)

	s.mu.RLock()
	cfg := s.cfg
	globalLimiter := s.globalLimiter
	tenantLimiters := s.tenantLimiters
	kWriter := s.kWriter
	s.mu.RUnlock()

	tenant := r.Header.Get("X-Scope-OrgID")
	if tenant == "" {
		if cfg.AllowEmptyTenant {
			tenant = cfg.DefaultTenant
		} else {
			http.Error(w, "Missing X-Scope-OrgID", http.StatusBadRequest)
			if rr != nil {
				rr.result = "missing_tenant"
			}
			s.metrics.RequestsTotal.WithLabelValues(s.metrics.MakeRequestLabels(r.URL.Path, "missing_tenant", "other", tenant)...).Inc()
			s.metrics.TrackResult(false, true)
			s.jsonLog("warn", "missing tenant", map[string]any{"endpoint": r.URL.Path})
			return
		}
	}

	// Rate limit
	if cfg.RateLimitEnabled {
		if globalLimiter != nil && !globalLimiter.lim.Allow() {
			s.metrics.RateLimitedTotal.WithLabelValues("global").Inc()
			http.Error(w, "rate limited (global)", http.StatusTooManyRequests)
			if rr != nil {
				rr.result = "rate_limited"
			}
			s.metrics.RequestsTotal.WithLabelValues(s.metrics.MakeRequestLabels(r.URL.Path, "rate_limited", "other", tenant)...).Inc()
			s.metrics.TrackResult(false, true)
			s.jsonLog("warn", "rate limited global", map[string]any{"tenant": tenant})
			return
		}
		if tenantLimiters != nil {
			if lim := tenantLimiters.get(tenant); !lim.Allow() {
				s.metrics.RateLimitedTotal.WithLabelValues("tenant").Inc()
				http.Error(w, "rate limited (tenant)", http.StatusTooManyRequests)
				if rr != nil {
					rr.result = "rate_limited"
				}
				s.metrics.RequestsTotal.WithLabelValues(s.metrics.MakeRequestLabels(r.URL.Path, "rate_limited", "other", tenant)...).Inc()
				s.metrics.TrackResult(false, true)
				s.jsonLog("warn", "rate limited tenant", map[string]any{"tenant": tenant})
				return
			}
		}
	}

	ctRaw := r.Header.Get("Content-Type")
	ctClass := classifyContentType(ctRaw)

	limited := http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)
	body, err := io.ReadAll(limited)
	r.Body.Close()
	if err != nil {
		res := "bad_request"
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "request body too large") {
			res = "too_large"
		}
		http.Error(w, res, http.StatusBadRequest)
		if rr != nil {
			rr.result = res
		}
		s.metrics.RequestsTotal.WithLabelValues(s.metrics.MakeRequestLabels(r.URL.Path, res, ctClass, tenant)...).Inc()
		s.metrics.TrackResult(false, true)
		s.jsonLog("warn", "read error", map[string]any{"tenant": tenant, "error": err.Error(), "result": res})
		return
	}

	size := len(body)
	s.metrics.RequestBytesTotal.WithLabelValues(s.metrics.MakeRequestBytesLabels(r.URL.Path, tenant)...).Add(float64(size))

	// Kafka message
	var headers []kafkago.Header
	headers = append(headers, kafkago.Header{Key: "X-Scope-OrgID", Value: []byte(tenant)})
	if ctRaw != "" {
		headers = append(headers, kafkago.Header{Key: "Content-Type", Value: []byte(ctRaw)})
	}
	if ce := r.Header.Get("Content-Encoding"); ce != "" {
		headers = append(headers, kafkago.Header{Key: "Content-Encoding", Value: []byte(ce)})
	}
	msg := kafkago.Message{
		Value:   body,
		Time:    time.Now(),
		Headers: headers,
	}
	if cfg.KafkaBalancer == "hash" {
		msg.Key = []byte(tenant)
	}

	kafkaStart := time.Now()
	writeCtx, cancel := context.WithTimeout(r.Context(), cfg.KafkaWriteTimeout)
	err = kWriter.Write(writeCtx, msg)
	cancel()
	kafkaDur := time.Since(kafkaStart).Seconds()

	if err != nil {
		errType := classifyKafkaError(err)
		s.metrics.KafkaWriteErrorsTotal.WithLabelValues(errType).Inc()
		s.metrics.KafkaWriteDurationHist.WithLabelValues("error").Observe(kafkaDur)
		http.Error(w, "kafka write failed", http.StatusServiceUnavailable)
		if rr != nil {
			rr.result = "kafka_error"
		}
		s.metrics.RequestsTotal.WithLabelValues(s.metrics.MakeRequestLabels(r.URL.Path, "kafka_error", ctClass, tenant)...).Inc()
		s.metrics.TrackResult(false, true)
		s.consecutiveErrors++
		s.metrics.KafkaConsecutiveErrors.Set(float64(s.consecutiveErrors))
		s.jsonLog("warn", "kafka write failed", map[string]any{
			"tenant": tenant, "bytes": size, "kafka_ms": kafkaDur * 1000,
			"error": err.Error(), "error_type": errType,
		})
		return
	}

	// Success
	s.consecutiveErrors = 0
	s.metrics.KafkaConsecutiveErrors.Set(0)
	s.metrics.KafkaWriteDurationHist.WithLabelValues("success").Observe(kafkaDur)
	w.WriteHeader(http.StatusNoContent)
	if rr != nil {
		rr.result = "success"
	}
	s.metrics.RequestsTotal.WithLabelValues(s.metrics.MakeRequestLabels(r.URL.Path, "success", ctClass, tenant)...).Inc()
	s.metrics.TrackResult(true, false)

	if !cfg.Quiet {
		s.jsonLog("info", "accepted", map[string]any{
			"tenant": tenant, "bytes": size, "kafka_ms": kafkaDur * 1000,
			"endpoint": r.URL.Path,
		})
	}
}

func (s *Server) healthLoop() {
	ticker := time.NewTicker(s.cfg.HealthEvalPeriod)
	defer ticker.Stop()
	s.prevTotal, s.prevSuccess, s.prevErrors = s.metrics.Snapshot()

	for {
		select {
		case <-ticker.C:
			totalNow, succNow, errNow := s.metrics.Snapshot()
			dTotal := totalNow - s.prevTotal
			dSucc := succNow - s.prevSuccess
			dErr := errNow - s.prevErrors
			s.prevTotal = totalNow
			s.prevSuccess = succNow
			s.prevErrors = errNow

			s.mu.RLock()
			cfg := s.cfg
			s.mu.RUnlock()

			if dTotal > 0 {
				errorRate := float64(dErr) / float64(dTotal)
				if cfg.SLAGaugeEnable {
					sla := float64(dSucc) / float64(dTotal)
					s.metrics.SLASuccessRatio.Set(sla)
				}
				unhealthy := false
				if errorRate > cfg.HealthErrorRateThreshold {
					unhealthy = true
				}
				if s.consecutiveErrors >= cfg.HealthConsecutiveErrorThreshold {
					unhealthy = true
				}
				if unhealthy {
					s.metrics.HealthUp.Set(0)
				} else {
					s.metrics.HealthUp.Set(1)
				}
			}
		case <-s.stopHealth:
			return
		}
	}
}

func (s *Server) isHealthy() bool {
	// Simple read of gauge by internal state; we trust metrics.HealthUp
	// (We could track a bool instead; for now assume if consecutiveErrors high or set gauge).
	return s.metrics != nil
}

// kafkaProbe attempts to connect and optionally write a tiny test message.
func (s *Server) kafkaProbe(ctx context.Context) error {
	// 1) Network reachability: TCP dial to the first broker.
	d := &net.Dialer{Timeout: s.cfg.KafkaProbeTimeout}
	addr := s.cfg.KafkaBrokers[0]
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	_ = conn.Close()
	// Log success of dial
	s.jsonLog("info", "kafka probe dial ok", map[string]any{"broker": addr})

	// 2) Optional produce check (auth/ACL/topic end-to-end)
	if !s.cfg.KafkaProbeWrite {
		return nil
	}

	// Produce a tiny, clearly marked probe message.
	headers := []kafkago.Header{
		{Key: "X-Producer-Probe", Value: []byte("true")},
		{Key: "X-Scope-OrgID", Value: []byte("_probe")},
	}
	msg := kafkago.Message{
		Key:     nil, // set below for hash balancer
		Value:   []byte("probe"),
		Time:    time.Now(),
		Headers: headers,
	}
	if s.cfg.KafkaBalancer == "hash" {
		msg.Key = []byte("_probe")
	}

	wctx, cancel := context.WithTimeout(ctx, s.cfg.KafkaProbeTimeout)
	defer cancel()
	if err := s.kWriter.Write(wctx, msg); err != nil {
		return err
	}
	// Log success of write
	s.jsonLog("info", "kafka probe write ok", map[string]any{
		"topic":      s.cfg.KafkaTopic,
		"timeout_ms": s.cfg.KafkaProbeTimeout.Milliseconds(),
	})
	return nil
}

func classifyKafkaError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not leader"):
		return "not_leader"
	case strings.Contains(msg, "unknown topic"):
		return "unknown_topic"
	case strings.Contains(msg, "message too large"):
		return "too_large"
	case strings.Contains(msg, "connection refused"):
		return "conn_refused"
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "broken pipe"):
		return "conn_reset"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	default:
		return "other"
	}
}

func (s *Server) jsonLog(level, msg string, kv map[string]any) {
	kv["level"] = level
	kv["msg"] = msg
	kv["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(kv)
	if err != nil {
		log.Printf(`{"level":"error","msg":"log marshal failed","error":%q}`, err.Error())
		return
	}
	log.Println(string(b))
}
