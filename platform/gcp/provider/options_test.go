package gcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func TestTheOptionsAreAProjectAndARegionAndNothingElse(t *testing.T) {
	t.Parallel()

	p, err := gcp.New(context.Background(), providerkit.Options{"project": "acme-prod", "region": "europe-west1"})
	if err != nil {
		t.Fatalf("New() = %v, want a provider", err)
	}
	if p.Vendor() != gcp.Vendor {
		t.Errorf("Vendor() = %q, want %q", p.Vendor(), gcp.Vendor)
	}
}

func TestAnOptionThisProviderDoesNotTakeIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		options providerkit.Options
		names   string
	}{
		{
			name:    "an option no provider accepts",
			options: providerkit.Options{"project": "acme-prod", "region": "europe-west1", "keyFile": "/tmp/sa.json"},
			names:   "keyFile",
		},
		{
			name:    "no project",
			options: providerkit.Options{"region": "europe-west1"},
			names:   "project",
		},
		{
			name:    "no region",
			options: providerkit.Options{"project": "acme-prod"},
			names:   "region",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var refusal providerkit.Refusal
			p, err := gcp.New(context.Background(), tc.options)
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
				t.Fatalf("New() = %v, %v, want an invalid refusal", p, err)
			}
			if !strings.Contains(refusal.Message, tc.names) {
				t.Errorf("New() refused with %q, want it to name %q so the author knows which line to edit", refusal.Message, tc.names)
			}
		})
	}
}
