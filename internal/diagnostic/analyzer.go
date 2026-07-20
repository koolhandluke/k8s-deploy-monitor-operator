package diagnostic

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/trace"
)

// AnalyzerConfig holds tunable parameters for the rollout analyzer.
// Tests inject short durations; production uses DefaultAnalyzerConfig().
type AnalyzerConfig struct {
	PollInterval      time.Duration
	InactivityTimeout time.Duration
	AbsoluteTimeout   time.Duration
	SoakPeriod        time.Duration
	RestartThreshold  int32
	RestartWindow     time.Duration
	ConfigErrorWindow time.Duration
	LogTailLines      int64
}

// DefaultAnalyzerConfig returns production defaults.
func DefaultAnalyzerConfig() AnalyzerConfig {
	return AnalyzerConfig{
		PollInterval:      10 * time.Second,
		InactivityTimeout: 5 * time.Minute,
		AbsoluteTimeout:   10 * time.Minute,
		SoakPeriod:        60 * time.Second,
		RestartThreshold:  3,
		RestartWindow:     5 * time.Minute,
		ConfigErrorWindow: 90 * time.Second,
		LogTailLines:      500,
	}
}

// errorLogPatterns are substrings we filter log lines for.
var errorLogPatterns = []string{"error", "fatal", "panic", "traceback", "exception"}

// ClientsetProvider abstracts cluster credential lookup.
// ClusterRegistry implements this; tests inject fakes.
type ClientsetProvider interface {
	ClientsetFor(clusterID string) (kubernetes.Interface, error)
}

// RolloutAnalyzer implements the two-phase runbook for a single rollout event.
type RolloutAnalyzer struct {
	provider ClientsetProvider
	config   AnalyzerConfig
}

// NewRolloutAnalyzer creates an analyzer backed by the given provider and config.
func NewRolloutAnalyzer(provider ClientsetProvider, cfg AnalyzerConfig) *RolloutAnalyzer {
	return &RolloutAnalyzer{provider: provider, config: cfg}
}

// progressState tracks forward progress for stall detection.
type progressState struct {
	lastUpdated     int32
	lastAvailable   int32
	lastUnavailable int32
	lastProgressAt  time.Time
}

// recordProgress updates the progress tracking state and resets the last progress
// timestamp when forward movement is detected in replica counts.
func (p *progressState) recordProgress(updated, available, unavailable int32, now time.Time) {
	if updated > p.lastUpdated || available > p.lastAvailable || unavailable < p.lastUnavailable {
		p.lastProgressAt = now
	}
	p.lastUpdated = updated
	p.lastAvailable = available
	p.lastUnavailable = unavailable
}

// Analyze runs the full runbook against the cluster for the given event.
func (a *RolloutAnalyzer) Analyze(ctx context.Context, event models.RolloutEvent) (*DiagnosticReport, error) {
	start := time.Now()

	clientset, err := a.provider.ClientsetFor(event.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("getting clientset for cluster %s: %w", event.ClusterID, err)
	}

	report := &DiagnosticReport{Event: event}

	// Phase 1: Monitor rollout
	result, reason := a.monitorRollout(ctx, clientset, event)
	report.Result = result
	report.FailureReason = reason

	// Phase 2: Gather failure context (only on non-SUCCESS)
	if result != ResultSuccess {
		a.gatherDiagnostics(ctx, clientset, event, report)
	}

	report.Duration = time.Since(start)
	return report, nil
}

