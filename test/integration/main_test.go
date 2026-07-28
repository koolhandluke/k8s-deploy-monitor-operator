//go:build integration

package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	// monitorClient connects to the cluster running the operator.
	monitorClient kubernetes.Interface
	monitorConfig *rest.Config

	// workloadClient connects to the cluster where test deployments are created.
	workloadClient kubernetes.Interface

	// statusAPIURL is the local URL for the port-forwarded status API.
	statusAPIURL string

	// pf is the port-forward session, stopped in TestMain cleanup.
	pf *portForwarder
)

func TestMain(m *testing.M) {
	monitorContext := envOrDefault("MONITOR_CONTEXT", "kind-monitor-cluster")
	workloadContext := envOrDefault("WORKLOAD_CONTEXT", "kind-workload-cluster")
	monitorNamespace := envOrDefault("MONITOR_NAMESPACE", "rollout-monitor")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Build clients from kubeconfig contexts
	var err error
	monitorConfig, monitorClient, err = buildClient(monitorContext)
	if err != nil {
		log.Fatalf("failed to build monitor client (context=%s): %v", monitorContext, err)
	}

	_, workloadClient, err = buildClient(workloadContext)
	if err != nil {
		log.Fatalf("failed to build workload client (context=%s): %v", workloadContext, err)
	}

	// Set up port-forward to operator's status API (port 8081)
	selectorLabels := "app.kubernetes.io/name=deploy-monitor"
	localPort, pfSession, err := portForwardToOperator(ctx, monitorConfig, monitorClient, monitorNamespace, selectorLabels, 8081)
	if err != nil {
		log.Fatalf("failed to port-forward to operator: %v", err)
	}
	pf = pfSession
	statusAPIURL = fmt.Sprintf("http://localhost:%d", localPort)

	log.Printf("status API available at %s", statusAPIURL)

	// Health check: wait for the status API to respond
	if err := healthCheck(statusAPIURL, 30*time.Second); err != nil {
		pf.Stop()
		log.Fatalf("status API health check failed: %v", err)
	}

	code := m.Run()

	pf.Stop()
	os.Exit(code)
}

func buildClient(contextName string) (*rest.Config, kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("build config for context %q: %w", contextName, err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create clientset: %w", err)
	}

	return cfg, cs, nil
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
