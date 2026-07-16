package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

func TestSlackTarget_Dispatch(t *testing.T) {
	var receivedMsg slackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&receivedMsg)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	target := NewSlackTarget(server.URL, server.Client())
	event := models.RolloutEvent{
		ClusterName:    "prod-us-east-1",
		Namespace:      "default",
		DeploymentName: "checkout",
		OldImages:      []string{"checkout:v1.9"},
		NewImages:      []string{"checkout:v2.0"},
	}

	err := target.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if !strings.Contains(receivedMsg.Text, "checkout") {
		t.Errorf("expected message to contain deployment name, got: %s", receivedMsg.Text)
	}
	if !strings.Contains(receivedMsg.Text, "prod-us-east-1") {
		t.Errorf("expected message to contain cluster name, got: %s", receivedMsg.Text)
	}
	if !strings.Contains(receivedMsg.Text, "checkout:v2.0") {
		t.Errorf("expected message to contain new image, got: %s", receivedMsg.Text)
	}
}

func TestSlackTarget_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	target := NewSlackTarget(server.URL, server.Client())
	err := target.Dispatch(context.Background(), models.RolloutEvent{
		DeploymentName: "app",
		OldImages:      []string{"app:v1"},
		NewImages:      []string{"app:v2"},
	})

	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}
