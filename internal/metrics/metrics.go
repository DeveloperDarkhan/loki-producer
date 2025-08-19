package metrics

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

type Registry struct {
	enableTenant bool

	RequestsTotal          *prometheus.CounterVec
	RequestBytesTotal      *prometheus.CounterVec
	KafkaWriteErrorsTotal  *prometheus.CounterVec
	KafkaWriteDurationHist *prometheus.HistogramVec
	RequestDurationHist    *prometheus.HistogramVec
	HealthUp               prometheus.Gauge
	KafkaConsecutiveErrors prometheus.Gauge
	SLASuccessRatio        prometheus.Gauge
	RateLimitedTotal       *prometheus.CounterVec

	totalSuccess atomic.Uint64
	totalError   atomic.Uint64
	totalAll     atomic.Uint64
}

func NewRegistry(enableTenant, slaGaugeEnable bool) *Registry {
	reqLabels := []string{"endpoint", "result", "content_type_class"}
	reqBytesLabels := []string{"endpoint"}
	if enableTenant {
		reqLabels = append(reqLabels, "tenant")
		reqBytesLabels = append(reqBytesLabels, "tenant")
	}

	r := &Registry{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_loki_produce_requests_total",
			Help: "Total HTTP push requests processed, partitioned by result",
		}, reqLabels),
		RequestBytesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_loki_produce_request_bytes_total",
			Help: "Total request body bytes received",
		}, reqBytesLabels),
		KafkaWriteErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_loki_produce_kafka_write_errors_total",
			Help: "Kafka write errors by classified type",
		}, []string{"error_type"}),
		KafkaWriteDurationHist: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pulse_loki_produce_kafka_write_duration_seconds",
			Help:    "Kafka write latency (WriteMessages duration)",
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}, []string{"result"}),
		RequestDurationHist: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pulse_loki_produce_request_duration_seconds",
			Help:    "End-to-end HTTP request handling duration",
			Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}, []string{"endpoint", "result"}),
		HealthUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pulse_loki_produce_health_up",
			Help: "1 if healthy, 0 degraded",
		}),
		KafkaConsecutiveErrors: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pulse_loki_produce_kafka_consecutive_error_count",
			Help: "Number of consecutive Kafka write errors",
		}),
		RateLimitedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pulse_loki_produce_rate_limited_total",
			Help: "Requests rejected due to rate limiting",
		}, []string{"scope"}),
	}

	if slaGaugeEnable {
		r.SLASuccessRatio = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pulse_loki_produce_sla_success_ratio",
			Help: "Success ratio (success/total) in last evaluation window",
		})
	}

	toRegister := []prometheus.Collector{
		r.RequestsTotal,
		r.RequestBytesTotal,
		r.KafkaWriteErrorsTotal,
		r.KafkaWriteDurationHist,
		r.RequestDurationHist,
		r.HealthUp,
		r.KafkaConsecutiveErrors,
		r.RateLimitedTotal,
	}
	if slaGaugeEnable {
		toRegister = append(toRegister, r.SLASuccessRatio)
	}
	for _, c := range toRegister {
		prometheus.MustRegister(c)
	}

	r.HealthUp.Set(1)
	r.KafkaConsecutiveErrors.Set(0)

	return r
}

func (r *Registry) MakeRequestLabels(endpoint, result, ctClass, tenant string) []string {
	if r.enableTenant {
		return []string{endpoint, result, ctClass, tenant}
	}
	return []string{endpoint, result, ctClass}
}
func (r *Registry) MakeRequestBytesLabels(endpoint, tenant string) []string {
	if r.enableTenant {
		return []string{endpoint, tenant}
	}
	return []string{endpoint}
}

func (r *Registry) TrackResult(isSuccess, isError bool) {
	r.totalAll.Add(1)
	if isSuccess {
		r.totalSuccess.Add(1)
	}
	if isError {
		r.totalError.Add(1)
	}
}

func (r *Registry) Snapshot() (total, success, errors uint64) {
	return r.totalAll.Load(), r.totalSuccess.Load(), r.totalError.Load()
}