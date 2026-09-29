package terminal

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
		{"a project and its tier", originAndEdge, "ocel  dev  acme › production"},
		{"an unnamed project leaves the tier alone on its line", &streamv1.IdentityEvent{
			Tier:   environmentv1.Tier_TIER_PRODUCTION,
			Origin: &streamv1.Party{Vendor: "fake", Account: "123456789012", Location: "example-region"},
		}, "ocel  dev  production"},
		{"an identity naming neither has no banner", &streamv1.IdentityEvent{Origin: originAndEdge.GetOrigin()}, "  fake  123456789012 · example-region · as deploy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := identityLines(Presentation{}, tc.ev)[0]; got != tc.want {
				t.Errorf("identityLines()[0] = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("colour paints the pill and the version, and no colour leaves not a single escape", func(t *testing.T) {
		t.Parallel()

		painted := strings.Join(identityLines(Presentation{Color: true}, originAndEdge), "\n")
		if !strings.Contains(painted, "\x1b[") || !strings.Contains(ansi.Strip(painted), "acme › production") {
			t.Errorf("coloured banner = %q, want escapes around the pill and the project and tier kept", painted)
		}
		if plain := strings.Join(identityLines(Presentation{}, originAndEdge), "\n"); strings.Contains(plain, "\x1b") {
			t.Errorf("uncoloured banner contains escapes: %q", plain)
		}
	})
}

var originAndEdge = &streamv1.IdentityEvent{
	Project: "acme",
	Tier:    environmentv1.Tier_TIER_PRODUCTION,
	Origin:  &streamv1.Party{Vendor: "fake", Account: "123456789012", Principal: "deploy", Location: "example-region"},
	Edge:    &streamv1.Party{Vendor: "relay", Account: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"},
}

func TestTheIdentityHeaderHasARowForEachAccountTheRunSignedInTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ev   *streamv1.IdentityEvent
		want string
	}{
		{"an origin and an edge", originAndEdge, "ocel  dev  acme › production\n\n" +
			"  fake   123456789012 · example-region · as deploy\n" +
			"  relay  a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"},
		{"a host with no region", &streamv1.IdentityEvent{
			Origin: &streamv1.Party{Vendor: "fake", Account: "srv1.example.com", Principal: "deploy"},
		}, "  fake  srv1.example.com · as deploy"},
		{"no principal", &streamv1.IdentityEvent{
			Origin: &streamv1.Party{Vendor: "fake", Account: "123456789012", Location: "example-region"},
		}, "  fake  123456789012 · example-region"},
		{"an account whose vendor is unnamed", &streamv1.IdentityEvent{
			Origin: &streamv1.Party{Account: "123456789012", Principal: "default"},
		}, "  your provider  123456789012 · as default"},
		{"no party at all", &streamv1.IdentityEvent{Project: "acme", Origin: &streamv1.Party{}}, "ocel  dev  acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := strings.Join(identityLines(Presentation{}, tc.ev), "\n"); got != tc.want {
				t.Errorf("identityLines() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestWithColourProductionIsBoldYellowAndAPreviewIsNot(t *testing.T) {
	t.Parallel()

	production := strings.Join(identityLines(Presentation{Color: true}, originAndEdge), "\n")
	if want := "\x1b[33;1mproduction\x1b[0;22m"; !strings.Contains(production, want) {
		t.Errorf("production header = %q, want it to contain %q", production, want)
	}
	preview := strings.Join(identityLines(Presentation{Color: true}, &streamv1.IdentityEvent{Project: "acme", Tier: environmentv1.Tier_TIER_PREVIEW}), "\n")
	if !strings.HasSuffix(preview, "\x1b[90m › \x1b[0mpreview") {
		t.Errorf("preview header = %q, want the tier unpainted", preview)
	}
}
