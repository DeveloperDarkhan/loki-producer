package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Immutable (changing requires Kafka writer rebuild)
	KafkaBrokers      []string      `yaml:"kafka_brokers"`
	KafkaTopic        string        `yaml:"kafka_topic"`
	KafkaRequiredAcks int           `yaml:"kafka_required_acks"`
	KafkaBalancer     string        `yaml:"kafka_balancer"` // sticky|round_robin|hash
	KafkaWriteTimeout time.Duration `yaml:"kafka_write_timeout"`

	// Security
	KafkaSASLEnabled           bool   `yaml:"kafka_sasl_enabled"`
	KafkaSASLMechanism         string `yaml:"kafka_sasl_mechanism"` // scram-sha-512|scram-sha-256
	KafkaSASLUsername          string `yaml:"kafka_sasl_username"`
	KafkaSASLPassword          string `yaml:"kafka_sasl_password"` // can be empty if provided via env KAFKA_SASL_PASSWORD
	KafkaTLSEnabled            bool   `yaml:"kafka_tls_enabled"`
	KafkaTLSInsecureSkipVerify bool   `yaml:"kafka_tls_insecure_skip_verify"`
	KafkaTLSCAFile             string `yaml:"kafka_tls_ca_file"` // optional CA path

	// Startup probe
	KafkaProbeEnabled  bool          `yaml:"kafka_probe_enabled"`
	KafkaProbeRequired bool          `yaml:"kafka_probe_required"`
	KafkaProbeTimeout  time.Duration `yaml:"kafka_probe_timeout"`
	KafkaProbeWrite    bool          `yaml:"kafka_probe_write"` // if true, send a tiny test message at startup

	// Mutable
	MaxBodyBytes             int64  `yaml:"max_body_bytes"`
	AllowEmptyTenant         bool   `yaml:"allow_empty_tenant"`
	DefaultTenant            string `yaml:"default_tenant"`
	MetricsEnableTenantLabel bool   `yaml:"metrics_enable_tenant_label"`

	HealthErrorRateThreshold        float64       `yaml:"health_error_rate_threshold"`
	HealthConsecutiveErrorThreshold int           `yaml:"health_consecutive_error_threshold"`
	HealthEvalPeriod                time.Duration `yaml:"health_eval_period"`
	SLAGaugeEnable                  bool          `yaml:"sla_gauge_enable"`

	RateLimitEnabled        bool    `yaml:"rate_limit_enabled"`
	RateLimitGlobalRPS      float64 `yaml:"rate_limit_global_rps"`
	RateLimitGlobalBurst    int     `yaml:"rate_limit_global_burst"`
	RateLimitPerTenantRPS   float64 `yaml:"rate_limit_per_tenant_rps"`
	RateLimitPerTenantBurst int     `yaml:"rate_limit_per_tenant_burst"`

	LogLevel string `yaml:"log_level"` // info|debug
	Quiet    bool   `yaml:"quiet"`
	Port     string `yaml:"port"`
}

var defaultConfig = Config{
	KafkaRequiredAcks:               1,
	KafkaBalancer:                   "sticky",
	KafkaWriteTimeout:               10 * time.Second,
	KafkaSASLEnabled:                false,
	KafkaSASLMechanism:              "scram-sha-512",
	KafkaTLSEnabled:                 false,
	KafkaTLSInsecureSkipVerify:      false,
	KafkaProbeEnabled:               true,
	KafkaProbeRequired:              true,
	KafkaProbeTimeout:               5 * time.Second,
	MaxBodyBytes:                    5 << 20,
	DefaultTenant:                   "anonymous",
	HealthErrorRateThreshold:        0.05,
	HealthConsecutiveErrorThreshold: 5,
	HealthEvalPeriod:                30 * time.Second,
	SLAGaugeEnable:                  true,
	LogLevel:                        "info",
	Port:                            "3101",
}

func LoadFromFile(path string) (*Config, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := Parse(b)
	if err != nil {
		return nil, b, err
	}
	return cfg, b, nil
}

