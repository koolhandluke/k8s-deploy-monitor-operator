package dispatch

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// rolloutRecordGVR is the GroupVersionResource for RolloutRecord CRDs.
var rolloutRecordGVR = schema.GroupVersionResource{
	Group:    "deploy-monitor.io",
	Version:  "v1alpha1",
	Resource: "rolloutrecords",
}

// RecordWatcher watches RolloutRecord CRDs and dispatches events using optimistic locking.
type RecordWatcher struct {
	dynClient  dynamic.Interface
	ctrlClient client.Client
	dispatcher *Dispatcher
	namespace  string
	stuckTimeout time.Duration
	stopCh     chan struct{}
}

// NewRecordWatcher creates a watcher that processes RolloutRecord CRDs.
func NewRecordWatcher(dynClient dynamic.Interface, ctrlClient client.Client, dispatcher *Dispatcher, namespace string) *RecordWatcher {
	return &RecordWatcher{
		dynClient:    dynClient,
		ctrlClient:   ctrlClient,
		dispatcher:   dispatcher,
		namespace:    namespace,
		stuckTimeout: 10 * time.Minute,
		stopCh:       make(chan struct{}),
	}
}

// Start begins watching RolloutRecord resources and processing them.
func (rw *RecordWatcher) Start(ctx context.Context) {
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		rw.dynClient, 30*time.Second, rw.namespace, nil,
	)
	informer := factory.ForResource(rolloutRecordGVR).Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			rw.handleRecord(ctx, obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			rw.handleRecord(ctx, newObj)
		},
	})

	// Start stuck record recovery
	go rw.recoverStuckRecords(ctx)

	factory.Start(rw.stopCh)
	factory.WaitForCacheSync(rw.stopCh)

	slog.Info("record watcher started", "namespace", rw.namespace)
}

// Stop stops the record watcher.
func (rw *RecordWatcher) Stop() {
	close(rw.stopCh)
	slog.Info("record watcher stopped")
}

// handleRecord processes a single RolloutRecord: claims it, dispatches the event, and updates status.
func (rw *RecordWatcher) handleRecord(ctx context.Context, obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}

	// Only process records with phase Detected
	phase := getNestedString(u, "status", "phase")
	if phase != string(v1alpha1.PhaseDetected) {
		return
	}

	// Attempt to claim via optimistic lock: update phase to Processing
	name := u.GetName()
	ns := u.GetNamespace()

	if !rw.claimRecord(ctx, u) {
		slog.Debug("record already claimed", "name", name, "namespace", ns)
		return
	}

	// Convert to RolloutEvent
	event := rw.toRolloutEvent(u)

	// Dispatch
	targetNames, dispatchErr := rw.dispatcher.DispatchEvent(ctx, event)

	// Update final status
	rw.updateRecordStatus(ctx, name, ns, targetNames, dispatchErr)
}

// claimRecord attempts to set phase to Processing using optimistic locking.
// Returns true if claim succeeded, false if conflict (another replica got it).
func (rw *RecordWatcher) claimRecord(ctx context.Context, u *unstructured.Unstructured) bool {
	// Deep copy to avoid mutating informer cache
	claim := u.DeepCopy()

	// Set status.phase to Processing
	status, ok := claim.Object["status"].(map[string]interface{})
	if !ok {
		status = make(map[string]interface{})
		claim.Object["status"] = status
	}
	status["phase"] = string(v1alpha1.PhaseProcessing)

	// Update via dynamic client — resourceVersion ensures compare-and-swap
	_, err := rw.dynClient.Resource(rolloutRecordGVR).Namespace(claim.GetNamespace()).
		UpdateStatus(ctx, claim, metav1Options())
	if err != nil {
		// 409 Conflict means another replica claimed it
		slog.Debug("claim failed (likely conflict)", "name", claim.GetName(), "error", err)
		return false
	}

	slog.Info("claimed record for processing", "name", claim.GetName())
	return true
}

