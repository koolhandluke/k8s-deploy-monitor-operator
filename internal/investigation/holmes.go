package investigation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/trace"
)

// HolmesInvestigator calls the Holmes API and maps the response to a DiagnosticReport.
type HolmesInvestigator struct {
	apiURL string
	client *http.Client
}

// NewHolmesInvestigator creates a Holmes-backed investigator.
func NewHolmesInvestigator(apiURL string, client *http.Client) *HolmesInvestigator {
	return &HolmesInvestigator{
		apiURL: strings.TrimRight(apiURL, "/"),
		client: client,
	}
}

// holmesChatRequest is the JSON request body sent to the Holmes /api/chat endpoint.
type holmesChatRequest struct {
	Ask string `json:"ask"`
}

// holmesChatResponse is the JSON response body returned by the Holmes /api/chat endpoint.
type holmesChatResponse struct {
	Analysis string `json:"analysis"`
}

// Investigate sends a rollout event to the Holmes API and maps the response to a DiagnosticReport.
func (h *HolmesInvestigator) Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error) {
	start := time.Now()

	query := fmt.Sprintf(
		"Deployment %s in namespace %s on cluster %s rolled out: %s -> %s. Analyse the rollout health and report any issues.",
		event.DeploymentName,
		event.Namespace,
		event.ClusterID,
		strings.Join(event.OldImages, ", "),
		strings.Join(event.NewImages, ", "),
	)

	body, _ := json.Marshal(holmesChatRequest{Ask: query})
	url := h.apiURL + "/api/chat"

	slog.Log(ctx, trace.LevelTrace, "holmes outgoing query",
		"url", url,
		"query", query,
		"body_bytes", len(body),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating holmes request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		// Retry once after 10s
		slog.Warn("holmes investigation failed, retrying in 10s", "error", err)
		slog.Log(ctx, trace.LevelTrace, "holmes retry attempt", "attempt", 2)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}

		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err = h.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("holmes investigation failed after retry: %w", err)
		}
	}
	defer resp.Body.Close()

	slog.Log(ctx, trace.LevelTrace, "holmes response received",
		"status_code", resp.StatusCode,
	)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("holmes API returned %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading holmes response: %w", err)
	}

	var chatResp holmesChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		// If response isn't structured JSON, use raw text as analysis
		chatResp.Analysis = string(respBody)
	}

	slog.Log(ctx, trace.LevelTrace, "holmes response body",
		"body_bytes", len(respBody),
	)

	// Map Holmes response to DiagnosticReport
	result := diagnostic.ResultSuccess
	if strings.Contains(strings.ToLower(chatResp.Analysis), "fail") ||
		strings.Contains(strings.ToLower(chatResp.Analysis), "error") ||
		strings.Contains(strings.ToLower(chatResp.Analysis), "crash") {
		result = diagnostic.ResultFailed
	}

	slog.Log(ctx, trace.LevelTrace, "holmes result classification",
		"result", string(result),
	)

	return &diagnostic.DiagnosticReport{
		Event:         event,
		Result:        result,
		FailureReason: chatResp.Analysis,
		Duration:      time.Since(start),
	}, nil
}
