package watchlist

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	v1alpha1 "github.com/koolhandluke/k8s-deploy-monitor-operator/api/v1alpha1"
)

// NewHandler returns an http.Handler serving the watchlist API.
//
//	GET    /api/v1/watchlist       → list all apps
//	PUT    /api/v1/watchlist/{app} → register/update app
//	DELETE /api/v1/watchlist/{app} → unregister app
func NewHandler(store *Store, reconcileTrigger chan<- struct{}) http.Handler {
	mux := http.NewServeMux()

	handler := func(w http.ResponseWriter, r *http.Request) {
		sub := strings.TrimPrefix(r.URL.Path, "/api/v1/watchlist")
		sub = strings.TrimPrefix(sub, "/")

		switch {
		case sub == "" && r.Method == http.MethodGet:
			handleListApps(store, w)
		case sub != "" && r.Method == http.MethodPut:
			handlePutApp(store, reconcileTrigger, sub, w, r)
		case sub != "" && r.Method == http.MethodDelete:
			handleDeleteApp(store, reconcileTrigger, sub, w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}

	mux.HandleFunc("/api/v1/watchlist", handler)
	mux.HandleFunc("/api/v1/watchlist/", handler)

	return mux
}

func handleListApps(store *Store, w http.ResponseWriter) {
	apps := store.ListAll()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apps)
}

// putRequest is the JSON body for PUT /api/v1/watchlist/{app}.
type putRequest struct {
	SlackChannel string              `json:"slackChannel"`
	Clusters     map[string][]string `json:"clusters"`
}

func handlePutApp(store *Store, trigger chan<- struct{}, app string, w http.ResponseWriter, r *http.Request) {
	var req putRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Clusters) == 0 {
		http.Error(w, "clusters field is required", http.StatusBadRequest)
		return
	}

	spec := &v1alpha1.AppWatchConfigSpec{
		SlackChannel: req.SlackChannel,
		Clusters:     req.Clusters,
	}

	result, err := store.Put(r.Context(), app, spec)
	if err != nil {
		if ce, ok := err.(*ConflictError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":     ce.Error(),
				"conflicts": ce.Conflicts,
			})
			return
		}
		slog.Error("watchlist put failed", "app", app, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	triggerReconcile(trigger)

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(result)
}

func handleDeleteApp(store *Store, trigger chan<- struct{}, app string, w http.ResponseWriter, r *http.Request) {
	if err := store.Delete(r.Context(), app); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "app not found", http.StatusNotFound)
			return
		}
		slog.Error("watchlist delete failed", "app", app, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	triggerReconcile(trigger)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "app": app})
}

// triggerReconcile sends a non-blocking signal to the reconcile trigger channel.
func triggerReconcile(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
