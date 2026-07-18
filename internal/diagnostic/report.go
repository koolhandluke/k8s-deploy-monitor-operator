package diagnostic

import (
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

// Result classifies the outcome of a rollout analysis.
type Result string

const (
	ResultSuccess      Result = "SUCCESS"
	ResultFailed       Result = "FAILED"
	ResultStalled      Result = "STALLED"
	ResultInconclusive Result = "INCONCLUSIVE"
	ResultUnstable     Result = "UNSTABLE"
)

// DiagnosticReport is the output of a rollout analysis.
type DiagnosticReport struct {
	Event         models.RolloutEvent
	Result        Result
	FailureReason string
	Events        []K8sEvent
	PodStatuses   []PodStatus
	LogSnippets   []LogSnippet
	Duration      time.Duration
}

// K8sEvent is a summary of a Kubernetes Warning event.
type K8sEvent struct {
	Reason    string
	Message   string
	Count     int32
	Object    string // e.g. "Pod/my-app-7f8b9-xk2lm"
	FirstSeen time.Time
	LastSeen  time.Time
}

// PodStatus summarizes a pod's state for the diagnostic report.
type PodStatus struct {
	Name          string
	Phase         string
	ContainerName string
	Reason        string // e.g. CrashLoopBackOff, ImagePullBackOff
	RestartCount  int32
	Ready         bool
	InitContainer bool
}

// LogSnippet holds filtered log lines from a failing container.
type LogSnippet struct {
	PodName       string
	ContainerName string
	InitContainer bool
	Previous      bool   // from previous container instance
	Lines         []string
	TotalLines    int // total lines before filtering
}
