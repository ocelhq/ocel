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

func TestABootstrapListsNoBucket(t *testing.T) {
	t.Parallel()
	if slices.Contains(bootstrap.BootstrapGrants.Permissions(), "storage.buckets.list") {
		t.Error("a bootstrap checks storage.buckets.list, and it finds no bucket by listing: it records each name before it creates the bucket")
	}
}

func TestABootstrapChecksThePermissionsItsAccountGrantsNeed(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{
		"resourcemanager.projects.getIamPolicy", "resourcemanager.projects.setIamPolicy",
		"cloudkms.cryptoKeys.getIamPolicy", "cloudkms.cryptoKeys.setIamPolicy",
	} {
		if !slices.Contains(bootstrap.BootstrapGrants.Permissions(), permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at an account's grant", permission)
		}
	}
	if !slices.Contains(bootstrap.GrantsFor(nil).Roles(), "roles/resourcemanager.projectIamAdmin") {
		t.Errorf("the refusal names %v, want roles/resourcemanager.projectIamAdmin among them: it is what covers a project policy write", bootstrap.GrantsFor(nil).Roles())
	}
}

func TestAProjectOwnerIsPermittedABootstrapThoughOnlyTheBucketsItCreatesGrantItTheirObjects(t *testing.T) {
	t.Parallel()

	bucketOnly := []string{"storage.buckets.get", "storage.buckets.update", "storage.objects.create", "storage.objects.list", "storage.objects.delete"}
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

func withholding(t *testing.T, withheld ...string) *gcloud.Clients {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Permissions []string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode testIamPermissions: %v", err)
		}
		granted := slices.DeleteFunc(request.Permissions, func(permission string) bool {
			return slices.Contains(withheld, permission)
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string][]string{"permissions": granted})
	}))
	t.Cleanup(server.Close)
	return &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL}
}

func TestARefusedCredentialIsToldOnlyWhatItLacksAndTheRolesThatGrantIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		parts    []string
		withheld []string
		want     string
	}{
		{
			withheld: []string{"storage.buckets.delete"},
			want:     "this credential lacks storage.buckets.delete on project acme-prod; grant it roles/storage.admin",
		},
		{
			withheld: []string{"storage.buckets.create", "storage.buckets.delete"},
			want:     "this credential lacks storage.buckets.create, storage.buckets.delete on project acme-prod; grant it roles/storage.admin",
		},
		{
			withheld: []string{"storage.buckets.delete", "iam.serviceAccounts.actAs"},
			want:     "this credential lacks storage.buckets.delete, iam.serviceAccounts.actAs on project acme-prod; grant it roles/storage.admin, roles/iam.serviceAccountUser",
		},
		{
			parts:    []string{bootstrap.CloudflareOriginPart},
			withheld: []string{"storage.hmacKeys.create"},
			want:     "this credential lacks storage.hmacKeys.create on project acme-prod; grant it roles/storage.hmacKeyAdmin",
		},
	} {
		b := composed(bootstrap.Engine{Clients: withholding(t, tc.withheld...)})

		err := b.Permitted(context.Background(), tc.parts)

		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
			t.Fatalf("permitted() withholding %v = %v, want a %s refusal", tc.withheld, err, refusal.CodeDenied)
		}
		if refused.Message != tc.want {
			t.Errorf("permitted() withholding %v refused with\n%s\nwant\n%s", tc.withheld, refused.Message, tc.want)
		}
	}
}

func TestEveryPermissionABootstrapChecksIsHeldByTheRoleItIsGroupedUnder(t *testing.T) {
	t.Parallel()

	predefined := predefinedRoles(t)
	for _, parts := range [][]string{nil, {bootstrap.CloudflareOriginPart}} {
		for _, grant := range bootstrap.GrantsFor(parts) {
			held := rolePermissions(t, predefined, grant.Role)
			for _, permission := range grant.Permissions {
				if !slices.Contains(held, permission) {
					t.Errorf("a bootstrap with parts %v groups %s under %s, which does not hold it", parts, permission, grant.Role)
				}
			}
		}
	}
}

func TestEveryRoleAPlainBootstrapRefusalCanNameIsOneItsCredentialIsGranted(t *testing.T) {
	t.Parallel()

	for _, role := range bootstrap.GrantsFor(nil).Roles() {
		if !slices.Contains(bootstrap.BootstrapRoles(), role) {
			t.Errorf("a bootstrap groups permissions under %s, and its credential is granted only %v", role, bootstrap.BootstrapRoles())
		}
	}
}

func TestABootstrapCredentialMayBillItsCallsToTheProjectItBootstraps(t *testing.T) {
	t.Parallel()

	if !slices.Contains(bootstrap.BootstrapRoles(), "roles/serviceusage.serviceUsageConsumer") {
		t.Errorf("a bootstrap credential is granted %v, want roles/serviceusage.serviceUsageConsumer: a user's credential names the project as the quota project of every call, and Google refuses each with USER_PROJECT_DENIED without serviceusage.services.use", bootstrap.BootstrapRoles())
	}
}

func TestNeitherABootstrapNorThePartsRoleAsksForThePermissionToSwitchServicesOn(t *testing.T) {
	t.Parallel()

	for _, parts := range [][]string{nil, {bootstrap.CloudflareOriginPart}} {
		if slices.Contains(bootstrap.PermissionsFor(parts), "serviceusage.services.enable") {
			t.Errorf("a bootstrap with parts %v demands serviceusage.services.enable, which only a run that finds a service off needs", parts)
		}
	}
	if slices.Contains(bootstrap.NewPartsRole(names.New("ocel", "acme-prod")).Permissions, "serviceusage.services.enable") {
		t.Error("the parts role holds serviceusage.services.enable, and it lets a deploy add the parts it needs and nothing else")
	}
}
