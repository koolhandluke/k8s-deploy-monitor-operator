package watcher

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
)

var monitorConfigGVR = schema.GroupVersionResource{
	Group:    "deploy-monitor.io",
	Version:  "v1alpha1",
	Resource: "monitorconfigs",
}

// ConfigWatcher watches MonitorConfig CRDs and updates the NamespaceFilter
// when the config changes.
type ConfigWatcher struct {
	filter    *NamespaceFilter
	client    client.Client
	dynClient dynamic.Interface
	stopCh    chan struct{}
}

// NewConfigWatcher creates a watcher that updates filter when MonitorConfig changes.
func NewConfigWatcher(filter *NamespaceFilter, c client.Client, dynClient dynamic.Interface) *ConfigWatcher {
	return &ConfigWatcher{
		filter:    filter,
		client:    c,
		dynClient: dynClient,
		stopCh:    make(chan struct{}),
	}
}

// Start begins watching MonitorConfig resources. It performs an initial sync
// and then watches for changes via a dynamic informer.
func (cw *ConfigWatcher) Start(ctx context.Context) {
	// Initial sync: check if a MonitorConfig "default" exists
	cw.initialSync(ctx)

	// Set up dynamic informer for MonitorConfig (cluster-scoped)
	factory := dynamicinformer.NewDynamicSharedInformerFactory(cw.dynClient, 30*time.Second)
	informer := factory.ForResource(monitorConfigGVR).Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cw.handleConfigChange(ctx, obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			cw.handleConfigChange(ctx, newObj)
		},
	})

	factory.Start(cw.stopCh)
	factory.WaitForCacheSync(cw.stopCh)

	slog.Info("config watcher started, watching MonitorConfig resources")
}

// Stop stops the config watcher.
func (cw *ConfigWatcher) Stop() {
	close(cw.stopCh)
	slog.Info("config watcher stopped")
}

func (cw *ConfigWatcher) initialSync(ctx context.Context) {
	var mc v1alpha1.MonitorConfig
	err := cw.client.Get(ctx, client.ObjectKey{Name: "default"}, &mc)
	if err != nil {
		slog.Debug("no MonitorConfig 'default' found, using env var defaults", "error", err)
		return
	}
	cw.applyConfig(ctx, mc.Spec.NamespaceAllowlist, mc.Spec.NamespaceDenylist)
}

func (cw *ConfigWatcher) handleConfigChange(ctx context.Context, obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		slog.Warn("config watcher received non-unstructured object")
		return
	}

	name := u.GetName()
	if name != "default" {
		slog.Debug("ignoring MonitorConfig with non-default name", "name", name)
		return
	}

	spec, ok := u.Object["spec"].(map[string]interface{})
	if !ok {
		slog.Warn("MonitorConfig 'default' has no spec")
		return
	}

	allowlist := toStringSlice(spec["namespaceAllowlist"])
	denylist := toStringSlice(spec["namespaceDenylist"])

	cw.applyConfig(ctx, allowlist, denylist)
}

func (cw *ConfigWatcher) applyConfig(ctx context.Context, allowlist, denylist []string) {
	cw.filter.Update(allowlist, denylist)

	slog.Info("namespace filter updated from MonitorConfig",
		"allowlist", allowlist,
		"denylist", denylist,
	)

	// Update status to reflect the config is active
	cw.updateStatus(ctx)
}

func (cw *ConfigWatcher) updateStatus(ctx context.Context) {
	var mc v1alpha1.MonitorConfig
	err := cw.client.Get(ctx, client.ObjectKey{Name: "default"}, &mc)
	if err != nil {
		slog.Debug("failed to get MonitorConfig for status update", "error", err)
		return
	}

	now := metav1.Now()
	mc.Status.Active = true
	mc.Status.LastApplied = &now

	if err := cw.client.Status().Update(ctx, &mc); err != nil {
		slog.Warn("failed to update MonitorConfig status", "error", err)
	}
}

func toStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	items, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
