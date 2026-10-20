package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestABootstrapNeedingMoreThanAHundredPermissionsAsksForThemInBatchesGoogleAccepts(t *testing.T) {
	t.Parallel()

	parts := []string{bootstrap.CloudflareOriginPart}
	if n := len(bootstrap.PermissionsFor(parts)); n <= 100 {
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
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL}})

	if err := b.Permitted(context.Background(), parts); err != nil {
		t.Fatalf("permitted: %v", err)
	}
	if want := len(bootstrap.PermissionsFor(parts)); asked != want {
		t.Errorf("asked about %d permissions, want %d", asked, want)
	}
}

func servingFirestoreIn(t *testing.T, regions ...string) *gcloud.Clients {
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
		googletest.WriteBody(w, map[string]any{"locations": locations})
	}))
	t.Cleanup(served.Close)
	return &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Endpoint: served.URL}
}

func TestARegionCloudRunServesAndFirestoreDoesNotIsRefusedNamingTheOnesFirestoreServes(t *testing.T) {
	t.Parallel()

	b := composed(bootstrap.Engine{Clients: servingFirestoreIn(t, "us-east1", "europe-west1")})

	err := b.RegionServed(context.Background(), bootstrap.Survey{Region: "europe-west12"})

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

	b := composed(bootstrap.Engine{Clients: servingFirestoreIn(t, "us-east1", "europe-west1")})

	if err := b.RegionServed(context.Background(), bootstrap.Survey{Region: "europe-west1"}); err != nil {
		t.Errorf("regionServed(europe-west1) = %v, want it admitted", err)
	}
}

func TestABootstrapChecksThePermissionsItFindsAndMendsItsBucketsWith(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{"storage.buckets.list", "storage.buckets.update"} {
		if !slices.Contains(bootstrap.BootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the survey finds a tier's randomly tagged buckets by listing them and mends them by updating", permission)
		}
	}
}

func TestABootstrapChecksThePermissionsItsAccountGrantsNeed(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{
		"resourcemanager.projects.getIamPolicy", "resourcemanager.projects.setIamPolicy",
		"cloudkms.cryptoKeys.getIamPolicy", "cloudkms.cryptoKeys.setIamPolicy",
	} {
		if !slices.Contains(bootstrap.BootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at an account's grant", permission)
		}
	}
	if !slices.Contains(bootstrap.RolesCovering(nil), "roles/resourcemanager.projectIamAdmin") {
		t.Errorf("the refusal names %v, want roles/resourcemanager.projectIamAdmin among them: it is what covers a project policy write", bootstrap.RolesCovering(nil))
	}
}

func TestAProjectOwnerIsPermittedABootstrapThoughOnlyTheBucketsItCreatesGrantItTheirObjects(t *testing.T) {
	t.Parallel()

	bucketOnly := []string{"storage.buckets.get", "storage.objects.create", "storage.objects.list", "storage.objects.delete"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Permissions []string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode testIamPermissions: %v", err)
		}
		granted := slices.DeleteFunc(request.Permissions, func(permission string) bool {
			return slices.Contains(bucketOnly, permission)
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string][]string{"permissions": granted})
	}))
	t.Cleanup(server.Close)
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL}})

	if err := b.Permitted(context.Background(), []string{bootstrap.CloudflareOriginPart}); err != nil {
		t.Fatalf("permitted: %v", err)
	}
}
