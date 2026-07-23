package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// HashObserver receives template hash changes for persistence.
type HashObserver interface {
	OnHashUpdate(clusterID, deployKey, hash string)
	OnHashDelete(clusterID, deployKey string)
}

// ClusterWatcher watches deployments on a single cluster using SharedInformerFactory.
type ClusterWatcher struct {
	clusterID      string
	clientset      kubernetes.Interface
	debouncer      *Debouncer
	nsFilter       func(string) bool
	eventEnricher  func(*models.RolloutEvent) // sets App + SlackChannel; nil-safe

	templateCache map[string]string // key: clusterID/namespace/name -> SHA256
	mu            sync.Mutex
	factory       informers.SharedInformerFactory
	cancel        context.CancelFunc

	hashObserver HashObserver
	startTimeout time.Duration

	// Health monitoring
	consecutiveErrors int64        // atomic, incremented by watch error handler
	lastSuccessTime   atomic.Value // time.Time, updated on every event handler call
	lastWatchError    atomic.Value // error, set by watch error handler
	permanent         atomic.Bool  // true if 401/403 detected
}

// NewClusterWatcher creates a watcher for a single cluster that detects
// deployment rollouts by tracking spec.template hash changes.
func NewClusterWatcher(
	clusterID string,
	clientset kubernetes.Interface,
	debouncer *Debouncer,
	nsFilter func(string) bool,
	hashObserver HashObserver,
	startTimeout time.Duration,
) *ClusterWatcher {
	return &ClusterWatcher{
		clusterID:     clusterID,
		clientset:     clientset,
		debouncer:     debouncer,
		nsFilter:      nsFilter,
		templateCache: make(map[string]string),
		hashObserver:  hashObserver,
		startTimeout:  startTimeout,
	}
}

// SeedHashes loads persisted hashes into the template cache before starting the informer.
// This enables gap detection — if a deployment changed while the monitor was down,
// the first update event will detect the hash mismatch.
func (w *ClusterWatcher) SeedHashes(hashes map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for deployKey, hash := range hashes {
		fullKey := w.clusterID + "/" + deployKey
		w.templateCache[fullKey] = hash
	}
	slog.Info("seeded persisted hashes", "cluster", w.clusterID, "count", len(hashes))
}

// Start begins watching deployments. Blocks until ctx is cancelled or cache sync fails.
func (w *ClusterWatcher) Start(ctx context.Context) error {
	ctx, w.cancel = context.WithCancel(ctx)

	// Step 1: Create factory with transform to strip unneeded fields
	w.factory = informers.NewSharedInformerFactoryWithOptions(
		w.clientset,
		0, // resync disabled
		informers.WithTransform(stripUnneededFields),
	)

	// Step 2: Get informer BEFORE starting
	deployInformer := w.factory.Apps().V1().Deployments().Informer()

	// Step 3: Set watch error handler BEFORE starting
	deployInformer.SetWatchErrorHandler(func(r *cache.Reflector, err error) {
		atomic.AddInt64(&w.consecutiveErrors, 1)
		w.lastWatchError.Store(err)
		if apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
			w.permanent.Store(true)
			slog.Error("permanent watch error — credentials invalid",
				"cluster", w.clusterID, "error", err)
		} else {
			slog.Warn("watch error (will reconnect)",
				"cluster", w.clusterID, "error", err)
		}
	})

	// Step 4: Add event handlers BEFORE starting
	_, err := deployInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    w.onAdd,
		UpdateFunc: w.onUpdate,
		DeleteFunc: w.onDelete,
	})
	if err != nil {
		return fmt.Errorf("add event handler for cluster %s: %w", w.clusterID, err)
	}

	// Step 5: Start (non-blocking)
	w.factory.Start(ctx.Done())

	// Step 6: Wait for cache sync with timeout
	syncCtx := ctx
	if w.startTimeout > 0 {
		var syncCancel context.CancelFunc
		syncCtx, syncCancel = context.WithTimeout(ctx, w.startTimeout)
		defer syncCancel()
	}
	if !cache.WaitForCacheSync(syncCtx.Done(), deployInformer.HasSynced) {
		return fmt.Errorf("cache sync timed out for cluster %s", w.clusterID)
	}

	slog.Info("cluster watcher started", "cluster", w.clusterID)
	return nil
}

// Stop shuts down the cluster watcher.
func (w *ClusterWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	if w.factory != nil {
		w.factory.Shutdown()
	}
}

// HealthStatus returns the health state of the watcher.
// Unhealthy if consecutiveErrors >= 5 or a permanent auth error was detected.
func (w *ClusterWatcher) HealthStatus() (healthy bool, permanentErr bool, lastErr error) {
	perm := w.permanent.Load()
	errs := atomic.LoadInt64(&w.consecutiveErrors)
	var lastE error
	if v := w.lastWatchError.Load(); v != nil {
		lastE = v.(error)
	}
	return errs < 5 && !perm, perm, lastE
}