func Parse(data []byte) (*Config, error) {
	var c Config = defaultConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	// Normalize legacy/alternative balancer names
	c.KafkaBalancer = normalizeBalancer(c.KafkaBalancer)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// normalizeBalancer maps legacy or alternative names to canonical values
func normalizeBalancer(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "sticky":
		return "sticky"
	case "round_robin", "roundrobin", "round-robin":
		return "round_robin"
	case "hash":
		return "hash"
	case "least", "least_bytes", "least-bytes":
		// legacy alias — map to sticky (implementation treats sticky as LeastBytes)
		return "sticky"
	default:
		return v
	}
}

// SupportedBalancers returns canonical balancer names supported by the service.
func SupportedBalancers() []string {
	return []string{"sticky", "round_robin", "hash"}
}

func (c *Config) Validate() error {
	if len(c.KafkaBrokers) == 0 {
		return errors.New("kafka_brokers required")
	}
	if c.KafkaTopic == "" {
		return errors.New("kafka_topic required")
	}
	switch strings.ToLower(c.KafkaBalancer) {
	case "sticky", "round_robin", "roundrobin", "hash":
	default:
		return fmt.Errorf("unsupported kafka_balancer: %s", c.KafkaBalancer)
	}
	if c.KafkaWriteTimeout <= 0 {
		return errors.New("kafka_write_timeout must be > 0")
	}
	if c.KafkaSASLEnabled {
		switch strings.ToLower(strings.TrimSpace(c.KafkaSASLMechanism)) {
		case "scram-sha-512", "scram-sha-256":
		default:
			return fmt.Errorf("unsupported kafka_sasl_mechanism: %s", c.KafkaSASLMechanism)
		}
		if strings.TrimSpace(c.KafkaSASLUsername) == "" {
			return errors.New("kafka_sasl_username required when SASL enabled")
		}
	}
	if c.KafkaProbeTimeout <= 0 {
		return errors.New("kafka_probe_timeout must be > 0")
	}
	if c.MaxBodyBytes <= 0 {
		return errors.New("max_body_bytes must be > 0")
	}
	if c.HealthEvalPeriod <= 0 {
		return errors.New("health_eval_period must be > 0")
	}
	if c.HealthErrorRateThreshold < 0 || c.HealthErrorRateThreshold > 1 {
		return errors.New("health_error_rate_threshold must be between 0 and 1")
	}
	if c.RateLimitGlobalRPS < 0 || c.RateLimitPerTenantRPS < 0 {
		return errors.New("rate limits RPS must be >= 0")
	}
	if c.RateLimitGlobalBurst < 0 || c.RateLimitPerTenantBurst < 0 {
		return errors.New("rate limit bursts must be >= 0")
	}
	switch c.LogLevel {
	case "info", "debug":
	default:
		return fmt.Errorf("invalid log_level: %s", c.LogLevel)
	}
	return nil
}

// ImmutableSubset returns a struct containing only immutable config fields.
type ImmutableSubset struct {
	KafkaBrokers               []string
	KafkaTopic                 string
	KafkaRequiredAcks          int
	KafkaBalancer              string
	KafkaWriteTimeout          time.Duration
	KafkaSASLEnabled           bool
	KafkaSASLMechanism         string
	KafkaSASLUsername          string
	KafkaTLSEnabled            bool
	KafkaTLSInsecureSkipVerify bool
	KafkaTLSCAFile             string
	MetricsEnableTenantLabel   bool
}

func (c *Config) ImmutableSubset() ImmutableSubset {
	return ImmutableSubset{
		KafkaBrokers:               append([]string{}, c.KafkaBrokers...),
		KafkaTopic:                 c.KafkaTopic,
		KafkaRequiredAcks:          c.KafkaRequiredAcks,
		KafkaBalancer:              c.KafkaBalancer,
		KafkaWriteTimeout:          c.KafkaWriteTimeout,
		KafkaSASLEnabled:           c.KafkaSASLEnabled,
		KafkaSASLMechanism:         c.KafkaSASLMechanism,
		KafkaSASLUsername:          c.KafkaSASLUsername,
		KafkaTLSEnabled:            c.KafkaTLSEnabled,
		KafkaTLSInsecureSkipVerify: c.KafkaTLSInsecureSkipVerify,
		KafkaTLSCAFile:             c.KafkaTLSCAFile,
		MetricsEnableTenantLabel:   c.MetricsEnableTenantLabel,
	}
}

