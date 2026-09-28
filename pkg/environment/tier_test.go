package environment_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestEachTierIsSpelledTheWayTagsKeysAndSealedValuesStoreIt(t *testing.T) {
	t.Parallel()

	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "production",
		environment.TierPreview:    "preview",
	} {
		if string(tier) != want {
			t.Errorf("tier = %q, want %q: resource tags, key names and every sealed value's coordinate already carry that spelling", tier, want)
		}
	}
}

func TestEachTierNamesTheOtherAsItsSibling(t *testing.T) {
	t.Parallel()

	for tier, want := range map[environment.Tier]environment.Tier{
		environment.TierProduction:  environment.TierPreview,
		environment.TierPreview:     environment.TierProduction,
		environment.Tier("staging"): "",
	} {
		if got := tier.Sibling(); got != want {
			t.Errorf("%q.Sibling() = %q, want %q", tier, got, want)
		}
	}
}
