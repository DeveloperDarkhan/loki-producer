package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
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
		tenant         = flag.String("tenant", "canary", "Tenant ID")
		rps            = flag.Float64("rps", 50, "Total requests per second")
		concurrency    = flag.Int("concurrency", 4, "Concurrent workers")
		linesPerStream = flag.Int("lines", 20, "Lines per stream")
		streamsPerReq  = flag.Int("streams", 1, "Streams per request")
		bodyFormat     = flag.String("format", "json", "Body format (only json supported)")
		labelApp       = flag.String("label-app", "canary", "Label 'app' value")
		runFor         = flag.Duration("duration", 0, "Run duration (0 = forever)")
		jitterPct      = flag.Float64("jitter-pct", 0.10, "Inter-request sleep jitter fraction (0.10 = ±10%)")
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

	log.Printf(`{"level":"info","msg":"canary start","url":%q,"tenant":%q,"rps":%.2f,"workers":%d,"interval_ms":%.2f,"streams_per_req":%d,"lines_per_stream":%d}`,
		*targetURL, *tenant, *rps, *concurrency, float64(interval.Milliseconds()), *streamsPerReq, *linesPerStream)

	client := &http.Client{Timeout: 15 * time.Second}

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
					body, size := makeBody(*streamsPerReq, *linesPerStream, *labelApp)
					req, _ := http.NewRequest("POST", *targetURL, bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Scope-OrgID", *tenant)
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
						log.Printf(`{"level":"debug","worker":%d,"status":%d,"lat_ms":%.2f,"bytes":%d}`, id, resp.StatusCode, lat.Seconds()*1000, size)
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

func makeBody(streams, lines int, app string) ([]byte, int) {
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
		for l := 0; l < lines; l++ {
			ts := now.Add(time.Duration(l) * time.Millisecond).UnixNano()
			line := fmt.Sprintf("canary line %d stream %d payload=%s", l, i, strings.Repeat("x", 16))
			st.Values = append(st.Values, [2]string{fmt.Sprintf("%d", ts), line})
		}
		req.Streams = append(req.Streams, st)
	}
	b, _ := json.Marshal(req)
	return b, len(b)
}