// monitorRollout implements Phase 1 of the runbook.
func (a *RolloutAnalyzer) monitorRollout(ctx context.Context, clientset kubernetes.Interface, event models.RolloutEvent) (Result, string) {
	absoluteDeadline := time.Now().Add(a.config.AbsoluteTimeout)
	progress := &progressState{lastProgressAt: time.Now()}
	var configErrorFirstSeen time.Time
	var lastSeenPaused bool

	ticker := time.NewTicker(a.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ResultInconclusive, "context cancelled"
		case <-ticker.C:
		}

		now := time.Now()
		if now.After(absoluteDeadline) {
			// If paused when timeout fires, classify as PAUSED
			if lastSeenPaused {
				return ResultPaused, "absolute timeout reached while deployment is paused"
			}
			// If we were still seeing progress, it's inconclusive rather than stalled.
			if now.Sub(progress.lastProgressAt) < a.config.InactivityTimeout {
				return ResultInconclusive, "absolute timeout reached while still making progress"
			}
			return ResultStalled, "absolute timeout reached with no recent progress"
		}

		deploy, err := clientset.AppsV1().Deployments(event.Namespace).Get(ctx, event.DeploymentName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return ResultDeleted, "deployment was deleted during analysis"
			}
			slog.Warn("failed to get deployment during analysis",
				"deployment", event.DeploymentKey(),
				"error", err,
			)
			continue
		}

		// Step 1: Gate on generation match
		if deploy.Status.ObservedGeneration < deploy.Generation {
			slog.Log(ctx, trace.LevelTrace, "generation mismatch, waiting for controller",
				"deployment", event.DeploymentKey(),
				"generation", deploy.Generation,
				"observed_generation", deploy.Status.ObservedGeneration,
			)
			progress.lastProgressAt = now // controller hasn't caught up yet, not stalled
			continue
		}

		// Step 2a: Paused check — skip evaluation while paused
		if deploy.Spec.Paused {
			slog.Log(ctx, trace.LevelTrace, "deployment is paused, skipping evaluation",
				"deployment", event.DeploymentKey(),
			)
			lastSeenPaused = true
			continue
		}
		lastSeenPaused = false

		// Step 2b: Check failure conditions
		if result, reason := a.checkFailureConditions(ctx, clientset, deploy, now, progress, &configErrorFirstSeen); result != "" {
			return result, reason
		}

		// Step 3: Check replica convergence
		desired := int32(1)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		updated := deploy.Status.UpdatedReplicas
		available := deploy.Status.AvailableReplicas
		unavailable := deploy.Status.UnavailableReplicas

		slog.Log(ctx, trace.LevelTrace, "poll cycle replica state",
			"deployment", event.DeploymentKey(),
			"desired", desired,
			"updated", updated,
			"available", available,
			"unavailable", unavailable,
			"last_progress_ago", now.Sub(progress.lastProgressAt).String(),
		)

		progress.recordProgress(updated, available, unavailable, now)

		if now.Sub(progress.lastProgressAt) > a.config.InactivityTimeout {
			return ResultStalled, fmt.Sprintf("no forward progress for %s", a.config.InactivityTimeout)
		}

		if updated == desired && available == desired && unavailable == 0 {
			slog.Log(ctx, trace.LevelTrace, "replicas converged, entering soak",
				"deployment", event.DeploymentKey(),
				"desired", desired,
			)
			// Step 4: Soak period
			return a.soak(ctx, clientset, event, deploy)
		}
	}
}

