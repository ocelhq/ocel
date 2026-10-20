package naming_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

func TestTierWordIsProdForProductionAndPreviewForPreview(t *testing.T) {
	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "prod",
		environment.TierPreview:    "preview",
	} {
		if got := naming.TierWord(tier); got != want {
			t.Errorf("TierWord(%q) = %q, want %q", tier, got, want)
		}
	}
}

func TestOcelNameJoinsTheNamespaceFieldTheTierWordAndOcelsWords(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		tier      environment.Tier
		words     []string
		want      string
	}{
		{"ocel", environment.TierProduction, []string{"edge", "k3x9q2"}, "ocel-prod-edge-k3x9q2"},
		{"ocel", environment.TierProduction, []string{"env-sync"}, "ocel-prod-env-sync"},
		{"ocel", environment.TierPreview, []string{"containers"}, "ocel-preview-containers"},
		{"ocel", environment.TierPreview, nil, "ocel-preview"},
		{"acme-staging-eu-west-blue-team", environment.TierProduction, []string{"state", "a1b2c3"}, "acme-20ddf6-prod-state-a1b2c3"},
	} {
		if got := naming.OcelName(tc.namespace, tc.tier, tc.words...); got != tc.want {
			t.Errorf("OcelName(%q, %q, %q) = %q, want %q", tc.namespace, tc.tier, tc.words, got, tc.want)
		}
	}
}

func TestOcelNameNeverSitsInsideTheAppFence(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if got := naming.OcelName("ocel", tier, "app", "env-sync"); strings.HasPrefix(got, naming.AppFence("ocel")) {
			t.Errorf("OcelName(%q, %q) = %q, inside the fence %q only app resources are named under", "ocel", tier, got, naming.AppFence("ocel"))
		}
	}
}

func TestMustUntieredOcelNameJoinsTheNamespaceFieldAndOcelsWords(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		words     []string
		want      string
	}{
		{"ocel", []string{"connector"}, "ocel-connector"},
		{"acme-staging-eu-west-blue-team", []string{"cert"}, "acme-20ddf6-cert"},
	} {
		if got := naming.MustUntieredOcelName(tc.namespace, tc.words...); got != tc.want {
			t.Errorf("MustUntieredOcelName(%q, %q) = %q, want %q", tc.namespace, tc.words, got, tc.want)
		}
	}
}

func TestMustUntieredOcelNamePanicsOnANameInsideTheAppFence(t *testing.T) {
	for _, words := range [][]string{{"app", "env-sync"}, {"App", "invalidator"}, {"app-env-sync"}, {}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("MustUntieredOcelName(%q, %q) returned, want a panic: its name would sit inside the fence %q or be the namespace alone", "ocel", words, naming.AppFence("ocel"))
				}
			}()
			naming.MustUntieredOcelName("ocel", words...)
		}()
	}
}
