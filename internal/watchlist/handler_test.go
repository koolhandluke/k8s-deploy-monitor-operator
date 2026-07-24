package watchlist

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestHandler(t *testing.T) (http.Handler, *Store, chan struct{}) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()
	store := NewStore(c, "test-ns", allClustersKnown)
	trigger := make(chan struct{}, 1)
	handler := NewHandler(store, trigger)
	return handler, store, trigger
}

func TestHandler_GET_Empty(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/watchlist", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var apps []AppSummary
	json.NewDecoder(w.Body).Decode(&apps)
	if len(apps) != 0 {
		t.Fatalf("expected empty list, got %d", len(apps))
	}
}

func TestHandler_PUT_Created(t *testing.T) {
	handler, _, trigger := newTestHandler(t)

	body := `{"slackChannel":"#deploys","clusters":{"cluster-a":["prod","staging"]}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/my-app", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var result PutResult
	json.NewDecoder(w.Body).Decode(&result)
	if !result.Created {
		t.Fatal("expected Created=true")
	}

	// Trigger channel should have been signaled
	select {
	case <-trigger:
	default:
		t.Fatal("expected reconcile trigger")
	}
}

func TestHandler_PUT_Updated(t *testing.T) {
	handler, _, _ := newTestHandler(t)

	body := `{"slackChannel":"#ch","clusters":{"cluster-a":["ns1"]}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/my-app", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first PUT: expected 201, got %d", w.Code)
	}

	body2 := `{"slackChannel":"#ch2","clusters":{"cluster-b":["ns2"]}}`
	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/my-app", bytes.NewBufferString(body2))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second PUT: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestHandler_PUT_BadJSON(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/my-app", bytes.NewBufferString("{invalid"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandler_PUT_NoClusters(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	body := `{"slackChannel":"#ch"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/my-app", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandler_PUT_Conflict(t *testing.T) {
	handler, _, _ := newTestHandler(t)

	body := `{"clusters":{"cluster-a":["prod"]}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/app-1", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/app-2", bytes.NewBufferString(body))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestHandler_PUT_PartialAcceptance(t *testing.T) {
	handler, _, _ := newTestHandler(t)

	body := `{"clusters":{"cluster-a":["ns1"],"unknown-x":["ns2"]}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/my-app", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var result PutResult
	json.NewDecoder(w.Body).Decode(&result)
	if _, ok := result.Accepted["cluster-a"]; !ok {
		t.Fatal("cluster-a should be accepted")
	}
	if _, ok := result.Rejected["unknown-x"]; !ok {
		t.Fatal("unknown-x should be rejected")
	}
}

func TestHandler_DELETE_Success(t *testing.T) {
	handler, _, trigger := newTestHandler(t)

	// Create first
	body := `{"clusters":{"cluster-a":["prod"]}}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/my-app", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	// Drain trigger from PUT
	<-trigger

	// Delete
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/watchlist/my-app", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	select {
	case <-trigger:
	default:
		t.Fatal("expected reconcile trigger after delete")
	}
}

func TestHandler_DELETE_NotFound(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/watchlist/nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	handler, _, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/watchlist", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}
