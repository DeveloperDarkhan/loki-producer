package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"runtime"

	// Updated to local module path
	"github.com/DeveloperDarkhan/loki-producer/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

var buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "pulse_loki_produce_build_info",
	Help: "Build information",
}, []string{"version", "commit", "date", "go_version"})

func init() {
	prometheus.MustRegister(buildInfo)
	buildInfo.WithLabelValues(Version, Commit, Date, runtime.Version()).Set(1)
}

func LogStartup(cfg config.Config, raw []byte) {
	hash := sha256.Sum256(raw)
	rawHash := hex.EncodeToString(hash[:8])
	type logged struct {
		Version   string             `json:"version"`
		Commit    string             `json:"commit"`
		Date      string             `json:"date"`
		ConfigRef string             `json:"config_hash"`
		Config    config.RuntimeView `json:"config_effective"`
	}
	l := logged{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		ConfigRef: rawHash,
		Config:    cfg.RuntimeView(),
	}
	b, _ := json.Marshal(l)
	log.Println(string(b))
	fmt.Println()
}
