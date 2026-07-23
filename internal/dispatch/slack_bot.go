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

// SlackBotTarget sends rollout notifications to per-channel Slack destinations
// using the Slack Web API (chat.postMessage) with a bot token.
type SlackBotTarget struct {
	botToken string
	client   *http.Client
	apiURL   string // overridable for testing; defaults to https://slack.com/api/chat.postMessage
}

// NewSlackBotTarget creates a SlackBotTarget that posts rollout notifications
// to the Slack channel specified in each event's SlackChannel field.
func NewSlackBotTarget(botToken string, client *http.Client) *SlackBotTarget {
	return &SlackBotTarget{
		botToken: botToken,
		client:   client,
		apiURL:   "https://slack.com/api/chat.postMessage",
	}
}

// Name returns the target identifier "slack-bot".
func (s *SlackBotTarget) Name() string { return "slack-bot" }

// slackBotMessage is the JSON payload sent to the Slack Web API.
type slackBotMessage struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// Dispatch sends a formatted rollout notification to the event's SlackChannel.
// If SlackChannel is empty, the event is skipped (no-op).
func (s *SlackBotTarget) Dispatch(ctx context.Context, event models.RolloutEvent) error {
	if event.SlackChannel == "" {
		return nil
	}

	text := fmt.Sprintf(
		"*Rollout detected:* `%s` (`%s`) on *%s*\n%s → %s",
		event.DeploymentName,
		event.Namespace,
		event.ClusterID,
		strings.Join(event.OldImages, ", "),
		strings.Join(event.NewImages, ", "),
	)

	body, _ := json.Marshal(slackBotMessage{
		Channel: event.SlackChannel,
		Text:    text,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack bot request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.botToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack bot API failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack bot API returned %d", resp.StatusCode)
	}

	slog.Info("dispatched to slack channel",
		"channel", event.SlackChannel,
		"cluster", event.ClusterID,
		"deployment", event.Namespace+"/"+event.DeploymentName,
	)
	return nil
}
