package investigation

import (
	"context"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/diagnostic"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// RunbookInvestigator wraps the built-in RolloutAnalyzer as an Investigator.
type RunbookInvestigator struct {
	analyzer *diagnostic.RolloutAnalyzer
}

// NewRunbookInvestigator creates a RunbookInvestigator backed by the given analyzer.
func NewRunbookInvestigator(analyzer *diagnostic.RolloutAnalyzer) *RunbookInvestigator {
	return &RunbookInvestigator{analyzer: analyzer}
}

// Investigate runs the built-in rollout analyzer against the event and returns a diagnostic report.
func (r *RunbookInvestigator) Investigate(ctx context.Context, event models.RolloutEvent) (*diagnostic.DiagnosticReport, error) {
	return r.analyzer.Analyze(ctx, event)
}
