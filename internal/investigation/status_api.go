package investigation

import (
	"encoding/json"
	"net/http"
	"strings"
)

// statusJSON mirrors InvestigationStatus but with a human-readable duration string.
type statusJSON struct {
	DeploymentKey string `json:"deployment_key"`
	Result        string `json:"result"`
	FailureReason string `json:"failure_reason,omitempty"`
	Duration      string `json:"duration"`
	Timestamp     string `json:"timestamp"`
}

func toStatusJSON(s InvestigationStatus) statusJSON {
	return statusJSON{
		DeploymentKey: s.DeploymentKey,
		Result:        string(s.Result),
		FailureReason: s.FailureReason,
		Duration:      s.Duration.String(),
		Timestamp:     s.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// NewStatusHandler returns an http.Handler serving the investigation status API.
//
//	GET /api/v1/investigations                    → all entries
//	GET /api/v1/investigations/{namespace}/{name} → single entry (matches suffix)
func NewStatusHandler(cache *StatusCache) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/investigations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Strip the prefix to check for a sub-path: /api/v1/investigations/{namespace}/{name}
		sub := strings.TrimPrefix(r.URL.Path, "/api/v1/investigations")
		sub = strings.TrimPrefix(sub, "/")

		if sub != "" {
			handleSingle(cache, sub, w)
			return
		}

		handleList(cache, w)
	})

	return mux
}

func handleList(cache *StatusCache, w http.ResponseWriter) {
	entries := cache.List()
	out := make([]statusJSON, len(entries))
	for i, s := range entries {
		out[i] = toStatusJSON(s)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func handleSingle(cache *StatusCache, sub string, w http.ResponseWriter) {
	// sub is "namespace/name" — match any deployment key ending with this suffix
	suffix := "/" + sub
	entries := cache.List()
	for _, s := range entries {
		if strings.HasSuffix(s.DeploymentKey, suffix) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(toStatusJSON(s))
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}
