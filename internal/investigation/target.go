package investigation

import (
	"context"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// InvestigationTarget implements dispatch.Target by delegating to the Orchestrator.
// Dispatch returns nil immediately; the investigation runs async.
type InvestigationTarget struct {
	orchestrator *Orchestrator
}

// NewInvestigationTarget creates a dispatch target backed by the orchestrator.
func NewInvestigationTarget(orchestrator *Orchestrator) *InvestigationTarget {
	return &InvestigationTarget{orchestrator: orchestrator}
}

// Name returns the target name used for logging and identification.
func (t *InvestigationTarget) Name() string {
	return "investigation"
}

// Dispatch starts an asynchronous investigation for the event and returns nil immediately.
func (t *InvestigationTarget) Dispatch(_ context.Context, event models.RolloutEvent) error {
	t.orchestrator.Investigate(event)
	return nil
}
