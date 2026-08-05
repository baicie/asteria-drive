package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type SLOOptions struct {
	BaseURL                string
	Token                  string
	RootID                 string
	Duration               time.Duration
	Warmup                 time.Duration
	Rate                   int
	Concurrency            int
	Timeout                time.Duration
	HealthOnly             bool
	IncludeDirectoryWrites bool
	WriteParentID          string
	Client                 *http.Client
}

type EndpointReport struct {
	Requests        int64   `json:"requests"`
	ServerErrors    int64   `json:"server_errors"`
	Unexpected      int64   `json:"unexpected_statuses"`
	P50Milliseconds float64 `json:"p50_ms"`
	P95Milliseconds float64 `json:"p95_ms"`
	P99Milliseconds float64 `json:"p99_ms"`
	MaxMilliseconds float64 `json:"max_ms"`
}

type SLOReport struct {
	StartedAt          time.Time                 `json:"started_at"`
	FinishedAt         time.Time                 `json:"finished_at"`
	DurationSeconds    float64                   `json:"duration_seconds"`
	WarmupSeconds      float64                   `json:"warmup_seconds"`
	Rate               int                       `json:"target_rps"`
	Concurrency        int                       `json:"concurrency"`
	Requests           int64                     `json:"requests"`
	Dropped            int64                     `json:"dropped_requests"`
	ServerErrors       int64                     `json:"server_errors"`
	UnexpectedStatuses int64                     `json:"unexpected_statuses"`
	ServerErrorRate    float64                   `json:"server_error_rate"`
	Endpoints          map[string]EndpointReport `json:"endpoints"`
}

type endpoint struct {
	name           string
	method         string
	path           string
	expectedStatus int
	body           func(int64) []byte
}

type sample struct {
	endpoint       string
	expectedStatus int
	duration       time.Duration
	status         int
}

type requestJob struct {
	endpoint endpoint
	sequence int64
}

