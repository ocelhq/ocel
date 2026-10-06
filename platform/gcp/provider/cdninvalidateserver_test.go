package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/api/compute/v1"
)

type invalidateServer struct {
	mu     sync.Mutex
	paths  []string
	rules  []compute.CacheInvalidationRule
	status int
}

func invalidating(t *testing.T, status int) (*Provider, *invalidateServer) {
	t.Helper()
	server := &invalidateServer{status: status}
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		defer server.mu.Unlock()
		var rule compute.CacheInvalidationRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		server.paths = append(server.paths, r.URL.Path)
		server.rules = append(server.rules, rule)
		w.Header().Set("Content-Type", "application/json")
		if server.status != 0 {
			w.WriteHeader(server.status)
			writeBody(w, map[string]any{"error": map[string]any{"code": server.status, "message": "refused"}})
			return
		}
		writeBody(w, &compute.Operation{Name: "op"})
	}))
	t.Cleanup(served.Close)
	return pushing(t, served.URL), server
}
