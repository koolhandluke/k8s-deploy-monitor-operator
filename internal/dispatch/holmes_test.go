package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/koolhandluke/k8s-deploy-monitor-operator/internal/models"
)

func TestHolmesTarget_Dispatch(t *testing.T) {
	var receivedBody holmesChatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}

		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	target := NewHolmesTarget(server.URL, server.Client())
	event := models.RolloutEvent{
		ClusterID:      "test-cluster",
		ClusterName:    "prod-us-east-1",
		Namespace:      "default",
		DeploymentName: "checkout",
		OldImages:      []string{"checkout:v1.9"},
		NewImages:      []string{"checkout:v2.0"},
		Timestamp:      time.Now(),
	}

	err := target.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if receivedBody.Ask == "" {
		t.Fatal("expected non-empty ask")
	}
}

func TestHolmesTarget_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	target := NewHolmesTarget(server.URL, server.Client())
	event := models.RolloutEvent{
		ClusterName:    "test",
		Namespace:      "default",
		DeploymentName: "app",
		OldImages:      []string{"app:v1"},
		NewImages:      []string{"app:v2"},
	}

	err := target.Dispatch(context.Background(), event)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
