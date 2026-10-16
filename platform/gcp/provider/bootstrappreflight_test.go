package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestABootstrapNeedingMoreThanAHundredPermissionsAsksForThemInBatchesGoogleAccepts(t *testing.T) {
	t.Parallel()

	parts := []string{cloudflareOriginPart}
	if n := len(permissionsFor(parts)); n <= 100 {
		t.Fatalf("the parts need %d permissions, want more than 100 to cross Google's limit", n)
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

	if err := b.permitted(context.Background(), parts); err != nil {
		t.Fatalf("permitted: %v", err)
	}
	if want := len(permissionsFor(parts)); asked != want {
		t.Errorf("asked about %d permissions, want %d", asked, want)
	}
}

func servingFirestoreIn(t *testing.T, regions ...string) *clients {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/acme-prod/locations" {
			t.Errorf("the Firestore check called %s %s, want only Firestore's locations of project acme-prod listed", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var locations []map[string]string
		for _, region := range regions {
			locations = append(locations, map[string]string{"name": "projects/acme-prod/locations/" + region, "locationId": region})
		}
		w.Header().Set("Content-Type", "application/json")
		writeBody(w, map[string]any{"locations": locations})
	}))
	t.Cleanup(served.Close)
	return &clients{Names: Names{namespace: provider.Namespace("ocel"), project: "acme-prod"}, endpoint: served.URL}
}

func TestARegionCloudRunServesAndFirestoreDoesNotIsRefusedNamingTheOnesFirestoreServes(t *testing.T) {
	t.Parallel()

	b := bootstrap{clients: servingFirestoreIn(t, "us-east1", "europe-west1")}

	err := b.regionServed(context.Background(), survey{Region: "europe-west12"})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("regionServed(europe-west12) = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	want := `option "region" names europe-west12, and the one region a bootstrap is given hosts this project's database as well as its buckets and keys: ` +
		"Firestore does not serve europe-west12.\nName one Firestore serves: europe-west1, us-east1"
	if refused.Message != want {
		t.Errorf("refused with\n%s\nwant\n%s", refused.Message, want)
	}
}

func TestARegionFirestoreServesIsAdmitted(t *testing.T) {
	t.Parallel()

	b := bootstrap{clients: servingFirestoreIn(t, "us-east1", "europe-west1")}

	if err := b.regionServed(context.Background(), survey{Region: "europe-west1"}); err != nil {
		t.Errorf("regionServed(europe-west1) = %v, want it admitted", err)
	}
}