// checkFailureConditions implements Step 2 of Phase 1.
func (a *RolloutAnalyzer) checkFailureConditions(
	ctx context.Context,
	clientset kubernetes.Interface,
	deploy *appsv1.Deployment,
	now time.Time,
	progress *progressState,
	configErrorFirstSeen *time.Time,
) (Result, string) {
	// Check Progressing condition for ProgressDeadlineExceeded
	for _, cond := range deploy.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing &&
			cond.Status == corev1.ConditionFalse &&
			cond.Reason == "ProgressDeadlineExceeded" {
			slog.Log(ctx, trace.LevelTrace, "ProgressDeadlineExceeded condition detected",
				"deployment", deploy.Namespace+"/"+deploy.Name,
			)
			return ResultFailed, "ProgressDeadlineExceeded"
		}
	}

	// Check pods of new ReplicaSet for early failure signals
	newRS, err := a.findNewReplicaSet(ctx, clientset, deploy)
	if err != nil || newRS == nil {
		slog.Log(ctx, trace.LevelTrace, "new ReplicaSet lookup",
			"deployment", deploy.Namespace+"/"+deploy.Name,
			"found", newRS != nil,
			"error", err,
		)
		return "", ""
	}

	pods, err := a.podsForReplicaSet(ctx, clientset, newRS)
	if err != nil {
		return "", ""
	}

	slog.Log(ctx, trace.LevelTrace, "checking failure conditions",
		"deployment", deploy.Namespace+"/"+deploy.Name,
		"rs", newRS.Name,
		"pod_count", len(pods),
	)

	var totalRestarts int32
	hasWaitingBeforeStart := false
	for _, pod := range pods {
		allStatuses := append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...)
		for _, cs := range allStatuses {
			// Generic waiting pattern: any container stuck in Waiting with no restarts
			// and the pod has existed longer than ConfigErrorWindow is a failure signal.
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.RestartCount == 0 {
				slog.Log(ctx, trace.LevelTrace, "container waiting before first start",
					"pod", pod.Name,
					"container", cs.Name,
					"reason", cs.State.Waiting.Reason,
				)
				hasWaitingBeforeStart = true

				if configErrorFirstSeen.IsZero() {
					*configErrorFirstSeen = now
				} else if now.Sub(*configErrorFirstSeen) >= a.config.ConfigErrorWindow {
					return ResultFailed, fmt.Sprintf("%s persisted for %s in pod %s container %s",
						cs.State.Waiting.Reason, a.config.ConfigErrorWindow, pod.Name, cs.Name)
				}
			}
			totalRestarts += cs.RestartCount
		}
	}

	slog.Log(ctx, trace.LevelTrace, "failure condition check complete",
		"deployment", deploy.Namespace+"/"+deploy.Name,
		"total_restarts", totalRestarts,
		"restart_threshold", a.config.RestartThreshold,
		"has_waiting", hasWaitingBeforeStart,
		"config_error_timer_active", !configErrorFirstSeen.IsZero(),
	)

	// Reset config error timer if no containers are stuck waiting before start
	if !configErrorFirstSeen.IsZero() && !hasWaitingBeforeStart {
		*configErrorFirstSeen = time.Time{}
	}

	// Restart threshold across pods within the window
	if totalRestarts >= a.config.RestartThreshold && now.Sub(progress.lastProgressAt) < a.config.RestartWindow {
		return ResultFailed, fmt.Sprintf("restart threshold exceeded: %d restarts", totalRestarts)
	}

	return "", ""
}

