package variablestore_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func TestPurgingAnEnvironmentRefusesTheTierAndProduction(t *testing.T) {
	for _, scope := range []variablestore.Scope{
		{Project: "shop", Tier: environment.TierPreview},
		{Project: "shop", Tier: environment.TierProduction},
	} {
		for _, preview := range []string{"", "*", "pr-7"} {
			if scope.Tier == environment.TierPreview && preview == "pr-7" {
				continue
			}
			t.Run(string(scope.Tier)+"/"+preview, func(t *testing.T) {
				store, _ := fixture()
				if err := store.PurgeEnvironment(t.Context(), scope, preview); err == nil {
					t.Fatal("PurgeEnvironment() accepted a scope other than one named preview")
				}
			})
		}
	}
}
