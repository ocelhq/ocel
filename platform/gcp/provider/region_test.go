package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type runLocation struct {
	Name       string `json:"name"`
	LocationID string `json:"locationId"`
}

type runLocationsPage struct {
	Locations     []runLocation `json:"locations"`
	NextPageToken string        `json:"nextPageToken,omitempty"`
}

func servingRunIn(t *testing.T, pages ...[]string) *gcloud.Clients {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/acme-prod/locations" {
			t.Errorf("the region check called %s %s, want only Cloud Run's locations of project acme-prod listed", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		index := 0
		if token := r.URL.Query().Get("pageToken"); token != "" {
			index = len(token)
		}
		page := runLocationsPage{}
		for _, id := range pages[index] {
			page.Locations = append(page.Locations, runLocation{Name: "projects/acme-prod/locations/" + id, LocationID: id})
		}
		if index+1 < len(pages) {
			page.NextPageToken = strings.Repeat("n", index+1)
		}
		w.Header().Set("Content-Type", "application/json")
		googletest.WriteBody(w, page)
	}))
	t.Cleanup(served.Close)
	return &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Endpoint: served.URL}
}

func refusedRegion(t *testing.T, c *gcloud.Clients, region string) refusal.Refusal {
	t.Helper()
	c.Region = region
	err := c.RefuseRegionNotServed(context.Background())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("refuseRegionNotServed(%q) = %v, want an %s refusal", region, err, refusal.CodeInvalid)
	}
	return refused
}

func TestAZoneIsRefusedNamingTheRegionItSitsIn(t *testing.T) {
	t.Parallel()

	refused := refusedRegion(t, servingRunIn(t, []string{"us-east1", "europe-west1", "us-central1"}), "us-central1-f")

	want := `option "region" names "us-central1-f", which is not a region Cloud Run serves. Did you mean "us-central1"? ` +
		"Regions: europe-west1, us-central1, us-east1. See https://docs.cloud.google.com/run/docs/locations"
	if refused.Message != want {
		t.Errorf("refused with\n%s\nwant\n%s", refused.Message, want)
	}
}

func TestAMistypedRegionIsRefusedNamingTheOneMeant(t *testing.T) {
	t.Parallel()

	refused := refusedRegion(t, servingRunIn(t, []string{"us-central1", "us-east1"}), "us-centrl1")

	if !strings.Contains(refused.Message, `Did you mean "us-central1"?`) {
		t.Errorf("refused with %q, want it to suggest us-central1", refused.Message)
	}
}

func TestARegionResemblingNoneIsRefusedListingThemWithoutAGuess(t *testing.T) {
	t.Parallel()

	refused := refusedRegion(t, servingRunIn(t, []string{"us-central1", "us-east1"}), "no-such-region1")

	if strings.Contains(refused.Message, "Did you mean") || !strings.Contains(refused.Message, "Regions: us-central1, us-east1.") {
		t.Errorf("refused with %q, want the regions listed and no guess", refused.Message)
	}
}

func TestARegionCloudRunServesIsAdmittedWhicheverPageListsIt(t *testing.T) {
	t.Parallel()

	c := servingRunIn(t, []string{"us-central1"}, []string{"europe-west1"})
	c.Region = "europe-west1"

	if err := c.RefuseRegionNotServed(context.Background()); err != nil {
		t.Errorf("refuseRegionNotServed(europe-west1) = %v, want it admitted", err)
	}
}

func TestABootstrapChecksThePermissionToListTheRegionsCloudRunServes(t *testing.T) {
	t.Parallel()

	for _, parts := range [][]string{nil, {bootstrap.CloudflareOriginPart}} {
		if !slices.Contains(bootstrap.PermissionsFor(parts), "run.locations.list") {
			t.Errorf("a bootstrap with parts %v checks %v, want run.locations.list among them: every run checks its region against the ones Cloud Run lists", parts, bootstrap.PermissionsFor(parts))
		}
	}
}

func TestACredentialThatMayNotListTheRegionsCloudRunServesIsRefusedNamingThePermission(t *testing.T) {
	t.Parallel()

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		googletest.WriteBody(w, map[string]any{"error": map[string]any{"code": http.StatusForbidden, "status": "PERMISSION_DENIED",
			"message": "Permission 'run.locations.list' denied on resource 'projects/acme-prod'"}})
	}))
	t.Cleanup(denied.Close)
	c := &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Endpoint: denied.URL, Region: "us-central1"}

	err := c.RefuseRegionNotServed(context.Background())

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("refuseRegionNotServed() = %v, want a %s refusal", err, refusal.CodeDenied)
	}
	want := "this credential may not list the regions Cloud Run serves project acme-prod from, which every run checks its region against: it lacks run.locations.list.\n" +
		"Granting roles/run.admin covers it, but the permission is what is checked"
	if refused.Message != want {
		t.Errorf("refused with\n%s\nwant\n%s", refused.Message, want)
	}
}

func TestARunChecksItsRegionAgainstCloudRunOnlyWhenTheChecksAreNotSkipped(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-such-key.json"))
	for skipChecks, wantChecked := range map[bool]bool{false: true, true: false} {
		p := &Provider{
			options:    Options{Project: "acme-prod", Region: "europe-west1"},
			resolved:   &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Region: "europe-west1"},
			skipChecks: skipChecks,
		}

		_, err := p.openClients(context.Background())
		if checked := err != nil; checked != wantChecked {
			t.Errorf("openClients() with the checks skipped %t = %v, want the region checked %t: with no credential, only the check reaches for one", skipChecks, err, wantChecked)
		}
	}
}
