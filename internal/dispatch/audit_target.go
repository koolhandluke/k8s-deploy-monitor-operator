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

func NewAuditTarget(recorder *persistence.AuditRecorder) *AuditTarget {
	return &AuditTarget{recorder: recorder}
}

func (a *AuditTarget) Name() string { return "audit" }

func (a *AuditTarget) Dispatch(ctx context.Context, event models.RolloutEvent) error {
	return a.recorder.RecordRollout(ctx, event)
}
