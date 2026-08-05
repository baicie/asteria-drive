package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGenerateProducesStableParentBeforeChildTree(t *testing.T) {
	options := TreeOptions{
		TenantID: "tenant", RootID: "root", Count: 7, Fanout: 2, Seed: "fixture",
	}
	var nodes []TreeNode
	if err := Generate(options, func(node TreeNode) error {
		nodes = append(nodes, node)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 7 {
		t.Fatalf("generated %d nodes, want 7", len(nodes))
	}
	parents := map[string]bool{"root": true}
	for _, node := range nodes {
		if !parents[node.ParentID] {
			t.Fatalf("node %d parent %s was not generated first", node.Index, node.ParentID)
		}
		parents[node.ID] = true
	}
	if nodes[0].ID != DeterministicUUID("tenant:fixture", 1) || nodes[0].DisplayName != "fixture-node-0000000001" {
		t.Fatalf("first deterministic node = %+v", nodes[0])
	}
	if nodes[2].ParentID != nodes[0].ID {
		t.Fatalf("third node parent = %s, want %s", nodes[2].ParentID, nodes[0].ID)
	}
	if nodes[0].ID == DeterministicUUID("other-tenant:fixture", 1) {
		t.Fatal("fixture IDs are not isolated by tenant")
	}
}

func TestRunSLOReportsShortAuthenticatedSample(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	report, err := RunSLO(context.Background(), SLOOptions{
		BaseURL: server.URL, Token: "test-token", RootID: "root", Duration: 150 * time.Millisecond,
		Rate: 100, Concurrency: 2, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests == 0 {
		t.Fatal("SLO runner did not issue any requests")
	}
	if report.ServerErrors != 0 || report.UnexpectedStatuses != 0 {
		t.Fatalf("unexpected SLO report: %+v", report)
	}
	for _, name := range []string{"healthz", "tenant", "list_children"} {
		if report.Endpoints[name].Requests == 0 {
			t.Fatalf("endpoint %s was not sampled: %+v", name, report.Endpoints)
		}
	}
}

func TestSLOOptionsRequireAuthenticatedInputs(t *testing.T) {
	options := SLOOptions{BaseURL: "http://example.test", Duration: time.Second, Rate: 1, Concurrency: 1, Timeout: time.Second}
	if err := options.Validate(); err == nil {
		t.Fatal("unauthenticated non-health workload was accepted")
	}
}

func TestRunSLOCanIncludeDedicatedDirectoryWrites(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/directories" {
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["parent_id"] != "write-parent" || body["name"] == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	report, err := RunSLO(context.Background(), SLOOptions{
		BaseURL: server.URL, Token: "test-token", RootID: "root", WriteParentID: "write-parent",
		IncludeDirectoryWrites: true, Duration: 200 * time.Millisecond, Rate: 100, Concurrency: 2, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Endpoints["create_directory"].Requests == 0 || report.UnexpectedStatuses != 0 {
		t.Fatalf("directory writes were not measured successfully: %+v", report)
	}
}