func ImmutableChanged(a, b ImmutableSubset) bool {
	return !reflect.DeepEqual(a, b)
}

// RuntimeView returns a safe view for logging/export.
type RuntimeView struct {
	KafkaBrokers               []string `json:"kafka_brokers"`
	KafkaTopic                 string   `json:"kafka_topic"`
	KafkaRequiredAcks          int      `json:"kafka_required_acks"`
	KafkaBalancer              string   `json:"kafka_balancer"`
	KafkaWriteTimeout          string   `json:"kafka_write_timeout"`
	KafkaSASLEnabled           bool     `json:"kafka_sasl_enabled"`
	KafkaSASLMechanism         string   `json:"kafka_sasl_mechanism"`
	KafkaSASLUsername          string   `json:"kafka_sasl_username"`
	KafkaTLSEnabled            bool     `json:"kafka_tls_enabled"`
	KafkaTLSInsecureSkipVerify bool     `json:"kafka_tls_insecure_skip_verify"`
	KafkaTLSCAFile             string   `json:"kafka_tls_ca_file"`

	MaxBodyBytes             int64  `json:"max_body_bytes"`
	AllowEmptyTenant         bool   `json:"allow_empty_tenant"`
	DefaultTenant            string `json:"default_tenant"`
	MetricsEnableTenantLabel bool   `json:"metrics_enable_tenant_label"`

	HealthErrorRateThreshold        float64 `json:"health_error_rate_threshold"`
	HealthConsecutiveErrorThreshold int     `json:"health_consecutive_error_threshold"`
	HealthEvalPeriod                string  `json:"health_eval_period"`
	SLAGaugeEnable                  bool    `json:"sla_gauge_enable"`

	RateLimitEnabled        bool    `json:"rate_limit_enabled"`
	RateLimitGlobalRPS      float64 `json:"rate_limit_global_rps"`
	RateLimitGlobalBurst    int     `json:"rate_limit_global_burst"`
	RateLimitPerTenantRPS   float64 `json:"rate_limit_per_tenant_rps"`
	RateLimitPerTenantBurst int     `json:"rate_limit_per_tenant_burst"`

	LogLevel string `json:"log_level"`
	Quiet    bool   `json:"quiet"`
	Port     string `json:"port"`
}

func (c Config) RuntimeView() RuntimeView {
	return RuntimeView{
		KafkaBrokers:               c.KafkaBrokers,
		KafkaTopic:                 c.KafkaTopic,
		KafkaRequiredAcks:          c.KafkaRequiredAcks,
		KafkaBalancer:              c.KafkaBalancer,
		KafkaWriteTimeout:          c.KafkaWriteTimeout.String(),
		KafkaSASLEnabled:           c.KafkaSASLEnabled,
		KafkaSASLMechanism:         c.KafkaSASLMechanism,
		KafkaSASLUsername:          c.KafkaSASLUsername,
		KafkaTLSEnabled:            c.KafkaTLSEnabled,
		KafkaTLSInsecureSkipVerify: c.KafkaTLSInsecureSkipVerify,
		KafkaTLSCAFile:             c.KafkaTLSCAFile,

		MaxBodyBytes:             c.MaxBodyBytes,
		AllowEmptyTenant:         c.AllowEmptyTenant,
		DefaultTenant:            c.DefaultTenant,
		MetricsEnableTenantLabel: c.MetricsEnableTenantLabel,

		HealthErrorRateThreshold:        c.HealthErrorRateThreshold,
		HealthConsecutiveErrorThreshold: c.HealthConsecutiveErrorThreshold,
		HealthEvalPeriod:                c.HealthEvalPeriod.String(),
		SLAGaugeEnable:                  c.SLAGaugeEnable,

		RateLimitEnabled:        c.RateLimitEnabled,
		RateLimitGlobalRPS:      c.RateLimitGlobalRPS,
		RateLimitGlobalBurst:    c.RateLimitGlobalBurst,
		RateLimitPerTenantRPS:   c.RateLimitPerTenantRPS,
		RateLimitPerTenantBurst: c.RateLimitPerTenantBurst,

		LogLevel: c.LogLevel,
		Quiet:    c.Quiet,
		Port:     c.Port,
	}
}

// Guard for reload concurrency if needed externally
var ReloadMutex sync.Mutex
