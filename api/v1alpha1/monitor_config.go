package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=mc
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=`.status.active`
// +kubebuilder:printcolumn:name="Last Applied",type=date,JSONPath=`.status.lastApplied`

// MonitorConfig controls runtime-reloadable namespace filtering.
// A single cluster-scoped instance named "default" is expected.
type MonitorConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MonitorConfigSpec   `json:"spec,omitempty"`
	Status MonitorConfigStatus `json:"status,omitempty"`
}

type MonitorConfigSpec struct {
	// NamespaceAllowlist, if non-empty, restricts monitoring to only these namespaces.
	// Takes precedence over NamespaceDenylist.
	NamespaceAllowlist []string `json:"namespaceAllowlist,omitempty"`

	// NamespaceDenylist excludes these namespaces from monitoring.
	// Ignored if NamespaceAllowlist is non-empty.
	NamespaceDenylist []string `json:"namespaceDenylist,omitempty"`
}

type MonitorConfigStatus struct {
	// Active indicates the config has been picked up by the monitor.
	Active bool `json:"active,omitempty"`

	// LastApplied is when the config was last applied.
	LastApplied *metav1.Time `json:"lastApplied,omitempty"`
}

// +kubebuilder:object:root=true

// MonitorConfigList contains a list of MonitorConfig.
type MonitorConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MonitorConfig `json:"items"`
}
