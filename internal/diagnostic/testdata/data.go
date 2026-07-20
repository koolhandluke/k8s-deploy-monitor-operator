// Package testdata provides embedded YAML fixtures for diagnostic package tests.
package testdata

import _ "embed"

var (
	// DeploymentStable holds a deployment YAML where the rollout has fully converged.
	//go:embed deployment-stable.yaml
	DeploymentStable string

	// DeploymentDeadlineExceeded holds a deployment YAML with a ProgressDeadlineExceeded condition.
	//go:embed deployment-deadline-exceeded.yaml
	DeploymentDeadlineExceeded string

	// DeploymentProgressing holds a deployment YAML that is still rolling out.
	//go:embed deployment-progressing.yaml
	DeploymentProgressing string

	// DeploymentGenerationMismatch holds a deployment YAML where observed generation lags behind generation.
	//go:embed deployment-generation-mismatch.yaml
	DeploymentGenerationMismatch string

	// DeploymentPaused holds a deployment YAML with spec.paused set to true.
	//go:embed deployment-paused.yaml
	DeploymentPaused string

	// PodReady holds a pod YAML in the ready state with all containers running.
	//go:embed pod-ready.yaml
	PodReady string

	// PodCrashloop holds a pod YAML with a container in CrashLoopBackOff.
	//go:embed pod-crashloop.yaml
	PodCrashloop string

	// PodImagePullBackoff holds a pod YAML with a container in ImagePullBackOff.
	//go:embed pod-image-pull-backoff.yaml
	PodImagePullBackoff string

	// PodInvalidImage holds a pod YAML with an invalid container image name.
	//go:embed pod-invalid-image.yaml
	PodInvalidImage string

	// PodConfigError holds a pod YAML with a CreateContainerConfigError.
	//go:embed pod-config-error.yaml
	PodConfigError string

	// PodOOMKilled holds a pod YAML with a container terminated by OOMKilled.
	//go:embed pod-oomkilled.yaml
	PodOOMKilled string

	// PodInitCrash holds a pod YAML with a crashing init container.
	//go:embed pod-init-crash.yaml
	PodInitCrash string

	// PodPendingNoStatus holds a pod YAML in Pending phase with no container statuses.
	//go:embed pod-pending-no-status.yaml
	PodPendingNoStatus string

	// ReplicasetNew holds a ReplicaSet YAML matching the latest deployment revision.
	//go:embed replicaset-new.yaml
	ReplicasetNew string

	// EventsWarning holds a Kubernetes EventList YAML containing Warning events.
	//go:embed events-warning.yaml
	EventsWarning string
)
