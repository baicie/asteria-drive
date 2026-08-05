package observability

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry            *prometheus.Registry
	httpRequests        *prometheus.CounterVec
	httpDuration        *prometheus.HistogramVec
	httpInFlight        prometheus.Gauge
	storageOperations   *prometheus.CounterVec
	storageDuration     *prometheus.HistogramVec
	maintenanceRuns     *prometheus.CounterVec
	maintenanceDuration *prometheus.HistogramVec
	maintenanceBacklog  *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "asteria", Subsystem: "http", Name: "requests_total",
			Help: "Completed HTTP requests by method, route template, and status class.",
		}, []string{"method", "route", "status_class"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "asteria", Subsystem: "http", Name: "request_duration_seconds",
			Help:    "HTTP request duration by method and route template.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"method", "route"}),
		httpInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "asteria", Subsystem: "http", Name: "in_flight_requests",
			Help: "Current number of HTTP requests being served.",
		}),
		storageOperations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "asteria", Subsystem: "storage", Name: "operations_total",
			Help: "S3 control-plane operations by operation and bounded result class.",
		}, []string{"operation", "result"}),
		storageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "asteria", Subsystem: "storage", Name: "operation_duration_seconds",
			Help:    "S3 control-plane operation duration by operation.",
			Buckets: prometheus.DefBuckets,
		}, []string{"operation"}),
		maintenanceRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "asteria", Subsystem: "maintenance", Name: "runs_total",
			Help: "Maintenance task runs by task and result.",
		}, []string{"task", "result"}),
		maintenanceDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "asteria", Subsystem: "maintenance", Name: "run_duration_seconds",
			Help:    "Maintenance task duration by task.",
			Buckets: prometheus.DefBuckets,
		}, []string{"task"}),
		maintenanceBacklog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "asteria", Subsystem: "maintenance", Name: "backlog_items",
			Help: "Current maintenance backlog by bounded task name.",
		}, []string{"task"}),
	}
	metrics.registry.MustRegister(
		metrics.httpRequests, metrics.httpDuration, metrics.httpInFlight,
		metrics.storageOperations, metrics.storageDuration,
		metrics.maintenanceRuns, metrics.maintenanceDuration, metrics.maintenanceBacklog,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return metrics
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (m *Metrics) HTTPRequestStarted() { m.httpInFlight.Inc() }

func (m *Metrics) HTTPRequestFinished(method, route string, status int, duration time.Duration) {
	m.httpInFlight.Dec()
	if route == "" {
		route = "unmatched"
	}
	statusClass := strconv.Itoa(status/100) + "xx"
	m.httpRequests.WithLabelValues(method, route, statusClass).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(duration.Seconds())
}

func (m *Metrics) ObserveMaintenance(task, result string, duration time.Duration) {
	m.maintenanceRuns.WithLabelValues(task, result).Inc()
	m.maintenanceDuration.WithLabelValues(task).Observe(duration.Seconds())
}

func (m *Metrics) SetMaintenanceBacklog(task string, count int) {
	m.maintenanceBacklog.WithLabelValues(task).Set(float64(count))
}

func (m *Metrics) observeStorage(operation string, started time.Time, err error) {
	m.storageOperations.WithLabelValues(operation, storageResult(err)).Inc()
	m.storageDuration.WithLabelValues(operation).Observe(time.Since(started).Seconds())
}

func storageResult(err error) string {
	if err == nil {
		return "success"
	}
	switch drive.CodeOf(err) {
	case drive.CodeNotFound:
		return "not_found"
	case drive.CodeInvalidRequest, drive.CodeInvalidState:
		return "rejected"
	case drive.CodeDependencyUnavailable:
		return "unavailable"
	default:
		return "error"
	}
}

type instrumentedStorage struct {
	drive.StorageProvider
	metrics *Metrics
}

func InstrumentStorage(storage drive.StorageProvider, metrics *Metrics) drive.StorageProvider {
	if storage == nil || metrics == nil {
		return storage
	}
	return &instrumentedStorage{StorageProvider: storage, metrics: metrics}
}

func (s *instrumentedStorage) Ready(ctx context.Context) (err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("ready", started, err) }()
	return s.StorageProvider.Ready(ctx)
}

func (s *instrumentedStorage) CreateMultipart(ctx context.Context, key, mediaType string, checksum drive.Checksum) (id string, err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("create_multipart", started, err) }()
	return s.StorageProvider.CreateMultipart(ctx, key, mediaType, checksum)
}

func (s *instrumentedStorage) SignUploadPart(ctx context.Context, key, uploadID string, part int, checksum drive.Checksum, ttl time.Duration) (signed drive.SignedPart, err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("sign_upload_part", started, err) }()
	return s.StorageProvider.SignUploadPart(ctx, key, uploadID, part, checksum, ttl)
}

func (s *instrumentedStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []drive.CompletedPart) (object drive.ObjectInfo, err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("complete_multipart", started, err) }()
	return s.StorageProvider.CompleteMultipart(ctx, key, uploadID, parts)
}

func (s *instrumentedStorage) AbortMultipart(ctx context.Context, key, uploadID string) (err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("abort_multipart", started, err) }()
	return s.StorageProvider.AbortMultipart(ctx, key, uploadID)
}

func (s *instrumentedStorage) StatObject(ctx context.Context, key string) (object drive.ObjectInfo, err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("stat_object", started, err) }()
	return s.StorageProvider.StatObject(ctx, key)
}

func (s *instrumentedStorage) SignDownload(ctx context.Context, key, filename string, ttl time.Duration) (signed drive.SignedDownload, err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("sign_download", started, err) }()
	return s.StorageProvider.SignDownload(ctx, key, filename, ttl)
}

func (s *instrumentedStorage) DeleteObject(ctx context.Context, key string) (err error) {
	started := time.Now()
	defer func() { s.metrics.observeStorage("delete_object", started, err) }()
	return s.StorageProvider.DeleteObject(ctx, key)
}

var _ drive.StorageProvider = (*instrumentedStorage)(nil)
