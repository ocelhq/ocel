package gcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func TestTheOptionsAreAProjectAndARegionAndNothingElse(t *testing.T) {
	t.Parallel()

	p, err := gcp.New(context.Background(), provider.Settings{Options: provider.Options{"project": "acme-prod", "region": "europe-west1"}})
	if err != nil {
		t.Fatalf("New() = %v, want a provider", err)
	}
	if p.Facts().Vendor != gcp.Vendor {
		t.Errorf("Facts().Vendor = %q, want %q", p.Facts().Vendor, gcp.Vendor)
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
			name:    "no region",
			options: provider.Options{"project": "acme-prod"},
			names:   "region",
			code:    refusal.CodeInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var refusal refusal.Refusal
			p, err := gcp.New(context.Background(), provider.Settings{Options: tc.options})
			if !errors.As(err, &refusal) || refusal.Code != tc.code {
				t.Fatalf("New() = %v, %v, want a %q refusal", p, err, tc.code)
			}
			if !strings.Contains(refusal.Message, tc.names) {
				t.Errorf("New() refused with %q, want it to name %q so the author knows which line to edit", refusal.Message, tc.names)
			}
		})
	}
}