// soak implements Step 4 of Phase 1.
func (a *RolloutAnalyzer) soak(ctx context.Context, clientset kubernetes.Interface, event models.RolloutEvent, deploy *appsv1.Deployment) (Result, string) {
	slog.Info("rollout converged, entering soak period",
		"deployment", event.DeploymentKey(),
		"soak_seconds", a.config.SoakPeriod.Seconds(),
	)

	// Capture pre-soak restart counts
	preRestarts := make(map[string]int32) // pod/container → restartCount
	newRS, err := a.findNewReplicaSet(ctx, clientset, deploy)
	if err == nil && newRS != nil {
		pods, err := a.podsForReplicaSet(ctx, clientset, newRS)
		if err == nil {
			for _, pod := range pods {
				for _, cs := range pod.Status.ContainerStatuses {
					preRestarts[pod.Name+"/"+cs.Name] = cs.RestartCount
				}
			}
		}
	}
	slog.Log(ctx, trace.LevelTrace, "pre-soak restart snapshot",
		"deployment", event.DeploymentKey(),
		"container_count", len(preRestarts),
	)

	select {
	case <-ctx.Done():
		return ResultInconclusive, "context cancelled during soak"
	case <-time.After(a.config.SoakPeriod):
	}

	// Re-fetch and check for regression
	deploy, err = clientset.AppsV1().Deployments(event.Namespace).Get(ctx, event.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return ResultInconclusive, fmt.Sprintf("failed to re-fetch deployment after soak: %v", err)
	}

	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}

	slog.Log(ctx, trace.LevelTrace, "post-soak deployment state",
		"deployment", event.DeploymentKey(),
		"desired", desired,
		"updated", deploy.Status.UpdatedReplicas,
		"available", deploy.Status.AvailableReplicas,
		"unavailable", deploy.Status.UnavailableReplicas,
	)

	if deploy.Status.UpdatedReplicas != desired ||
		deploy.Status.AvailableReplicas != desired ||
		deploy.Status.UnavailableReplicas != 0 {
		return ResultUnstable, "replica counts regressed during soak period"
	}

	// Check restart counts increased
	newRS, err = a.findNewReplicaSet(ctx, clientset, deploy)
	if err == nil && newRS != nil {
		pods, err := a.podsForReplicaSet(ctx, clientset, newRS)
		if err == nil {
			for _, pod := range pods {
				for _, cs := range pod.Status.ContainerStatuses {
					key := pod.Name + "/" + cs.Name
					if pre, ok := preRestarts[key]; ok && cs.RestartCount > pre {
						slog.Log(ctx, trace.LevelTrace, "restart count increased during soak",
							"pod", pod.Name,
							"container", cs.Name,
							"pre_soak", pre,
							"post_soak", cs.RestartCount,
						)
						return ResultUnstable, fmt.Sprintf("container %s in pod %s restarted during soak (%d → %d)",
							cs.Name, pod.Name, pre, cs.RestartCount)
					}
				}

				// Check pods still ready
				for _, cond := range pod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status != corev1.ConditionTrue {
						slog.Log(ctx, trace.LevelTrace, "pod not ready during soak",
							"pod", pod.Name,
						)
						return ResultUnstable, fmt.Sprintf("pod %s dropped out of Ready during soak", pod.Name)
					}
				}
			}
		}
	}

	return ResultSuccess, ""
}

// gatherDiagnostics implements Phase 2 of the runbook.
func (a *RolloutAnalyzer) gatherDiagnostics(ctx context.Context, clientset kubernetes.Interface, event models.RolloutEvent, report *DiagnosticReport) {
	deploy, err := clientset.AppsV1().Deployments(event.Namespace).Get(ctx, event.DeploymentName, metav1.GetOptions{})
	if err != nil {
		slog.Warn("failed to get deployment for diagnostics", "deployment", event.DeploymentKey(), "error", err)
		return
	}

	// Step 6: Collect Warning events
	report.Events = a.collectEvents(ctx, clientset, event)
	slog.Log(ctx, trace.LevelTrace, "gathered diagnostic events",
		"deployment", event.DeploymentKey(),
		"event_count", len(report.Events),
	)

	// Step 7: Inspect pod status
	newRS, err := a.findNewReplicaSet(ctx, clientset, deploy)
	if err != nil || newRS == nil {
		slog.Log(ctx, trace.LevelTrace, "new RS not found for diagnostics",
			"deployment", event.DeploymentKey(),
			"error", err,
		)
		return
	}

	pods, err := a.podsForReplicaSet(ctx, clientset, newRS)
	if err != nil {
		return
	}

	slog.Log(ctx, trace.LevelTrace, "gathering diagnostics from pods",
		"deployment", event.DeploymentKey(),
		"rs", newRS.Name,
		"pod_count", len(pods),
	)

	for _, pod := range pods {
		// Init container statuses
		for _, cs := range pod.Status.InitContainerStatuses {
			report.PodStatuses = append(report.PodStatuses, PodStatus{
				Name:          pod.Name,
				Phase:         string(pod.Status.Phase),
				ContainerName: cs.Name,
				Reason:        waitingReason(cs),
				RestartCount:  cs.RestartCount,
				Ready:         cs.Ready,
				InitContainer: true,
			})
		}
		// Regular container statuses
		for _, cs := range pod.Status.ContainerStatuses {
			report.PodStatuses = append(report.PodStatuses, PodStatus{
				Name:          pod.Name,
				Phase:         string(pod.Status.Phase),
				ContainerName: cs.Name,
				Reason:        waitingReason(cs),
				RestartCount:  cs.RestartCount,
				Ready:         cs.Ready,
			})
		}
	}

	// Step 8: Collect logs from failing pods
	for _, pod := range pods {
		if !isPodFailing(pod) {
			continue
		}

		// Collect from init containers
		for _, cs := range pod.Status.InitContainerStatuses {
			a.collectContainerLogs(ctx, clientset, pod, cs.Name, true, event.Timestamp, report)
		}
		// Collect from regular containers
		for _, cs := range pod.Status.ContainerStatuses {
			a.collectContainerLogs(ctx, clientset, pod, cs.Name, false, event.Timestamp, report)
		}
	}
}

