package cloudflare

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestCredentialPermissionsListsWhatEachPurposeMints(t *testing.T) {
	for _, tc := range []struct {
		name    string
		purpose edge.CredentialPurpose
		want    []string
		gone    []string
	}{
		{
			name:    "bootstrap mints the R2 access token, so it edits API tokens",
			purpose: edge.PurposeBootstrap,
			want: []string{
				"User · API Tokens · Edit",
				"Account · Workers Scripts · Edit",
				"Zone · Workers Routes · Edit",
			},
		},
		{
			name:    "deploy mints nothing, so it never edits API tokens",
			purpose: edge.PurposeDeploy,
			want: []string{
				"Account · Workers Scripts · Edit",
				"Zone · Workers Routes · Edit",
			},
			gone: []string{"User · API Tokens · Edit"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := New("ocel").Hooks().DescribeCredentialPermissions(tc.purpose)
			if err != nil {
				t.Fatalf("DescribeCredentialPermissions(%v) error = %v", tc.purpose, err)
			}
			if doc.Heading != "Cloudflare API token" {
				t.Errorf("DescribeCredentialPermissions(%v) heading = %q, want the Cloudflare token named", tc.purpose, doc.Heading)
			}
			for _, want := range tc.want {
				if !strings.Contains(doc.Document, want) {
					t.Errorf("DescribeCredentialPermissions(%v) = %q, want it to include %q", tc.purpose, doc.Document, want)
				}
			}
			for _, gone := range tc.gone {
				if strings.Contains(doc.Document, gone) {
					t.Errorf("DescribeCredentialPermissions(%v) = %q, want %q left out", tc.purpose, doc.Document, gone)
				}
			}
		})
	}

	if _, err := New("ocel").Hooks().DescribeCredentialPermissions("admin"); err == nil || !strings.Contains(err.Error(), `"admin"`) {
		t.Errorf("DescribeCredentialPermissions(admin) err = %v, want it to name the purpose it was asked for", err)
	}
}
