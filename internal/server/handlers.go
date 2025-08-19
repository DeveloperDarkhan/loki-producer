package server

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/your-org/simple-distributor/internal/config"
	"github.com/your-org/simple-distributor/internal/model"
	"github.com/your-org/simple-distributor/internal/validation"
)

const (
	headerTenant = "X-Scope-OrgID"
)

type Server struct {
	httpServer *http.Server
	validator  *validation.Validator
	cfg        *config.Config
}

func New(addr string, v *validation.Validator, cfg *config.Config) *Server {
	mux := http.NewServeMux()
	s := &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		validator: v,
		cfg:       cfg,
	}
	// Loki совместимые endpoints
	mux.HandleFunc("/loki/api/v1/push", s.handlePush)
	mux.HandleFunc("/api/prom/push", s.handlePush)
	// Health
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	return s
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tenant := r.Header.Get(headerTenant)
	if tenant == "" {
		tenant = "anonymous"
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10MB safety
	if err != nil {
		http.Error(w, "read body error: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	ct := r.Header.Get("Content-Type")
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}

	// Упрощённо: принимаем только JSON push (как promtail умеет).
	// (Чтобы поддержать protobuf: content-type=application/x-protobuf и
	//  нужно распарсить logproto.PushRequest.)
	if ct == "" || ct == "application/json" || ct == "application/octet-stream" {
		var pr model.PushRequest
		if err := json.Unmarshal(body, &pr); err != nil {
			http.Error(w, "json unmarshal failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		validated, err := s.validator.ValidatePush(tenant, &pr)
		if err != nil {
			http.Error(w, "validation failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		for _, e := range validated {
			log.Printf("tenant=%s labels=%s ts=%s line=%q",
				e.Tenant, e.LabelsStr, e.Timestamp.Format(time.RFC3339Nano), e.Line)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	http.Error(w, "unsupported content-type (only JSON push implemented in this minimal version)", http.StatusUnsupportedMediaType)
}
