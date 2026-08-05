package observability

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type metricsStorage struct {
	drive.StorageProvider
	statErr error
}

func (s *metricsStorage) Bucket() string { return "bucket" }

func (s *metricsStorage) Ready(context.Context) error { return nil }

func (s *metricsStorage) StatObject(context.Context, string) (drive.ObjectInfo, error) {
	return drive.ObjectInfo{}, s.statErr
}

func TestMetricsExposeOnlyBoundedHTTPAndStorageLabels(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics()
	metrics.HTTPRequestStarted()
	metrics.HTTPRequestFinished("GET", "/api/v1/files/{id}", 404, 25*time.Millisecond)
	storage := InstrumentStorage(&metricsStorage{statErr: drive.E(drive.CodeNotFound, "missing")}, metrics)
	if _, err := storage.StatObject(context.Background(), "private/object/key"); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("instrumented storage changed error: %v", err)
	}

	request := httptest.NewRequest("GET", "http://metrics.test/metrics", nil)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, request)
	result := response.Result()
	defer result.Body.Close()
	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, expected := range []string{
		`asteria_http_requests_total{method="GET",route="/api/v1/files/{id}",status_class="4xx"} 1`,
		`asteria_storage_operations_total{operation="stat_object",result="not_found"} 1`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("metrics output is missing %q", expected)
		}
	}
	for _, forbidden := range []string{"private/object/key", "tenant_id", "principal_id", "request_id"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("metrics output contains forbidden high-cardinality value %q", forbidden)
		}
	}
}
