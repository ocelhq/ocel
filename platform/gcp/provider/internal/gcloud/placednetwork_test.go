package gcloud

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAPlacedNetworkIsNamedByItsPathInTheDeploysProject(t *testing.T) {
	t.Parallel()

	for _, written := range []string{
		"shared",
		"projects/acme-prod/global/networks/shared",
		"https://www.googleapis.com/compute/v1/projects/acme-prod/global/networks/shared",
	} {
		got, err := PlacedNetworkPath("acme-prod", written)
		if err != nil || got != "projects/acme-prod/global/networks/shared" {
			t.Errorf("PlacedNetworkPath(%q) = %q, %v, want projects/acme-prod/global/networks/shared", written, got, err)
		}
	}
}

func TestAPlacedNetworkInAnotherProjectIsRefused(t *testing.T) {
	t.Parallel()

	_, err := PlacedNetworkPath("acme-prod", "projects/acme-shared/global/networks/shared")
	if err == nil || !strings.Contains(err.Error(), "acme-shared") || !strings.Contains(err.Error(), "acme-prod") {
		t.Errorf("PlacedNetworkPath() of another project's network = %v, want an error naming both projects", err)
	}
	for _, written := range []string{"", "Shared", "projects/acme-prod/regions/europe-west1/subnetworks/shared"} {
		if _, err := PlacedNetworkPath("acme-prod", written); err == nil {
			t.Errorf("PlacedNetworkPath(%q) = nil error, want it refused as no network", written)
		}
	}
}

func TestAnEndpointIsNeverMovedToAnotherNetwork(t *testing.T) {
	t.Parallel()

	held := []string{"projects/acme-prod/global/networks/ocel-prod-network"}
	if err := RefuseEndpointMove("postgres orders", "Cloud SQL instance db-1", held, held[0]); err != nil {
		t.Errorf("RefuseEndpointMove() onto the network it is on = %v, want nothing to refuse", err)
	}
	if err := RefuseEndpointMove("postgres orders", "Cloud SQL instance db-1", nil, "projects/acme-prod/global/networks/shared"); err != nil {
		t.Errorf("RefuseEndpointMove() of an instance with no endpoint = %v, want nothing to refuse", err)
	}
	err := RefuseEndpointMove("postgres orders", "Cloud SQL instance db-1", held, "projects/acme-prod/global/networks/shared")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid ||
		!strings.Contains(refused.Message, "ocel-prod-network") || !strings.Contains(refused.Message, "shared") || !strings.Contains(refused.Message, "db-1") {
		t.Errorf("RefuseEndpointMove() onto another network = %v, want an %s refusal naming the instance and both networks", err, refusal.CodeInvalid)
	}
}