// toRolloutEvent converts an unstructured RolloutRecord into a RolloutEvent.
func (rw *RecordWatcher) toRolloutEvent(u *unstructured.Unstructured) models.RolloutEvent {
	spec, _ := u.Object["spec"].(map[string]interface{})

	detectedAtStr, _ := spec["detectedAt"].(string)
	detectedAt, _ := time.Parse(time.RFC3339, detectedAtStr)

	return models.RolloutEvent{
		ClusterID:       getStr(spec, "clusterID"),
		ClusterName:     getStr(spec, "clusterName"),
		Namespace:       getStr(spec, "namespace"),
		DeploymentName:  getStr(spec, "deployment"),
		OldTemplateHash: getStr(spec, "oldTemplateHash"),
		NewTemplateHash: getStr(spec, "newTemplateHash"),
		OldImages:       getStrSlice(spec, "oldImages"),
		NewImages:       getStrSlice(spec, "newImages"),
		Timestamp:       detectedAt,
	}
}

// updateRecordStatus sets the final phase, dispatch targets, and error on a RolloutRecord.
func (rw *RecordWatcher) updateRecordStatus(ctx context.Context, name, ns string, targets []string, dispatchErr string) {
	// Get the latest version
	record, err := rw.dynClient.Resource(rolloutRecordGVR).Namespace(ns).Get(ctx, name, metav1GetOptions())
	if err != nil {
		slog.Warn("failed to get record for status update", "name", name, "error", err)
		return
	}

	status, ok := record.Object["status"].(map[string]interface{})
	if !ok {
		status = make(map[string]interface{})
		record.Object["status"] = status
	}

	if dispatchErr != "" {
		status["phase"] = string(v1alpha1.PhaseFailed)
		status["error"] = dispatchErr
	} else {
		status["phase"] = string(v1alpha1.PhaseDispatched)
	}
	status["dispatchedAt"] = time.Now().UTC().Format(time.RFC3339)
	status["dispatchTargets"] = toInterfaceSlice(targets)

	_, err = rw.dynClient.Resource(rolloutRecordGVR).Namespace(ns).
		UpdateStatus(ctx, record, metav1Options())
	if err != nil {
		slog.Warn("failed to update record status", "name", name, "error", err)
	}
}

// recoverStuckRecords periodically scans for records stuck in Processing and resets them.
func (rw *RecordWatcher) recoverStuckRecords(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-rw.stopCh:
			return
		case <-ticker.C:
			rw.doRecoverStuck(ctx)
		}
	}
}

// doRecoverStuck lists Processing records and resets any that exceed the stuck timeout to Detected.
func (rw *RecordWatcher) doRecoverStuck(ctx context.Context) {
	list, err := rw.dynClient.Resource(rolloutRecordGVR).Namespace(rw.namespace).
		List(ctx, metav1ListOptions())
	if err != nil {
		slog.Warn("failed to list records for stuck recovery", "error", err)
		return
	}

	now := time.Now()
	for i := range list.Items {
		item := &list.Items[i]
		phase := getNestedString(item, "status", "phase")
		if phase != string(v1alpha1.PhaseProcessing) {
			continue
		}

		// Check if it's been stuck longer than timeout
		creationTime := item.GetCreationTimestamp().Time
		// Use dispatchedAt if set, otherwise creationTimestamp as proxy
		if elapsed := now.Sub(creationTime); elapsed < rw.stuckTimeout {
			continue
		}

		// Reset to Detected
		status, ok := item.Object["status"].(map[string]interface{})
		if !ok {
			continue
		}
		status["phase"] = string(v1alpha1.PhaseDetected)

		_, err := rw.dynClient.Resource(rolloutRecordGVR).Namespace(rw.namespace).
			UpdateStatus(ctx, item, metav1Options())
		if err != nil {
			slog.Debug("failed to reset stuck record", "name", item.GetName(), "error", err)
		} else {
			slog.Info("reset stuck record to Detected", "name", item.GetName())
		}
	}
}

// getNestedString traverses nested maps in an Unstructured object and returns the string at the given path.
func getNestedString(u *unstructured.Unstructured, fields ...string) string {
	obj := u.Object
	for i, f := range fields {
		if i == len(fields)-1 {
			s, _ := obj[f].(string)
			return s
		}
		obj, _ = obj[f].(map[string]interface{})
		if obj == nil {
			return ""
		}
	}
	return ""
}

// getStr returns the string value for key in m, or "" if missing or not a string.
func getStr(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// getStrSlice returns the []string value for key in m, converting from []interface{}.
func getStrSlice(m map[string]interface{}, key string) []string {
	v, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(v))
	for _, item := range v {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// toInterfaceSlice converts a []string to []interface{} for use in unstructured objects.
func toInterfaceSlice(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
