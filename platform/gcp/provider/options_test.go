package gcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func TestTheOptionsNeedOnlyAProjectAndARegion(t *testing.T) {
	t.Parallel()

	p, err := gcp.New(context.Background(), provider.Settings{Options: provider.Options{"project": "acme-prod", "region": "europe-west1"}})
	if err != nil {
		t.Fatalf("New() = %v, want a provider", err)
	}
	if p.Facts().Vendor != gcloud.Vendor {
		t.Errorf("Facts().Vendor = %q, want %q", p.Facts().Vendor, gcloud.Vendor)
	}
}

func TestTheOptionsTakeANetworkAndASubnetworkOfTheProjectForItsAppsToJoin(t *testing.T) {
	t.Parallel()

	_, err := gcp.New(context.Background(), provider.Settings{Options: provider.Options{
		"project": "acme-prod", "region": "europe-west1",
		"network": map[string]any{"network": "fleet", "subnetwork": "fleet-europe-west1"},
		"apps":    map[string]any{"api": map[string]any{"network": true}},
	}})
	if err != nil {
		t.Errorf("New() = %v, want a provider joining the network fleet", err)
	}
}

func TestAnOptionThisProviderDoesNotTakeIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		options provider.Options
		names   string
		code    refusal.Code
	}{
		{
			name:    "an option no provider accepts",
			options: provider.Options{"project": "acme-prod", "region": "europe-west1", "keyFile": "/tmp/sa.json"},
			names:   "keyFile",
			code:    refusal.CodeUnknownOption,
		},
		{
			name: "a billing Cloud Run has no name for",
			options: provider.Options{"project": "acme-prod", "region": "europe-west1", "apps": map[string]any{
				"web": map[string]any{"billing": "requests"},
			}},
			names: `"apps.web.billing"`,
			code:  refusal.CodeInvalid,
		},
		{
			name: "a network with no subnetwork",
			options: provider.Options{"project": "acme-prod", "region": "europe-west1", "network": map[string]any{
				"network": "fleet",
			}},
			names: `"network.subnetwork"`,
			code:  refusal.CodeInvalid,
		},
		{
			name: "a network named by its path",
			options: provider.Options{"project": "acme-prod", "region": "europe-west1", "network": map[string]any{
				"network": "projects/acme-hosts/global/networks/fleet", "subnetwork": "fleet-europe-west1",
			}},
			names: `"network.network"`,
			code:  refusal.CodeInvalid,
		},
		{
			name: "an app reaching a network none names",
			options: provider.Options{"project": "acme-prod", "region": "europe-west1", "apps": map[string]any{
				"web": map[string]any{"network": true},
			}},
			names: `"apps.web.network"`,
			code:  refusal.CodeInvalid,
		},
		{
			name:    "no region",
			options: provider.Options{"project": "acme-prod"},
			names:   "region",
			code:    refusal.CodeInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var refused refusal.Refusal
			p, err := gcp.New(context.Background(), provider.Settings{Options: tc.options})
			if !errors.As(err, &refused) || refused.Code != tc.code {
				t.Fatalf("New() = %v, %v, want a %q refusal", p, err, tc.code)
			}
			if !strings.Contains(refused.Message, tc.names) {
				t.Errorf("New() refused with %q, want it to name %q so the author knows which line to edit", refused.Message, tc.names)
			}
		})
	}
}
