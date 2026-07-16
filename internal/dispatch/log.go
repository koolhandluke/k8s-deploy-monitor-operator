package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// LogTarget logs rollout events to stdout.
type LogTarget struct{}

func (l *LogTarget) Name() string { return "log" }

func (l *LogTarget) Dispatch(_ context.Context, event models.RolloutEvent) error {
	slog.Info("ROLLOUT EVENT",
		"cluster", event.ClusterName,
		"namespace", event.Namespace,
		"deployment", event.DeploymentName,
		"images", fmt.Sprintf("%s -> %s",
			strings.Join(event.OldImages, ","),
			strings.Join(event.NewImages, ",")),
		"template_hash", fmt.Sprintf("%.12s -> %.12s", event.OldTemplateHash, event.NewTemplateHash),
	)
	return nil
}
