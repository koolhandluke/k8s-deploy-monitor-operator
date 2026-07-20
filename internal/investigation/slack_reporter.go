package investigation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/trace"
)

// maxLogChars is the maximum number of characters to include from container logs in a Slack message.
const maxLogChars = 3000

// SlackReporter posts investigation results to Slack using Block Kit formatting.
type SlackReporter struct {
	webhookURL string
	client     *http.Client
}

// NewSlackReporter creates a Slack reporter targeting the given webhook URL.
func NewSlackReporter(webhookURL string, client *http.Client) *SlackReporter {
	return &SlackReporter{
		webhookURL: webhookURL,
		client:     client,
	}
}

// PostReport sends a formatted investigation report to the configured Slack webhook.
func (s *SlackReporter) PostReport(ctx context.Context, report *diagnostic.DiagnosticReport) error {
	var payload map[string]interface{}

	payloadType := "success"
	if report.Result == diagnostic.ResultSuccess {
		payload = s.successMessage(report)
	} else {
		payloadType = "failure"
		payload = s.failureMessage(report)
	}

	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling slack payload: %w", err)
	}

	blockCount := 0
	if blocks, ok := payload["blocks"].([]interface{}); ok {
		blockCount = len(blocks)
	}
	slog.Log(ctx, trace.LevelTrace, "slack report payload constructed",
		"deployment", report.Event.DeploymentKey(),
		"payload_type", payloadType,
		"payload_bytes", len(body),
		"block_count", blockCount,
	)

	// Test mode: dump payload to stdout instead of posting
	if s.webhookURL == "TEST" {
		fmt.Printf("{\"time\":%q,\"level\":\"INFO\",\"msg\":\"investigation report (test mode)\",\"deployment\":%q,\"result\":%q,\"payload\":%s}\n",
			time.Now().UTC().Format(time.RFC3339Nano),
			report.Event.DeploymentKey(),
			string(report.Result),
			body,
		)
		return nil
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

	slog.Info("posted investigation report to slack",
		"deployment", report.Event.DeploymentKey(),
		"result", string(report.Result),
	)
	return nil
}

// successMessage builds a simple Slack text payload for a successful rollout.
func (s *SlackReporter) successMessage(report *diagnostic.DiagnosticReport) map[string]interface{} {
	text := fmt.Sprintf(":white_check_mark: Rollout *SUCCESS*: `%s` (`%s`) on *%s* — completed in %s",
		report.Event.DeploymentName,
		report.Event.Namespace,
		report.Event.ClusterName,
		report.Duration.Round(1e9).String(), // round to seconds
	)
	return map[string]interface{}{
		"text": text,
	}
}

// failureMessage builds a detailed Slack Block Kit payload for a failed or degraded rollout.
func (s *SlackReporter) failureMessage(report *diagnostic.DiagnosticReport) map[string]interface{} {
	blocks := []interface{}{}

	// Header
	emoji := resultEmoji(report.Result)
	headerText := fmt.Sprintf("%s Rollout *%s*: `%s`", emoji, report.Result, report.Event.DeploymentName)
	blocks = append(blocks, block("header", headerText))

	// Context: namespace, cluster, duration, reason
	contextLines := []string{
		fmt.Sprintf("*Namespace:* `%s` | *Cluster:* `%s`", report.Event.Namespace, report.Event.ClusterName),
		fmt.Sprintf("*Duration:* %s", report.Duration.Round(1e9).String()),
	}
	if report.FailureReason != "" {
		contextLines = append(contextLines, fmt.Sprintf("*Reason:* %s", report.FailureReason))
	}
	blocks = append(blocks, sectionBlock(strings.Join(contextLines, "\n")))

	// Pod errors
	if len(report.PodStatuses) > 0 {
		blocks = append(blocks, divider())
		var podLines []string
		for _, ps := range report.PodStatuses {
			if ps.Reason == "" {
				continue
			}
			prefix := ""
			if ps.InitContainer {
				prefix = "(init) "
			}
			podLines = append(podLines, fmt.Sprintf("• `%s` %s`%s`: %s (restarts: %d)",
				ps.Name, prefix, ps.ContainerName, ps.Reason, ps.RestartCount))
		}
		if len(podLines) > 0 {
			blocks = append(blocks, sectionBlock("*Pod Errors:*\n"+strings.Join(podLines, "\n")))
		}
	}

	// Events
	if len(report.Events) > 0 {
		blocks = append(blocks, divider())
		var eventLines []string
		for _, ev := range report.Events {
			line := fmt.Sprintf("• `%s` %s: %s", ev.Object, ev.Reason, ev.Message)
			if ev.Count > 1 {
				line += fmt.Sprintf(" (x%d)", ev.Count)
			}
			eventLines = append(eventLines, line)
		}
		blocks = append(blocks, sectionBlock("*Events:*\n"+strings.Join(eventLines, "\n")))
	}

	// Logs
	if len(report.LogSnippets) > 0 {
		blocks = append(blocks, divider())
		for _, ls := range report.LogSnippets {
			prefix := ""
			if ls.InitContainer {
				prefix = "(init) "
			}
			logLabel := "current"
			if ls.Previous {
				logLabel = "previous"
			}
			header := fmt.Sprintf("*Logs* `%s` %s`%s` [%s]:", ls.PodName, prefix, ls.ContainerName, logLabel)

			logText := strings.Join(ls.Lines, "\n")
			if len(logText) > maxLogChars {
				logText = logText[:maxLogChars] + fmt.Sprintf("\n... truncated (%d total lines)", ls.TotalLines)
			}

			blocks = append(blocks, sectionBlock(header+"\n```\n"+logText+"\n```"))
		}
	}

	return map[string]interface{}{
		"text":   fmt.Sprintf("%s Rollout %s: %s", emoji, report.Result, report.Event.DeploymentKey()),
		"blocks": blocks,
	}
}

// resultEmoji returns a Slack emoji string corresponding to the diagnostic result.
func resultEmoji(r diagnostic.Result) string {
	switch r {
	case diagnostic.ResultFailed:
		return ":x:"
	case diagnostic.ResultStalled:
		return ":warning:"
	case diagnostic.ResultUnstable:
		return ":large_orange_circle:"
	case diagnostic.ResultPaused:
		return ":double_vertical_bar:"
	case diagnostic.ResultDeleted:
		return ":wastebasket:"
	case diagnostic.ResultInconclusive:
		return ":grey_question:"
	default:
		return ":white_check_mark:"
	}
}

// block builds a Slack block of the given type with the provided text content.
func block(blockType, text string) map[string]interface{} {
	if blockType == "header" {
		return map[string]interface{}{
			"type": "header",
			"text": map[string]interface{}{
				"type": "plain_text",
				"text": text,
			},
		}
	}
	return sectionBlock(text)
}

// sectionBlock builds a Slack section block with mrkdwn-formatted text.
func sectionBlock(text string) map[string]interface{} {
	return map[string]interface{}{
		"type": "section",
		"text": map[string]interface{}{
			"type": "mrkdwn",
			"text": text,
		},
	}
}

// divider builds a Slack divider block.
func divider() map[string]interface{} {
	return map[string]interface{}{
		"type": "divider",
	}
}
