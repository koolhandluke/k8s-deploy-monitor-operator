package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// HolmesTarget dispatches rollout events to Holmes API for investigation.
type HolmesTarget struct {
	apiURL string
	client *http.Client
}

func NewHolmesTarget(apiURL string) *HolmesTarget {
	return &HolmesTarget{
		apiURL: strings.TrimRight(apiURL, "/"),
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *HolmesTarget) Name() string { return "holmes" }

type holmesChatRequest struct {
	Query string `json:"user_prompt"`
}

func (h *HolmesTarget) Dispatch(ctx context.Context, event models.RolloutEvent) error {
	query := fmt.Sprintf(
		"Deployment %s in namespace %s on cluster %s rolled out: %s -> %s. Analyse the rollout health.",
		event.DeploymentName,
		event.Namespace,
		event.ClusterName,
		strings.Join(event.OldImages, ", "),
		strings.Join(event.NewImages, ", "),
	)

	body := holmesChatRequest{Query: query}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling holmes request: %w", err)
	}

	url := h.apiURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("creating holmes request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		// Retry once after 10s
		slog.Warn("holmes dispatch failed, retrying in 10s", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}

		resp, err = h.client.Do(req)
		if err != nil {
			return fmt.Errorf("holmes dispatch failed after retry: %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("holmes API returned %d", resp.StatusCode)
	}

	slog.Info("dispatched to holmes",
		"cluster", event.ClusterName,
		"deployment", event.Namespace+"/"+event.DeploymentName,
		"status", resp.StatusCode,
	)
	return nil
}
