package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvConfigs(t *testing.T) {
	dir := t.TempDir()

	prodYAML := `clusters:
  - name: prod-us-east
    deployments:
      - app: payments-service
        namespaces: [payments, payments-jobs]
      - app: checkout-api
        namespaces: [checkout]
  - name: prod-eu-west
    deployments:
      - app: payments-service
        namespaces: [payments]
`
	os.WriteFile(filepath.Join(dir, "prod.yaml"), []byte(prodYAML), 0644)

	configs, err := LoadEnvConfigs(dir)
	if err != nil {
		t.Fatalf("LoadEnvConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if len(configs[0].Clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(configs[0].Clusters))
	}
	if configs[0].Clusters[0].Name != "prod-us-east" {
		t.Errorf("expected prod-us-east, got %s", configs[0].Clusters[0].Name)
	}
	if len(configs[0].Clusters[0].Deployments) != 2 {
		t.Fatalf("expected 2 deployments, got %d", len(configs[0].Clusters[0].Deployments))
	}
	if configs[0].Clusters[0].Deployments[0].App != "payments-service" {
		t.Errorf("expected payments-service, got %s", configs[0].Clusters[0].Deployments[0].App)
	}
	if len(configs[0].Clusters[0].Deployments[0].Namespaces) != 2 {
		t.Errorf("expected 2 namespaces, got %d", len(configs[0].Clusters[0].Deployments[0].Namespaces))
	}
}

func TestLoadEnvConfigs_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadEnvConfigs(dir)
	if err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestLoadSlackRouting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.yaml")
	data := `payments-service: "#payments-deploys"
checkout-api: "#checkout-deploys"
`
	os.WriteFile(path, []byte(data), 0644)

	routing, err := LoadSlackRouting(path)
	if err != nil {
		t.Fatalf("LoadSlackRouting: %v", err)
	}
	if routing["payments-service"] != "#payments-deploys" {
		t.Errorf("expected #payments-deploys, got %s", routing["payments-service"])
	}
	if routing["checkout-api"] != "#checkout-deploys" {
		t.Errorf("expected #checkout-deploys, got %s", routing["checkout-api"])
	}
}

func TestBuildNamespaceLookup(t *testing.T) {
	configs := []EnvConfig{{
		Clusters: []ClusterDeployments{
			{
				Name: "prod-us-east",
				Deployments: []AppDeployment{
					{App: "payments-service", Namespaces: []string{"payments", "payments-jobs"}},
					{App: "checkout-api", Namespaces: []string{"checkout"}},
				},
			},
		},
	}}

	lookup := BuildNamespaceLookup(configs)

	if app := lookup.GetApp("prod-us-east", "payments"); app != "payments-service" {
		t.Errorf("expected payments-service, got %s", app)
	}
	if app := lookup.GetApp("prod-us-east", "payments-jobs"); app != "payments-service" {
		t.Errorf("expected payments-service, got %s", app)
	}
	if app := lookup.GetApp("prod-us-east", "checkout"); app != "checkout-api" {
		t.Errorf("expected checkout-api, got %s", app)
	}
	if app := lookup.GetApp("prod-us-east", "unknown"); app != "" {
		t.Errorf("expected empty, got %s", app)
	}
	if app := lookup.GetApp("unknown-cluster", "payments"); app != "" {
		t.Errorf("expected empty for unknown cluster, got %s", app)
	}
}

func TestNamespaceLookup_GetSlackChannel(t *testing.T) {
	configs := []EnvConfig{{
		Clusters: []ClusterDeployments{
			{
				Name: "prod",
				Deployments: []AppDeployment{
					{App: "myapp", Namespaces: []string{"default"}},
				},
			},
		},
	}}
	lookup := BuildNamespaceLookup(configs)
	routing := SlackRouting{"myapp": "#myapp-deploys"}

	ch := lookup.GetSlackChannel("prod", "default", routing)
	if ch != "#myapp-deploys" {
		t.Errorf("expected #myapp-deploys, got %s", ch)
	}

	// Unknown namespace returns empty
	ch = lookup.GetSlackChannel("prod", "unknown", routing)
	if ch != "" {
		t.Errorf("expected empty, got %s", ch)
	}
}

func TestRequiredClusters(t *testing.T) {
	configs := []EnvConfig{
		{Clusters: []ClusterDeployments{{Name: "a"}, {Name: "b"}}},
		{Clusters: []ClusterDeployments{{Name: "b"}, {Name: "c"}}},
	}
	clusters := RequiredClusters(configs)
	if len(clusters) != 3 {
		t.Errorf("expected 3 unique clusters, got %d", len(clusters))
	}
}

func TestAllowedNamespaces(t *testing.T) {
	configs := []EnvConfig{{
		Clusters: []ClusterDeployments{
			{
				Name: "prod",
				Deployments: []AppDeployment{
					{App: "a", Namespaces: []string{"ns1", "ns2"}},
					{App: "b", Namespaces: []string{"ns3"}},
				},
			},
		},
	}}
	ns := AllowedNamespaces(configs, "prod")
	if len(ns) != 3 {
		t.Errorf("expected 3 namespaces, got %d", len(ns))
	}
	if !ns["ns1"] || !ns["ns2"] || !ns["ns3"] {
		t.Errorf("missing expected namespace: %v", ns)
	}
}
