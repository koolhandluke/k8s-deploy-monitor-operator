package persistence

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// AuditRecorder handles creating and updating RolloutRecord CRDs.
type AuditRecorder struct {
	client    client.Client
	namespace string
}

func NewAuditRecorder(c client.Client, namespace string) *AuditRecorder {
	return &AuditRecorder{
		client:    c,
		namespace: namespace,
	}
}

// RecordRollout creates a RolloutRecord CR for the event.
func (r *AuditRecorder) RecordRollout(ctx context.Context, event models.RolloutEvent) error {
	name := recordName(event)

	record := &v1alpha1.RolloutRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.namespace,
			Labels: map[string]string{
				"deploy-monitor.io/cluster":    sanitizeName(event.ClusterID),
				"deploy-monitor.io/namespace":  event.Namespace,
				"deploy-monitor.io/deployment": sanitizeName(event.DeploymentName),
			},
		},
		Spec: v1alpha1.RolloutRecordSpec{
			ClusterID:       event.ClusterID,
			ClusterName:     event.ClusterName,
			Namespace:       event.Namespace,
			Deployment:      event.DeploymentName,
			OldImages:       event.OldImages,
			NewImages:       event.NewImages,
			OldTemplateHash: event.OldTemplateHash,
			NewTemplateHash: event.NewTemplateHash,
			DetectedAt:      metav1.NewTime(event.Timestamp),
		},
		Status: v1alpha1.RolloutRecordStatus{
			Phase: v1alpha1.PhaseDetected,
		},
	}

	if err := r.client.Create(ctx, record); err != nil {
		if errors.IsAlreadyExists(err) {
			slog.Warn("rollout record already exists", "name", name)
			return nil
		}
		return fmt.Errorf("creating rollout record: %w", err)
	}

	slog.Info("rollout record created",
		"name", name,
		"cluster", event.ClusterName,
		"deployment", event.Namespace+"/"+event.DeploymentName,
	)
	return nil
}

// UpdateRecordStatus updates the status of a RolloutRecord after dispatch.
func (r *AuditRecorder) UpdateRecordStatus(ctx context.Context, event models.RolloutEvent, phase v1alpha1.RolloutPhase, targets []string, dispatchErr string) {
	name := recordName(event)

	record := &v1alpha1.RolloutRecord{}
	key := types.NamespacedName{Name: name, Namespace: r.namespace}

	if err := r.client.Get(ctx, key, record); err != nil {
		slog.Warn("failed to get rollout record for status update",
			"name", name, "error", err)
		return
	}

	now := metav1.Now()
	record.Status.Phase = phase
	record.Status.DispatchedAt = &now
	record.Status.DispatchTargets = targets
	record.Status.Error = dispatchErr

	if err := r.client.Status().Update(ctx, record); err != nil {
		slog.Warn("failed to update rollout record status",
			"name", name, "error", err)
	}
}

func recordName(event models.RolloutEvent) string {
	name := fmt.Sprintf("%s-%s-%s-%d",
		sanitizeName(event.ClusterID),
		sanitizeName(event.Namespace),
		sanitizeName(event.DeploymentName),
		event.Timestamp.Unix(),
	)
	// K8s name max is 253 chars
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}
