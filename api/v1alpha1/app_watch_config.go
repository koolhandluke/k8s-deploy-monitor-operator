package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=awc

// AppWatchConfig registers an application for watchlist-driven monitoring.
// The CR name is the application name. Namespaced in PERSISTENCE_NAMESPACE.
type AppWatchConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AppWatchConfigSpec `json:"spec,omitempty"`
}

// AppWatchConfigSpec defines which clusters and namespaces to watch for an app,
// and where to route notifications.
type AppWatchConfigSpec struct {
	// SlackChannel is the Slack channel to route notifications for this app.
	SlackChannel string `json:"slackChannel,omitempty"`

	// Clusters maps clusterID to a list of namespaces to watch in that cluster.
	Clusters map[string][]string `json:"clusters,omitempty"`
}

// +kubebuilder:object:root=true

// AppWatchConfigList contains a list of AppWatchConfig.
type AppWatchConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AppWatchConfig `json:"items"`
}
