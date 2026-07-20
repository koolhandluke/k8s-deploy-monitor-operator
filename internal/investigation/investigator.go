package investigation

import (
	"context"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// Investigator is the common interface for rollout investigation backends.
// Both the built-in runbook analyzer and Holmes implement this.
type Investigator interface {
	Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error)
}