// resetHealthCounters resets the consecutive error counter and updates lastSuccessTime.
func (w *ClusterWatcher) resetHealthCounters() {
	atomic.StoreInt64(&w.consecutiveErrors, 0)
	w.lastSuccessTime.Store(time.Now())
}

// onAdd seeds the template cache on initial LIST (baseline — not a rollout).
func (w *ClusterWatcher) onAdd(obj interface{}) {
	deploy, ok := obj.(*appsv1.Deployment)
	if !ok {
		return
	}
	if !w.nsFilter(deploy.Namespace) {
		return
	}

	w.resetHealthCounters()

	fullKey := w.clusterID + "/" + deploy.Namespace + "/" + deploy.Name
	deployKey := deploy.Namespace + "/" + deploy.Name
	hash := templateHash(deploy)

	w.mu.Lock()
	w.templateCache[fullKey] = hash
	w.mu.Unlock()

	if w.hashObserver != nil {
		w.hashObserver.OnHashUpdate(w.clusterID, deployKey, hash)
	}
}

// onUpdate detects rollouts by comparing spec.template hashes.
func (w *ClusterWatcher) onUpdate(oldObj, newObj interface{}) {
	newDeploy, ok := newObj.(*appsv1.Deployment)
	if !ok {
		return
	}
	if !w.nsFilter(newDeploy.Namespace) {
		return
	}

	w.resetHealthCounters()

	fullKey := w.clusterID + "/" + newDeploy.Namespace + "/" + newDeploy.Name
	deployKey := newDeploy.Namespace + "/" + newDeploy.Name
	newHash := templateHash(newDeploy)

	w.mu.Lock()
	oldHash, exists := w.templateCache[fullKey]
	w.templateCache[fullKey] = newHash
	w.mu.Unlock()

	// Persist the new hash
	if w.hashObserver != nil {
		w.hashObserver.OnHashUpdate(w.clusterID, deployKey, newHash)
	}

	if !exists {
		slog.Debug("update for unseen deployment, seeding cache",
			"cluster", w.clusterID,
			"deployment", deployKey,
			"hash", newHash[:12],
		)
		return
	}
	if newHash == oldHash {
		slog.Debug("update with unchanged template hash, skipping",
			"cluster", w.clusterID,
			"deployment", deployKey,
			"hash", newHash[:12],
		)
		return
	}

	oldDeploy, _ := oldObj.(*appsv1.Deployment)

	event := models.RolloutEvent{
		ClusterID:       w.clusterID,
		Namespace:       newDeploy.Namespace,
		DeploymentName:  newDeploy.Name,
		OldTemplateHash: oldHash,
		NewTemplateHash: newHash,
		OldImages:       extractImages(oldDeploy),
		NewImages:       extractImages(newDeploy),
		Timestamp:       time.Now(),
	}

	if w.eventEnricher != nil {
		w.eventEnricher(&event)
	}

	slog.Info("rollout detected",
		"cluster", w.clusterID,
		"app", event.App,
		"deployment", newDeploy.Namespace+"/"+newDeploy.Name,
		"images", fmt.Sprintf("%v -> %v", event.OldImages, event.NewImages),
	)

	w.debouncer.Submit(fullKey, event)
}

// onDelete removes the deployment from the template cache to prevent unbounded growth.
func (w *ClusterWatcher) onDelete(obj interface{}) {
	w.resetHealthCounters()

	// Unwrap tombstones
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	deploy, ok := obj.(*appsv1.Deployment)
	if !ok {
		return
	}

	fullKey := w.clusterID + "/" + deploy.Namespace + "/" + deploy.Name
	deployKey := deploy.Namespace + "/" + deploy.Name

	w.mu.Lock()
	delete(w.templateCache, fullKey)
	w.mu.Unlock()

	if w.hashObserver != nil {
		w.hashObserver.OnHashDelete(w.clusterID, deployKey)
	}
}

// templateHash computes SHA256 of the deployment's pod template spec.
func templateHash(deploy *appsv1.Deployment) string {
	data, _ := json.Marshal(deploy.Spec.Template)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// extractImages returns the container image refs from a deployment.
func extractImages(deploy *appsv1.Deployment) []string {
	if deploy == nil {
		return nil
	}
	containers := deploy.Spec.Template.Spec.Containers
	images := make([]string, 0, len(containers))
	for _, c := range containers {
		images = append(images, c.Image)
	}
	return images
}

// stripUnneededFields removes managedFields and last-applied-configuration
// to reduce memory usage across many clusters.
func stripUnneededFields(obj interface{}) (interface{}, error) {
	if acc, ok := obj.(metav1.Object); ok {
		acc.SetManagedFields(nil)
		annotations := acc.GetAnnotations()
		if annotations != nil {
			delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
			acc.SetAnnotations(annotations)
		}
	}
	return obj, nil
}