// collectEvents gathers Warning events from the namespace related to the deployment.
func (a *RolloutAnalyzer) collectEvents(ctx context.Context, clientset kubernetes.Interface, event models.RolloutEvent) []K8sEvent {
	eventList, err := clientset.CoreV1().Events(event.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
	})
	if err != nil {
		slog.Warn("failed to list events", "namespace", event.Namespace, "error", err)
		return nil
	}

	var result []K8sEvent
	for _, ev := range eventList.Items {
		// Filter to events related to the deployment by name prefix
		objName := ev.InvolvedObject.Name
		if !strings.HasPrefix(objName, event.DeploymentName) {
			continue
		}

		result = append(result, K8sEvent{
			Reason:    ev.Reason,
			Message:   ev.Message,
			Count:     ev.Count,
			Object:    ev.InvolvedObject.Kind + "/" + ev.InvolvedObject.Name,
			FirstSeen: ev.FirstTimestamp.Time,
			LastSeen:  ev.LastTimestamp.Time,
		})
	}
	return result
}

// collectContainerLogs fetches current and previous logs for a container, filters for error patterns.
func (a *RolloutAnalyzer) collectContainerLogs(
	ctx context.Context,
	clientset kubernetes.Interface,
	pod corev1.Pod,
	containerName string,
	initContainer bool,
	rolloutStart time.Time,
	report *DiagnosticReport,
) {
	sinceTime := metav1.NewTime(rolloutStart)

	// Current logs
	current := a.fetchLogs(ctx, clientset, pod.Namespace, pod.Name, containerName, false, a.config.LogTailLines, &sinceTime)
	if current != nil {
		current.InitContainer = initContainer
		report.LogSnippets = append(report.LogSnippets, *current)
	}

	// Previous logs (critical for crash loops)
	previous := a.fetchLogs(ctx, clientset, pod.Namespace, pod.Name, containerName, true, a.config.LogTailLines, nil)
	if previous != nil {
		previous.InitContainer = initContainer
		report.LogSnippets = append(report.LogSnippets, *previous)
	}
}

