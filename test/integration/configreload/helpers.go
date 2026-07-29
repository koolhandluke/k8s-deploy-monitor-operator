//go:build integration

package configreload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// statusResponse mirrors the JSON returned by the investigation status API.
type statusResponse struct {
	DeploymentKey string `json:"deployment_key"`
	Result        string `json:"result"`
	FailureReason string `json:"failure_reason,omitempty"`
	Duration      string `json:"duration"`
	Timestamp     string `json:"timestamp"`
}

// createDeployment creates a single-replica Deployment in the given namespace.
func createDeployment(ctx context.Context, cs kubernetes.Interface, ns, name, image string) error {
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: image,
							Ports: []corev1.ContainerPort{{ContainerPort: 80}},
						},
					},
				},
			},
		},
	}
	_, err := cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
	return err
}

// updateImage patches a Deployment's first container image.
func updateImage(ctx context.Context, cs kubernetes.Interface, ns, name, newImage string) error {
	dep, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}
	dep.Spec.Template.Spec.Containers[0].Image = newImage
	_, err = cs.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

// waitForInvestigation polls the status API until the given deployment shows
// the expected result, or the timeout expires.
func waitForInvestigation(ctx context.Context, apiURL, ns, name, expectedResult string, timeout time.Duration) (*statusResponse, error) {
	deadline := time.Now().Add(timeout)
	endpoint := fmt.Sprintf("%s/api/v1/investigations/%s/%s", apiURL, ns, name)

	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint)
		if err != nil {
			lastErr = err
			time.Sleep(5 * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			time.Sleep(5 * time.Second)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
			time.Sleep(5 * time.Second)
			continue
		}

		var status statusResponse
		if err := json.Unmarshal(body, &status); err != nil {
			lastErr = fmt.Errorf("unmarshal: %w", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if status.Result == expectedResult {
			return &status, nil
		}
		lastErr = fmt.Errorf("got result %q, want %q", status.Result, expectedResult)
		time.Sleep(5 * time.Second)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("timeout waiting for %s/%s result=%s: %w", ns, name, expectedResult, lastErr)
	}
	return nil, fmt.Errorf("timeout waiting for %s/%s result=%s", ns, name, expectedResult)
}

// assertNoInvestigation polls the status API for the given duration and asserts
// that no investigation result appears for the deployment.
func assertNoInvestigation(ctx context.Context, apiURL, ns, name string, duration time.Duration) error {
	deadline := time.Now().Add(duration)
	endpoint := fmt.Sprintf("%s/api/v1/investigations/%s/%s", apiURL, ns, name)

	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			time.Sleep(2 * time.Second)
			continue
		}

		return fmt.Errorf("expected no investigation for %s/%s but got status %d: %s", ns, name, resp.StatusCode, string(body))
	}
	return nil
}

// createNamespace creates a namespace if it doesn't already exist.
func createNamespace(ctx context.Context, cs kubernetes.Interface, ns string) error {
	_, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{})
	return err
}

// deleteNamespace deletes a namespace and all its contents.
func deleteNamespace(ctx context.Context, cs kubernetes.Interface, ns string) error {
	return cs.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
}

// portForwarder wraps a client-go port-forward session.
type portForwarder struct {
	stopCh chan struct{}
	fw     *portforward.PortForwarder
}

// portForwardToOperator sets up a port-forward to the operator pod matching the
// given label selector. Returns the local port and a stopper function.
func portForwardToOperator(ctx context.Context, cfg *rest.Config, cs kubernetes.Interface, namespace, labelSelector string, remotePort int) (localPort int, pf *portForwarder, err error) {
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: "status.phase=Running",
	})
	if err != nil {
		return 0, nil, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return 0, nil, fmt.Errorf("no running pods found with selector %q in %s", labelSelector, namespace)
	}
	podName := pods.Items[0].Name

	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return 0, nil, fmt.Errorf("create round tripper: %w", err)
	}

	pfURL := cfg.Host + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, mustParseURL(pfURL))

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})

	ports := []string{fmt.Sprintf("0:%d", remotePort)}
	fw, err := portforward.New(dialer, ports, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return 0, nil, fmt.Errorf("create port forwarder: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- fw.ForwardPorts()
	}()

	select {
	case <-readyCh:
	case err := <-errCh:
		return 0, nil, fmt.Errorf("port forward failed: %w", err)
	case <-ctx.Done():
		close(stopCh)
		return 0, nil, ctx.Err()
	}

	forwardedPorts, err := fw.GetPorts()
	if err != nil {
		close(stopCh)
		return 0, nil, fmt.Errorf("get forwarded ports: %w", err)
	}

	return int(forwardedPorts[0].Local), &portForwarder{stopCh: stopCh, fw: fw}, nil
}

func (pf *portForwarder) Stop() {
	close(pf.stopCh)
}

// mustParseURL parses a URL string. Panics on parse failure.
func mustParseURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(fmt.Sprintf("invalid URL %q: %v", rawURL, err))
	}
	return u
}

// waitForDeploymentReady waits until a deployment has at least 1 available replica.
func waitForDeploymentReady(ctx context.Context, cs kubernetes.Interface, ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dep, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if dep.Status.AvailableReplicas > 0 {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for deployment %s/%s to become ready", ns, name)
}

// uniqueName returns a test-scoped deployment name to avoid collisions.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%100000)
}

// healthCheck polls the status API root until it responds 200, or timeout expires.
func healthCheck(apiURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	endpoint := apiURL + "/api/v1/investigations"
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("status API not healthy after %v", timeout)
}
