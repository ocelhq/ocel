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