// fetchLogs retrieves container logs from the Kubernetes API and filters them
// for error patterns. Returns nil if no matching lines are found.
func (a *RolloutAnalyzer) fetchLogs(
	ctx context.Context,
	clientset kubernetes.Interface,
	namespace, podName, containerName string,
	previous bool,
	tailLines int64,
	sinceTime *metav1.Time,
) *LogSnippet {
	opts := &corev1.PodLogOptions{
		Container: containerName,
		Previous:  previous,
		TailLines: &tailLines,
	}
	if sinceTime != nil && !previous {
		opts.SinceTime = sinceTime
	}

	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		// Previous logs may not exist — that's normal
		if !previous {
			slog.Debug("failed to get container logs",
				"pod", podName, "container", containerName, "previous", previous, "error", err,
			)
		}
		return nil
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return nil
	}
	if len(data) == 0 {
		return nil
	}

	allLines := countLines(data)
	filtered := filterErrorLines(data)

	slog.Log(ctx, trace.LevelTrace, "fetched container logs",
		"pod", podName,
		"container", containerName,
		"previous", previous,
		"total_lines", allLines,
		"filtered_lines", len(filtered),
	)

	if len(filtered) == 0 {
		return nil
	}

	return &LogSnippet{
		PodName:       podName,
		ContainerName: containerName,
		Previous:      previous,
		Lines:         filtered,
		TotalLines:    allLines,
	}
}

// findNewReplicaSet finds the ReplicaSet that matches the deployment's current template hash.
func (a *RolloutAnalyzer) findNewReplicaSet(ctx context.Context, clientset kubernetes.Interface, deploy *appsv1.Deployment) (*appsv1.ReplicaSet, error) {
	rsList, err := clientset.AppsV1().ReplicaSets(deploy.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(deploy.Spec.Selector.MatchLabels).String(),
	})
	if err != nil {
		return nil, err
	}

	for i := range rsList.Items {
		rs := &rsList.Items[i]
		// The new RS is the one owned by this deployment with the matching pod-template-hash
		for _, ref := range rs.OwnerReferences {
			if ref.UID == deploy.UID {
				revision := rs.Annotations["deployment.kubernetes.io/revision"]
				deployRevision := deploy.Annotations["deployment.kubernetes.io/revision"]
				if revision == deployRevision {
					return rs, nil
				}
			}
		}
	}

	return nil, nil
}

// podsForReplicaSet lists pods owned by the given ReplicaSet.
func (a *RolloutAnalyzer) podsForReplicaSet(ctx context.Context, clientset kubernetes.Interface, rs *appsv1.ReplicaSet) ([]corev1.Pod, error) {
	podList, err := clientset.CoreV1().Pods(rs.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(rs.Spec.Selector.MatchLabels).String(),
	})
	if err != nil {
		return nil, err
	}

	var owned []corev1.Pod
	for _, pod := range podList.Items {
		for _, ref := range pod.OwnerReferences {
			if ref.UID == rs.UID {
				owned = append(owned, pod)
				break
			}
		}
	}
	return owned, nil
}

// waitingReason extracts the waiting reason from a container status, checking lastState too.
func waitingReason(cs corev1.ContainerStatus) string {
	if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
		return cs.State.Waiting.Reason
	}
	if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
		return cs.State.Terminated.Reason
	}
	if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason != "" {
		return cs.LastTerminationState.Terminated.Reason
	}
	return ""
}

// isPodFailing returns true if the pod has crash-looping, errored, or restarted containers.
func isPodFailing(pod corev1.Pod) bool {
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		if cs.RestartCount > 0 {
			return true
		}
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull",
				"CreateContainerConfigError", "InvalidImageName":
				return true
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			return true
		}
	}
	return false
}

// filterErrorLines extracts lines matching error patterns, with deduplication.
func filterErrorLines(data []byte) []string {
	seen := make(map[string]int)
	var result []string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		lower := strings.ToLower(line)
		for _, pattern := range errorLogPatterns {
			if strings.Contains(lower, pattern) {
				if count, ok := seen[line]; ok {
					seen[line] = count + 1
				} else {
					seen[line] = 1
					result = append(result, line)
				}
				break
			}
		}
	}

	// Annotate duplicates
	for i, line := range result {
		if count := seen[line]; count > 1 {
			result[i] = fmt.Sprintf("%s (seen %d times)", line, count)
		}
	}
	return result
}

// countLines returns the number of newline-delimited lines in data.
func countLines(data []byte) int {
	n := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		n++
	}
	return n
}
