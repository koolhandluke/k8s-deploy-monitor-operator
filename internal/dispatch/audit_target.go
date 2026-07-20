package dispatch

import (
	"context"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/persistence"
)

// AuditTarget implements Target by recording rollout events as CRDs.
type AuditTarget struct {
	recorder *persistence.AuditRecorder
}

// NewAuditTarget creates an AuditTarget that writes events using the given recorder.
func NewAuditTarget(recorder *persistence.AuditRecorder) *AuditTarget {
	return &AuditTarget{recorder: recorder}
}

// Name returns the target identifier "audit".
func (a *AuditTarget) Name() string { return "audit" }

// Dispatch records the rollout event as a RolloutRecord CRD.
func (a *AuditTarget) Dispatch(ctx context.Context, event models.RolloutEvent) error {
	return a.recorder.RecordRollout(ctx, event)
}
