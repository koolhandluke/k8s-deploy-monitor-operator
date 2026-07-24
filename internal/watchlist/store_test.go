package watchlist

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	v1alpha1.AddToScheme(s)
	return s
}

func allClustersKnown() map[string]bool {
	return map[string]bool{"cluster-a": true, "cluster-b": true, "cluster-c": true}
}

func TestLoadAll_Empty(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	if err := s.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll empty: %v", err)
	}
	if len(s.apps) != 0 {
		t.Fatalf("expected 0 apps, got %d", len(s.apps))
	}
}

func TestLoadAll_WithData(t *testing.T) {
	existing := &v1alpha1.AppWatchConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-app",
			Namespace: "test-ns",
		},
		Spec: v1alpha1.AppWatchConfigSpec{
			SlackChannel: "#deploys",
			Clusters: map[string][]string{
				"cluster-a": {"default", "staging"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(existing).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	if err := s.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if len(s.apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(s.apps))
	}

	entry, ok := s.Lookup("cluster-a", "default")
	if !ok {
		t.Fatal("expected lookup hit for cluster-a/default")
	}
	if entry.App != "my-app" || entry.SlackChannel != "#deploys" {
		t.Fatalf("unexpected entry: %+v", entry)
	}

	_, ok = s.Lookup("cluster-a", "production")
	if ok {
		t.Fatal("expected lookup miss for cluster-a/production")
	}
}

func TestPut_New(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	spec := &v1alpha1.AppWatchConfigSpec{
		SlackChannel: "#alerts",
		Clusters:     map[string][]string{"cluster-a": {"prod"}},
	}
	result, err := s.Put(context.Background(), "new-app", spec)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !result.Created {
		t.Fatal("expected Created=true")
	}
	if len(result.Rejected) != 0 {
		t.Fatalf("expected no rejections, got %v", result.Rejected)
	}

	entry, ok := s.Lookup("cluster-a", "prod")
	if !ok || entry.App != "new-app" {
		t.Fatalf("lookup after Put failed: ok=%v, entry=%+v", ok, entry)
	}
}

func TestPut_Update(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	spec1 := &v1alpha1.AppWatchConfigSpec{
		SlackChannel: "#old",
		Clusters:     map[string][]string{"cluster-a": {"ns1"}},
	}
	s.Put(context.Background(), "app", spec1)

	spec2 := &v1alpha1.AppWatchConfigSpec{
		SlackChannel: "#new",
		Clusters:     map[string][]string{"cluster-b": {"ns2"}},
	}
	result, err := s.Put(context.Background(), "app", spec2)
	if err != nil {
		t.Fatalf("Put update: %v", err)
	}
	if result.Created {
		t.Fatal("expected Created=false on update")
	}

	// Old entry removed
	_, ok := s.Lookup("cluster-a", "ns1")
	if ok {
		t.Fatal("old index entry should be removed")
	}
	// New entry present
	entry, ok := s.Lookup("cluster-b", "ns2")
	if !ok || entry.SlackChannel != "#new" {
		t.Fatalf("new entry missing or wrong: %+v", entry)
	}
}

func TestPut_UnknownClusters(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	spec := &v1alpha1.AppWatchConfigSpec{
		Clusters: map[string][]string{
			"cluster-a":   {"ns1"},
			"unknown-xyz": {"ns2"},
		},
	}
	result, err := s.Put(context.Background(), "app", spec)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := result.Accepted["cluster-a"]; !ok {
		t.Fatal("cluster-a should be accepted")
	}
	if _, ok := result.Rejected["unknown-xyz"]; !ok {
		t.Fatal("unknown-xyz should be rejected")
	}
}

func TestPut_AllUnknownClusters(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	spec := &v1alpha1.AppWatchConfigSpec{
		Clusters: map[string][]string{
			"unknown-1": {"ns1"},
			"unknown-2": {"ns2"},
		},
	}
	result, err := s.Put(context.Background(), "app", spec)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(result.Accepted) != 0 {
		t.Fatalf("expected 0 accepted, got %d", len(result.Accepted))
	}
	if len(result.Rejected) != 2 {
		t.Fatalf("expected 2 rejected, got %d", len(result.Rejected))
	}
}

func TestPut_Conflict(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	spec1 := &v1alpha1.AppWatchConfigSpec{
		Clusters: map[string][]string{"cluster-a": {"prod"}},
	}
	s.Put(context.Background(), "app-1", spec1)

	spec2 := &v1alpha1.AppWatchConfigSpec{
		Clusters: map[string][]string{"cluster-a": {"prod"}},
	}
	_, err := s.Put(context.Background(), "app-2", spec2)
	if err == nil {
		t.Fatal("expected ConflictError")
	}
	ce, ok := err.(*ConflictError)
	if !ok {
		t.Fatalf("expected *ConflictError, got %T", err)
	}
	if len(ce.Conflicts) != 1 || ce.Conflicts[0].OwnerApp != "app-1" {
		t.Fatalf("unexpected conflicts: %+v", ce.Conflicts)
	}
}

func TestDelete_Existing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	spec := &v1alpha1.AppWatchConfigSpec{
		Clusters: map[string][]string{"cluster-a": {"prod"}},
	}
	s.Put(context.Background(), "app", spec)

	if err := s.Delete(context.Background(), "app"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, ok := s.Lookup("cluster-a", "prod")
	if ok {
		t.Fatal("expected lookup miss after delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	err := s.Delete(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent app")
	}
}

func TestDesiredClusters(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	s.Put(context.Background(), "app-1", &v1alpha1.AppWatchConfigSpec{
		Clusters: map[string][]string{"cluster-a": {"ns1"}},
	})
	s.Put(context.Background(), "app-2", &v1alpha1.AppWatchConfigSpec{
		Clusters: map[string][]string{"cluster-b": {"ns2"}, "cluster-a": {"ns3"}},
	})

	desired := s.DesiredClusters()
	if !desired["cluster-a"] || !desired["cluster-b"] {
		t.Fatalf("expected cluster-a and cluster-b in desired, got %v", desired)
	}
	if desired["cluster-c"] {
		t.Fatal("cluster-c should not be in desired")
	}
}

func TestListAll(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	s := NewStore(c, "test-ns", allClustersKnown)

	s.Put(context.Background(), "app-1", &v1alpha1.AppWatchConfigSpec{
		SlackChannel: "#ch1",
		Clusters:     map[string][]string{"cluster-a": {"ns1"}},
	})
	s.Put(context.Background(), "app-2", &v1alpha1.AppWatchConfigSpec{
		SlackChannel: "#ch2",
		Clusters:     map[string][]string{"cluster-b": {"ns2"}},
	})

	all := s.ListAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(all))
	}
}
