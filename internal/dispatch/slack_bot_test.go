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

func TestSlackBotTarget_Dispatch(t *testing.T) {
	var receivedMsg slackBotMessage
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&receivedMsg)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	target := NewSlackBotTarget("xoxb-test-token", server.Client())
	target.apiURL = server.URL

	event := models.RolloutEvent{
		ClusterID:      "prod-us-east",
		Namespace:      "payments",
		DeploymentName: "payments-api",
		OldImages:      []string{"payments:v1"},
		NewImages:      []string{"payments:v2"},
		SlackChannel:   "#payments-deploys",
	}

	err := target.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if receivedAuth != "Bearer xoxb-test-token" {
		t.Errorf("expected Bearer token, got %s", receivedAuth)
	}
	if receivedMsg.Channel != "#payments-deploys" {
		t.Errorf("expected #payments-deploys, got %s", receivedMsg.Channel)
	}
	if !strings.Contains(receivedMsg.Text, "payments-api") {
		t.Errorf("expected message to contain deployment name, got: %s", receivedMsg.Text)
	}
}

func TestSlackBotTarget_EmptyChannel_Skips(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not have made a request for empty channel")
	}))
	defer server.Close()

	target := NewSlackBotTarget("xoxb-test-token", server.Client())
	target.apiURL = server.URL

	event := models.RolloutEvent{
		ClusterID:      "prod",
		DeploymentName: "app",
		SlackChannel:   "", // empty — should skip
	}

	err := target.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error for empty channel, got: %v", err)
	}
}

func TestSlackBotTarget_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	target := NewSlackBotTarget("xoxb-test-token", server.Client())
	target.apiURL = server.URL

	event := models.RolloutEvent{
		SlackChannel:   "#test",
		DeploymentName: "app",
	}

	err := target.Dispatch(context.Background(), event)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestSlackBotTarget_Name(t *testing.T) {
	target := NewSlackBotTarget("token", &http.Client{})
	if target.Name() != "slack-bot" {
		t.Errorf("expected slack-bot, got %s", target.Name())
	}
}
