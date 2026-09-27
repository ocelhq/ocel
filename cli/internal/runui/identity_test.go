package runui

import (
	"strings"
	"testing"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestTheIdentityBannerNamesTheProjectAndItsTier(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ev   *streamv1.IdentityEvent
		want string
	}{
		{"a project and its tier", awsAndCloudflare, "ocel  dev  acme › production"},
		{"an unnamed project leaves the tier alone on its line", &streamv1.IdentityEvent{
			Tier:   environmentv1.Tier_TIER_PRODUCTION,
			Origin: &streamv1.Party{Vendor: "aws", Account: "123456789012", Location: "us-east-1"},
		}, "ocel  dev  production"},
		{"an identity naming neither has no banner", &streamv1.IdentityEvent{Origin: awsAndCloudflare.GetOrigin()}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := strings.Join(identityLines(Presentation{}, tc.ev), "\n"); got != tc.want {
				t.Errorf("identityLines() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}

	t.Run("colour paints the pill and the version, and no colour leaves not a single escape", func(t *testing.T) {
		t.Parallel()

		painted := strings.Join(identityLines(Presentation{Color: true}, awsAndCloudflare), "\n")
		if !strings.Contains(painted, "\x1b[") || !strings.Contains(painted, "acme › production") {
			t.Errorf("coloured banner = %q, want escapes around the pill and the project and tier kept", painted)
		}
		if plain := strings.Join(identityLines(Presentation{}, awsAndCloudflare), "\n"); strings.Contains(plain, "\x1b") {
			t.Errorf("uncoloured banner contains escapes: %q", plain)
		}
	})
}

var awsAndCloudflare = &streamv1.IdentityEvent{
	Project: "acme",
	Tier:    environmentv1.Tier_TIER_PRODUCTION,
	Origin:  &streamv1.Party{Vendor: "aws", Account: "123456789012", Principal: "deploy", Location: "us-east-1"},
	Edge:    &streamv1.Party{Vendor: "cloudflare", Account: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"},
}

func TestWhoTheRunSignedInAsNamesThePrincipalAndWhichAccountAndWhereAtEachVendor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ev   *streamv1.IdentityEvent
		want string
	}{
		{"an origin and an edge", awsAndCloudflare,
			"Signed in to aws as deploy (123456789012, us-east-1) and cloudflare (a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4)"},
		{"a host with no region", &streamv1.IdentityEvent{
			Origin: &streamv1.Party{Vendor: "vps", Account: "srv1.example.com", Principal: "deploy"},
		}, "Signed in to vps as deploy (srv1.example.com)"},
		{"no principal", &streamv1.IdentityEvent{
			Origin: &streamv1.Party{Vendor: "aws", Account: "123456789012", Location: "us-east-1"},
		}, "Signed in to aws (123456789012, us-east-1)"},
		{"an account whose vendor is unnamed", &streamv1.IdentityEvent{
			Origin: &streamv1.Party{Account: "123456789012", Principal: "default"},
		}, "Signed in to your provider as default (123456789012)"},
		{"no party at all", &streamv1.IdentityEvent{Project: "acme", Origin: &streamv1.Party{}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := signedIn(tc.ev); got != tc.want {
				t.Errorf("signedIn() = %q, want %q", got, tc.want)
			}
		})
	}
}
