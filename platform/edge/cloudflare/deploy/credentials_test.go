package cloudflare

import (
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestCredentialPermissionsListsWhatEachTierMints(t *testing.T) {
	for _, tc := range []struct {
		name string
		tier edge.CredentialTier
		want []string
		gone []string
	}{
		{
			name: "bootstrap mints the R2 access token, so it edits API tokens",
			tier: edge.TierBootstrap,
			want: []string{
				"User · API Tokens · Edit",
				"Account · Workers Scripts · Edit",
				"Zone · Workers Routes · Edit",
			},
		},
		{
			name: "deploy mints nothing, so it never edits API tokens",
			tier: edge.TierDeploy,
			want: []string{
				"Account · Workers Scripts · Edit",
				"Zone · Workers Routes · Edit",
			},
			gone: []string{"User · API Tokens · Edit"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := (&provider{}).CredentialPermissions(tc.tier)
			if err != nil {
				t.Fatalf("CredentialPermissions(%v) error = %v", tc.tier, err)
			}
			if doc.Heading != "Cloudflare API token" {
				t.Errorf("CredentialPermissions(%v) heading = %q, want the Cloudflare token named", tc.tier, doc.Heading)
			}
			for _, want := range tc.want {
				if !strings.Contains(doc.Document, want) {
					t.Errorf("CredentialPermissions(%v) = %q, want it to carry %q", tc.tier, doc.Document, want)
				}
			}
			for _, gone := range tc.gone {
				if strings.Contains(doc.Document, gone) {
					t.Errorf("CredentialPermissions(%v) = %q, want %q left out", tc.tier, doc.Document, gone)
				}
			}
		})
	}

	if _, err := (&provider{}).CredentialPermissions("admin"); err == nil || !strings.Contains(err.Error(), `"admin"`) {
		t.Errorf("CredentialPermissions(admin) err = %v, want it to name the tier it was asked for", err)
	}
}
