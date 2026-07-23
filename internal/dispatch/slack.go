package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// SlackTarget sends rollout notifications to a Slack webhook.
type SlackTarget struct {
	webhookURL string
	client     *http.Client
}

// NewSlackTarget creates a SlackTarget that posts rollout notifications to the given webhook URL.
func NewSlackTarget(webhookURL string, client *http.Client) *SlackTarget {
	return &SlackTarget{
		webhookURL: webhookURL,
		client:     client,
	}
}

// Name returns the target identifier "slack".
func (s *SlackTarget) Name() string { return "slack" }

// slackMessage is the JSON payload sent to a Slack incoming webhook.
type slackMessage struct {
	Text string `json:"text"`
}

// Dispatch sends a formatted rollout notification to the configured Slack webhook.
func (s *SlackTarget) Dispatch(ctx context.Context, event models.RolloutEvent) error {
	text := fmt.Sprintf(
		"*Rollout detected:* `%s` (`%s`) on *%s*\n%s → %s",
		event.DeploymentName,
		event.Namespace,
		event.ClusterID,
		strings.Join(event.OldImages, ", "),
		strings.Join(event.NewImages, ", "),
	)

	body, err := json.Marshal(slackMessage{Text: text})
	if err != nil {
		return fmt.Errorf("marshalling slack message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack webhook failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}

	slog.Info("dispatched to slack",
		"cluster", event.ClusterID,
		"deployment", event.Namespace+"/"+event.DeploymentName,
	)
	return nil
}
