package testdata

import _ "embed"

var (
	//go:embed deployment-stable.yaml
	DeploymentStable string

	//go:embed deployment-deadline-exceeded.yaml
	DeploymentDeadlineExceeded string

	//go:embed deployment-progressing.yaml
	DeploymentProgressing string

	//go:embed deployment-generation-mismatch.yaml
	DeploymentGenerationMismatch string

	//go:embed deployment-paused.yaml
	DeploymentPaused string

	//go:embed pod-ready.yaml
	PodReady string

	//go:embed pod-crashloop.yaml
	PodCrashloop string

	//go:embed pod-image-pull-backoff.yaml
	PodImagePullBackoff string

	//go:embed pod-invalid-image.yaml
	PodInvalidImage string

	//go:embed pod-config-error.yaml
	PodConfigError string

	//go:embed pod-oomkilled.yaml
	PodOOMKilled string

	//go:embed pod-init-crash.yaml
	PodInitCrash string

	//go:embed pod-pending-no-status.yaml
	PodPendingNoStatus string

	//go:embed replicaset-new.yaml
	ReplicasetNew string

	//go:embed events-warning.yaml
	EventsWarning string
)
