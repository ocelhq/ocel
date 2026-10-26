package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestMoreThanAHundredPermissionsAreAskedAboutInBatchesGoogleAccepts(t *testing.T) {
	t.Parallel()

	var needed []string
	for i := range 150 {
		needed = append(needed, fmt.Sprintf("service.resources.verb%d", i))
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
	b := Engine{ServiceStates: &ServiceStates{}, Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL}}

	granted, err := b.heldPermissions(context.Background(), needed)
	if err != nil {
		t.Fatalf("heldPermissions: %v", err)
	}
	if asked != len(needed) || len(granted) != len(needed) {
		t.Errorf("asked about %d permissions and was granted %d, want all %d", asked, len(granted), len(needed))
	}
}
