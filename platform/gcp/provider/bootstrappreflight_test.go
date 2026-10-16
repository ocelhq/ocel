package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestABootstrapNeedingMoreThanAHundredPermissionsAsksForThemInBatchesGoogleAccepts(t *testing.T) {
	t.Parallel()

	features := []string{albShieldedFeature, networkFeature, tasksFeature}
	if n := len(permissionsFor(features)); n <= 100 {
		t.Fatalf("the features need %d permissions, want more than 100 to cross Google's limit", n)
	}
	var asked int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Permissions []string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode testIamPermissions: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if len(request.Permissions) > 100 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"message":"The number of permissions is greater than the maximum allowed (100).","status":"INVALID_ARGUMENT"}}`))
			return
		}
		asked += len(request.Permissions)
		json.NewEncoder(w).Encode(map[string][]string{"permissions": request.Permissions})
	}))
	t.Cleanup(server.Close)
	b := bootstrap{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1", endpoint: server.URL}}

	if err := b.permitted(context.Background(), features); err != nil {
		t.Fatalf("permitted: %v", err)
	}
	if want := len(permissionsFor(features)); asked != want {
		t.Errorf("asked about %d permissions, want %d", asked, want)
	}
}