func (o SLOOptions) Validate() error {
	if strings.TrimSpace(o.BaseURL) == "" {
		return fmt.Errorf("base url is required")
	}
	parsed, err := url.Parse(o.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("base url must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("base url scheme must be http or https")
	}
	if o.Duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	if o.Warmup < 0 {
		return fmt.Errorf("warmup must be non-negative")
	}
	if o.Rate < 1 || o.Rate > 10000 {
		return fmt.Errorf("rate must be between 1 and 10000")
	}
	if o.Concurrency < 1 || o.Concurrency > 1000 {
		return fmt.Errorf("concurrency must be between 1 and 1000")
	}
	if o.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if !o.HealthOnly && strings.TrimSpace(o.Token) == "" {
		return fmt.Errorf("token is required for authenticated endpoints")
	}
	if !o.HealthOnly && strings.TrimSpace(o.RootID) == "" {
		return fmt.Errorf("root id is required for authenticated endpoints")
	}
	if o.IncludeDirectoryWrites && o.HealthOnly {
		return fmt.Errorf("directory writes cannot be used with health-only mode")
	}
	if o.IncludeDirectoryWrites && strings.TrimSpace(o.WriteParentID) == "" {
		return fmt.Errorf("write parent id is required when directory writes are enabled")
	}
	return nil
}

func RunSLO(ctx context.Context, options SLOOptions) (SLOReport, error) {
	if options.Duration == 0 {
		options.Duration = 10 * time.Minute
	}
	if options.Rate == 0 {
		options.Rate = 50
	}
	if options.Concurrency == 0 {
		options.Concurrency = 16
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	if err := options.Validate(); err != nil {
		return SLOReport{}, err
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: options.Timeout}
	}
	started := time.Now().UTC()
	endpoints := []endpoint{{name: "healthz", method: http.MethodGet, path: "/healthz", expectedStatus: http.StatusOK}}
	if !options.HealthOnly {
		endpoints = append(endpoints,
			endpoint{name: "tenant", method: http.MethodGet, path: "/api/v1/tenant", expectedStatus: http.StatusOK},
			endpoint{name: "list_children", method: http.MethodGet, path: "/api/v1/directories/" + url.PathEscape(options.RootID) + "/children?limit=50", expectedStatus: http.StatusOK},
		)
		if options.IncludeDirectoryWrites {
			runID := started.Format("20060102t150405.000000000")
			endpoints = append(endpoints, endpoint{
				name: "create_directory", method: http.MethodPost, path: "/api/v1/directories", expectedStatus: http.StatusCreated,
				body: func(sequence int64) []byte {
					payload, _ := json.Marshal(map[string]string{
						"parent_id": options.WriteParentID,
						"name":      fmt.Sprintf("slo-%s-%010d", runID, sequence),
					})
					return payload
				},
			})
		}
	}
	if err := runPhase(ctx, options, endpoints, options.Warmup, nil); err != nil {
		return SLOReport{}, err
	}
	var samplesMu sync.Mutex
	samples := make([]sample, 0, options.Rate*int(options.Duration/time.Second)+1)
	var dropped int64
	collect := func(value sample) {
		samplesMu.Lock()
		samples = append(samples, value)
		samplesMu.Unlock()
	}
	if err := runPhase(ctx, options, endpoints, options.Duration, collectWithDrop{onCollect: collect, dropped: &dropped}); err != nil {
		return SLOReport{}, err
	}
	finished := time.Now().UTC()
	report := SLOReport{StartedAt: started, FinishedAt: finished, DurationSeconds: options.Duration.Seconds(),
		WarmupSeconds: options.Warmup.Seconds(), Rate: options.Rate, Concurrency: options.Concurrency,
		Dropped: dropped, Endpoints: make(map[string]EndpointReport, len(endpoints))}
	for _, current := range samples {
		report.Requests++
		if current.status >= 500 {
			report.ServerErrors++
		}
		if current.status != current.expectedStatus {
			report.UnexpectedStatuses++
		}
	}
	if report.Requests > 0 {
		report.ServerErrorRate = float64(report.ServerErrors) / float64(report.Requests)
	}
	for _, current := range endpoints {
		filtered := make([]time.Duration, 0)
		var serverErrors, unexpected int64
		for _, value := range samples {
			if value.endpoint != current.name {
				continue
			}
			filtered = append(filtered, value.duration)
			if value.status >= 500 {
				serverErrors++
			}
			if value.status != value.expectedStatus {
				unexpected++
			}
		}
		report.Endpoints[current.name] = endpointStats(filtered, serverErrors, unexpected)
	}
	return report, nil
}

type dropCollector interface {
	collect(sample)
	drop()
}

type collectWithDrop struct {
	onCollect func(sample)
	dropped   *int64
}

func (c collectWithDrop) collect(value sample) { c.onCollect(value) }
func (c collectWithDrop) drop() {
	// The phase owns dispatching and calls this only for a request that could
	// not be queued at the configured rate. There is one dispatcher goroutine.
	*c.dropped = *c.dropped + 1
}

func runPhase(ctx context.Context, options SLOOptions, endpoints []endpoint, duration time.Duration, collector dropCollector) error {
	if duration <= 0 {
		return nil
	}
	phaseCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	jobs := make(chan requestJob, options.Concurrency*2)
	var workers sync.WaitGroup
	for i := 0; i < options.Concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-phaseCtx.Done():
					return
				case job := <-jobs:
					if job.endpoint.name == "" {
						return
					}
					current := job.endpoint
					requestCtx, requestCancel := context.WithTimeout(phaseCtx, options.Timeout)
					var body *bytes.Reader
					if current.body != nil {
						body = bytes.NewReader(current.body(job.sequence))
					} else {
						body = bytes.NewReader(nil)
					}
					request, err := http.NewRequestWithContext(requestCtx, current.method, strings.TrimRight(options.BaseURL, "/")+current.path, body)
					if err != nil {
						requestCancel()
						continue
					}
					if options.Token != "" {
						request.Header.Set("Authorization", "Bearer "+options.Token)
					}
					if current.body != nil {
						request.Header.Set("Content-Type", "application/json")
					}
					begin := time.Now()
					response, requestErr := options.Client.Do(request)
					elapsed := time.Since(begin)
					status := 599
					if requestErr == nil {
						status = response.StatusCode
						response.Body.Close()
					}
					requestCancel()
					if requestErr != nil && phaseCtx.Err() != nil {
						// The measurement window ended while this request was in
						// flight. It is not a server failure and is excluded.
						continue
					}
					if requestErr != nil {
						status = 599
					}
					if collector != nil {
						collector.collect(sample{endpoint: current.name, expectedStatus: current.expectedStatus, duration: elapsed, status: status})
					}
				}
			}
		}()
	}
	ticker := time.NewTicker(time.Second / time.Duration(options.Rate))
	defer ticker.Stop()
	index := 0
	var sequence int64
dispatch:
	for {
		select {
		case <-phaseCtx.Done():
			break dispatch
		case <-ticker.C:
			current := endpoints[index%len(endpoints)]
			index++
			sequence++
			select {
			case jobs <- requestJob{endpoint: current, sequence: sequence}:
			default:
				if collector != nil {
					collector.drop()
				}
			}
		}
	}
	cancel()
	workers.Wait()
	return nil
}

func endpointStats(values []time.Duration, serverErrors, unexpected int64) EndpointReport {
	if len(values) == 0 {
		return EndpointReport{ServerErrors: serverErrors, Unexpected: unexpected}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(fraction float64) float64 {
		position := fraction * float64(len(values)-1)
		lower := int(position)
		upper := lower + 1
		if upper >= len(values) {
			return float64(values[lower]) / float64(time.Millisecond)
		}
		weight := position - float64(lower)
		return (float64(values[lower])*(1-weight) + float64(values[upper])*weight) / float64(time.Millisecond)
	}
	return EndpointReport{Requests: int64(len(values)), ServerErrors: serverErrors, Unexpected: unexpected,
		P50Milliseconds: percentile(0.50), P95Milliseconds: percentile(0.95), P99Milliseconds: percentile(0.99),
		MaxMilliseconds: float64(values[len(values)-1]) / float64(time.Millisecond)}
}
