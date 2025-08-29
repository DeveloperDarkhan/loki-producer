package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type pushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}
type pushRequest struct {
	Streams []pushStream `json:"streams"`
}

func main() {
	log.SetOutput(os.Stdout)

	var (
		targetURL      = flag.String("url", "http://localhost:3101/loki/api/v1/push", "Distributor push URL")
		tenant         = flag.String("tenant", "canary", "Tenant ID (used when -tenants=1)")
		tenantPrefix   = flag.String("tenant-prefix", "canary", "Tenant prefix for multi-tenant mode")
		tenants        = flag.Int("tenants", 1, "Number of distinct tenants (>=1)")
		rps            = flag.Float64("rps", 50, "Total requests per second")
		concurrency    = flag.Int("concurrency", 4, "Concurrent workers")
		linesPerStream = flag.Int("lines", 20, "Lines per stream")
		streamsPerReq  = flag.Int("streams", 1, "Streams per request")
		bodyFormat     = flag.String("format", "json", "Body format (only json supported)")
		labelApp       = flag.String("label-app", "canary", "Label 'app' value")
		labelExtra     = flag.String("label-extra", "", "Extra labels as k=v,k2=v2")
		payloadBytes   = flag.Int("payload-bytes", 16, "Additional payload bytes per line")
		runFor         = flag.Duration("duration", 0, "Run duration (0 = forever)")
		jitterPct      = flag.Float64("jitter-pct", 0.10, "Inter-request sleep jitter fraction (0.10 = ±10%)")
		useGzip        = flag.Bool("gzip", false, "Compress body with gzip")
		httpTimeout    = flag.Duration("http-timeout", 15*time.Second, "HTTP client timeout")
		logLevel       = flag.String("log-level", "info", "Log level: info|debug")
	)
	flag.Parse()

	if *bodyFormat != "json" {
		log.Fatalf(`{"level":"fatal","msg":"unsupported format","format":%q}`, *bodyFormat)
	}

	perWorkerRPS := *rps / float64(*concurrency)
	if perWorkerRPS <= 0 {
		log.Fatalf(`{"level":"fatal","msg":"invalid rps or concurrency","rps":%.2f,"concurrency":%d}`, *rps, *concurrency)
	}
	interval := time.Duration(float64(time.Second) / perWorkerRPS)

	log.Printf(`{"level":"info","msg":"canary start","url":%q,"tenant":%q,"tenants":%d,"rps":%.2f,"workers":%d,"interval_ms":%.2f,"streams_per_req":%d,"lines_per_stream":%d,"gzip":%t}`,
		*targetURL, *tenant, *tenants, *rps, *concurrency, float64(interval.Milliseconds()), *streamsPerReq, *linesPerStream, *useGzip)

	// client := &http.Client{Timeout: *httpTimeout}
	tr := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: *concurrency * 2,
		MaxConnsPerHost:     *concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   *httpTimeout,
	}

	// parse extra labels
	extra := parseLabels(*labelExtra)

	var wg sync.WaitGroup
	stopCh := make(chan struct{})

	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					// jitter
					if *jitterPct > 0 {
						j := (rand.Float64()*2 - 1) * *jitterPct
						sleep := time.Duration(j * float64(interval))
						if sleep > 0 {
							time.Sleep(sleep)
						}
					}
					// pick tenant
					tid := *tenant
					if *tenants > 1 {
						idx := rand.Intn(*tenants)
						tid = fmt.Sprintf("%s-%d", strings.TrimSpace(*tenantPrefix), idx)
					}
					body, size := makeBody(*streamsPerReq, *linesPerStream, *labelApp, extra, *payloadBytes)
					var reader io.Reader = bytes.NewReader(body)
					var contentEncoding string
					if *useGzip {
						var buf bytes.Buffer
						gz := gzip.NewWriter(&buf)
						_, _ = gz.Write(body)
						_ = gz.Close()
						reader = &buf
						contentEncoding = "gzip"
					}
					req, _ := http.NewRequest("POST", *targetURL, reader)
					req.Header.Set("Content-Type", "application/json")
					if contentEncoding != "" {
						req.Header.Set("Content-Encoding", contentEncoding)
					}
					req.Header.Set("X-Scope-OrgID", tid)
					start := time.Now()
					resp, err := client.Do(req)
					lat := time.Since(start)
					if err != nil {
						log.Printf(`{"level":"warn","worker":%d,"msg":"send failed","error":%q}`, id, err.Error())
						continue
					}
					resp.Body.Close()
					if resp.StatusCode >= 300 {
						log.Printf(`{"level":"warn","worker":%d,"status":%d,"lat_ms":%.2f,"bytes":%d}`, id, resp.StatusCode, lat.Seconds()*1000, size)
					} else {
						if *logLevel == "debug" {
							log.Printf(`{"level":"debug","worker":%d,"status":%d,"lat_ms":%.2f,"bytes":%d}`, id, resp.StatusCode, lat.Seconds()*1000, size)
						}
					}
				}
			}
		}(w)
	}

	if *runFor > 0 {
		time.Sleep(*runFor)
		close(stopCh)
		wg.Wait()
		log.Println(`{"level":"info","msg":"canary finished"}`)
	} else {
		select {}
	}
}

func makeBody(streams, lines int, app string, extra map[string]string, payloadBytes int) ([]byte, int) {
	req := pushRequest{Streams: make([]pushStream, 0, streams)}
	now := time.Now()
	for i := 0; i < streams; i++ {
		st := pushStream{
			Stream: map[string]string{
				"app":  app,
				"pod":  fmt.Sprintf("p-%02d", i),
				"zone": []string{"a", "b", "c"}[i%3],
			},
			Values: make([][2]string, 0, lines),
		}
		for k, v := range extra {
			st.Stream[k] = v
		}
		for l := 0; l < lines; l++ {
			ts := now.Add(time.Duration(l) * time.Millisecond).UnixNano()
			line := fmt.Sprintf("canary line %d stream %d payload=%s", l, i, strings.Repeat("x", max(0, payloadBytes)))
			st.Values = append(st.Values, [2]string{fmt.Sprintf("%d", ts), line})
		}
		req.Streams = append(req.Streams, st)
	}
	b, _ := json.Marshal(req)
	return b, len(b)
}

func parseLabels(s string) map[string]string {
	res := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return res
	}
	parts := strings.Split(s, ",")
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if k != "" {
			res[k] = v
		}
	}
	return res
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
