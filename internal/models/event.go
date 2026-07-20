// Package models defines shared data types used across the rollout monitor pipeline.
package models

import "time"

// RolloutEvent represents a detected deployment rollout.
type RolloutEvent struct {
	ClusterID       string    // cluster identifier (kubeconfig context name or filename)
	ClusterName     string    // human-readable cluster name
	Namespace       string
	DeploymentName  string
	OldTemplateHash string    // SHA256 of previous spec.template
	NewTemplateHash string    // SHA256 of new spec.template
	OldImages       []string  // container images before rollout
	NewImages       []string  // container images after rollout
	Timestamp       time.Time
}

// DeploymentKey returns a unique key for this deployment across clusters.
func (e RolloutEvent) DeploymentKey() string {
	return e.ClusterID + "/" + e.Namespace + "/" + e.DeploymentName
}
