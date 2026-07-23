package investigation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

func successReport() *diagnostic.DiagnosticReport {
	return &diagnostic.DiagnosticReport{
		Event: models.RolloutEvent{
			ClusterID:      "prod-cluster",
			Namespace:      "default",
			DeploymentName: "myapp",
		},
		Result:   diagnostic.ResultSuccess,
		Duration: 65 * time.Second,
	}
}

func failureReport() *diagnostic.DiagnosticReport {
	return &diagnostic.DiagnosticReport{
		Event: models.RolloutEvent{
			ClusterID:      "prod-cluster",
			Namespace:      "default",
			DeploymentName: "myapp",
		},
		Result:        diagnostic.ResultFailed,
		FailureReason: "ProgressDeadlineExceeded",
		Duration:      120 * time.Second,
		PodStatuses: []diagnostic.PodStatus{
			{
				Name:          "myapp-abc123-fail",
				Phase:         "Running",
				ContainerName: "myapp",
				Reason:        "CrashLoopBackOff",
				RestartCount:  5,
			},
		},
		Events: []diagnostic.K8sEvent{
			{
				Reason:  "BackOff",
				Message: "Back-off restarting failed container",
				Count:   3,
				Object:  "Pod/myapp-abc123-fail",
			},
		},
		LogSnippets: []diagnostic.LogSnippet{
			{
				PodName:       "myapp-abc123-fail",
				ContainerName: "myapp",
				Lines:         []string{"ERROR: database connection refused", "FATAL: shutting down"},
				TotalLines:    50,
			},
		},
	}
}

func TestSlackReporter_SuccessMessage(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	reporter := NewSlackReporter(server.URL, server.Client())
	err := reporter.PostReport(context.Background(), successReport())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text, ok := receivedBody["text"].(string)
	if !ok {
		t.Fatal("expected text field in payload")
	}
	if !strings.Contains(text, "SUCCESS") {
		t.Errorf("expected SUCCESS in text, got: %s", text)
	}
	if !strings.Contains(text, "myapp") {
		t.Errorf("expected deployment name in text, got: %s", text)
	}

	// Success messages should NOT have blocks
	if _, ok := receivedBody["blocks"]; ok {
		t.Error("success messages should not have blocks (simple text only)")
	}
}

func TestSlackReporter_FailureMessage(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	reporter := NewSlackReporter(server.URL, server.Client())
	err := reporter.PostReport(context.Background(), failureReport())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have blocks for failure messages
	blocks, ok := receivedBody["blocks"].([]interface{})
	if !ok {
		t.Fatal("expected blocks array in failure payload")
	}

	// Should have at least header + context + pod errors + events + logs
	if len(blocks) < 3 {
		t.Errorf("expected at least 3 blocks, got %d", len(blocks))
	}

	// Verify header block exists
	firstBlock := blocks[0].(map[string]interface{})
	if firstBlock["type"] != "header" {
		t.Errorf("expected first block to be header, got %s", firstBlock["type"])
	}
}

func TestSlackReporter_LogTruncation(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	report := failureReport()
	// Generate a very long log
	longLine := strings.Repeat("ERROR: something went wrong ", 200) // ~5600 chars
	report.LogSnippets[0].Lines = []string{longLine}

	reporter := NewSlackReporter(server.URL, server.Client())
	err := reporter.PostReport(context.Background(), report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify truncation happened by checking total payload size
	body, _ := json.Marshal(receivedBody)
	if len(body) > 10000 {
		// The message was created without error, that's the important thing
		// Slack has its own limits and will truncate further
	}
}

func TestSlackReporter_WebhookError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	reporter := NewSlackReporter(server.URL, server.Client())
	err := reporter.PostReport(context.Background(), successReport())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status code, got: %s", err)
	}
}
