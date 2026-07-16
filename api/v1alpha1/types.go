package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=crs
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterID`
// +kubebuilder:printcolumn:name="Deployments",type=integer,JSONPath=`.status.trackedDeployments`
// +kubebuilder:printcolumn:name="Last Sync",type=date,JSONPath=`.status.lastSyncTime`

// ClusterRolloutState persists template hashes for a single cluster.
// Survives monitor restarts and enables gap detection.
type ClusterRolloutState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterRolloutStateSpec   `json:"spec,omitempty"`
	Status ClusterRolloutStateStatus `json:"status,omitempty"`
}

type ClusterRolloutStateSpec struct {
	// ClusterID is the unique identifier for the cluster.
	ClusterID string `json:"clusterID"`

	// TemplateHashes maps "namespace/deploymentName" to the SHA256 hash of spec.template.
	TemplateHashes map[string]string `json:"templateHashes,omitempty"`
}

type ClusterRolloutStateStatus struct {
	// TrackedDeployments is the number of deployments being tracked.
	TrackedDeployments int `json:"trackedDeployments,omitempty"`

	// LastSyncTime is when the hashes were last persisted.
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterRolloutStateList contains a list of ClusterRolloutState.
type ClusterRolloutStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRolloutState `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=rr
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Deployment",type=string,JSONPath=`.spec.deployment`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RolloutRecord tracks a single detected rollout event with dispatch status.
type RolloutRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RolloutRecordSpec   `json:"spec,omitempty"`
	Status RolloutRecordStatus `json:"status,omitempty"`
}

type RolloutRecordSpec struct {
	// ClusterID is the unique identifier for the cluster.
	ClusterID string `json:"clusterID"`

	// ClusterName is the human-readable cluster name.
	ClusterName string `json:"clusterName"`

	// Namespace of the deployment.
	Namespace string `json:"namespace"`

	// Deployment name.
	Deployment string `json:"deployment"`

	// OldImages are the container images before rollout.
	OldImages []string `json:"oldImages,omitempty"`

	// NewImages are the container images after rollout.
	NewImages []string `json:"newImages,omitempty"`

	// OldTemplateHash is the SHA256 of the previous spec.template.
	OldTemplateHash string `json:"oldTemplateHash"`

	// NewTemplateHash is the SHA256 of the new spec.template.
	NewTemplateHash string `json:"newTemplateHash"`

	// DetectedAt is when the rollout was first detected.
	DetectedAt metav1.Time `json:"detectedAt"`
}

// RolloutPhase represents the current state of rollout processing.
type RolloutPhase string

const (
	PhaseDetected     RolloutPhase = "Detected"
	PhaseDispatched   RolloutPhase = "Dispatched"
	PhaseInvestigated RolloutPhase = "Investigated"
	PhaseFailed       RolloutPhase = "Failed"
)

type RolloutRecordStatus struct {
	// Phase of the rollout processing.
	// +kubebuilder:validation:Enum=Detected;Dispatched;Investigated;Failed
	Phase RolloutPhase `json:"phase,omitempty"`

	// DispatchedAt is when the event was dispatched.
	DispatchedAt *metav1.Time `json:"dispatchedAt,omitempty"`

	// DispatchTargets lists where the event was sent.
	DispatchTargets []string `json:"dispatchTargets,omitempty"`

	// Error message if dispatch failed.
	Error string `json:"error,omitempty"`
}

// +kubebuilder:object:root=true

// RolloutRecordList contains a list of RolloutRecord.
type RolloutRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RolloutRecord `json:"items"`
}
